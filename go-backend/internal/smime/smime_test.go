package smime

// Tests of the step 26 S/MIME engine: the self-signed certificate of
// SnappyMail\SMime\Certificate, the PKCS#8 private key exports, the ".smime"
// certificate store of SnappyMail\SMime\OpenSSL and the PKCS#7 decrypt/verify.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smallstep/pkcs7"
)

// testBits keeps the generated keys small enough for a unit test.
const testBits = 2048

// testPassphrase is the passphrase of the encrypted test keys.
const testPassphrase = "s3cret"

// testAuthorityPath is the bundled CA of the PHP library, relative to this
// package.
const testAuthorityPath = "../../../snappymail/v/0.0.0/app/libraries/snappymail/smime"

// newTestCertificate creates a self-signed certificate for test@example.com.
func newTestCertificate(t *testing.T) (*Certificate, *x509.Certificate, *rsa.PrivateKey) {
	t.Helper()

	certificate, err := NewCertificate("", "")
	if nil != err {
		t.Fatalf("NewCertificate: %v", err)
	}
	certificate.KeyBits = testBits
	certificate.DistinguishedName[DNCommonName] = "Test User"
	certificate.DistinguishedName[DNEmailAddress] = "test@example.com"

	_, err = certificate.CreateSelfSigned(testPassphrase, "")
	if nil != err {
		t.Fatalf("CreateSelfSigned: %v", err)
	}

	key, ok := certificate.Pkey.(*rsa.PrivateKey)
	if !ok {
		t.Fatalf("Pkey = %T", certificate.Pkey)
	}

	return certificate, certificate.X509, key
}

// TestCreateSelfSigned checks the document of Certificate::createSelfSigned()
// and the properties the S/MIME certificates of SnappyMail have.
func TestCreateSelfSigned(t *testing.T) {
	certificate, err := NewCertificate("", "")
	if nil != err {
		t.Fatalf("NewCertificate: %v", err)
	}
	certificate.KeyBits = testBits
	certificate.DistinguishedName[DNCommonName] = "Test User"
	certificate.DistinguishedName[DNEmailAddress] = "test@example.com"

	result, err := certificate.CreateSelfSigned(testPassphrase, "")
	if nil != err {
		t.Fatalf("CreateSelfSigned: %v", err)
	}

	// The PHP array is {pkey, x509}.
	if 2 != len(result) {
		t.Fatalf("result = %#v", result)
	}
	if !strings.HasPrefix(result["pkey"], "-----BEGIN ENCRYPTED PRIVATE KEY-----") {
		t.Errorf("pkey = %.40q", result["pkey"])
	}
	if !strings.HasPrefix(result["x509"], "-----BEGIN CERTIFICATE-----") {
		t.Errorf("x509 = %.40q", result["x509"])
	}

	// The key is readable with the passphrase, and not without it.
	if _, err := parsePrivateKey(result["pkey"], testPassphrase); nil != err {
		t.Errorf("parsePrivateKey(passphrase): %v", err)
	}
	if _, err := parsePrivateKey(result["pkey"], "wrong"); nil == err {
		t.Error("a wrong passphrase must not open the key")
	}

	parsed, err := ParseCertificate(result["x509"])
	if nil != err {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if "Test User" != parsed.Subject.CommonName {
		t.Errorf("CN = %q", parsed.Subject.CommonName)
	}
	if "test@example.com" != EmailAddressOf(parsed) {
		t.Errorf("emailAddress = %q", EmailAddressOf(parsed))
	}
	if 0 == len(parsed.SubjectKeyId) {
		t.Error("the certificate has no subjectKeyIdentifier")
	}
	// openssl.cnf [snappymail_ca]: basicConstraints = critical,CA:true and
	// extendedKeyUsage = clientAuth, emailProtection.
	if !parsed.IsCA || !parsed.BasicConstraintsValid {
		t.Errorf("basic constraints = %v/%v", parsed.IsCA, parsed.BasicConstraintsValid)
	}
	if !CanSign(parsed) || !CanEncrypt(parsed) {
		t.Errorf("purposes = sign:%v encrypt:%v", CanSign(parsed), CanEncrypt(parsed))
	}
	// 1185 days.
	if days := int(parsed.NotAfter.Sub(parsed.NotBefore).Hours() / 24); 1185 != days {
		t.Errorf("validity = %d days", days)
	}
}

// TestCreateSelfSignedWithAuthority checks that the bundled SnappyMail CA signs
// the certificate when its files are readable (the PHP behaviour).
func TestCreateSelfSignedWithAuthority(t *testing.T) {
	authority := LoadAuthority(testAuthorityPath)
	if nil == authority {
		t.Skipf("the bundled CA of %s is not available", testAuthorityPath)
	}

	certificate, err := NewCertificate("", "")
	if nil != err {
		t.Fatalf("NewCertificate: %v", err)
	}
	certificate.KeyBits = testBits
	certificate.SetAuthority(authority)
	certificate.DistinguishedName[DNCommonName] = "Test User"
	certificate.DistinguishedName[DNEmailAddress] = "test@example.com"

	result, err := certificate.CreateSelfSigned("", "")
	if nil != err {
		t.Fatalf("CreateSelfSigned: %v", err)
	}
	// No passphrase: openssl_pkey_export() writes an unencrypted PKCS#8 key.
	if !strings.HasPrefix(result["pkey"], "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("pkey = %.40q", result["pkey"])
	}

	parsed, err := ParseCertificate(result["x509"])
	if nil != err {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if "snappymail.eu" != parsed.Issuer.CommonName {
		t.Errorf("issuer = %q", parsed.Issuer.CommonName)
	}
}

// TestExportPrivateKey checks the PKCS#8 exports of both passphrase forms.
func TestExportPrivateKey(t *testing.T) {
	certificate, _, key := newTestCertificate(t)

	plain, err := exportPrivateKey(key, "")
	if nil != err {
		t.Fatalf("exportPrivateKey: %v", err)
	}
	if !strings.HasPrefix(plain, "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("plain = %.40q", plain)
	}

	block, _ := pem.Decode([]byte(plain))
	if nil == block {
		t.Fatal("the exported key is not PEM")
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); nil != err {
		t.Errorf("ParsePKCS8PrivateKey: %v", err)
	}

	encrypted, err := exportPrivateKey(key, testPassphrase)
	if nil != err {
		t.Fatalf("exportPrivateKey(encrypted): %v", err)
	}

	// openssl_pkey_get_private() with the passphrase, then the round trip.
	parsed, err := parsePrivateKey(encrypted, testPassphrase)
	if nil != err {
		t.Fatalf("parsePrivateKey: %v", err)
	}
	if !publicKeysEqual(certificate.X509, parsed) {
		t.Error("the parsed key does not belong to the certificate")
	}

	// The legacy PEM encryption of old OpenSSL versions.
	legacy, err := x509.EncryptPEMBlock(
		rand.Reader,
		"RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), []byte(testPassphrase),
		x509.PEMCipherAES256,
	)
	if nil != err {
		t.Fatalf("EncryptPEMBlock: %v", err)
	}
	if _, err := parsePrivateKey(string(pem.EncodeToMemory(legacy)), testPassphrase); nil != err {
		t.Errorf("parsePrivateKey(legacy): %v", err)
	}
}

// TestStoreSignature checks the Identifier() file stem of a stored certificate.
func TestStoreSignature(t *testing.T) {
	store := NewStore(t.TempDir())

	certificate, _, _ := newTestCertificate(t)
	pemBlock, err := certificate.Export()
	if nil != err {
		t.Fatalf("Export: %v", err)
	}

	if ok := store.StoreCertificate(pemBlock); !ok {
		t.Fatal("StoreCertificate = false")
	}

	// "<subjectKeyIdentifier>.crt" with uppercase hex and no colons.
	id := Identifier(certificate.X509)
	expected := filepath.Join(store.Homedir(), id+".crt")
	if content, err := os.ReadFile(expected); nil != err || string(content) != pemBlock {
		t.Fatalf("stored file %s: %v", expected, err)
	}
	if strings.ContainsRune(id, ':') || strings.ToUpper(id) != id {
		t.Errorf("id = %q", id)
	}

	// Storing twice is a no-op.
	if ok := store.StoreCertificate(pemBlock); !ok {
		t.Error("the second StoreCertificate failed")
	}
}

// TestCertificates checks the certificates() document and its cache.
func TestCertificates(t *testing.T) {
	store := NewStore(t.TempDir())

	certificate, cert, key := newTestCertificate(t)
	certificatePEM, err := certificate.Export()
	if nil != err {
		t.Fatalf("Export: %v", err)
	}
	if ok := store.StoreCertificate(certificatePEM); !ok {
		t.Fatal("StoreCertificate = false")
	}

	list, err := store.Certificates(false)
	if nil != err {
		t.Fatalf("Certificates: %v", err)
	}
	if 1 != len(list) {
		t.Fatalf("list = %+v", list)
	}

	entry := list[0]
	if Identifier(cert)+".crt" != entry.File || Identifier(cert) != entry.ID {
		t.Errorf("file/id = %q/%q", entry.File, entry.ID)
	}
	if "Test User" != entry.CN || "test@example.com" != entry.EmailAddress {
		t.Errorf("CN/emailAddress = %v/%v", entry.CN, entry.EmailAddress)
	}
	if cert.NotAfter.Unix() != entry.ValidToTimeT {
		t.Errorf("validTo_time_t = %d", entry.ValidToTimeT)
	}
	if !entry.SmimeSign || !entry.SmimeEncrypt {
		t.Errorf("purposes = %+v", entry)
	}
	if nil != entry.PrivateKey {
		t.Errorf("privateKey = %v", entry.PrivateKey)
	}

	// The certificates.json cache PHP keeps.
	if _, err := os.Stat(filepath.Join(store.Homedir(), cacheFileName)); nil != err {
		t.Fatalf("cache file: %v", err)
	}

	// An unencrypted PKCS#8 key next to it is matched to the certificate.
	keyPEM, err := exportPrivateKey(key, "")
	if nil != err {
		t.Fatalf("exportPrivateKey: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Homedir(), "test.key"), []byte(keyPEM), 0o600); nil != err {
		t.Fatalf("write key: %v", err)
	}

	list, err = store.Certificates(true)
	if nil != err {
		t.Fatalf("Certificates(force): %v", err)
	}
	if "test.key" != list[0].PrivateKey {
		t.Errorf("privateKey = %v", list[0].PrivateKey)
	}

	// An encrypted key cannot be matched ("Can't check ENCRYPTED PRIVATE KEY").
	encrypted, err := exportPrivateKey(key, testPassphrase)
	if nil != err {
		t.Fatalf("exportPrivateKey(encrypted): %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.Homedir(), "enc.key"), []byte(encrypted), 0o600); nil != err {
		t.Fatalf("write encrypted key: %v", err)
	}
	if err := os.Remove(filepath.Join(store.Homedir(), "test.key")); nil != err {
		t.Fatalf("remove key: %v", err)
	}

	list, err = store.Certificates(true)
	if nil != err {
		t.Fatalf("Certificates(force): %v", err)
	}
	if nil != list[0].PrivateKey {
		t.Errorf("privateKey = %v, want null for an encrypted key", list[0].PrivateKey)
	}
}

// TestStoreDecrypt checks the decrypt() of an application/pkcs7-mime part.
func TestStoreDecrypt(t *testing.T) {
	store := NewStore(t.TempDir())

	certificate, cert, key := newTestCertificate(t)
	certificatePEM, err := certificate.Export()
	if nil != err {
		t.Fatalf("Export: %v", err)
	}

	const plaintext = "Content-Type: text/plain\r\n\r\nHello S/MIME\r\n"

	der, err := pkcs7.Encrypt([]byte(plaintext), []*x509.Certificate{cert})
	if nil != err {
		t.Fatalf("pkcs7.Encrypt: %v", err)
	}

	body := "Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		foldBase64(base64.StdEncoding.EncodeToString(der))

	if err := store.SetCertificate(certificatePEM); nil != err {
		t.Fatalf("SetCertificate: %v", err)
	}
	if err := store.SetPrivateKey(exportedKey(t, key, testPassphrase), testPassphrase); nil != err {
		t.Fatalf("SetPrivateKey: %v", err)
	}

	decrypted, err := store.Decrypt(body)
	if nil != err {
		t.Fatalf("Decrypt: %v", err)
	}
	if plaintext != decrypted {
		t.Errorf("Decrypt = %q", decrypted)
	}

	// A body without PKCS#7 data is an error, like a failing openssl_pkcs7_decrypt.
	if _, err := store.Decrypt("nothing useful"); nil == err {
		t.Error("Decrypt of garbage must fail")
	}
}

// TestStoreVerify checks the verify() of a detached multipart/signed entity and
// of an opaque signed-data part.
func TestStoreVerify(t *testing.T) {
	store := NewStore(t.TempDir())

	_, cert, key := newTestCertificate(t)

	signed, err := pkcs7.NewSignedData([]byte(detachedContent))
	if nil != err {
		t.Fatalf("NewSignedData: %v", err)
	}
	if err := signed.AddSigner(cert, key, pkcs7.SignerInfoConfig{}); nil != err {
		t.Fatalf("AddSigner: %v", err)
	}
	signed.Detach()
	der, err := signed.Finish()
	if nil != err {
		t.Fatalf("Finish: %v", err)
	}

	// A detached signature: multipart/signed with the signature in the second
	// part, exactly as DoSendMessage() renders it.
	detachedBody := "Content-Type: multipart/signed; micalg=\"sha-256\"; " +
		"protocol=\"application/pkcs7-signature\"; boundary=\"--smime-test\"\r\n\r\n" +
		"--" + "--smime-test\r\n" +
		detachedContent +
		"\r\n--" + "--smime-test\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"signature.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		foldBase64(base64.StdEncoding.EncodeToString(der)) +
		"\r\n--" + "--smime-test--\r\n"

	result, err := store.Verify(detachedBody, false)
	if nil != err {
		t.Fatalf("Verify(detached): %v", err)
	}
	if !result.Success || result.Opaque || "" != result.Body {
		t.Errorf("Verify(detached) = %+v", result)
	}

	// Opaque signed-data (application/pkcs7-mime): the content is extracted.
	opaque, err := pkcs7.NewSignedData([]byte(signedContent))
	if nil != err {
		t.Fatalf("NewSignedData: %v", err)
	}
	if err := opaque.AddSigner(cert, key, pkcs7.SignerInfoConfig{}); nil != err {
		t.Fatalf("AddSigner: %v", err)
	}
	opaqueDER, err := opaque.Finish()
	if nil != err {
		t.Fatalf("Finish: %v", err)
	}

	opaqueBody := "Content-Type: application/pkcs7-mime; smime-type=signed-data; name=\"smime.p7m\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		foldBase64(base64.StdEncoding.EncodeToString(opaqueDER))

	result, err = store.Verify(opaqueBody, true)
	if nil != err {
		t.Fatalf("Verify(opaque): %v", err)
	}
	if !result.Success || !result.Opaque {
		t.Fatalf("Verify(opaque) = %+v", result)
	}
	if signedContent != result.Body {
		t.Errorf("Verify(opaque).Body = %q", result.Body)
	}

	// The certificates of the signature part, for the automatic import.
	certificates := CertificatesOfPKCS7(foldBase64(base64.StdEncoding.EncodeToString(opaqueDER)))
	if 1 != len(certificates) {
		t.Fatalf("CertificatesOfPKCS7 = %d certificates", len(certificates))
	}
	if parsed, err := ParseCertificate(certificates[0]); nil != err ||
		Identifier(parsed) != Identifier(cert) {
		t.Errorf("CertificatesOfPKCS7[0] = %v", err)
	}

	// A tampered content must not verify.
	if _, err := store.Verify(strings.Replace(detachedBody, "Hello", "Jello", 1), false); nil == err {
		t.Error("a tampered detached content must not verify")
	}
}

// The content of the signed fixtures.
const (
	signedContent = "Content-Type: text/plain\r\n\r\nHello S/MIME\r\n"
	// detachedContent is the first part of the multipart/signed entity; it is
	// the byte range the signature covers.
	detachedContent = "Content-Type: text/plain\r\n\r\nHello S/MIME"
)

// exportedKey is the PEM private key of a test certificate.
func exportedKey(t *testing.T, key *rsa.PrivateKey, passphrase string) string {
	t.Helper()

	exported, err := exportPrivateKey(key, passphrase)
	if nil != err {
		t.Fatalf("exportPrivateKey: %v", err)
	}
	return exported
}

// foldBase64 wraps the base64 output at 76 columns, like an S/MIME part.
func foldBase64(value string) string {
	return strings.Join(splitEvery(value, 76), "\r\n") + "\r\n"
}

// splitEvery splits value into chunks of size bytes.
func splitEvery(value string, size int) []string {
	chunks := make([]string, 0, len(value)/size+1)
	for len(value) > size {
		chunks = append(chunks, value[:size])
		value = value[size:]
	}
	if 0 < len(value) {
		chunks = append(chunks, value)
	}
	return chunks
}
