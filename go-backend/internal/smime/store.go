package smime

// SnappyMail\SMime\OpenSSL: the ".smime" home directory of the (main) account,
// the certificate store inside it and the message decrypt/verify.
//
// PKCS#7 (CMS) itself is handled by github.com/smallstep/pkcs7. That library
// parses DER or PEM input, but the PHP code hands openssl_pkcs7_*() a complete
// S/MIME entity (MIME headers plus the base64/wrapped body). The extraction of
// the DER blob out of such an entity is therefore done here — it is also what
// makes the detached multipart/signed case work, because the signed content has
// to be handed to the library as p7.Content exactly as OpenSSL hashes it.

import (
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smallstep/pkcs7"
)

// The ".smime" store file names (index.php of the library folder).
const (
	// cacheFileName is the certificates.json cache of certificates().
	cacheFileName = "certificates.json"
	// certificateExtension is the suffix of a stored certificate.
	certificateExtension = ".crt"
	// privateKeyExtension is the suffix of a stored private key.
	privateKeyExtension = ".key"
	// unencryptedKeyMarker is the PEM header certificates() looks for: an
	// encrypted private key cannot be matched to a certificate.
	unencryptedKeyMarker = "-----BEGIN PRIVATE KEY-----"
)

// CertificateInfo is one entry of the certificates() array:
//
//	[
//		'file' => $filename,
//		'id' => $key,
//		'CN' => $data['subject']['CN'],
//		'emailAddress' => $data['subject']['emailAddress'],
//		'validTo_time_t' => $data['validTo_time_t'],
//		'smimesign' => false,
//		'smimeencrypt' => false,
//		'privateKey' => null // not found or encrypted
//	]
//
// CN, EmailAddress and PrivateKey are `any` because openssl_x509_parse() and the
// key lookup yield null for an absent value, which is also what
// json_encode() wrote into the certificates.json cache.
type CertificateInfo struct {
	File         string `json:"file"`
	ID           string `json:"id"`
	CN           any    `json:"CN"`
	EmailAddress any    `json:"emailAddress"`
	ValidToTimeT int64  `json:"validTo_time_t"`
	SmimeSign    bool   `json:"smimesign"`
	SmimeEncrypt bool   `json:"smimeencrypt"`
	PrivateKey   any    `json:"privateKey"`
}

// VerifyResult is the array verify() returns: the extracted body (only for
// opaque signed data) and the success flag.
type VerifyResult struct {
	// Body is the verified content of opaque signed data, "" otherwise.
	Body string
	// Opaque reports whether the message was an application/pkcs7-mime
	// signed-data entity (the $opaque argument, possibly forced by the content).
	Opaque bool
	// Success is true once the signature verified.
	Success bool
}

// Store is SnappyMail\SMime\OpenSSL.
type Store struct {
	homedir string

	certificate *x509.Certificate
	privateKey  crypto.Signer
}

// NewStore is OpenSSL::__construct().
func NewStore(homedir string) *Store {
	return &Store{homedir: strings.TrimRight(strings.TrimSpace(homedir), `/\`)}
}

// Homedir returns the ".smime" folder of the store.
func (s *Store) Homedir() string {
	if nil == s {
		return ""
	}
	return s.homedir
}

// GetCertificate is OpenSSL::getCertificate(): the verbatim file content.
func (s *Store) GetCertificate(filename string) string {
	content, err := readFile(s.path(filename))
	if nil != err {
		return ""
	}
	return content
}

// path resolves a store file name, refusing anything that leaves the folder.
func (s *Store) path(name string) string {
	cleaned := filepath.Base(strings.TrimSpace(name))
	if "" == s.homedir || "." == cleaned || ".." == cleaned || "/" == cleaned {
		return ""
	}
	return filepath.Join(s.homedir, cleaned)
}

// StoreCertificate is OpenSSL::storeCertificate(): keep the PEM blob as
// "<subjectKeyIdentifier>.crt" and refresh the certificate list cache.
func (s *Store) StoreCertificate(certificate string) bool {
	if nil == s || "" == s.homedir {
		return false
	}

	parsed, err := ParseCertificate(certificate)
	if nil != err {
		return false
	}

	name := Identifier(parsed) + certificateExtension
	if _, statErr := os.Stat(s.path(name)); nil == statErr {
		// PHP logs "certificate {key} already imported" at debug level.
		return true
	}

	if err := s.writeFile(name, certificate); nil != err {
		return false
	}

	// PHP: $this->certificates(true) — drop the stale certificate list cache.
	_, _ = s.Certificates(true)

	return true
}

// Certificates is OpenSSL::certificates(): the certificate list, from the
// certificates.json cache when it exists.
func (s *Store) Certificates(force bool) ([]CertificateInfo, error) {
	if nil == s || "" == s.homedir {
		return []CertificateInfo{}, nil
	}

	if !force {
		if cached, ok := s.readCache(); ok {
			return cached, nil
		}
	}

	// The unencrypted private keys first, because a certificate is matched
	// against them ("Can't check ENCRYPTED PRIVATE KEY").
	type storedKey struct {
		file    string
		content string
	}

	keys := []storedKey{}
	for _, name := range s.files(privateKeyExtension) {
		content, err := readFile(s.path(name))
		if nil != err || !strings.Contains(content, unencryptedKeyMarker) {
			continue
		}
		keys = append(keys, storedKey{file: name, content: content})
	}

	result := []CertificateInfo{}

	for _, name := range s.files(certificateExtension) {
		content, err := readFile(s.path(name))
		if nil != err {
			continue
		}

		parsed, err := ParseCertificate(content)
		if nil != err {
			// PHP logs "parse({$file}): ..." at error level and skips the file.
			continue
		}

		id := Identifier(parsed)
		if expected := id + certificateExtension; name != expected {
			if renameErr := os.Rename(s.path(name), s.path(expected)); nil == renameErr {
				name = expected
			}
		}

		info := CertificateInfo{
			File:         name,
			ID:           id,
			ValidToTimeT: parsed.NotAfter.Unix(),
			// The purpose checks of openssl_x509_parse()['purposes'].
			SmimeSign:    CanSign(parsed),
			SmimeEncrypt: CanEncrypt(parsed),
			PrivateKey:   nil,
		}
		if cn := CommonNameOf(parsed); "" != cn {
			info.CN = cn
		}
		if address := EmailAddressOf(parsed); "" != address {
			info.EmailAddress = address
		}

		for _, key := range keys {
			signer, keyErr := parsePrivateKey(key.content, "")
			if nil == keyErr && publicKeysEqual(parsed, signer) {
				info.PrivateKey = key.file
				break
			}
		}

		result = append(result, info)
	}

	s.writeCache(result)

	return result, nil
}

// files lists the store files with an extension, sorted, like glob() does.
func (s *Store) files(extension string) []string {
	entries, err := os.ReadDir(s.homedir)
	if nil != err {
		return nil
	}

	result := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), extension) {
			continue
		}
		result = append(result, entry.Name())
	}
	sort.Strings(result)

	return result
}

// readCache is the cached branch of certificates().
func (s *Store) readCache() ([]CertificateInfo, bool) {
	content, err := readFile(s.path(cacheFileName))
	if nil != err {
		return nil, false
	}

	var cached []CertificateInfo
	if err := json.Unmarshal([]byte(content), &cached); nil != err {
		return nil, false
	}
	if nil == cached {
		return nil, false
	}

	return cached, true
}

// writeCache stores the certificate list the way json_encode() writes it.
func (s *Store) writeCache(certificates []CertificateInfo) {
	encoded, err := json.Marshal(certificates)
	if nil != err {
		return
	}
	_ = s.writeFile(cacheFileName, string(encoded))
}

// writeFile writes a store file with 0600, creating the folder with 0700.
func (s *Store) writeFile(name, content string) error {
	if "" == s.homedir {
		return errors.New("smime: no home directory")
	}
	if err := os.MkdirAll(s.homedir, 0o700); nil != err {
		return fmt.Errorf("smime: %w", err)
	}
	return os.WriteFile(s.path(name), []byte(content), 0o600)
}

// SetCertificate is OpenSSL::setCertificate().
func (s *Store) SetCertificate(certificate string) error {
	parsed, err := ParseCertificate(certificate)
	if nil != err {
		return fmt.Errorf("OpenSSL x509: %w", err)
	}
	if nil != s.privateKey && !publicKeysEqual(parsed, s.privateKey) {
		return errors.New("OpenSSL x509: private key does not match the certificate")
	}
	s.certificate = parsed
	return nil
}

// SetPrivateKey is OpenSSL::setPrivateKey().
func (s *Store) SetPrivateKey(privateKey, passphrase string) error {
	signer, err := parsePrivateKey(privateKey, passphrase)
	if nil != err {
		return fmt.Errorf("OpenSSL setPrivateKey: %w", err)
	}
	if nil != s.certificate && !publicKeysEqual(s.certificate, signer) {
		return errors.New("OpenSSL setPrivateKey: private key does not match the certificate")
	}
	s.privateKey = signer
	return nil
}

// ExportPrivateKey is OpenSSL::exportPrivateKey(): the loaded key, re-encrypted
// with the new passphrase.
func (s *Store) ExportPrivateKey(passphrase string) (string, error) {
	if nil == s || nil == s.privateKey {
		return "", errors.New("OpenSSL exportPrivateKey: key not loaded")
	}
	return exportPrivateKey(s.privateKey, passphrase)
}

// Decrypt is OpenSSL::decrypt(): the plaintext MIME entity of an
// application/pkcs7-mime part.
func (s *Store) Decrypt(body string) (string, error) {
	if nil == s || nil == s.certificate || nil == s.privateKey {
		return "", errors.New("OpenSSL decrypt: certificate or private key not loaded")
	}

	der := pkcs7DER(body)
	if nil == der {
		return "", errors.New("OpenSSL decrypt: unable to read the PKCS7 data")
	}

	message, err := pkcs7.Parse(der)
	if nil != err {
		return "", fmt.Errorf("OpenSSL decrypt: %w", err)
	}

	plain, err := message.Decrypt(s.certificate, s.privateKey)
	if nil != err {
		return "", fmt.Errorf("OpenSSL decrypt: %w", err)
	}

	return string(plain), nil
}

// Verify is OpenSSL::verify(): check the signature of a signed S/MIME entity.
//
// opaque tells whether the message is application/pkcs7-mime (signed-data): only
// then is the extracted content reported back. Like the PHP flags
// PKCS7_NOVERIFY | PKCS7_NOCHAIN | PKCS7_NOSIGS the certificate chain is not
// validated, only the signature itself.
func (s *Store) Verify(body string, opaque bool) (*VerifyResult, error) {
	if "" == strings.TrimSpace(body) {
		return nil, errors.New("OpenSSL verify: no data")
	}

	if !opaque && (strings.Contains(body, "application/pkcs7-mime") ||
		strings.Contains(body, "application/x-pkcs7-mime")) {
		opaque = true
	}

	var (
		der     []byte
		content []byte
	)

	// openssl_pkcs7_verify() reads the structure from the MIME entity itself: a
	// multipart/signed message carries a detached signature even when the
	// caller passed an OID that suggests otherwise.
	if strings.Contains(body, "multipart/signed") {
		if signed, signature, ok := splitMultipartSigned(body); ok {
			content = signed
			der = pkcs7DER(string(signature))
		}
	}
	if nil == der {
		der = pkcs7DER(body)
	}
	if nil == der {
		return nil, errors.New("OpenSSL verify: unable to read the PKCS7 data")
	}

	message, err := pkcs7.Parse(der)
	if nil != err {
		return nil, fmt.Errorf("OpenSSL verify: %w", err)
	}

	// A detached signature carries no content of its own.
	if nil != content && 0 == len(message.Content) {
		message.Content = content
	}

	if err := message.Verify(); nil != err {
		return nil, fmt.Errorf("OpenSSL verify: %w", err)
	}

	result := &VerifyResult{Success: true, Opaque: opaque}
	if opaque {
		result.Body = string(message.Content)
	}

	return result, nil
}

// CertificatesOfPKCS7 is openssl_pkcs7_read(): the certificates of a PKCS#7
// document (a "-----BEGIN PKCS7-----" block or the base64 body of a signature
// part), as PEM blocks.
//
// The PHP code only logs a read failure, so nil is returned instead of an error.
func CertificatesOfPKCS7(value string) []string {
	der := pkcs7DER(value)
	if nil == der {
		return nil
	}

	message, err := pkcs7.Parse(der)
	if nil != err || nil == message {
		return nil
	}

	result := make([]string, 0, len(message.Certificates))
	for _, certificate := range message.Certificates {
		if nil == certificate {
			continue
		}
		result = append(result, encodeCertificate(certificate))
	}

	return result
}

// pkcs7DER extracts the PKCS#7 (CMS) DER blob of an S/MIME part: the
// "-----BEGIN PKCS7-----" block when the frontend sent one, else the
// base64 encoded body of the part.
func pkcs7DER(body string) []byte {
	body = strings.TrimSpace(body)
	if "" == body {
		return nil
	}

	// A "-----BEGIN PKCS7-----" block is what openssl_pkcs7_read() of
	// DoSMimeVerifyMessage() parses; the frontend may also send the bare DER
	// wrapped that way.
	if offset := strings.Index(body, "-----BEGIN "+pemTypePKCS7+"-----"); 0 <= offset {
		if block, _ := pem.Decode([]byte(body[offset:])); nil != block {
			return block.Bytes
		}
	}

	if _, rest, ok := splitHeaderBody(body); ok {
		body = rest
	}

	return decodeBase64(body)
}

// decodeBase64 is base64_decode() over a folded MIME body.
func decodeBase64(value string) []byte {
	compact := strings.Map(func(character rune) rune {
		switch character {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return character
	}, value)

	if "" == compact {
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(compact)
	if nil != err {
		return nil
	}
	return decoded
}

// splitHeaderBody splits a MIME entity into the header block and the body.
func splitHeaderBody(value string) (header, body string, ok bool) {
	if index := strings.Index(value, "\r\n\r\n"); 0 <= index {
		return value[:index], value[index+4:], true
	}
	if index := strings.Index(value, "\n\n"); 0 <= index {
		return value[:index], value[index+2:], true
	}
	return value, "", false
}

// headerParameter is a Content-Type parameter of a header block ("boundary"),
// case insensitive and aware of the quoted form.
func headerParameter(header, name string) string {
	lower := strings.ToLower(header)
	key := strings.ToLower(name) + "="

	for index := 0; index < len(lower); {
		offset := strings.Index(lower[index:], key)
		if 0 > offset {
			return ""
		}
		at := index + offset
		// The parameter name must start a parameter (not "xboundary=").
		if 0 < at {
			switch lower[at-1] {
			case ';', ' ', '\t', '\r', '\n':
			default:
				index = at + len(key)
				continue
			}
		}

		value := strings.TrimSpace(header[at+len(key):])
		if strings.HasPrefix(value, `"`) {
			value = value[1:]
			if end := strings.IndexByte(value, '"'); 0 <= end {
				return value[:end]
			}
			return ""
		}
		if end := strings.IndexAny(value, ";\r\n\t "); 0 <= end {
			value = value[:end]
		}
		return value
	}

	return ""
}

// splitMultipartSigned extracts the signed content and the detached signature
// part of a multipart/signed entity.
func splitMultipartSigned(value string) (content, signature []byte, ok bool) {
	header, body, found := splitHeaderBody(value)
	if !found {
		return nil, nil, false
	}

	boundary := headerParameter(header, "boundary")
	if "" == boundary {
		return nil, nil, false
	}
	delimiter := "--" + boundary

	offset := strings.Index(body, delimiter)
	if 0 > offset {
		return nil, nil, false
	}

	// The first part starts on the line after the opening delimiter.
	firstStart := offset + len(delimiter)
	eol := strings.IndexAny(body[firstStart:], "\r\n")
	if 0 > eol {
		return nil, nil, false
	}
	firstStart = skipLineBreak(body, firstStart+eol)

	firstEnd, secondStart, found := nextBoundary(body, firstStart, delimiter)
	if !found {
		return nil, nil, false
	}
	secondEnd, _, _ := nextBoundary(body, secondStart, delimiter)

	return []byte(body[firstStart:firstEnd]), []byte(body[secondStart:secondEnd]), true
}

// nextBoundary locates the delimiter line that ends the part starting at from:
// contentEnd is where the part content stops (the line break belongs to the
// delimiter) and partStart the first byte of the following part.
func nextBoundary(value string, from int, delimiter string) (contentEnd, partStart int, found bool) {
	for search := from; ; {
		offset := strings.Index(value[search:], delimiter)
		if 0 > offset {
			return len(value), len(value), false
		}

		begin := search + offset
		if 0 == begin || '\n' == value[begin-1] {
			contentEnd = begin
			if contentEnd > from && '\n' == value[contentEnd-1] {
				contentEnd--
				if contentEnd > from && '\r' == value[contentEnd-1] {
					contentEnd--
				}
			}

			partStart = begin + len(delimiter)
			if eol := strings.IndexAny(value[partStart:], "\r\n"); 0 <= eol {
				partStart = skipLineBreak(value, partStart+eol)
			} else {
				partStart = len(value)
			}

			return contentEnd, partStart, true
		}

		search = begin + len(delimiter)
	}
}

// skipLineBreak skips one CRLF or LF at index.
func skipLineBreak(value string, index int) int {
	if index < len(value) && '\r' == value[index] {
		index++
	}
	if index < len(value) && '\n' == value[index] {
		index++
	}
	return index
}
