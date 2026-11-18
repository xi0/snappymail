package cryptox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestPHPCrossCheck runs the real snappymail/v/0.0.0/app/libraries/snappymail/
// crypt.php and MailSo/Base/Xxtea.php and checks that blobs travel in both
// directions for all three methods.
//
// It is skipped when no "php" binary is available, so the Go test suite still
// works on a machine without PHP. On CI the PHP backend is available, which
// makes this the "PHP-produced fixtures" check of Step 6.
func TestPHPCrossCheck(t *testing.T) {
	phpPath, err := exec.LookPath("php")
	if nil != err {
		t.Skip("php binary not available")
	}

	libraries, err := filepath.Abs(filepath.Join("..", "..", "..", "snappymail", "v", "0.0.0", "app", "libraries"))
	if nil != err {
		t.Fatalf("resolve libraries: %v", err)
	}
	if _, err := os.Stat(filepath.Join(libraries, "snappymail", "crypt.php")); nil != err {
		t.Skipf("the PHP libraries are not available: %v", err)
	}

	crypter := New(testSalt, testVersion, "aes-256-cbc")
	plaintext := `{"a":"b","n":1}`
	expectedPHP := map[string]any{"a": "b", "n": float64(1)}
	expectedGo := map[string]any{"a": "b", "n": int64(1)}

	methods := []struct {
		name  string
		nonce []byte
	}{
		{MethodSodium, bytes.Repeat([]byte{0x01}, sodiumNonceSize)},
		{MethodOpenSSL, bytes.Repeat([]byte{0x02}, opensslIVSize)},
		{MethodXxtea, bytes.Repeat([]byte{0x03}, xxteaSaltSize)},
	}

	job := phpCryptJob{
		Salt:      testSalt,
		Version:   testVersion,
		Libraries: libraries,
	}

	decryptMethods := make([]string, 0, len(methods))
	encryptMethods := make([]string, 0, len(methods))
	encryptNonces := make([][]byte, 0, len(methods))

	for _, method := range methods {
		payload, err := encryptRaw(crypter, method.name, plaintext, method.nonce)
		if nil != err {
			t.Fatalf("%s: encrypt for the PHP cross-check: %v", method.name, err)
		}

		job.Decrypt = append(job.Decrypt, phpCryptCase{
			Method:  method.name,
			Nonce:   base64.StdEncoding.EncodeToString(method.nonce),
			Payload: base64.StdEncoding.EncodeToString(payload),
			Key:     testKey,
		})
		decryptMethods = append(decryptMethods, method.name)

		job.Encrypt = append(job.Encrypt, phpCryptCase{
			Method:    method.name,
			Nonce:     base64.StdEncoding.EncodeToString(method.nonce),
			Plaintext: plaintext,
			Key:       testKey,
		})
		encryptMethods = append(encryptMethods, method.name)
		encryptNonces = append(encryptNonces, method.nonce)
	}

	result := runPHPCrossCheck(t, phpPath, job)

	for index, method := range decryptMethods {
		if !phpSupports(result, method) {
			t.Logf("skipping %s: the PHP build lacks the extension", method)
			continue
		}
		if index >= len(result.Decrypted) || "null" == string(result.Decrypted[index]) {
			t.Errorf("%s: PHP did not decrypt the Go blob", method)
			continue
		}

		var decoded any
		if err := json.Unmarshal(result.Decrypted[index], &decoded); nil != err {
			t.Errorf("%s: decode PHP result: %v", method, err)
			continue
		}
		if !reflect.DeepEqual(decoded, expectedPHP) {
			t.Errorf("%s: PHP decrypted %#v, want %#v", method, decoded, expectedPHP)
		}
	}

	for index, method := range encryptMethods {
		if !phpSupports(result, method) {
			t.Logf("skipping %s: the PHP build lacks the extension", method)
			continue
		}
		if index >= len(result.Encrypted) || nil == result.Encrypted[index] {
			t.Errorf("%s: PHP did not encrypt the plaintext", method)
			continue
		}

		payload, err := base64.StdEncoding.DecodeString(*result.Encrypted[index])
		if nil != err {
			t.Errorf("%s: decode PHP payload: %v", method, err)
			continue
		}

		decoded, err := crypter.Decrypt(Blob{Method: method, Nonce: encryptNonces[index], Payload: payload}, testKey)
		if nil != err {
			t.Errorf("%s: Go could not decrypt the PHP blob: %v", method, err)
			continue
		}
		if !reflect.DeepEqual(decoded, expectedGo) {
			t.Errorf("%s: Go decrypted %#v, want %#v", method, decoded, expectedGo)
		}
	}
}

// encryptRaw encrypts with a fixed nonce/IV/salt of the given method.
func encryptRaw(crypter *Crypter, method, plaintext string, nonce []byte) ([]byte, error) {
	switch method {
	case MethodSodium:
		return crypter.SodiumEncrypt(plaintext, nonce, testKey)
	case MethodOpenSSL:
		return crypter.OpenSSLEncrypt(plaintext, nonce, testKey)
	case MethodXxtea:
		return crypter.XxteaEncrypt(plaintext, nonce, testKey)
	}
	return nil, ErrInvalidBlob
}

// phpSupports reports whether the PHP build can handle method.
func phpSupports(result phpCryptResult, method string) bool {
	switch method {
	case MethodSodium:
		return result.Capabilities.Sodium
	case MethodOpenSSL:
		return result.Capabilities.OpenSSL
	}
	return true // xxtea is implemented by the included MailSo class
}

// phpCryptCase is one decrypt or encrypt operation of the PHP script.
type phpCryptCase struct {
	Method    string `json:"method"`
	Nonce     string `json:"nonce"`
	Payload   string `json:"payload,omitempty"`
	Plaintext string `json:"plaintext,omitempty"`
	Key       string `json:"key"`
}

// phpCryptJob is the stdin document of the PHP script.
type phpCryptJob struct {
	Salt      string         `json:"salt"`
	Version   string         `json:"version"`
	Libraries string         `json:"libraries"`
	Decrypt   []phpCryptCase `json:"decrypt"`
	Encrypt   []phpCryptCase `json:"encrypt"`
}

// phpCryptResult is the stdout document of the PHP script.
type phpCryptResult struct {
	Capabilities struct {
		Sodium  bool `json:"sodium"`
		OpenSSL bool `json:"openssl"`
	} `json:"capabilities"`
	Decrypted []json.RawMessage `json:"decrypted"`
	Encrypted []*string         `json:"encrypted"`
}

// runPHPCrossCheck writes the script, feeds it the job and decodes the result.
func runPHPCrossCheck(t *testing.T, phpPath string, job phpCryptJob) phpCryptResult {
	t.Helper()

	script := filepath.Join(t.TempDir(), "crypt_crosscheck.php")
	if err := os.WriteFile(script, []byte(phpCryptScript), 0o600); nil != err {
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

	var result phpCryptResult
	if err := json.Unmarshal(stdout.Bytes(), &result); nil != err {
		t.Fatalf("decode PHP result: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	return result
}

// phpCryptScript includes the real PHP implementation with the few globals it
// needs stubbed (APP_SALT, RainLoop\Api and SnappyMail\Log).
const phpCryptScript = `<?php
namespace {
	$input = json_decode(stream_get_contents(STDIN), true);
	define('APP_SALT', $input['salt']);
	define('APP_VERSION', $input['version']);
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
			return 'aes-256-cbc';
		}
	}
	class Api {
		public static function Config() { return new ConfigStub(); }
	}
}

namespace {
	require $input['libraries'] . '/MailSo/Base/Xxtea.php';
	require $input['libraries'] . '/snappymail/crypt.php';

	$result = [
		'capabilities' => [
			'sodium' => function_exists('sodium_crypto_aead_xchacha20poly1305_ietf_encrypt'),
			'openssl' => extension_loaded('openssl'),
		],
		'decrypted' => [],
		'encrypted' => [],
	];

	foreach ($input['decrypt'] as $case) {
		$data = [$case['method'], base64_decode($case['nonce']), base64_decode($case['payload'])];
		$result['decrypted'][] = \SnappyMail\Crypt::Decrypt($data, $case['key']);
	}

	foreach ($input['encrypt'] as $case) {
		$nonce = base64_decode($case['nonce']);
		$payload = null;
		try {
			switch ($case['method']) {
				case 'sodium':
					$payload = \SnappyMail\Crypt::SodiumEncrypt($case['plaintext'], $nonce, $case['key']);
					break;
				case 'openssl':
					$payload = \SnappyMail\Crypt::OpenSSLEncrypt($case['plaintext'], $nonce, $case['key']);
					break;
				case 'xxtea':
					$payload = \SnappyMail\Crypt::XxteaEncrypt($case['plaintext'], $nonce, $case['key']);
					break;
			}
		} catch (\Throwable $e) {
			$payload = null;
		}
		$result['encrypted'][] = null === $payload ? null : base64_encode($payload);
	}

	echo json_encode($result);
}
`
