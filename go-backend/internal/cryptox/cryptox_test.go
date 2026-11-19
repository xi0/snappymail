package cryptox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

const (
	testSalt    = "0123456789abcdef0123456789abcdef"
	testVersion = "0.0.0"
	testKey     = "secret-key"
)

func testCrypter() *Crypter {
	return New(testSalt, testVersion, "aes-256-cbc-hmac-sha1")
}

func TestPassphraseIsRawSHA1(t *testing.T) {
	crypter := testCrypter()

	got, err := crypter.Passphrase(testKey)
	if nil != err {
		t.Fatalf("Passphrase: %v", err)
	}

	want := sha1.Sum([]byte(testKey + testSalt))
	if !bytes.Equal(got, want[:]) {
		t.Errorf("Passphrase = %x, want %x", got, want)
	}
}

func TestPassphraseEmptyKeyNeedsCallback(t *testing.T) {
	crypter := testCrypter()

	if _, err := crypter.Passphrase(""); nil == err {
		t.Fatal("an empty key without a callback must fail")
	}

	crypter.DefaultPassphrase = func() (string, error) { return "smctoken", nil }

	got, err := crypter.Passphrase("")
	if nil != err {
		t.Fatalf("Passphrase: %v", err)
	}
	want := sha1.Sum([]byte("smctoken" + testVersion + testSalt))
	if !bytes.Equal(got, want[:]) {
		t.Errorf("Passphrase = %x, want smctoken + APP_VERSION + APP_SALT", got)
	}
}

func TestSodiumKeyRepeatsDigest(t *testing.T) {
	digest := sha1.Sum([]byte("x"))
	key := sodiumKey(digest[:])

	if sodiumKeySize != len(key) {
		t.Fatalf("len(key) = %d, want %d", len(key), sodiumKeySize)
	}
	for index := range key {
		if key[index] != digest[index%len(digest)] {
			t.Fatalf("key[%d] = %x, want digest[%d] (str_pad repeats, not zero pads)",
				index, key[index], index%len(digest))
		}
	}
}

func TestSodiumRoundTrip(t *testing.T) {
	crypter := testCrypter()
	nonce := bytes.Repeat([]byte{0x42}, sodiumNonceSize)

	payload, err := crypter.SodiumEncrypt("hello world", nonce, testKey)
	if nil != err {
		t.Fatalf("SodiumEncrypt: %v", err)
	}

	plaintext, err := crypter.SodiumDecrypt(payload, nonce, testKey)
	if nil != err {
		t.Fatalf("SodiumDecrypt: %v", err)
	}
	if "hello world" != string(plaintext) {
		t.Errorf("plaintext = %q, want hello world", plaintext)
	}
}

func TestSodiumBindsSaltAsAdditionalData(t *testing.T) {
	crypter := testCrypter()
	nonce := bytes.Repeat([]byte{0x42}, sodiumNonceSize)

	payload, err := crypter.SodiumEncrypt("hello", nonce, testKey)
	if nil != err {
		t.Fatalf("SodiumEncrypt: %v", err)
	}

	other := New(strings.Repeat("f", 32), testVersion, "aes-256-cbc")
	if _, err := other.SodiumDecrypt(payload, nonce, testKey); nil == err {
		t.Fatal("a different APP_SALT must fail the authenticated decryption")
	}
}

func TestSodiumRejectsWrongNonceLength(t *testing.T) {
	crypter := testCrypter()

	if _, err := crypter.SodiumEncrypt("x", make([]byte, 8), testKey); nil == err {
		t.Fatal("a short nonce must be rejected")
	}
}

func TestOpenSSLRoundTripAllCiphers(t *testing.T) {
	iv := bytes.Repeat([]byte{0x24}, opensslIVSize)

	for _, name := range ListCiphers() {
		crypter := New(testSalt, testVersion, name)

		payload, err := crypter.OpenSSLEncrypt("hello world", iv, testKey)
		if nil != err {
			t.Errorf("%s: OpenSSLEncrypt: %v", name, err)
			continue
		}

		plaintext, err := crypter.OpenSSLDecrypt(payload, iv, testKey)
		if nil != err {
			t.Errorf("%s: OpenSSLDecrypt: %v", name, err)
			continue
		}
		if "hello world" != string(plaintext) {
			t.Errorf("%s: plaintext = %q", name, plaintext)
		}
	}
}

func TestOpenSSLKeyLengths(t *testing.T) {
	tests := map[string]int{
		"aes-128-cbc":             16,
		"aes-128-cbc-hmac-sha1":   16,
		"aes-192-cbc":             24,
		"aes-192-cbc-hmac-sha1":   24,
		"aes-256-cbc":             32,
		"aes-256-cbc-hmac-sha1":   32,
		"aes-256-cbc-hmac-sha256": 32,
	}

	for name, want := range tests {
		got, err := cipherKeySize(name)
		if nil != err {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if want != got {
			t.Errorf("%s: key size = %d, want %d", name, got, want)
		}
	}

	if _, err := cipherKeySize("des-ede3-cbc"); nil == err {
		t.Error("an unsupported cipher must be rejected")
	}
}

func TestOpenSSLMatchesManualAES(t *testing.T) {
	// The key derivation is the documented PHP behaviour: the raw sha1 digest
	// truncated (aes-128) or zero padded (aes-256).
	iv := bytes.Repeat([]byte{0x11}, opensslIVSize)
	passphrase, err := testCrypter().Passphrase(testKey)
	if nil != err {
		t.Fatalf("Passphrase: %v", err)
	}

	for _, test := range []struct {
		cipher string
		keyLen int
	}{
		{"aes-128-cbc", 16},
		{"aes-256-cbc", 32},
	} {
		key := make([]byte, test.keyLen)
		copy(key, passphrase)

		block, err := aes.NewCipher(key)
		if nil != err {
			t.Fatalf("%s: aes.NewCipher: %v", test.cipher, err)
		}
		padded := pkcs7Pad([]byte("payload"), block.BlockSize())
		want := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(want, padded)

		crypter := New(testSalt, testVersion, test.cipher)
		got, err := crypter.OpenSSLEncrypt("payload", iv, testKey)
		if nil != err {
			t.Fatalf("%s: OpenSSLEncrypt: %v", test.cipher, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: ciphertext = %x, want %x", test.cipher, got, want)
		}
	}
}

func TestPKCS7Padding(t *testing.T) {
	for _, size := range []int{0, 1, 15, 16, 17, 31, 32} {
		data := bytes.Repeat([]byte{'x'}, size)
		padded := pkcs7Pad(data, aes.BlockSize)

		if 0 != len(padded)%aes.BlockSize {
			t.Fatalf("size %d: padded length %d is not block aligned", size, len(padded))
		}

		unpadded, err := pkcs7Unpad(padded, aes.BlockSize)
		if nil != err {
			t.Fatalf("size %d: pkcs7Unpad: %v", size, err)
		}
		if !bytes.Equal(unpadded, data) {
			t.Errorf("size %d: round trip = %q, want %q", size, unpadded, data)
		}
	}

	for _, invalid := range [][]byte{
		{},
		{0x00},
		{0x01, 0x02, 0x10},
		bytes.Repeat([]byte{0xff}, aes.BlockSize),
	} {
		if _, err := pkcs7Unpad(invalid, aes.BlockSize); nil == err {
			t.Errorf("pkcs7Unpad(%x) accepted invalid padding", invalid)
		}
	}
}

func TestXxteaRoundTrip(t *testing.T) {
	crypter := testCrypter()
	salt := bytes.Repeat([]byte{0x5a}, xxteaSaltSize)

	inputs := []string{"a", "hello world", strings.Repeat("0123456789", 37)}
	for _, input := range inputs {
		payload, err := crypter.XxteaEncrypt(input, salt, testKey)
		if nil != err {
			t.Fatalf("XxteaEncrypt(%d bytes): %v", len(input), err)
		}

		plaintext, err := crypter.XxteaDecrypt(payload, salt, testKey)
		if nil != err {
			t.Fatalf("XxteaDecrypt(%d bytes): %v", len(input), err)
		}
		if input != string(plaintext) {
			t.Errorf("xxtea round trip = %q, want %q", plaintext, input)
		}
	}
}

func TestXxteaHandlesBinary(t *testing.T) {
	crypter := testCrypter()
	salt := bytes.Repeat([]byte{0x01}, xxteaSaltSize)

	input := string([]byte{0x00, 0x01, 0x02, 0xff, 0x00, 0xfe, 0x7f, 0x80})
	payload, err := crypter.XxteaEncrypt(input, salt, testKey)
	if nil != err {
		t.Fatalf("XxteaEncrypt: %v", err)
	}
	plaintext, err := crypter.XxteaDecrypt(payload, salt, testKey)
	if nil != err {
		t.Fatalf("XxteaDecrypt: %v", err)
	}
	if input != string(plaintext) {
		t.Errorf("binary round trip = %x, want %x", plaintext, input)
	}
}

func TestXxteaRejectsEmptyInput(t *testing.T) {
	crypter := testCrypter()
	salt := bytes.Repeat([]byte{0x01}, xxteaSaltSize)

	if _, err := crypter.XxteaEncrypt("", salt, testKey); nil == err {
		t.Error("empty plaintext must be rejected like PHP's ValueError")
	}
	if _, err := crypter.XxteaDecrypt([]byte("x"), nil, testKey); nil == err {
		t.Error("an empty salt must be rejected")
	}
}

func TestBlobJSONLayout(t *testing.T) {
	blob := Blob{
		Method:  MethodSodium,
		Nonce:   []byte{0x00, 0x01, 0x02},
		Payload: []byte{0xff, 0xff, 0xff},
	}

	// PHP: json_encode(array_map('base64_encode', Encrypt(...))): all three
	// elements are base64 encoded, including the method name, and the "/" of the
	// payload is escaped as "\/\/\/\/" by json_encode().
	want := `["c29kaXVt","AAEC","\/\/\/\/"]`
	if got := blob.JSON(); got != want {
		t.Errorf("Blob.JSON() = %s, want %s", got, want)
	}

	if got := base64.StdEncoding.EncodeToString(blob.Payload); "////" != got {
		t.Fatalf("test vector is wrong: %q", got)
	}
	if got := base64.StdEncoding.EncodeToString([]byte(blob.Method)); "c29kaXVt" != got {
		t.Fatalf("the base64 of the method = %q", got)
	}
}

func TestDecryptFromJSONNeedsABase64Method(t *testing.T) {
	crypter := testCrypter()
	nonce := bytes.Repeat([]byte{0x11}, sodiumNonceSize)

	payload, err := crypter.SodiumEncrypt(`{"a":"b"}`, nonce, testKey)
	if nil != err {
		t.Fatalf("SodiumEncrypt: %v", err)
	}

	blob := Blob{Method: MethodSodium, Nonce: nonce, Payload: payload}

	// A raw (not base64 encoded) method name is not a PHP blob.
	raw := `["sodium","` + base64.StdEncoding.EncodeToString(nonce) + `","` +
		base64.StdEncoding.EncodeToString(payload) + `"]`
	if _, err := crypter.DecryptFromJSON(raw, testKey); nil == err {
		t.Error("a blob with a raw method name must be rejected, like PHP does")
	}

	if _, err := crypter.DecryptFromJSON(blob.JSON(), testKey); nil != err {
		t.Errorf("DecryptFromJSON(Blob.JSON()): %v", err)
	}
}

func TestBlobURLSafeLayout(t *testing.T) {
	blob := Blob{
		Method:  MethodOpenSSL,
		Nonce:   []byte{0xff, 0xfe},
		Payload: []byte{0xff, 0x00, 0xfe},
	}

	encoded := blob.URLSafe()
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("URLSafe = %q, contains a character that must be translated", encoded)
	}

	parts := strings.Split(encoded, ".")
	if 3 != len(parts) {
		t.Fatalf("URLSafe has %d parts, want 3", len(parts))
	}

	nonce, err := URLSafeBase64Decode(parts[1])
	if nil != err {
		t.Fatalf("decode nonce: %v", err)
	}
	if !bytes.Equal(nonce, blob.Nonce) {
		t.Errorf("nonce = %x, want %x", nonce, blob.Nonce)
	}
}

func TestURLSafeBase64RoundTrip(t *testing.T) {
	for _, value := range []string{"", "a", "hello", "\x00\xff\xfe", strings.Repeat("x", 300)} {
		encoded := URLSafeBase64Encode(value)
		if strings.ContainsAny(encoded, "+/=") {
			t.Errorf("%q: encoding %q contains +, / or =", value, encoded)
		}

		decoded, err := URLSafeBase64Decode(encoded)
		if nil != err {
			t.Fatalf("%q: decode: %v", value, err)
		}
		if value != string(decoded) {
			t.Errorf("round trip = %q, want %q", decoded, value)
		}
	}
}

func TestEncryptToJSONRoundTrip(t *testing.T) {
	crypter := testCrypter()
	data := map[string]any{
		"user": "a@example.com",
		"flag": true,
		"list": []any{"x", "y"},
	}

	encoded, err := crypter.EncryptToJSON(data, testKey)
	if nil != err {
		t.Fatalf("EncryptToJSON: %v", err)
	}

	decoded, err := crypter.DecryptFromJSON(encoded, testKey)
	if nil != err {
		t.Fatalf("DecryptFromJSON: %v", err)
	}
	if !reflect.DeepEqual(decoded, data) {
		t.Errorf("decoded = %#v, want %#v", decoded, data)
	}
}

func TestEncryptURLSafeRoundTrip(t *testing.T) {
	crypter := testCrypter()
	data := map[string]any{"account": "a@example.com", "time": int64(1700000000)}

	encoded, err := crypter.EncryptURLSafe(data, testKey)
	if nil != err {
		t.Fatalf("EncryptURLSafe: %v", err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("EncryptURLSafe = %q, contains +, / or =", encoded)
	}

	decoded, err := crypter.DecryptURLSafe(encoded, testKey)
	if nil != err {
		t.Fatalf("DecryptURLSafe: %v", err)
	}
	want := map[string]any{"account": "a@example.com", "time": int64(1700000000)}
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("decoded = %#v, want %#v", decoded, want)
	}
}

func TestEncryptUsingAllMethods(t *testing.T) {
	crypter := testCrypter()
	data := map[string]any{"method": "test"}

	for _, method := range []string{MethodSodium, MethodOpenSSL, MethodXxtea} {
		blob, err := crypter.EncryptUsing(method, data, testKey)
		if nil != err {
			t.Fatalf("%s: EncryptUsing: %v", method, err)
		}
		if method != blob.Method {
			t.Errorf("Blob.Method = %q, want %q", blob.Method, method)
		}

		decoded, err := crypter.Decrypt(blob, testKey)
		if nil != err {
			t.Fatalf("%s: Decrypt: %v", method, err)
		}
		if !reflect.DeepEqual(decoded, data) {
			t.Errorf("%s: decoded = %#v", method, decoded)
		}
	}
}

func TestDecryptRejectsMalformedBlobs(t *testing.T) {
	crypter := testCrypter()

	if _, err := crypter.Decrypt(Blob{}, testKey); nil == err {
		t.Error("an empty method must be rejected")
	}
	if _, err := crypter.Decrypt(Blob{Method: "nope"}, testKey); nil == err {
		t.Error("an unknown method must be rejected")
	}
	if _, err := crypter.DecryptFromJSON("not json", testKey); nil == err {
		t.Error("invalid JSON must be rejected")
	}
	if _, err := crypter.DecryptFromJSON(`["sodium","",""]`, testKey); nil == err {
		t.Error("an empty nonce must be rejected")
	}
	if _, err := crypter.DecryptURLSafe("a.b", testKey); nil == err {
		t.Error("a two part url-safe blob must be rejected")
	}
}

func TestDecryptJSONNumbersMatchPHP(t *testing.T) {
	crypter := testCrypter()
	nonce := bytes.Repeat([]byte{0x07}, sodiumNonceSize)

	payload, err := crypter.SodiumEncrypt(`{"int":1,"float":1.5,"exp":1e3}`, nonce, testKey)
	if nil != err {
		t.Fatalf("SodiumEncrypt: %v", err)
	}

	value, err := crypter.Decrypt(Blob{Method: MethodSodium, Nonce: nonce, Payload: payload}, testKey)
	if nil != err {
		t.Fatalf("Decrypt: %v", err)
	}

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("decoded = %#v, want a map", value)
	}
	if got, ok := object["int"].(int64); !ok || 1 != got {
		t.Errorf("int = %#v, want int64(1)", object["int"])
	}
	if got, ok := object["float"].(float64); !ok || 1.5 != got {
		t.Errorf("float = %#v, want float64(1.5)", object["float"])
	}
	if got, ok := object["exp"].(float64); !ok || 1000 != got {
		t.Errorf("exp = %#v, want float64(1000)", object["exp"])
	}
}

func TestCipherList(t *testing.T) {
	if !CipherSupported(DefaultCipher) {
		t.Errorf("the default cipher %q must be supported", DefaultCipher)
	}
	if CipherSupported("des-ede3-cbc") {
		t.Error("des-ede3-cbc must not be supported")
	}
	if got := len(ListCiphers()); 9 != got {
		t.Errorf("len(ListCiphers()) = %d, want 9", got)
	}

	// ListCiphers returns a copy.
	list := ListCiphers()
	list[0] = "mutated"
	if "mutated" == ListCiphers()[0] {
		t.Error("ListCiphers must return a copy")
	}
}
