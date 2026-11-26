package smime

// The private key plumbing of the S/MIME package.
//
// PHP hands private keys to (and receives them from) the openssl extension, so
// the PEM documents a SnappyMail installation has stored in its identities must
// keep working. The encodings that show up in practice are all read:
//
//   - "-----BEGIN PRIVATE KEY-----"      unencrypted PKCS#8 (the modern export),
//   - "-----BEGIN RSA PRIVATE KEY-----"  unencrypted PKCS#1,
//   - "-----BEGIN ENCRYPTED PRIVATE KEY-----" PKCS#8 PBES2 (PBKDF2 + AES-CBC),
//     which is what openssl_pkey_export() writes for a passphrase, and
//   - "-----BEGIN RSA PRIVATE KEY-----" with a Proc-Type/DEK-Info header, the
//     legacy OpenSSL PEM encryption.
//
// Exported keys are written the way openssl_pkey_export() writes them: PKCS#8,
// encrypted with PBES2 (PBKDF2-HMAC-SHA256, 2048 iterations, AES-256-CBC) when a
// passphrase was given and unencrypted otherwise (PHP only encrypts for a
// non-empty passphrase).

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"os"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// The algorithm identifiers of the PKCS#8 PBES2 documents.
var (
	oidPBES2          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACWithSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACWithSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
	oidAES128CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

// The PBES2 parameters openssl_pkey_export() uses (OpenSSL 3 defaults).
const (
	// pbes2Iterations is the PBKDF2 iteration count.
	pbes2Iterations = 2048
	// pbes2SaltLength is PKCS5_SALT_LEN.
	pbes2SaltLength = 16
	// pbes2KeyLength is the AES-256 key size.
	pbes2KeyLength = 32
)

// The PEM block types.
const (
	pemTypePrivateKey          = "PRIVATE KEY"
	pemTypeEncryptedPrivateKey = "ENCRYPTED PRIVATE KEY"
	pemTypePKCS7               = "PKCS7"
)

// algorithmIdentifier is the ASN.1 AlgorithmIdentifier of RFC 5652.
type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

// pbkdf2Params is RFC 8018 PBKDF2-params; the PRF defaults to hmacWithSHA1 and
// the key length to the cipher key size.
type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                 `asn1:"optional"`
	PRF        algorithmIdentifier `asn1:"optional"`
}

// pbes2Params is RFC 8018 PBES2-params.
type pbes2Params struct {
	KeyDerivationFunc algorithmIdentifier
	EncryptionScheme  algorithmIdentifier
}

// encryptedPrivateKeyInfo is RFC 5958 EncryptedPrivateKeyInfo.
type encryptedPrivateKeyInfo struct {
	EncryptionAlgorithm algorithmIdentifier
	EncryptedData       []byte
}

// nullRawValue is the ASN.1 NULL some AlgorithmIdentifier parameters use.
var nullRawValue = asn1.RawValue{Tag: 5}

// readFile reads a whole file, the Go equivalent of file_get_contents().
func readFile(name string) (string, error) {
	content, err := os.ReadFile(name)
	if nil != err {
		return "", err
	}
	return string(content), nil
}

// hashByName maps an openssl_x509_fingerprint() algorithm name onto its hash.
func hashByName(name string) (func([]byte) []byte, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sha1":
		return func(value []byte) []byte {
			sum := sha1.Sum(value)
			return sum[:]
		}, nil
	case "sha256":
		return func(value []byte) []byte {
			sum := sha256.Sum256(value)
			return sum[:]
		}, nil
	case "sha512":
		return func(value []byte) []byte {
			sum := sha512.Sum512(value)
			return sum[:]
		}, nil
	case "md5":
		return func(value []byte) []byte {
			sum := md5.Sum(value)
			return sum[:]
		}, nil
	}
	return nil, fmt.Errorf("unknown digest algorithm %q", name)
}

// parsePrivateKey is openssl_pkey_get_private(): a PEM private key, decrypted
// with passphrase when it is encrypted.
func parsePrivateKey(value, passphrase string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(value)))
	if nil == block {
		return nil, errors.New("not a PEM encoded private key")
	}

	der := block.Bytes

	switch {
	case pemTypeEncryptedPrivateKey == block.Type:
		decrypted, err := decryptPKCS8(der, passphrase)
		if nil != err {
			return nil, err
		}
		der = decrypted
	case strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED"):
		// The legacy "DEK-Info" PEM encryption of old openssl_pkey_export().
		decrypted, err := x509.DecryptPEMBlock(block, []byte(passphrase))
		if nil != err {
			return nil, err
		}
		der = decrypted
	}

	return parsePrivateKeyDER(der)
}

// parsePrivateKeyDER parses a PKCS#8, PKCS#1 or SEC 1 private key.
func parsePrivateKeyDER(der []byte) (crypto.Signer, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); nil == err {
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, errors.New("unsupported private key type")
		}
		return signer, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); nil == err {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); nil == err {
		return key, nil
	}
	return nil, errors.New("unsupported private key encoding")
}

// exportPrivateKey is openssl_pkey_export(): PKCS#8 PEM, encrypted with
// passphrase when one was given.
func exportPrivateKey(signer crypto.Signer, passphrase string) (string, error) {
	if nil == signer {
		return "", errors.New("OpenSSL exportPrivateKey: key not loaded")
	}

	der, err := x509.MarshalPKCS8PrivateKey(signer)
	if nil != err {
		return "", fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	if "" == passphrase {
		return string(pem.EncodeToMemory(&pem.Block{Type: pemTypePrivateKey, Bytes: der})), nil
	}

	encrypted, err := encryptPKCS8(der, passphrase)
	if nil != err {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: pemTypeEncryptedPrivateKey, Bytes: encrypted})), nil
}

// encryptPKCS8 writes an EncryptedPrivateKeyInfo (PBES2: PBKDF2-HMAC-SHA256 +
// AES-256-CBC), the shape openssl_pkey_export() produces.
func encryptPKCS8(der []byte, passphrase string) ([]byte, error) {
	salt := make([]byte, pbes2SaltLength)
	if _, err := rand.Read(salt); nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	block, err := aes.NewCipher(pbkdf2.Key([]byte(passphrase), salt, pbes2Iterations, pbes2KeyLength, sha256.New))
	if nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	padded := padPKCS7(der, aes.BlockSize)
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, padded)

	kdfParams, err := asn1.Marshal(pbkdf2Params{
		Salt:       salt,
		Iterations: pbes2Iterations,
		PRF:        algorithmIdentifier{Algorithm: oidHMACWithSHA256, Parameters: nullRawValue},
	})
	if nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	ivDER, err := asn1.Marshal(iv)
	if nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	params, err := asn1.Marshal(pbes2Params{
		KeyDerivationFunc: algorithmIdentifier{
			Algorithm:  oidPBKDF2,
			Parameters: asn1.RawValue{FullBytes: kdfParams},
		},
		EncryptionScheme: algorithmIdentifier{
			Algorithm:  oidAES256CBC,
			Parameters: asn1.RawValue{FullBytes: ivDER},
		},
	})
	if nil != err {
		return nil, fmt.Errorf("OpenSSL exportPrivateKey: %w", err)
	}

	return asn1.Marshal(encryptedPrivateKeyInfo{
		EncryptionAlgorithm: algorithmIdentifier{
			Algorithm:  oidPBES2,
			Parameters: asn1.RawValue{FullBytes: params},
		},
		EncryptedData: encrypted,
	})
}

// decryptPKCS8 reads an EncryptedPrivateKeyInfo and returns the plaintext
// PrivateKeyInfo DER.
func decryptPKCS8(der []byte, passphrase string) ([]byte, error) {
	var info encryptedPrivateKeyInfo
	if _, err := asn1.Unmarshal(der, &info); nil != err {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}

	if !info.EncryptionAlgorithm.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: unsupported key encryption algorithm %v",
			info.EncryptionAlgorithm.Algorithm)
	}

	var params pbes2Params
	if _, err := asn1.Unmarshal(info.EncryptionAlgorithm.Parameters.FullBytes, &params); nil != err {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}
	if !params.KeyDerivationFunc.Algorithm.Equal(oidPBKDF2) {
		return nil, errors.New("OpenSSL setPrivateKey: unsupported key derivation function")
	}

	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KeyDerivationFunc.Parameters.FullBytes, &kdf); nil != err {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}

	var prf func() hash.Hash = sha1.New
	if 0 != len(kdf.PRF.Algorithm) {
		switch {
		case kdf.PRF.Algorithm.Equal(oidHMACWithSHA1):
			prf = sha1.New
		case kdf.PRF.Algorithm.Equal(oidHMACWithSHA256):
			prf = sha256.New
		case kdf.PRF.Algorithm.Equal(oidHMACWithSHA512):
			prf = sha512.New
		default:
			return nil, fmt.Errorf("OpenSSL setPrivateKey: unsupported PRF %v", kdf.PRF.Algorithm)
		}
	}

	keyLength := pbes2KeyLength
	switch {
	case params.EncryptionScheme.Algorithm.Equal(oidAES128CBC):
		keyLength = 16
	case params.EncryptionScheme.Algorithm.Equal(oidAES192CBC):
		keyLength = 24
	case params.EncryptionScheme.Algorithm.Equal(oidAES256CBC):
		keyLength = 32
	default:
		return nil, fmt.Errorf("OpenSSL setPrivateKey: unsupported cipher %v",
			params.EncryptionScheme.Algorithm)
	}
	if 0 < kdf.KeyLength {
		keyLength = kdf.KeyLength
	}

	var iv []byte
	if _, err := asn1.Unmarshal(params.EncryptionScheme.Parameters.FullBytes, &iv); nil != err {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}

	block, err := aes.NewCipher(pbkdf2.Key([]byte(passphrase), kdf.Salt, kdf.Iterations, keyLength, prf))
	if nil != err {
		return nil, fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}
	if block.BlockSize() != len(iv) || 0 != len(info.EncryptedData)%block.BlockSize() {
		return nil, errors.New("OpenSSL setPrivateKey: invalid encrypted key")
	}

	plain := make([]byte, len(info.EncryptedData))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, info.EncryptedData)

	return unpadPKCS7(plain, block.BlockSize())
}

// publicKeysEqual is openssl_x509_check_private_key(): the key belongs to the
// certificate.
func publicKeysEqual(certificate *x509.Certificate, key crypto.Signer) bool {
	if nil == certificate || nil == key {
		return false
	}

	certDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if nil != err {
		return false
	}
	keyDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if nil != err {
		return false
	}

	return 0 == strings.Compare(string(certDER), string(keyDER))
}

// padPKCS7 appends the PKCS#7 padding of a block cipher.
func padPKCS7(value []byte, blockSize int) []byte {
	padding := blockSize - len(value)%blockSize
	result := make([]byte, len(value)+padding)
	copy(result, value)
	for index := len(value); index < len(result); index++ {
		result[index] = byte(padding)
	}
	return result
}

// unpadPKCS7 strips and validates the PKCS#7 padding of a block cipher.
func unpadPKCS7(value []byte, blockSize int) ([]byte, error) {
	if 0 == len(value) || 0 != len(value)%blockSize {
		return nil, errors.New("invalid encrypted key length")
	}

	padding := int(value[len(value)-1])
	if 1 > padding || padding > blockSize || padding > len(value) {
		return nil, errors.New("invalid encrypted key padding")
	}
	for _, item := range value[len(value)-padding:] {
		if int(item) != padding {
			return nil, errors.New("invalid encrypted key padding")
		}
	}

	return value[:len(value)-padding], nil
}
