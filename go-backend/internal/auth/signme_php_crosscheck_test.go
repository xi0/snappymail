package auth

// Cross-check of the sign-me cookie format against the PHP backend.
//
// The cookie and the .sign_me payload are a compatibility contract (both
// backends may serve the same browser session during a migration), so this test
// runs the shipped SnappyMail\Crypt and RainLoop\Model\MainAccount and verifies
// both directions:
//
//   - a cookie + payload written the way Actions\UserAuth::SetSignMeToken()
//     writes them authenticates in Go;
//   - a cookie + payload written by Go is decrypted by PHP into the same
//     account document.
//
// It is skipped when no "php" binary is available, like the token cross-check
// of step 7.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestSignMePHPCrossCheck(t *testing.T) {
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

	parameters := map[string]string{
		"salt":      testSalt,
		"version":   testVersion,
		"smctoken":  testToken,
		"email":     testEmail,
		"login":     testEmail,
		"password":  testPassword,
		"libraries": libraries,
	}

	// 1. PHP writes the sign-me data, Go authenticates with it.
	written := runSignMeScript(t, phpPath, "write", parameters)
	if nil != written["error"] {
		t.Fatalf("the PHP writer failed: %v", written["error"])
	}

	payload, err := base64.StdEncoding.DecodeString(textField(written, "payload"))
	if nil != err {
		t.Fatalf("decode the PHP payload: %v", err)
	}

	env := newTestEnv(t)
	uuid := textField(written, "uuid")
	if err := env.storage.Put(testEmail, storage.TypeSignMe, uuid, string(payload)); nil != err {
		t.Fatalf("store the PHP payload: %v", err)
	}

	login, _ := env.loginFor(requestWith(map[string]string{
		AuthSignMeTokenKey: textField(written, "cookie"),
	}))

	main := login.GetAccountFromSignMeToken()
	if nil == main {
		t.Fatal("the PHP sign-me cookie did not authenticate in Go")
	}
	if testEmail != main.Email() || testPassword != main.ImapPass() {
		t.Errorf("resolved = %q/%q", main.Email(), main.ImapPass())
	}

	// 2. Go writes the sign-me data, PHP decrypts it.
	goLogin, goRecorder := env.loginFor(nil)
	goLogin.SetSignMeToken(env.mainAccount())

	token := goLogin.GetSignMeToken()
	if nil == token {
		t.Fatal("Go did not write a sign-me token")
	}
	goPayload, ok := env.storage.Get(testEmail, storage.TypeSignMe, token.uuid)
	if !ok {
		t.Fatal("Go did not write the .sign_me payload")
	}

	verified := runSignMeScript(t, phpPath, "read", mergeParameters(parameters, map[string]string{
		"cookie":  recorderCookieValue(goRecorder, AuthSignMeTokenKey),
		"uuid":    token.uuid,
		"payload": base64.StdEncoding.EncodeToString([]byte(goPayload)),
	}))
	if nil != verified["error"] {
		t.Fatalf("the PHP reader failed: %v", verified["error"])
	}

	account, ok := verified["account"].(map[string]any)
	if !ok {
		t.Fatalf("the PHP reader returned %T", verified["account"])
	}
	if testEmail != account["email"] || testEmail != account["login"] || testPassword != account["pass"] {
		t.Errorf("PHP decrypted %v", account)
	}
}

// mergeParameters combines two parameter maps.
func mergeParameters(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}

// textField returns a string field of the PHP result.
func textField(result map[string]any, name string) string {
	if value, ok := result[name].(string); ok {
		return value
	}
	return ""
}

// runSignMeScript runs the PHP script in the given mode and decodes its result.
func runSignMeScript(t *testing.T, phpPath, mode string, parameters map[string]string) map[string]any {
	t.Helper()

	script := filepath.Join(t.TempDir(), "signme_crosscheck.php")
	if err := os.WriteFile(script, []byte(signMePHPScript), 0o600); nil != err {
		t.Fatalf("write PHP script: %v", err)
	}

	job := mergeParameters(parameters, map[string]string{"mode": mode})
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

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); nil != err {
		t.Fatalf("decode PHP result: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return result
}

// signMePHPScript is the PHP half of the cross-check: it writes or reads the
// sign-me data with the shipped SnappyMail\Crypt and RainLoop\Model\MainAccount.
//
// The globals the classes reach for (APP_SALT, APP_VERSION, RainLoop\Api,
// SnappyMail\Log) are stubbed, like the crypt cross-check of step 6.
const signMePHPScript = `<?php
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
	spl_autoload_register(function ($name) {
		$file = SNAPPYMAIL_LIBRARIES_PATH . strtolower(strtr($name, '\\', '/')) . '.php';
		if (is_file($file)) {
			include_once $file;
		}
	});

	$result = array('error' => null);
	try {
		if ('write' === $input['mode']) {
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

			// Actions\UserAuth::SetSignMeToken()
			$uuid = \SnappyMail\UUID::generate();
			$data = \SnappyMail\Crypt::Encrypt($oAccount, 'signme');
			$result['cookie'] = \SnappyMail\Crypt::EncryptUrlSafe(array(
				'e' => $oAccount->Email(),
				'u' => $uuid,
				'c' => $data[0],
				'd' => \base64_encode($data[1])
			), 'signme');
			$result['uuid'] = $uuid;
			$result['payload'] = \base64_encode($data[2]);
		} else {
			// Actions\UserAuth::GetAccountFromSignMeToken()
			$aTokenData = \SnappyMail\Crypt::DecryptUrlSafe($input['cookie'], 'signme');
			if (!isset($aTokenData['e'], $aTokenData['u'], $aTokenData['c'], $aTokenData['d'])) {
				throw new \RuntimeException('cookie has no sign-me data');
			}
			$result['account'] = \SnappyMail\Crypt::Decrypt(array(
				$aTokenData['c'],
				\base64_decode($aTokenData['d']),
				\base64_decode($input['payload'])
			), 'signme');
		}
	} catch (\Throwable $e) {
		$result['error'] = $e->getMessage() . ' @ ' . $e->getFile() . '#' . $e->getLine();
	}

	echo json_encode($result);
}
`
