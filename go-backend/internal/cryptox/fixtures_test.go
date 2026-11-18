package cryptox

import (
	"bytes"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// phpCryptFixtures were produced by the PHP backend
// (snappymail/v/0.0.0/app/libraries/snappymail/crypt.php + MailSo/Base/Xxtea.php)
// for the fixed nonce/IV/salt below, with:
//
//	APP_SALT    = testSalt
//	APP_VERSION = testVersion
//	key         = testKey
//	cipher      = "aes-256-cbc"
//	plaintext   = {"a":"b","n":1}
//
// They keep the wire format pinned even on a machine without PHP; the
// TestPHPCrossCheck test regenerates the same values when PHP is available.
var phpCryptFixtures = []struct {
	method  string
	nonce   []byte
	payload string // base64, as PHP's base64_encode() writes it
}{
	{
		method:  MethodSodium,
		nonce:   bytes.Repeat([]byte{0x01}, sodiumNonceSize),
		payload: "uB/w0WEDQM8v6ekdZM7z4cJMmT0LqW9geH1n9UYGkA==",
	},
	{
		method:  MethodOpenSSL,
		nonce:   bytes.Repeat([]byte{0x02}, opensslIVSize),
		payload: "XR3HbXCOtQUgPF1Tf7EvPw==",
	},
	{
		method:  MethodXxtea,
		nonce:   bytes.Repeat([]byte{0x03}, xxteaSaltSize),
		payload: "QwtEb9SBP0nEGq4KCM5Pbac2ov8=",
	},
}

// phpFixturePlaintext is the JSON document the fixtures encrypt.
const phpFixturePlaintext = `{"a":"b","n":1}`

func TestPHPProducedFixturesDecrypt(t *testing.T) {
	crypter := New(testSalt, testVersion, "aes-256-cbc")
	want := map[string]any{"a": "b", "n": int64(1)}

	for _, fixture := range phpCryptFixtures {
		payload, err := base64.StdEncoding.DecodeString(fixture.payload)
		if nil != err {
			t.Fatalf("%s: decode fixture: %v", fixture.method, err)
		}

		got, err := crypter.Decrypt(Blob{
			Method:  fixture.method,
			Nonce:   fixture.nonce,
			Payload: payload,
		}, testKey)
		if nil != err {
			t.Errorf("%s: Decrypt: %v", fixture.method, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Decrypt = %#v, want %#v", fixture.method, got, want)
		}
	}
}

func TestGoMatchesPHPFixtures(t *testing.T) {
	crypter := New(testSalt, testVersion, "aes-256-cbc")

	for _, fixture := range phpCryptFixtures {
		var payload []byte
		var err error

		switch fixture.method {
		case MethodSodium:
			payload, err = crypter.SodiumEncrypt(phpFixturePlaintext, fixture.nonce, testKey)
		case MethodOpenSSL:
			payload, err = crypter.OpenSSLEncrypt(phpFixturePlaintext, fixture.nonce, testKey)
		case MethodXxtea:
			payload, err = crypter.XxteaEncrypt(phpFixturePlaintext, fixture.nonce, testKey)
		}
		if nil != err {
			t.Errorf("%s: encrypt: %v", fixture.method, err)
			continue
		}

		if got := base64.StdEncoding.EncodeToString(payload); fixture.payload != got {
			t.Errorf("%s: Go ciphertext = %s, want the PHP fixture %s",
				fixture.method, got, fixture.payload)
		}
	}
}

func TestPHPFixtureJSONShape(t *testing.T) {
	crypter := New(testSalt, testVersion, "aes-256-cbc")

	for _, fixture := range phpCryptFixtures {
		// PHP: json_encode(array_map('base64_encode', [method, nonce, payload]))
		// escapes "/" as "\/", so a base64 payload containing "/" is written
		// with the escape. DecryptFromJSON must read either form.
		encoded := `["` + fixture.method + `","` +
			base64.StdEncoding.EncodeToString(fixture.nonce) + `","` +
			strings.ReplaceAll(fixture.payload, "/", `\/`) + `"]`

		got, err := crypter.DecryptFromJSON(encoded, testKey)
		if nil != err {
			t.Errorf("%s: DecryptFromJSON: %v", fixture.method, err)
			continue
		}
		if !reflect.DeepEqual(got, map[string]any{"a": "b", "n": int64(1)}) {
			t.Errorf("%s: DecryptFromJSON = %#v", fixture.method, got)
		}
	}
}
