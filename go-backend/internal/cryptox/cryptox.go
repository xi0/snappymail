// Package cryptox ports SnappyMail\Crypt and SnappyMail\Cookies.
//
// Encrypted blobs and cookies are a compatibility contract: the Go and PHP
// backends may serve the same browser session during a migration and read/write
// the same sign-me tokens, stored passwords and SSO hashes, so the wire formats
// are ported as-is instead of being redesigned:
//
//   - a blob is a three element array [method, nonce/iv/salt, payload];
//   - "sodium" is XChaCha20-Poly1305 (IETF) with APP_SALT as associated data and
//     a key of 32 bytes built by repeating the raw sha1(passphrase + APP_SALT)
//     digest (PHP's str_pad("", 32, $digest));
//   - "openssl" is AES-128/192/256-CBC with PKCS#7 padding, a 16 byte IV and
//     the raw sha1 digest truncated/zero padded to the key length (the way PHP
//     passes a short key to openssl_encrypt());
//   - "xxtea" is the MailSo\Base\Xxtea variant, kept for decrypting old blobs
//     (Encrypt() still writes it when neither sodium nor OpenSSL is available);
//   - EncryptToJSON() uses base64() in a JSON array exactly like
//     json_encode(array_map('base64_encode', ...)), including the "\/" escaping;
//   - EncryptUrlSafe() joins url-safe base64 without padding with dots.
//
// The package deliberately keeps APP_SALT and APP_VERSION as configuration
// instead of globals, so it is testable and can be wired by the caller.
package cryptox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// DefaultCipher mirrors RainLoop\Config\Application's
// security.encrypt_cipher default; only the OpenSSL fallback uses it.
const DefaultCipher = "aes-256-cbc-hmac-sha1"

// The blob methods, i.e. the first element of the PHP array.
const (
	MethodSodium  = "sodium"
	MethodOpenSSL = "openssl"
	MethodXxtea   = "xxtea"
)

// Sizes of the random values PHP generates in Crypt::Encrypt().
const (
	sodiumNonceSize = chacha20poly1305.NonceSizeX // 24
	sodiumKeySize   = chacha20poly1305.KeySize    // 32
	xxteaSaltSize   = 16
	opensslIVSize   = aes.BlockSize // 16
)

// Errors reported by the package.
var (
	// ErrNoPassphrase is returned when an empty key is used without a
	// DefaultPassphrase callback (the smctoken fallback of Crypt::Passphrase()).
	ErrNoPassphrase = errors.New("cryptox: empty passphrase and no default passphrase")
	// ErrInvalidBlob is returned for malformed JSON/url-safe blobs.
	ErrInvalidBlob = errors.New("cryptox: invalid encrypted data")
	// ErrUnsupportedCipher is returned for an unknown OpenSSL cipher name.
	ErrUnsupportedCipher = errors.New("cryptox: unsupported cipher")
	// ErrInvalidPadding is returned when an AES-CBC payload has bad PKCS#7 padding.
	ErrInvalidPadding = errors.New("cryptox: invalid PKCS#7 padding")
)

// supportedCiphers is the OpenSSL cipher subset Go can reproduce: the AES-CBC
// methods and their -hmac-sha1/-hmac-sha256 aliases. OpenSSL treats those
// aliases as plain AES-CBC for EVP_EncryptUpdate()/EVP_DecryptUpdate(), which
// is what SnappyMail uses them for.
var supportedCiphers = []string{
	"aes-128-cbc",
	"aes-128-cbc-hmac-sha1",
	"aes-128-cbc-hmac-sha256",
	"aes-192-cbc",
	"aes-192-cbc-hmac-sha1",
	"aes-192-cbc-hmac-sha256",
	"aes-256-cbc",
	"aes-256-cbc-hmac-sha1",
	"aes-256-cbc-hmac-sha256",
}

// ListCiphers is SnappyMail\Crypt::listCiphers() restricted to the methods this
// port implements, already sorted (natcasesort).
func ListCiphers() []string {
	list := make([]string, len(supportedCiphers))
	copy(list, supportedCiphers)
	return list
}

// CipherSupported is SnappyMail\Crypt::cipherSupported(): an exact, case
// sensitive match against ListCiphers().
func CipherSupported(cipher string) bool {
	for _, supported := range supportedCiphers {
		if supported == cipher {
			return true
		}
	}
	return false
}

// Blob is the three element PHP array of Crypt::Encrypt(): the method, the
// nonce/IV/salt and the payload.
type Blob struct {
	Method  string
	Nonce   []byte
	Payload []byte
}

// Crypter is the configured SnappyMail\Crypt. It is safe to reuse for many
// requests as long as the fields are not mutated.
type Crypter struct {
	// Salt is APP_SALT, the md5(salt + privateDataName + salt) value.
	Salt string
	// Version is APP_VERSION, appended to the smctoken fallback passphrase.
	Version string
	// Cipher is security.encrypt_cipher, used by the OpenSSL fallback.
	Cipher string
	// DefaultPassphrase returns the key used when the caller passes "". It must
	// return $_COOKIE['smctoken'] . APP_VERSION (creating the cookie when
	// missing), exactly like Crypt::Passphrase(). A nil callback makes an empty
	// key an error.
	DefaultPassphrase func() (string, error)
	// Rand is the randomness source for nonces, IVs and salts. A nil value
	// uses crypto/rand.Reader.
	Rand io.Reader
}

// New builds a Crypter for APP_SALT/APP_VERSION and an OpenSSL cipher.
func New(salt, version, cipher string) *Crypter {
	return &Crypter{Salt: salt, Version: version, Cipher: cipher}
}

// random returns n cryptographically secure random bytes.
func (c *Crypter) random(n int) ([]byte, error) {
	reader := c.Rand
	if nil == reader {
		reader = rand.Reader
	}
	buffer := make([]byte, n)
	if _, err := io.ReadFull(reader, buffer); nil != err {
		return nil, err
	}
	return buffer, nil
}

// Passphrase is Crypt::Passphrase(): the raw 20 byte sha1 of key + APP_SALT.
//
// An empty key falls back to DefaultPassphrase() + APP_VERSION, mirroring the
// smctoken cookie. Unlike PHP this never creates a cookie itself; the callback
// is expected to do that.
func (c *Crypter) Passphrase(key string) ([]byte, error) {
	if "" == key {
		if nil == c.DefaultPassphrase {
			return nil, ErrNoPassphrase
		}
		token, err := c.DefaultPassphrase()
		if nil != err {
			return nil, err
		}
		key = token + c.Version
	}
	digest := sha1.Sum([]byte(key + c.Salt))
	return digest[:], nil
}

// sodiumKey builds the XChaCha20-Poly1305 key: PHP's
// str_pad("", 32, $passphrase) repeats the 20 byte digest until 32 bytes are
// filled (it does not zero pad, despite what the SPEC summary suggests).
func sodiumKey(passphrase []byte) []byte {
	key := make([]byte, sodiumKeySize)
	for index := range key {
		key[index] = passphrase[index%len(passphrase)]
	}
	return key
}

// SodiumEncrypt is Crypt::SodiumEncrypt().
func (c *Crypter) SodiumEncrypt(plaintext string, nonce []byte, key string) ([]byte, error) {
	if len(nonce) != sodiumNonceSize {
		return nil, fmt.Errorf("%w: sodium nonce must be %d bytes", ErrInvalidBlob, sodiumNonceSize)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(sodiumKey(passphrase))
	if nil != err {
		return nil, err
	}
	return aead.Seal(nil, nonce, []byte(plaintext), []byte(c.Salt)), nil
}

// SodiumDecrypt is Crypt::SodiumDecrypt().
func (c *Crypter) SodiumDecrypt(payload, nonce []byte, key string) ([]byte, error) {
	if len(nonce) != sodiumNonceSize {
		return nil, fmt.Errorf("%w: sodium nonce must be %d bytes", ErrInvalidBlob, sodiumNonceSize)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(sodiumKey(passphrase))
	if nil != err {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, payload, []byte(c.Salt))
	if nil != err {
		return nil, fmt.Errorf("cryptox: sodium decrypt: %w", err)
	}
	return plaintext, nil
}

// cipherKeySize returns the OpenSSL key length of cipher.
func cipherKeySize(cipher string) (int, error) {
	if !CipherSupported(cipher) {
		return 0, fmt.Errorf("%w: %q", ErrUnsupportedCipher, cipher)
	}
	switch {
	case strings.Contains(cipher, "aes-128-"):
		return 16, nil
	case strings.Contains(cipher, "aes-192-"):
		return 24, nil
	default:
		return 32, nil
	}
}

// aesBlock builds the AES block cipher with the PHP key derivation: the raw
// sha1 digest, truncated when it is longer than the cipher key and zero padded
// when it is shorter.
func (c *Crypter) aesBlock(passphrase []byte) (cipher.Block, error) {
	size, err := cipherKeySize(c.Cipher)
	if nil != err {
		return nil, err
	}
	key := make([]byte, size)
	copy(key, passphrase)
	return aes.NewCipher(key)
}

// OpenSSLEncrypt is Crypt::OpenSSLEncrypt() (AES-CBC with PKCS#7).
func (c *Crypter) OpenSSLEncrypt(plaintext string, iv []byte, key string) ([]byte, error) {
	if 0 == len(plaintext) || 0 == len(iv) {
		return nil, fmt.Errorf("%w: empty data or iv", ErrInvalidBlob)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	block, err := c.aesBlock(passphrase)
	if nil != err {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("%w: IV must be %d bytes", ErrInvalidBlob, block.BlockSize())
	}
	padded := pkcs7Pad([]byte(plaintext), block.BlockSize())
	output := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(output, padded)
	return output, nil
}

// OpenSSLDecrypt is Crypt::OpenSSLDecrypt().
func (c *Crypter) OpenSSLDecrypt(payload, iv []byte, key string) ([]byte, error) {
	if 0 == len(payload) || 0 == len(iv) {
		return nil, fmt.Errorf("%w: empty data or iv", ErrInvalidBlob)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	block, err := c.aesBlock(passphrase)
	if nil != err {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("%w: IV must be %d bytes", ErrInvalidBlob, block.BlockSize())
	}
	if 0 != len(payload)%block.BlockSize() {
		return nil, fmt.Errorf("%w: ciphertext is not block aligned", ErrInvalidBlob)
	}
	plaintext := make([]byte, len(payload))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, payload)
	return pkcs7Unpad(plaintext, block.BlockSize())
}

// XxteaEncrypt is Crypt::XxteaEncrypt(): the key is salt + Passphrase(key).
func (c *Crypter) XxteaEncrypt(plaintext string, salt []byte, key string) ([]byte, error) {
	if "" == plaintext || 0 == len(salt) {
		return nil, fmt.Errorf("%w: empty data or salt", ErrInvalidBlob)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	fullKey := make([]byte, 0, len(salt)+len(passphrase))
	fullKey = append(fullKey, salt...)
	fullKey = append(fullKey, passphrase...)
	encrypted, err := xxteaEncrypt(plaintext, string(fullKey))
	if nil != err {
		return nil, err
	}
	return []byte(encrypted), nil
}

// XxteaDecrypt is Crypt::XxteaDecrypt().
func (c *Crypter) XxteaDecrypt(payload, salt []byte, key string) ([]byte, error) {
	if 0 == len(payload) || 0 == len(salt) {
		return nil, fmt.Errorf("%w: empty data or salt", ErrInvalidBlob)
	}
	passphrase, err := c.Passphrase(key)
	if nil != err {
		return nil, err
	}
	fullKey := make([]byte, 0, len(salt)+len(passphrase))
	fullKey = append(fullKey, salt...)
	fullKey = append(fullKey, passphrase...)
	decrypted, err := xxteaDecrypt(string(payload), string(fullKey))
	if nil != err {
		return nil, err
	}
	return []byte(decrypted), nil
}

// Encrypt is Crypt::Encrypt(): JSON encode data, then encrypt it with the
// preferred method. Go always has XChaCha20-Poly1305, so "sodium" is used like
// PHP does on a modern build.
func (c *Crypter) Encrypt(data any, key string) (Blob, error) {
	return c.EncryptUsing(MethodSodium, data, key)
}

// EncryptUsing encrypts data with a specific method. It exists for tests and
// for fixtures of the OpenSSL/XXTEA formats; production code uses Encrypt.
func (c *Crypter) EncryptUsing(method string, data any, key string) (Blob, error) {
	plaintext, err := encodeJSON(data)
	if nil != err {
		return Blob{}, err
	}

	switch method {
	case MethodSodium:
		nonce, err := c.random(sodiumNonceSize)
		if nil != err {
			return Blob{}, err
		}
		payload, err := c.SodiumEncrypt(string(plaintext), nonce, key)
		if nil != err {
			return Blob{}, err
		}
		return Blob{Method: method, Nonce: nonce, Payload: payload}, nil

	case MethodOpenSSL:
		iv, err := c.random(opensslIVSize)
		if nil != err {
			return Blob{}, err
		}
		payload, err := c.OpenSSLEncrypt(string(plaintext), iv, key)
		if nil != err {
			return Blob{}, err
		}
		return Blob{Method: method, Nonce: iv, Payload: payload}, nil

	case MethodXxtea:
		salt, err := c.random(xxteaSaltSize)
		if nil != err {
			return Blob{}, err
		}
		payload, err := c.XxteaEncrypt(string(plaintext), salt, key)
		if nil != err {
			return Blob{}, err
		}
		return Blob{Method: method, Nonce: salt, Payload: payload}, nil
	}

	return Blob{}, fmt.Errorf("%w: %q", ErrInvalidBlob, method)
}

// Decrypt is Crypt::Decrypt(): decrypt the blob and JSON decode the plaintext.
func (c *Crypter) Decrypt(blob Blob, key string) (any, error) {
	if "" == blob.Method {
		return nil, ErrInvalidBlob
	}

	var plaintext []byte
	var err error

	switch blob.Method {
	case MethodSodium:
		plaintext, err = c.SodiumDecrypt(blob.Payload, blob.Nonce, key)
	case MethodOpenSSL:
		plaintext, err = c.OpenSSLDecrypt(blob.Payload, blob.Nonce, key)
	case MethodXxtea:
		plaintext, err = c.XxteaDecrypt(blob.Payload, blob.Nonce, key)
	default:
		return nil, fmt.Errorf("%w: unknown method %q", ErrInvalidBlob, blob.Method)
	}
	if nil != err {
		return nil, err
	}

	return decodeJSON(plaintext)
}

// JSON is Crypt::EncryptToJSON()'s body: a JSON array of base64 strings.
func (b Blob) JSON() string {
	return phpJSONArray([]string{
		b.Method,
		base64.StdEncoding.EncodeToString(b.Nonce),
		base64.StdEncoding.EncodeToString(b.Payload),
	})
}

// URLSafe is Crypt::EncryptUrlSafe()'s body: dotted url-safe base64 without
// padding.
func (b Blob) URLSafe() string {
	return strings.Join([]string{
		URLSafeBase64Encode(b.Method),
		URLSafeBase64EncodeBytes(b.Nonce),
		URLSafeBase64EncodeBytes(b.Payload),
	}, ".")
}

// EncryptToJSON is Crypt::EncryptToJSON().
func (c *Crypter) EncryptToJSON(data any, key string) (string, error) {
	blob, err := c.Encrypt(data, key)
	if nil != err {
		return "", err
	}
	return blob.JSON(), nil
}

// DecryptFromJSON is Crypt::DecryptFromJSON().
func (c *Crypter) DecryptFromJSON(data, key string) (any, error) {
	var parts []string
	if err := json.Unmarshal([]byte(data), &parts); nil != err {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBlob, err)
	}

	blob, err := blobFromParts(parts)
	if nil != err {
		return nil, err
	}
	return c.Decrypt(blob, key)
}

// EncryptURLSafe is Crypt::EncryptUrlSafe().
func (c *Crypter) EncryptURLSafe(data any, key string) (string, error) {
	blob, err := c.Encrypt(data, key)
	if nil != err {
		return "", err
	}
	return blob.URLSafe(), nil
}

// DecryptURLSafe is Crypt::DecryptUrlSafe().
func (c *Crypter) DecryptURLSafe(data, key string) (any, error) {
	parts := strings.Split(data, ".")
	if 3 != len(parts) {
		return nil, ErrInvalidBlob
	}

	decoded := make([]string, 3)
	for index, part := range parts {
		value, err := URLSafeBase64Decode(part)
		if nil != err {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBlob, err)
		}
		decoded[index] = string(value)
	}

	blob := Blob{Method: decoded[0], Nonce: []byte(decoded[1]), Payload: []byte(decoded[2])}
	return c.Decrypt(blob, key)
}

// blobFromParts builds a Blob from the three strings of DecryptFromJSON().
func blobFromParts(parts []string) (Blob, error) {
	if 3 != len(parts) || "" == parts[0] {
		return Blob{}, ErrInvalidBlob
	}
	nonce, err := base64Decode(parts[1])
	if nil != err {
		return Blob{}, fmt.Errorf("%w: %v", ErrInvalidBlob, err)
	}
	payload, err := base64Decode(parts[2])
	if nil != err {
		return Blob{}, fmt.Errorf("%w: %v", ErrInvalidBlob, err)
	}
	return Blob{Method: parts[0], Nonce: nonce, Payload: payload}, nil
}

// URLSafeBase64Encode is MailSo\Base\Utils::UrlSafeBase64Encode().
func URLSafeBase64Encode(value string) string {
	return URLSafeBase64EncodeBytes([]byte(value))
}

// URLSafeBase64EncodeBytes is UrlSafeBase64Encode() for binary data.
func URLSafeBase64EncodeBytes(value []byte) string {
	encoded := base64.StdEncoding.EncodeToString(value)
	encoded = strings.NewReplacer("+", "-", "/", "_").Replace(encoded)
	return strings.TrimRight(encoded, "=")
}

// URLSafeBase64Decode is MailSo\Base\Utils::UrlSafeBase64Decode().
func URLSafeBase64Decode(value string) ([]byte, error) {
	return base64Decode(strings.NewReplacer("-", "+", "_", "/").Replace(value))
}

// base64Decode is base64_decode() (non strict): characters outside the base64
// alphabet are ignored and missing padding is added.
func base64Decode(value string) ([]byte, error) {
	cleaned := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case 'A' <= character && 'Z' >= character,
			'a' <= character && 'z' >= character,
			'0' <= character && '9' >= character,
			'+' == character, '/' == character, '=' == character:
			cleaned = append(cleaned, character)
		}
	}

	if remainder := len(cleaned) % 4; 0 != remainder {
		cleaned = append(cleaned, []byte(strings.Repeat("=", 4-remainder))...)
	}

	return base64.StdEncoding.DecodeString(string(cleaned))
}

// phpJSONArray renders a JSON array of strings the way PHP's json_encode()
// does by default: every "/" is escaped as "\/" (the difference matters for
// byte-identical cookies).
func phpJSONArray(values []string) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if 0 < index {
			builder.WriteByte(',')
		}
		builder.WriteByte('"')
		for position := 0; position < len(value); position++ {
			switch character := value[position]; character {
			case '"':
				builder.WriteString(`\"`)
			case '\\':
				builder.WriteString(`\\`)
			case '/':
				builder.WriteString(`\/`)
			case '\b':
				builder.WriteString(`\b`)
			case '\f':
				builder.WriteString(`\f`)
			case '\n':
				builder.WriteString(`\n`)
			case '\r':
				builder.WriteString(`\r`)
			case '\t':
				builder.WriteString(`\t`)
			default:
				if 0x20 > character {
					builder.WriteString(fmt.Sprintf(`\u%04x`, character))
				} else {
					builder.WriteByte(character)
				}
			}
		}
		builder.WriteByte('"')
	}
	builder.WriteByte(']')
	return builder.String()
}

// encodeJSON is json_encode() without the trailing newline of json.Encoder.
// HTML characters stay unescaped (PHP does not escape them either); slashes are
// left alone because the plaintext is encrypted and only decoded again.
func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); nil != err {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// decodeJSON is json_decode($data, true) with PHP's number types: integers
// become int64, everything with a decimal point/exponent becomes float64.
func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); nil != err {
		return nil, err
	}

	// PHP's JSON_THROW_ON_ERROR rejects trailing data as well.
	if err := decoder.Decode(new(any)); io.EOF != err {
		if nil == err {
			return nil, errors.New("cryptox: trailing JSON data")
		}
		return nil, err
	}

	return normalizeNumbers(value), nil
}

// normalizeNumbers recursively converts json.Number into PHP's int64/float64.
func normalizeNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := string(typed)
		if !strings.ContainsAny(text, ".eE") {
			if number, err := strconv.ParseInt(text, 10, 64); nil == err {
				return number
			}
		}
		if number, err := strconv.ParseFloat(text, 64); nil == err {
			return number
		}
		return text
	case []any:
		for index := range typed {
			typed[index] = normalizeNumbers(typed[index])
		}
		return typed
	case map[string]any:
		for key := range typed {
			typed[key] = normalizeNumbers(typed[key])
		}
		return typed
	}
	return value
}

// pkcs7Pad pads data to a multiple of blockSize (openssl_encrypt default).
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padded := make([]byte, len(data)+padding)
	copy(padded, data)
	for index := len(data); index < len(padded); index++ {
		padded[index] = byte(padding)
	}
	return padded
}

// pkcs7Unpad removes and validates the PKCS#7 padding of a decrypted block.
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if 0 == len(data) || 0 != len(data)%blockSize {
		return nil, ErrInvalidPadding
	}

	padding := int(data[len(data)-1])
	if 1 > padding || blockSize < padding || len(data) < padding {
		return nil, ErrInvalidPadding
	}

	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return nil, ErrInvalidPadding
		}
	}

	return data[:len(data)-padding], nil
}
