package handler

// Tests of the Step 26 S/MIME actions: the envelope shapes of the frontend
// contract, the "call on null" failures of a request without the wiring and the
// certificate/verify round trips through the real ".smime" store.

import (
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/smallstep/pkcs7"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/smime"
)

// testSMimePassphrase is the passphrase of the generated test key pair.
const testSMimePassphrase = "s3cret"

// fakeSMIME is a handler.SMIME backed by a real store in a temporary folder.
type fakeSMIME struct {
	store *smime.Store
}

// newFakeSMIME builds an empty ".smime" store.
func newFakeSMIME(t *testing.T) *fakeSMIME {
	t.Helper()
	return &fakeSMIME{store: smime.NewStore(t.TempDir())}
}

// NewCertificate is new \SnappyMail\SMime\Certificate().
func (f *fakeSMIME) NewCertificate() *smime.Certificate {
	certificate, err := smime.NewCertificate("", "")
	if nil != err {
		return nil
	}
	// The production default is 4096 bits; a shorter key keeps the test fast.
	certificate.KeyBits = 2048
	return certificate
}

// Certificates is OpenSSL::certificates().
func (f *fakeSMIME) Certificates() ([]smime.CertificateInfo, error) {
	return f.store.Certificates(false)
}

// StoreCertificate is OpenSSL::storeCertificate().
func (f *fakeSMIME) StoreCertificate(certificate string) (bool, error) {
	return f.store.StoreCertificate(certificate), nil
}

// SetCertificate is OpenSSL::setCertificate().
func (f *fakeSMIME) SetCertificate(certificate string) error {
	return f.store.SetCertificate(certificate)
}

// SetPrivateKey is OpenSSL::setPrivateKey().
func (f *fakeSMIME) SetPrivateKey(privateKey, passphrase string) error {
	return f.store.SetPrivateKey(privateKey, passphrase)
}

// ExportPrivateKey is OpenSSL::exportPrivateKey().
func (f *fakeSMIME) ExportPrivateKey(passphrase string) (string, error) {
	return f.store.ExportPrivateKey(passphrase)
}

// Decrypt is OpenSSL::decrypt().
func (f *fakeSMIME) Decrypt(body string) (string, error) {
	return f.store.Decrypt(body)
}

// Verify is OpenSSL::verify().
func (f *fakeSMIME) Verify(body string, opaque bool) (*smime.VerifyResult, error) {
	return f.store.Verify(body, opaque)
}

// signedData signs content the way Actions\Messages::buildMessage() does for the
// S/MIME compose path (a detached signature by default).
func signedData(content []byte, certificate *x509.Certificate, key crypto.PrivateKey, detached bool) ([]byte, error) {
	signed, err := pkcs7.NewSignedData(content)
	if nil != err {
		return nil, err
	}
	if err := signed.AddSigner(certificate, key, pkcs7.SignerInfoConfig{}); nil != err {
		return nil, err
	}
	if detached {
		signed.Detach()
	}
	return signed.Finish()
}

// smimeDispatcher builds a dispatcher wired to the given S/MIME provider.
func smimeDispatcher(session Session, provider SMIME) *Dispatcher {
	return NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		SMIME:   func(Session) SMIME { return provider },
	})
}

// smimeResponse unwraps the envelope of an action.
func smimeResponse(t *testing.T, result any, err error) map[string]any {
	t.Helper()

	if nil != err {
		t.Fatalf("action failed: %v", err)
	}
	response, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("response = %#v", result)
	}
	return response
}

// TestSMimeActionsRegistered checks that every action name of the PHP trait has
// a handler ("method_exists()" of ServiceJson()).
func TestSMimeActionsRegistered(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	for _, action := range []string{
		"GetSMimeCertificate", "SMimeGetCertificates", "SMimeCreateCertificate",
		"SMimeExportPrivateKey", "SMimeDecryptMessage", "SMimeVerifyMessage",
		"SMimeImportCertificate", "SMimeImportCertificatesFromMessage",
	} {
		if !dispatcher.registry.Has(action) {
			t.Errorf("action %q is not registered", action)
		}
	}
}

// TestGetSMimeCertificate checks the array_values(array_unique(...)) result of
// DoGetSMimeCertificate: three empty strings collapse to one.
func TestGetSMimeCertificate(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	result, err := doGetSMimeCertificate(&Context{dispatcher: dispatcher})
	if nil != err {
		t.Fatalf("doGetSMimeCertificate: %v", err)
	}

	list, ok := mustResponse(result, err)["Result"].([]any)
	if !ok || 1 != len(list) || "" != list[0] {
		t.Errorf("Result = %#v", mustResponse(result, err)["Result"])
	}
}

// TestSMimeCreateCertificate checks the {pkey, x509} document and the
// account defaults of the distinguished name.
func TestSMimeCreateCertificate(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	dispatcher := smimeDispatcher(session, newFakeSMIME(t))

	result, err := doSMimeCreateCertificate(folderContext(dispatcher, session, map[string]any{
		"passphrase": testSMimePassphrase,
	}))
	document, ok := smimeResponse(t, result, err)["Result"].(map[string]string)
	if !ok {
		t.Fatalf("Result = %#v", result)
	}
	if !strings.HasPrefix(document["pkey"], "-----BEGIN ENCRYPTED PRIVATE KEY-----") {
		t.Errorf("pkey = %.40q", document["pkey"])
	}

	certificate, err := smime.ParseCertificate(document["x509"])
	if nil != err {
		t.Fatalf("ParseCertificate: %v", err)
	}
	// The empty name/e-mail parameters fall back to the account; its name is
	// empty here, so the certificate only carries the e-mail address.
	if "user@example.com" != smime.EmailAddressOf(certificate) {
		t.Errorf("emailAddress = %q", smime.EmailAddressOf(certificate))
	}

	// Explicit parameters win.
	result, err = doSMimeCreateCertificate(folderContext(dispatcher, session, map[string]any{
		"name":  "Test User",
		"email": "test@example.com",
	}))
	document, _ = smimeResponse(t, result, err)["Result"].(map[string]string)
	certificate, err = smime.ParseCertificate(document["x509"])
	if nil != err {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if "Test User" != certificate.Subject.CommonName {
		t.Errorf("CN = %q", certificate.Subject.CommonName)
	}
	// No passphrase: an unencrypted PKCS#8 key, like openssl_pkey_export().
	if !strings.HasPrefix(document["pkey"], "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("pkey = %.40q", document["pkey"])
	}

	// Without an account the action reports InvalidToken (101).
	if _, err := doSMimeCreateCertificate(folderContext(nil, nil, map[string]any{})); nil == err {
		t.Error("the action must need an account")
	} else if appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("code = %d", clientCodeOf(err))
	}
}

// TestSMimeImportAndListCertificates checks the store round trip of
// DoSMimeImportCertificate and DoSMimeGetCertificates.
func TestSMimeImportAndListCertificates(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	provider := newFakeSMIME(t)
	dispatcher := smimeDispatcher(session, provider)

	certificate, _, _ := newTestSMimeCertificate(t)
	certificatePEM, err := certificate.Export()
	if nil != err {
		t.Fatalf("Export: %v", err)
	}

	result, err := doSMimeImportCertificate(folderContext(dispatcher, session, map[string]any{
		"pem": certificatePEM,
	}))
	if true != smimeResponse(t, result, err)["Result"] {
		t.Errorf("Result = %#v", result)
	}

	// A blob that is not a certificate reports false (PHP logs and returns false).
	result, err = doSMimeImportCertificate(folderContext(dispatcher, session, map[string]any{
		"pem": "not a certificate",
	}))
	if false != smimeResponse(t, result, err)["Result"] {
		t.Errorf("Result = %#v", result)
	}

	result, err = doSMimeGetCertificates(folderContext(dispatcher, session, map[string]any{}))
	list, ok := smimeResponse(t, result, err)["Result"].([]smime.CertificateInfo)
	if !ok || 1 != len(list) {
		t.Fatalf("Result = %#v", result)
	}
	if "Test User" != list[0].CN || "test@example.com" != list[0].EmailAddress {
		t.Errorf("entry = %+v", list[0])
	}
	if !list[0].SmimeSign || !list[0].SmimeEncrypt {
		t.Errorf("purposes = %+v", list[0])
	}
}

// TestSMimeExportPrivateKey checks the re-encryption of the private key.
func TestSMimeExportPrivateKey(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	dispatcher := smimeDispatcher(session, newFakeSMIME(t))

	_, _, encryptedKey := newTestSMimeCertificate(t)

	result, err := doSMimeExportPrivateKey(folderContext(dispatcher, session, map[string]any{
		"privateKey":    encryptedKey,
		"oldPassphrase": testSMimePassphrase,
		"newPassphrase": "another-secret",
	}))

	exported := phpString(smimeResponse(t, result, err)["Result"])
	if !strings.HasPrefix(exported, "-----BEGIN ENCRYPTED PRIVATE KEY-----") {
		t.Fatalf("Result = %.40q", exported)
	}

	// The old passphrase no longer opens it, the new one does.
	if _, err := doSMimeExportPrivateKey(folderContext(dispatcher, session, map[string]any{
		"privateKey":    exported,
		"oldPassphrase": testSMimePassphrase,
	})); nil == err {
		t.Error("the old passphrase must not open the re-encrypted key")
	}

	if _, err := doSMimeExportPrivateKey(folderContext(dispatcher, session, map[string]any{
		"privateKey":    exported,
		"oldPassphrase": "another-secret",
		"newPassphrase": "",
	})); nil != err {
		t.Fatalf("doSMimeExportPrivateKey: %v", err)
	}
}

// TestSMimeVerifyMessage checks the inline bodyPart branch of the action.
func TestSMimeVerifyMessage(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	dispatcher := smimeDispatcher(session, newFakeSMIME(t))

	body := newSignedMessage(t)

	result, err := doSMimeVerifyMessage(folderContext(dispatcher, session, map[string]any{
		"bodyPart": body,
		"detached": 1,
	}))
	document, ok := smimeResponse(t, result, err)["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", result)
	}
	if true != document["success"] {
		t.Errorf("success = %#v", document["success"])
	}
	// A detached verification reports no body, like the PHP null.
	if nil != document["body"] {
		t.Errorf("body = %#v", document["body"])
	}

	// A tampered body must fail with the UnknownError of the PHP exception.
	if _, err := doSMimeVerifyMessage(folderContext(dispatcher, session, map[string]any{
		"bodyPart": strings.Replace(body, "Hello", "Jello", 1),
		"detached": 1,
	})); nil == err {
		t.Error("a tampered body must not verify")
	}
}

// TestSMimeVerifyMessageImportsTheCertificates checks the automatic
// storeCertificate() of the signature part.
func TestSMimeVerifyMessageImportsTheCertificates(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	provider := newFakeSMIME(t)
	dispatcher := smimeDispatcher(session, provider)

	if _, err := doSMimeVerifyMessage(folderContext(dispatcher, session, map[string]any{
		"bodyPart": newSignedMessage(t),
		"sigPart":  newOpaqueSignature(t),
	})); nil != err {
		t.Fatalf("doSMimeVerifyMessage: %v", err)
	}

	list, err := provider.Certificates()
	if nil != err || 1 != len(list) {
		t.Fatalf("the signature certificate was not imported: %v %+v", err, list)
	}
}

// TestSMimeDecryptMessageNeedsAMailConnection checks the per-request IMAP
// connection of the decrypt action: without one the InvalidToken failure of
// initMailClientConnection() is reported.
func TestSMimeDecryptMessageNeedsAMailConnection(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	dispatcher := smimeDispatcher(session, newFakeSMIME(t))

	for _, params := range []map[string]any{
		{"folder": "INBOX", "uid": 1, "partId": "2"},
		// An invalid MIME part id is InvalidInputArgument (903).
		{"folder": "INBOX", "uid": 1, "partId": "../../etc/passwd"},
	} {
		if _, err := doSMimeDecryptMessage(folderContext(dispatcher, session, params)); nil == err {
			t.Errorf("doSMimeDecryptMessage(%v) must fail", params)
		}
	}

	// The action that needs no connection still runs once the part id is valid.
	if _, err := doSMimeVerifyMessage(folderContext(dispatcher, session, map[string]any{
		"partId": "1.2", "bodyPart": newSignedMessage(t),
	})); nil != err {
		t.Errorf("doSMimeVerifyMessage: %v", err)
	}
}

// TestSMimeActionsWithoutProvider checks the "call to a member function on null"
// failures of a request without the S/MIME wiring.
func TestSMimeActionsWithoutProvider(t *testing.T) {
	session := &folderSession{account: folderAccount()}
	dispatcher := smimeDispatcher(session, nil)

	cases := []struct {
		name   string
		action ActionFunc
		params map[string]any
	}{
		{"SMimeGetCertificates", doSMimeGetCertificates, map[string]any{}},
		{"SMimeExportPrivateKey", doSMimeExportPrivateKey, map[string]any{"privateKey": "x"}},
		{"SMimeImportCertificate", doSMimeImportCertificate, map[string]any{"pem": "x"}},
		{"SMimeVerifyMessage", doSMimeVerifyMessage, map[string]any{"bodyPart": "x"}},
		{"SMimeImportCertificatesFromMessage", doSMimeImportCertificatesFromMessage, map[string]any{}},
	}

	for _, test := range cases {
		_, err := test.action(folderContext(dispatcher, session, test.params))
		if nil == err {
			t.Errorf("%s must report a failure", test.name)
			continue
		}
		if appctx.UnknownError != clientCodeOf(err) {
			t.Errorf("%s code = %d, want %d", test.name, clientCodeOf(err), appctx.UnknownError)
		}
	}
}

// newTestSMimeCertificate creates a certificate plus its keys for the tests.
func newTestSMimeCertificate(t *testing.T) (*smime.Certificate, string, string) {
	t.Helper()

	certificate, err := smime.NewCertificate("", "")
	if nil != err {
		t.Fatalf("NewCertificate: %v", err)
	}
	certificate.KeyBits = 2048
	certificate.DistinguishedName[smime.DNCommonName] = "Test User"
	certificate.DistinguishedName[smime.DNEmailAddress] = "test@example.com"

	result, err := certificate.CreateSelfSigned(testSMimePassphrase, "")
	if nil != err {
		t.Fatalf("CreateSelfSigned: %v", err)
	}

	return certificate, result["x509"], result["pkey"]
}

// signedContent is the first part of the multipart/signed fixture, i.e. the byte
// range the detached signature covers.
const signedContent = "Content-Type: text/plain\r\n\r\nHello S/MIME"

// newSignedMessage builds the multipart/signed entity of a signed message.
func newSignedMessage(t *testing.T) string {
	t.Helper()

	certificate, _, _ := newTestSMimeCertificate(t)

	der, err := signedData([]byte(signedContent), certificate.X509, certificate.Pkey, true)
	if nil != err {
		t.Fatalf("sign: %v", err)
	}

	return "Content-Type: multipart/signed; micalg=\"sha-256\"; " +
		"protocol=\"application/pkcs7-signature\"; boundary=\"smime\"\r\n\r\n" +
		"--smime\r\n" + signedContent + "\r\n" +
		"--smime\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"signature.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		foldBase64(der) + "\r\n" +
		"--smime--\r\n"
}

// newOpaqueSignature builds the base64 PKCS#7 of the same signature, the shape
// sigPart carries when the frontend read it from the message.
func newOpaqueSignature(t *testing.T) string {
	t.Helper()

	certificate, _, _ := newTestSMimeCertificate(t)

	der, err := signedData([]byte(signedContent), certificate.X509, certificate.Pkey, true)
	if nil != err {
		t.Fatalf("sign: %v", err)
	}

	return foldBase64(der)
}

// foldBase64 wraps a DER blob at 76 columns, like an S/MIME part.
func foldBase64(der []byte) string {
	encoded := base64.StdEncoding.EncodeToString(der)

	lines := []string{}
	for 76 < len(encoded) {
		lines = append(lines, encoded[:76])
		encoded = encoded[76:]
	}
	if 0 < len(encoded) {
		lines = append(lines, encoded)
	}

	return strings.Join(lines, "\r\n")
}
