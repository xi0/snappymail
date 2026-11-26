package service

// The production implementation of the step 26 handler surface:
// SnappyMail\SMime\OpenSSL over the ".smime" folder of the *main* account.
//
// The PHP code resolves the folder per request in Actions\SMime::SMIME():
//
//	$oAccount = $this->getMainAccountFromToken();
//	$homedir = rtrim(StorageProvider()->GenerateFilePath($oAccount, ROOT), '/') . '/.smime';
//	MailSo\Base\Utils::mkdir($homedir);
//	if (!is_writable($homedir)) throw new \Exception("smime homedir '{$homedir}' not writable");
//
// main wires NewSMIME() into handler.Options.SMIME for every request, so the
// DoSMime* actions see exactly the account the cookie stack resolved.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/smime"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// smimeFolder is the ROOT subfolder Actions\SMime::SMIME() uses.
const smimeFolder = ".smime"

// smimeProbeName is the file the writability check of the home directory creates
// (PHP: is_writable()).
const smimeProbeName = ".writable"

// SMIMEOptions is the state of one request's S/MIME surface.
type SMIMEOptions struct {
	// Session resolves the main account (getMainAccountFromToken).
	Session *auth.Authenticator
	// Storage is Actions::StorageProvider(): the ROOT folder of the account.
	Storage *storage.Provider
	// Authority is the bundled snappymail.crt/snappymail.key pair that
	// Certificate::createSelfSigned() signs with; nil self-signs.
	Authority *smime.Authority
}

// SMIME is the handler.SMIME implementation.
type SMIME struct {
	options SMIMEOptions
	store   *smime.Store
	err     error
	opened  bool
}

// NewSMIME builds the S/MIME surface of one request.
func NewSMIME(options SMIMEOptions) *SMIME {
	return &SMIME{options: options}
}

// NewCertificate is new \SnappyMail\SMime\Certificate() with the bundled CA.
//
// The creator needs no home directory (and no account), exactly like the PHP
// class, which only reads its own openssl.cnf/snappymail.* files.
func (s *SMIME) NewCertificate() *smime.Certificate {
	certificate, err := smime.NewCertificate("", "")
	if nil != err {
		// An empty certificate can not fail to parse; keep the signature simple
		// and let the caller report the resulting error instead.
		return &smime.Certificate{DistinguishedName: map[string]string{}}
	}
	return certificate.SetAuthority(s.authority())
}

// authority is the CA of the options.
func (s *SMIME) authority() *smime.Authority {
	if nil == s {
		return nil
	}
	return s.options.Authority
}

// Certificates is OpenSSL::certificates().
func (s *SMIME) Certificates() ([]smime.CertificateInfo, error) {
	store, err := s.open()
	if nil == store {
		return nil, nullStore("certificates", err)
	}
	return store.Certificates(false)
}

// StoreCertificate is OpenSSL::storeCertificate().
func (s *SMIME) StoreCertificate(certificate string) (bool, error) {
	store, err := s.open()
	if nil == store {
		return false, nullStore("storeCertificate", err)
	}
	return store.StoreCertificate(certificate), nil
}

// SetCertificate is OpenSSL::setCertificate().
func (s *SMIME) SetCertificate(certificate string) error {
	store, err := s.open()
	if nil == store {
		return nullStore("setCertificate", err)
	}
	return store.SetCertificate(certificate)
}

// SetPrivateKey is OpenSSL::setPrivateKey().
func (s *SMIME) SetPrivateKey(privateKey, passphrase string) error {
	store, err := s.open()
	if nil == store {
		return nullStore("setPrivateKey", err)
	}
	return store.SetPrivateKey(privateKey, passphrase)
}

// ExportPrivateKey is OpenSSL::exportPrivateKey().
func (s *SMIME) ExportPrivateKey(passphrase string) (string, error) {
	store, err := s.open()
	if nil == store {
		return "", nullStore("exportPrivateKey", err)
	}
	return store.ExportPrivateKey(passphrase)
}

// Decrypt is OpenSSL::decrypt().
func (s *SMIME) Decrypt(body string) (string, error) {
	store, err := s.open()
	if nil == store {
		return "", nullStore("decrypt", err)
	}
	return store.Decrypt(body)
}

// Verify is OpenSSL::verify().
func (s *SMIME) Verify(body string, opaque bool) (*smime.VerifyResult, error) {
	store, err := s.open()
	if nil == store {
		return nil, nullStore("verify", err)
	}
	return store.Verify(body, opaque)
}

// open resolves the ".smime" store of the main account, once per request.
//
// A nil store is the PHP "SMIME() === null" case: no session, or no main account
// in it. A non-nil error is the exception Actions\SMime::SMIME() throws.
func (s *SMIME) open() (*smime.Store, error) {
	if nil == s {
		return nil, nil
	}
	if s.opened {
		return s.store, s.err
	}
	s.opened = true

	if nil == s.options.Session {
		return nil, nil
	}

	main, err := s.options.Session.GetMainAccountFromToken(true)
	if nil != err {
		return nil, err
	}
	if nil == main || nil == s.options.Storage {
		return nil, nil
	}

	base := s.options.Storage.GenerateFilePath(main, storage.TypeRoot, true)
	if "" == base {
		return nil, nil
	}

	homedir := strings.TrimRight(base, `/\`) + "/" + smimeFolder

	if err := storage.Mkdir(homedir); nil != err {
		return nil, err
	}
	if err := smimeWritable(homedir); nil != err {
		return nil, err
	}

	s.store = smime.NewStore(homedir)

	return s.store, nil
}

// nullStore is the "call to a member function on null" the PHP actions hit when
// Actions\SMime::SMIME() returned null (reported as UnknownError 999).
func nullStore(method string, err error) error {
	if nil != err {
		return err
	}
	return errors.New("Call to a member function " + method + "() on null")
}

// smimeWritable is is_writable() of the home directory: a probe file is created
// and removed again.
func smimeWritable(directory string) error {
	probe := filepath.Join(directory, smimeProbeName)

	file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if nil != err {
		return errors.New("smime homedir '" + directory + "' not writable")
	}
	_ = file.Close()
	_ = os.Remove(probe)

	return nil
}
