// Package smime implements the S/MIME support of the PHP backend:
// SnappyMail\SMime\Certificate (self-signed certificate creation) and
// SnappyMail\SMime\OpenSSL (the ".smime" home directory of the account, the
// stored certificates and the decrypt/verify of S/MIME messages).
//
// The PHP implementation is a thin wrapper around the openssl extension; Go has
// no such extension, so the same operations are done with crypto/x509,
// crypto/rsa and github.com/smallstep/pkcs7. Everything that ends up on disk is
// a compatibility contract with PHP and is ported as-is:
//
//   - a stored certificate is the verbatim PEM blob at "<homedir>/<id>.crt",
//     where <id> is the certificates subjectKeyIdentifier without the colons
//     (uppercase hex) or, for a certificate without that extension, the
//     openssl_x509_parse() 'hash' fallback;
//   - "certificates.json" is the json_encode() cache of the certificate list,
//     with the PHP keys file, id, CN, emailAddress, validTo_time_t, smimesign,
//     smimeencrypt and privateKey;
//   - only unencrypted "-----BEGIN PRIVATE KEY-----" files are matched against a
//     certificate, because openssl_x509_check_private_key() cannot open an
//     encrypted one without a passphrase;
//   - an exported private key is PKCS#8 ("-----BEGIN PRIVATE KEY-----"), and with
//     a passphrase the PBES2/AES-CBC form of "-----BEGIN ENCRYPTED PRIVATE KEY-----"
//     that openssl_pkey_export() writes.
package smime

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// The distinguished name attribute OIDs Certificate::$NID maps (13, 14, 15, 16,
// 17, 18 and 48).
var (
	oidCountry            = asn1.ObjectIdentifier{2, 5, 4, 6}
	oidStateOrProvince    = asn1.ObjectIdentifier{2, 5, 4, 8}
	oidLocality           = asn1.ObjectIdentifier{2, 5, 4, 7}
	oidOrganization       = asn1.ObjectIdentifier{2, 5, 4, 10}
	oidOrganizationalUnit = asn1.ObjectIdentifier{2, 5, 4, 11}
	oidCommonName         = asn1.ObjectIdentifier{2, 5, 4, 3}
	oidEmailAddress       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 1}
)

// The Certificate defaults of SnappyMail\SMime\Certificate.
const (
	// defaultDigest is Certificate::$digest.
	defaultDigest = "sha256"
	// defaultKeyBits is Certificate::$keyBits (OPENSSL_KEYTYPE_RSA).
	defaultKeyBits = 4096
	// defaultDays is Certificate::$days ("EV = 1185, DV/OV = 825").
	defaultDays = 1185
)

// The Certificate::distinguishedName keys the actions set.
const (
	// DNCommonName is $distinguishedName['commonName'] (max 64 bytes).
	DNCommonName = "commonName"
	// DNEmailAddress is $distinguishedName['emailAddress'] (max 64 bytes).
	DNEmailAddress = "emailAddress"
)

// Authority is the issuer Certificate::createSelfSigned() signs with: the
// bundled snappymail.crt/snappymail.key of the PHP library folder.
//
// Go cannot embed assets (see SPEC.md), so the files are read from the install
// tree. A missing or unreadable file leaves Authority nil, in which case the
// certificate is self-signed instead of CA-signed — the only observable
// difference is the issuer DN.
type Authority struct {
	// Cert is the signing certificate (snappymail.crt).
	Cert *x509.Certificate
	// Key is the signing private key (snappymail.key).
	Key crypto.Signer
}

// LoadAuthority reads the snappymail.crt/snappymail.key pair of a library
// folder, the bundled CA Certificate::createSelfSigned() signs with.
//
// A missing or unusable pair yields nil, so the caller falls back to a truly
// self-signed certificate.
func LoadAuthority(directory string) *Authority {
	directory = strings.TrimRight(strings.TrimSpace(directory), `/\`)
	if "" == directory {
		return nil
	}

	certPEM, err := readFile(directory + "/snappymail.crt")
	if nil != err {
		return nil
	}
	keyPEM, err := readFile(directory + "/snappymail.key")
	if nil != err {
		return nil
	}

	certificate, err := ParseCertificate(certPEM)
	if nil != err {
		return nil
	}
	key, err := parsePrivateKey(keyPEM, "")
	if nil != err {
		return nil
	}
	if !publicKeysEqual(certificate, key) {
		return nil
	}

	return &Authority{Cert: certificate, Key: key}
}

// Certificate is SnappyMail\SMime\Certificate.
type Certificate struct {
	// X509 is Certificate::$x509.
	X509 *x509.Certificate
	// Pkey is Certificate::$pkey.
	Pkey crypto.Signer

	// Digest is Certificate::$digest.
	Digest string
	// KeyBits is Certificate::$keyBits.
	KeyBits int
	// Days is Certificate::$days.
	Days int
	// DistinguishedName is Certificate::$distinguishedName.
	DistinguishedName map[string]string

	// authority is the bundled CA to sign with; nil self-signs.
	authority *Authority
}

// NewCertificate is Certificate::__construct(): an empty certificate, or the
// given x509 certificate with its private key when the key matches.
//
// An unreadable certificate is the RuntimeException of PHP (the caller reports
// it as UnknownError); an unreadable or non-matching private key only leaves
// Pkey unset, like the "&& openssl_x509_check_private_key()" check.
func NewCertificate(x509cert, privateKey string) (*Certificate, error) {
	certificate := &Certificate{
		Digest:            defaultDigest,
		KeyBits:           defaultKeyBits,
		Days:              defaultDays,
		DistinguishedName: map[string]string{DNCommonName: "", DNEmailAddress: ""},
	}

	if "" == strings.TrimSpace(x509cert) {
		return certificate, nil
	}

	parsed, err := ParseCertificate(x509cert)
	if nil != err {
		return nil, fmt.Errorf("OpenSSL x509: %w", err)
	}
	certificate.X509 = parsed

	if "" != strings.TrimSpace(privateKey) {
		if key, keyErr := parsePrivateKey(privateKey, ""); nil == keyErr && publicKeysEqual(parsed, key) {
			certificate.Pkey = key
		}
	}

	return certificate, nil
}

// SetAuthority is the Go only configuration of the signing CA.
func (c *Certificate) SetAuthority(authority *Authority) *Certificate {
	if nil != c {
		c.authority = authority
	}
	return c
}

// Export is Certificate::export(): the PEM encoded certificate.
func (c *Certificate) Export() (string, error) {
	if nil == c || nil == c.X509 {
		return "", nil
	}
	return encodeCertificate(c.X509), nil
}

// Fingerprint is Certificate::fingerprint().
func (c *Certificate) Fingerprint(hashAlgorithm string, raw bool) ([]byte, error) {
	if nil == c || nil == c.X509 {
		return nil, nil
	}

	hash, err := hashByName(hashAlgorithm)
	if nil != err {
		return nil, err
	}
	sum := hash(c.X509.Raw)
	if raw {
		return sum, nil
	}
	return []byte(hex.EncodeToString(sum)), nil
}

// CanSign is Certificate::canSign(): X509_PURPOSE_SMIME_SIGN.
func (c *Certificate) CanSign() bool {
	return nil != c && nil != c.X509 && CanSign(c.X509)
}

// CanEncrypt is Certificate::canEncrypt(): X509_PURPOSE_SMIME_ENCRYPT.
func (c *Certificate) CanEncrypt() bool {
	return nil != c && nil != c.X509 && CanEncrypt(c.X509)
}

// CreateSelfSigned is Certificate::createSelfSigned(): a new certificate for the
// distinguished name, signed by the bundled CA (or self-signed without one).
//
// The returned map is the PHP array {pkey, x509}: the (optionally encrypted)
// PEM private key and the PEM certificate.
func (c *Certificate) CreateSelfSigned(passphrase, privateKey string) (map[string]string, error) {
	if nil == c {
		return nil, errors.New("OpenSSL csr: no certificate")
	}

	signer := c.Pkey
	if "" != strings.TrimSpace(privateKey) {
		key, err := parsePrivateKey(privateKey, passphrase)
		if nil != err {
			return nil, fmt.Errorf("OpenSSL pkey: %w", err)
		}
		signer = key
	} else if nil == signer {
		bits := c.KeyBits
		if 0 >= bits {
			bits = defaultKeyBits
		}
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if nil != err {
			return nil, fmt.Errorf("OpenSSL pkey: %w", err)
		}
		signer = key
	}

	days := c.Days
	if 0 >= days {
		days = defaultDays
	}

	serial, err := randomSerial()
	if nil != err {
		return nil, fmt.Errorf("OpenSSL csr: %w", err)
	}

	// openssl.cnf [snappymail_ca]:
	//   basicConstraints = critical,CA:true
	//   keyUsage = nonRepudiation, digitalSignature, keyEncipherment
	//   extendedKeyUsage = clientAuth, emailProtection
	//   subjectAltName = email:copy
	//   subjectKeyIdentifier = hash
	//   authorityKeyIdentifier = keyid:always,issuer
	keyUsage := x509.KeyUsageContentCommitment | x509.KeyUsageDigitalSignature |
		x509.KeyUsageKeyEncipherment

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               c.subject(),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              keyUsage,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageEmailProtection},
		SignatureAlgorithm:    x509.SHA256WithRSA,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(0, 0, days),
		SubjectKeyId:          subjectKeyID(signer.Public()),
	}

	if email := strings.TrimSpace(c.DistinguishedName[DNEmailAddress]); "" != email {
		template.EmailAddresses = []string{email}
	}

	parent := template
	parentKey := signer
	if nil != c.authority && nil != c.authority.Cert && nil != c.authority.Key {
		parent = c.authority.Cert
		parentKey = c.authority.Key
		template.AuthorityKeyId = parent.SubjectKeyId
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, signer.Public(), parentKey)
	if nil != err {
		return nil, fmt.Errorf("OpenSSL sign: %w", err)
	}

	parsed, err := x509.ParseCertificate(der)
	if nil != err {
		return nil, fmt.Errorf("OpenSSL sign: %w", err)
	}

	keyPEM, err := exportPrivateKey(signer, passphrase)
	if nil != err {
		return nil, err
	}

	c.X509 = parsed
	c.Pkey = signer

	return map[string]string{"pkey": keyPEM, "x509": encodeCertificate(parsed)}, nil
}

// subject builds the pkix.Name of the distinguishedName array.
//
// Every attribute goes into ExtraNames: pkix.Name.ToRDNSequence() marshals those
// verbatim, while the typed fields it also has (CommonName, Organization, …)
// would either be dropped or duplicate an entry. The order is the RFC 4514 one
// OpenSSL writes (C, ST, L, O, OU, CN, emailAddress).
func (c *Certificate) subject() pkix.Name {
	names := []pkix.AttributeTypeAndValue{}

	add := func(oid asn1.ObjectIdentifier, key string) {
		value := strings.TrimSpace(c.DistinguishedName[key])
		if "" == value {
			return
		}
		names = append(names, pkix.AttributeTypeAndValue{Type: oid, Value: value})
	}

	add(oidCountry, "countryName")
	add(oidStateOrProvince, "stateOrProvinceName")
	add(oidLocality, "localityName")
	add(oidOrganization, "organizationName")
	// The PHP code unsets an empty organizationalUnitName and nothing else.
	add(oidOrganizationalUnit, "organizationalUnitName")
	add(oidCommonName, DNCommonName)
	add(oidEmailAddress, DNEmailAddress)

	return pkix.Name{ExtraNames: names}
}

// randomSerial is the serial OpenSSL assigns to a new certificate: 20 random
// bytes with the top bit cleared, so the number stays positive.
func randomSerial() (*big.Int, error) {
	buffer := make([]byte, 20)
	if _, err := rand.Read(buffer); nil != err {
		return nil, err
	}
	buffer[0] &= 0x7f
	return new(big.Int).SetBytes(buffer), nil
}

// ParseCertificate is openssl_x509_read(): a PEM or DER encoded certificate.
func ParseCertificate(value string) (*x509.Certificate, error) {
	trimmed := strings.TrimSpace(value)
	if "" == trimmed {
		return nil, errors.New("empty certificate")
	}

	for rest := []byte(trimmed); ; {
		block, remainder := pem.Decode(rest)
		if nil == block {
			break
		}
		if "CERTIFICATE" == block.Type {
			return x509.ParseCertificate(block.Bytes)
		}
		if 0 == len(remainder) {
			break
		}
		rest = remainder
	}

	return x509.ParseCertificate([]byte(trimmed))
}

// encodeCertificate is openssl_x509_export() with the default $notext = true.
func encodeCertificate(certificate *x509.Certificate) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
}

// Identifier is the "<subjectKeyIdentifier>.crt" file stem of storeCertificate()
// and certificates(): the extension without the colons, uppercase.
//
// PHP falls back to openssl_x509_parse()['hash'] for a certificate without the
// subjectKeyIdentifier extension. That value is an OpenSSL internal hash of the
// subject name, which Go does not reproduce; the first four bytes of the subject
// hash are used instead (documented deviation, only affects such certificates).
func Identifier(certificate *x509.Certificate) string {
	if nil == certificate {
		return ""
	}
	if 0 < len(certificate.SubjectKeyId) {
		return strings.ToUpper(hex.EncodeToString(certificate.SubjectKeyId))
	}

	hash := sha1.Sum(certificate.RawSubject)
	return strings.ToUpper(hex.EncodeToString(hash[:4]))
}

// CanSign reports X509_PURPOSE_SMIME_SIGN: the certificate may sign e-mail.
func CanSign(certificate *x509.Certificate) bool {
	if nil == certificate || !hasEmailProtectionPurpose(certificate) {
		return false
	}
	if 0 == certificate.KeyUsage {
		return true
	}
	return 0 != certificate.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment)
}

// CanEncrypt reports X509_PURPOSE_SMIME_ENCRYPT: the certificate may encrypt
// e-mail.
func CanEncrypt(certificate *x509.Certificate) bool {
	if nil == certificate || !hasEmailProtectionPurpose(certificate) {
		return false
	}
	if 0 == certificate.KeyUsage {
		return true
	}
	return 0 != certificate.KeyUsage&(x509.KeyUsageKeyEncipherment|x509.KeyUsageKeyAgreement)
}

// hasEmailProtectionPurpose reports whether the extended key usage allows
// e-mail protection. A certificate without an extended key usage is usable for
// every purpose, like openssl_x509_checkpurpose() treats it.
func hasEmailProtectionPurpose(certificate *x509.Certificate) bool {
	if 0 == len(certificate.ExtKeyUsage) && 0 == len(certificate.UnknownExtKeyUsage) {
		return true
	}
	for _, usage := range certificate.ExtKeyUsage {
		if x509.ExtKeyUsageEmailProtection == usage || x509.ExtKeyUsageAny == usage {
			return true
		}
	}
	return false
}

// subjectKeyID is subjectKeyIdentifier = hash: method 1 of RFC 5280 4.2.1.2,
// the 160 bit SHA-1 of the subjectPublicKey BIT STRING.
func subjectKeyID(public crypto.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(public)
	if nil != err {
		return nil
	}

	var info struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &info); nil != err {
		return nil
	}

	sum := sha1.Sum(info.PublicKey.Bytes)
	return sum[:]
}

// EmailAddressOf is $data['subject']['emailAddress'] of openssl_x509_parse():
// the PKCS#9 emailAddress attribute of the subject, and "" when it is absent.
func EmailAddressOf(certificate *x509.Certificate) string {
	if nil == certificate {
		return ""
	}
	for _, name := range certificate.Subject.Names {
		if name.Type.Equal(oidEmailAddress) {
			if value, ok := name.Value.(string); ok {
				return value
			}
		}
	}
	return ""
}

// CommonNameOf is $data['subject']['CN'] of openssl_x509_parse().
func CommonNameOf(certificate *x509.Certificate) string {
	if nil == certificate {
		return ""
	}
	return certificate.Subject.CommonName
}
