package auth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// phpSessionResult is the stdout document of the cross-check script.
type phpSessionResult struct {
	Error *string `json:"error"`
	// Hash is Account::Hash() of the PHP account.
	Hash string `json:"hash"`
	// JSON is json_encode($oAccount), the document of the smaccount cookie.
	JSON string `json:"json"`
	// Cookie is the smaccount cookie value
	// (urlsafe-base64 of Crypt::EncryptToJSON($oAccount)).
	Cookie string `json:"cookie"`
	// Session is Utils::GetSessionToken() for SessionTokenInput.
	Session string `json:"session"`
	// CryptKey is the sealed crypt key
	// (Crypt::EncryptToJSON(sha1($pass . APP_SALT), $pass)).
	CryptKey string `json:"cryptkey"`
}

// TestPHPCrossCheck authenticates with a session the PHP backend produced.
//
// It runs RainLoop\Model\MainAccount + SnappyMail\Crypt + SnappyMail\Cookies
// from snappymail/v/0.0.0 and checks that Go derives the identical account hash,
// session storage key and crypt key from the same inputs, and that the PHP
// cookie authenticates the request.
//
// It is skipped when no "php" binary is available, so the Go test suite still
// works on a machine without PHP. On CI the PHP backend is available, which
// makes this the "a signature valid in PHP authenticates in Go" check of
// Step 7.
func TestPHPCrossCheck(t *testing.T) {
	phpPath, err := exec.LookPath("php")
	if nil != err {
		t.Skip("php binary not available")
	}

	libraries, err := filepath.Abs(filepath.Join("..", "..", "..", "snappymail", "v", "0.0.0", "app", "libraries"))
	if nil != err {
		t.Fatalf("resolve libraries: %v", err)
	}
	if _, err := os.Stat(filepath.Join(libraries, "RainLoop", "Model", "Account.php")); nil != err {
		t.Skipf("the PHP libraries are not available: %v", err)
	}

	const rawSession = "php-session-token"

	result := runPHPSessionScript(t, phpPath, map[string]string{
		"salt":      testSalt,
		"version":   testVersion,
		"smctoken":  testToken,
		"email":     testEmail,
		"login":     testEmail,
		"password":  testPassword,
		"session":   rawSession,
		"libraries": libraries,
	})

	if nil != result.Error {
		t.Fatalf("the PHP script failed: %s", *result.Error)
	}

	env := newTestEnv(t)
	main := env.mainAccount()

	// 1. Account::Hash() is identical.
	if want := main.Hash(testSalt); want != result.Hash {
		t.Errorf("Hash = %q, want the PHP value %q", want, result.Hash)
	}

	// 2. jsonSerialize() has the same shape (PHP writes an empty array as [],
	//    Go as {}; both decode to an empty array in PHP).
	if want, got := canonicalJSON(t, result.JSON), canonicalJSON(t, marshalTokenArray(t, main)); want != got {
		t.Errorf("account JSON = %s, want the PHP document %s", got, want)
	}

	// 3. The PHP cookie decrypts with the Go crypter.
	jar := cryptox.NewJar(requestWith(map[string]string{AuthSpecTokenKey: result.Cookie}), cryptox.CookieOptions{})
	value, found, err := jar.GetSecure(env.crypt, AuthSpecTokenKey)
	if nil != err || !found {
		t.Fatalf("the PHP cookie could not be decrypted: %v", err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("the PHP cookie holds %T, want an account document", value)
	}
	if testEmail != document["email"] || testEmail != document["login"] || testPassword != document["pass"] {
		t.Errorf("the PHP token array = %v", document)
	}

	// 4. The session storage key is identical, and that cookie authenticates.
	request := requestWith(map[string]string{
		AuthSpecTokenKey: result.Cookie,
		SessionTokenName: rawSession,
	})

	authenticator, _ := env.authenticator(request)
	if got := authenticator.SessionToken(false); result.Session != got {
		t.Errorf("SessionToken = %q, want the PHP value %q", got, result.Session)
	}

	if err := env.storage.Put(main, storage.TypeSession, result.Session, "true"); nil != err {
		t.Fatalf("store the session: %v", err)
	}
	if err := env.storage.Put(main, storage.TypeRoot, ".cryptkey", result.CryptKey); nil != err {
		t.Fatalf("store the crypt key: %v", err)
	}

	resolved, err := authenticator.GetMainAccountFromToken(true)
	if nil != err {
		t.Fatalf("the PHP session did not authenticate: %v", err)
	}
	if nil == resolved || testEmail != resolved.Email() {
		t.Fatalf("resolved = %v", resolved)
	}
	if want := resolved.Hash(testSalt); result.Hash != want {
		t.Errorf("the authenticated account hash = %q, want %q", want, result.Hash)
	}

	// 5. The PHP sealed crypt key opens (and yields the same key Go would have
	//    written itself).
	phpKey, err := resolved.CryptKey(authenticator.Context)
	if nil != err {
		t.Fatalf("CryptKey from the PHP blob: %v", err)
	}
	digest, err := hex.DecodeString(storage.SHA1(testPassword + testSalt))
	if nil != err {
		t.Fatalf("decode hex: %v", err)
	}
	if string(digest) != phpKey {
		t.Errorf("CryptKey = %x, want %x", phpKey, digest)
	}
}

// marshalTokenArray encodes a value the way cryptox does (SetEscapeHTML(false)).
func marshalTokenArray(t *testing.T, value any) string {
	t.Helper()

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); nil != err {
		t.Fatalf("encode: %v", err)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// canonicalJSON re-encodes a JSON document with sorted keys, so the PHP and Go
// serializations can be compared: PHP writes an empty array as "[]" and Go as
// "{}", and both decode to an empty PHP array.
func canonicalJSON(t *testing.T, document string) string {
	t.Helper()

	var value any
	if err := json.Unmarshal([]byte(document), &value); nil != err {
		t.Fatalf("decode %q: %v", document, err)
	}

	encoded, err := json.Marshal(normalizeEmptyContainers(value))
	if nil != err {
		t.Fatalf("encode: %v", err)
	}
	return string(encoded)
}

// normalizeEmptyContainers turns an empty JSON array into an empty JSON object.
func normalizeEmptyContainers(value any) any {
	switch typed := value.(type) {
	case []any:
		if 0 == len(typed) {
			return map[string]any{}
		}
		for index := range typed {
			typed[index] = normalizeEmptyContainers(typed[index])
		}
		return typed
	case map[string]any:
		for key := range typed {
			typed[key] = normalizeEmptyContainers(typed[key])
		}
		return typed
	}
	return value
}

// runPHPSessionScript writes the script, feeds it the job and decodes the result.
func runPHPSessionScript(t *testing.T, phpPath string, job map[string]string) phpSessionResult {
	t.Helper()

	script := filepath.Join(t.TempDir(), "session_crosscheck.php")
	if err := os.WriteFile(script, []byte(phpSessionScript), 0o600); nil != err {
		t.Fatalf("write PHP script: %v", err)
	}

	stdin, err := json.Marshal(job)
	if nil != err {
		t.Fatalf("encode PHP job: %v", err)
	}

	command := exec.Command(phpPath,
		"-d", "display_startup_errors=0",
		"-d", "display_errors=stderr",
		script)
	command.Stdin = bytes.NewReader(stdin)

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); nil != err {
		t.Fatalf("run PHP: %v\nstderr: %s", err, stderr.String())
	}

	var result phpSessionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); nil != err {
		t.Fatalf("decode PHP result: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return result
}

// phpSessionScript builds an account with the real PHP model, then exports the
// token array, the cookie value, the session key and the crypt key blob.
//
// The globals the classes reach for (APP_SALT, APP_VERSION, RainLoop\Api,
// SnappyMail\Log) are stubbed, like the crypt cross-check of step 6; everything
// else is the shipped implementation.
const phpSessionScript = `<?php
namespace {
	$input = json_decode(stream_get_contents(STDIN), true);
	define('APP_SALT', $input['salt']);
	define('APP_VERSION', $input['version']);
	define('SNAPPYMAIL_LIBRARIES_PATH', rtrim($input['libraries'], '/') . '/');
	$_COOKIE['smctoken'] = $input['smctoken'];
}

namespace SnappyMail {
	class Log {
		public static function error($category, $message = '') {}
		public static function warning($category, $message = '') {}
		public static function notice($category, $message = '') {}
		public static function debug($category, $message = '') {}
	}
}

namespace RainLoop {
	class ConfigStub {
		public function Get($section = '', $key = '', $default = null) {
			return 'encrypt_cipher' === $key ? 'aes-256-cbc' : $default;
		}
	}
	class ActionsStub {
		public function logMask($value) {}
	}
	class Api {
		public static function Config() { return new ConfigStub; }
		public static function Actions() { return new ActionsStub; }
	}
}

namespace {
	spl_autoload_register(function ($name) {
		$file = SNAPPYMAIL_LIBRARIES_PATH . strtr($name, '\\', '/') . '.php';
		if (is_file($file)) {
			include_once $file;
		}
	});
	// include.php's lowercase autoloader: SnappyMail\SensitiveString lives in
	// snappymail/sensitivestring.php.
	spl_autoload_register(function ($name) {
		$file = SNAPPYMAIL_LIBRARIES_PATH . strtolower(strtr($name, '\\', '/')) . '.php';
		if (is_file($file)) {
			include_once $file;
		}
	});

	$result = array('error' => null);
	try {
		$oDomain = \RainLoop\Model\Domain::fromArray('example.com', array(
			'IMAP' => array('host' => 'imap.example.com', 'port' => 993, 'type' => 1),
			'SMTP' => array('host' => 'smtp.example.com', 'port' => 465, 'type' => 1),
			'Sieve' => array('host' => '', 'port' => 4190, 'type' => 1, 'enabled' => false),
			'whiteList' => ''
		));
		$oAccount = new \RainLoop\Model\MainAccount;
		$oAccount->setCredentials(
			$oDomain,
			$input['email'],
			$input['login'],
			new \SnappyMail\SensitiveString($input['password'])
		);

		$result['hash'] = $oAccount->Hash();
		$result['json'] = \json_encode($oAccount);
		$result['cookie'] = \MailSo\Base\Utils::UrlSafeBase64Encode(
			\SnappyMail\Crypt::EncryptToJSON($oAccount)
		);
		$result['session'] = \sha1('Session' . APP_SALT . $input['session'] . 'Token' . APP_SALT);
		$result['cryptkey'] = \SnappyMail\Crypt::EncryptToJSON(
			\sha1($input['password'] . APP_SALT),
			$input['password']
		);
	} catch (\Throwable $e) {
		$result['error'] = $e->getMessage() . ' @ ' . $e->getFile() . '#' . $e->getLine();
	}

	echo json_encode($result);
}
`
