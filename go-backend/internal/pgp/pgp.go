// Package pgp ports the OpenPGP surface of SnappyMail: the key escrow of
// SnappyMail\PGP\Backup, the engine selection of SnappyMail\PGP\GnuPG and the
// HKP key server lookups of SnappyMail\PGP\Keyservers.
//
// The contract with the PHP backend is the *storage format*: a stored key pair
// is the four element JSON blob Backup::PGPKey() writes (method, base64 nonce,
// base64 payload, hmac) sealed with the main account crypt key, so an operator
// can switch backends and the browser (OpenPGP.js) still finds its keys. That
// format is ported byte for byte in backup.go.
//
// The engine, on the other hand, is a local matter: PHP has two implementations
// (the gpg binary of SnappyMail\GPG\PGP and the pecl gnupg extension, which is
// disabled) and Go has two as well —
//
//   - GnuPG runs the gpg binary with the very same arguments PHP uses, so the
//     output of keyInfo()/import()/verify() is identical on a host that has gpg;
//   - OpenPGP is a pure Go implementation on github.com/ProtonMail/go-crypto
//     that keeps its keyring in <homedir>/snappymail-keys.asc.
//
// GnuPG::getInstance() prefers the binary and falls back to pecl; GetInstance()
// prefers the binary and falls back to the Go engine, which is why
// "Capa.GnuPG" is true whenever "security.gnupg" is on (PHP needs the binary or
// the extension for that capability). The PHP keyInfo()/allKeysInfo() shape —
// the `gpg --with-colons` field set — is reproduced by both engines, because the
// frontend reads it.
//
// Known deviations, each of them documented where it shows:
//
//   - the Go engine reports an empty getEncryptedMessageKeys() list (PHP parses
//     the ENC_TO status lines of a gpg run), and internal/mail does not fill the
//     "keyIds" of a PGP encrypted part;
//   - the gpg binary gets its passphrase from a private temporary file
//     (--passphrase-file) instead of the PINENTRY_USER_DATA pinentry program PHP
//     installs, because Go has no proc_open() with extra file descriptors;
//   - a generated key pair is stored with SerializePrivateWithoutSigning(), so
//     the self-signatures of the entity are kept as they were signed;
//   - signing and encrypting an outgoing message stays a browser task
//     (OpenPGP.js) exactly like in PHP; DoSendMessage() only renders the
//     `signed`/`encrypted` wire shapes of step 19.
package pgp

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// maxSocketSafeLength is the BSD 4.4 socket name budget of the PHP workaround
// (104 minus the "/S.gpg-agent.extra" suffix gives 80 usable characters).
const maxSocketSafeLength = 80

// ErrNotSupported is returned when no engine can be built for the homedir.
var ErrNotSupported = errors.New("pgp: no OpenPGP engine available")

// UID is one user id of a key, the 'uid' record of gpg --with-colons (and the
// array PGPKeySettings parses the address from).
type UID struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Email   string `json:"email"`
	UID     string `json:"uid"`
	Revoked bool   `json:"revoked"`
	Invalid bool   `json:"invalid"`
}

// Subkey is one 'sub'/'ssb' record of keyInfo().
//
// Timestamp/Expires/Length/Algorithm stay strings, because that is what the PHP
// parser copies out of the colon records and what the frontend tolerates.
type Subkey struct {
	Fingerprint       string `json:"fingerprint"`
	KeyID             string `json:"keyid"`
	Timestamp         string `json:"timestamp"`
	Expires           string `json:"expires"`
	Keygrip           string `json:"keygrip,omitempty"`
	IsSecret          bool   `json:"is_secret"`
	Invalid           bool   `json:"invalid"`
	CanEncrypt        bool   `json:"can_encrypt"`
	CanSign           bool   `json:"can_sign"`
	CanCertify        bool   `json:"can_certify"`
	CanAuthenticate   bool   `json:"can_authenticate"`
	Disabled          bool   `json:"disabled"`
	Expired           bool   `json:"expired"`
	Revoked           bool   `json:"revoked"`
	Length            string `json:"length"`
	Algorithm         string `json:"algorithm"`
}

// Key is one 'pub'/'sec' record of keyInfo(), including its uids and subkeys.
type Key struct {
	Disabled   bool     `json:"disabled"`
	Expired    bool     `json:"expired"`
	Revoked    bool     `json:"revoked"`
	IsSecret   bool     `json:"is_secret"`
	CanSign    bool     `json:"can_sign"`
	CanEncrypt bool     `json:"can_encrypt"`
	UIDs       []UID    `json:"uids"`
	Subkeys    []Subkey `json:"subkeys"`

	// CanVerify/CanDecrypt are the renamed capabilities of allKeysInfo():
	// the public listing reports can_verify instead of can_sign and the
	// private listing can_decrypt instead of can_encrypt.
	CanVerify  bool `json:"can_verify,omitempty"`
	CanDecrypt bool `json:"can_decrypt,omitempty"`
}

// KeySet is the allKeysInfo() document.
type KeySet struct {
	Public  []Key `json:"public"`
	Private []Key `json:"private"`
}

// Signature is one entry of _verify()'s signature list.
type Signature struct {
	Fingerprint string
	Validity    int
	Timestamp   int64
	Expires     int64
	Version     int
	Status      int
	Summary     int
	KeyID       string
	UID         string
	Valid       bool
}

// ImportStats is the _importKey() result: the IMPORT_RES status counters.
type ImportStats struct {
	Imported        int    `json:"imported"`
	Unchanged       int    `json:"unchanged"`
	NewUserIDs      int    `json:"newuserids"`
	NewSubkeys      int    `json:"newsubkeys"`
	SecretImported  int    `json:"secretimported"`
	SecretUnchanged int    `json:"secretunchanged"`
	NewSignatures   int    `json:"newsignatures"`
	SkippedKeys     int    `json:"skippedkeys"`
	Fingerprint     string `json:"fingerprint"`
}

// Engine is the subset of SnappyMail\PGP\PGPInterface the DoGnupg* actions use.
//
// A nil Engine means "no engine": every action then reports FalseResponse (or,
// for the ones PHP calls unconditionally, an UnknownError), exactly like the
// "if (!$GPG)" branches of the trait.
type Engine interface {
	// EngineInfo is getEngineInfo().
	EngineInfo() map[string]any
	// Version is getEngineInfo()['version']: the gpg version, or the Go
	// stand-in marker for the pure Go engine.
	Version() string
	// KeyInfo is keyInfo(): every key matching pattern ("" matches all).
	KeyInfo(pattern string, private bool) []Key
	// AllKeysInfo is allKeysInfo().
	AllKeysInfo(pattern string) KeySet
	// Import is import().
	Import(keydata string) (ImportStats, error)
	// Export is export(); a nil passphrase exports the public key, a (possibly
	// empty) one the key pair sealed with it.
	Export(fingerprint string, passphrase *string) (string, error)
	// DeleteKey is deleteKey().
	DeleteKey(keyID string, private bool) (bool, error)
	// GenerateKey is generateKey(); uid is "Name <email>".
	GenerateKey(uid, passphrase string) (string, error)
	// Sign is sign(): clearsigned by default.
	Sign(plaintext string) (string, error)
	// Verify is verify(): signature is the detached signature, or "" for
	// clearsigned text.
	Verify(signedText, signature string) ([]Signature, error)
	// Decrypt is decrypt().
	Decrypt(text string) (string, error)
	// Encrypt is encrypt(): the recipients are the AddEncryptKey() keys.
	Encrypt(plaintext string) (string, error)
	// EncryptedMessageKeys is getEncryptedMessageKeys(): the ENC_TO key ids.
	EncryptedMessageKeys(data string) []string
	// AddEncryptKey is addEncryptKey().
	AddEncryptKey(fingerprint string) bool
	// AddDecryptKey is addDecryptKey(): the passphrase is remembered for the
	// next Decrypt()/Export().
	AddDecryptKey(fingerprint, passphrase string) bool
	// ClearKeys is clear().
	ClearKeys() bool
}

// FindBinary is GPG\Base::findBinary(): PATH first, then the usual locations.
//
// Unlike PHP this never runs a shell, so a host without the binary simply gets
// "" and the OpenPGP engine is used.
func FindBinary(name string) string {
	if path, err := exec.LookPath(name); nil == err {
		return path
	}

	for _, directory := range []string{
		"/sw/bin/",
		"/usr/bin/",
		"/usr/local/bin/",
		"/opt/local/bin/",
		"/run/current-system/sw/bin/",
	} {
		candidate := directory + name
		if info, err := os.Stat(candidate); nil == err && !info.IsDir() && 0 != info.Mode()&0o111 {
			return candidate
		}
	}

	return ""
}

// GnuPGSupported is SnappyMail\PGP\GnuPG::isSupported(): the gpg binary can be
// executed, i.e. the GnuPG engine (not the Go one) is available.
func GnuPGSupported() bool { return "" != FindBinary("gpg") }

// EngineVersion is
// SnappyMail\PGP\GnuPG::getInstance('')->getEngineInfo()['version']: the gpg
// version of the host, or the Go engine marker. It never fails, because the Go
// engine is always available.
func EngineVersion() string {
	if binary := FindBinary("gpg"); "" != binary {
		// An empty home directory: `gpg --with-colons --list-config` does not
		// touch one.
		return NewGnuPG(binary, "").Version()
	}
	return openpgpVersion
}

// GetInstance is SnappyMail\PGP\GnuPG::getInstance(): the engine of a homedir.
//
// The homedir is created when missing and must be writable, otherwise the PHP
// method throws ("gpg homedir ... not writable").
func GetInstance(homedir string) (Engine, error) {
	homedir, err := prepareHomeDir(homedir)
	if nil != err {
		return nil, err
	}

	if binary := FindBinary("gpg"); "" != binary {
		return NewGnuPG(binary, homedir), nil
	}

	return NewOpenPGP(homedir)
}

// prepareHomeDir is the homedir block of Actions\Pgp::GnuPG().
func prepareHomeDir(homedir string) (string, error) {
	homedir = strings.TrimRight(strings.TrimSpace(homedir), `/\`)
	if "" == homedir {
		return "", errors.New("pgp: empty gnupg home directory")
	}

	if err := storage.Mkdir(homedir); nil != err {
		return "", err
	}
	if !isWritable(homedir) {
		return "", errors.New("gpg homedir '" + homedir + "' not writable")
	}

	// Workaround for "socket name for '/very/long/path/to/.gnupg/S.gpg-agent
	// .extra' is too long" (BSD 4.4 max length = 104): use a short symlink in
	// the temp directory, else $HOME/.gnupg/<hash of the address>.
	if maxSocketSafeLength < len(homedir) {
		tmpdir := filepath.Join(os.TempDir(), "snappymail")
		if err := storage.Mkdir(tmpdir); nil == err && isWritable(tmpdir) {
			if link := filepath.Join(tmpdir, md5Hex(homedir)); ensureSymlink(homedir, link) {
				homedir = link
			}
		}
	}

	return homedir, nil
}

// ensureSymlink creates link -> target (or accepts an existing link), the
// "is_link($link) || symlink($homedir, $link)" test of the PHP workaround.
func ensureSymlink(target, link string) bool {
	if info, err := os.Lstat(link); nil == err {
		return 0 != info.Mode()&os.ModeSymlink
	}
	return nil == os.Symlink(target, link)
}

// isWritable is is_writable() for a directory.
func isWritable(directory string) bool {
	probe, err := os.CreateTemp(directory, ".pgp-write-test-")
	if nil != err {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// keyIDFromFingerprint is the OpenPGP key id of a fingerprint: the low 64 bits
// for a v4 key (20 bytes) and the first 64 bits for a v6 key (32 bytes), which
// is what gpg prints as the keyid.
func keyIDFromFingerprint(fingerprint []byte) string {
	if 20 == len(fingerprint) {
		return strings.ToUpper(hex.EncodeToString(fingerprint[12:]))
	}
	if 32 == len(fingerprint) {
		return strings.ToUpper(hex.EncodeToString(fingerprint[:8]))
	}
	return strings.ToUpper(hex.EncodeToString(fingerprint))
}

// hexUpper is bin2hex() in the upper case gpg uses for fingerprints.
func hexUpper(value []byte) string { return strings.ToUpper(hex.EncodeToString(value)) }

// The uid patterns of the keyInfo() parser ("Name (Comment) <email>").
var (
	// uidEmailPattern is '/^(.*?)<([^>]+)>$/' of the colon record parser.
	uidEmailPattern = regexp.MustCompile(`^(?s)(.*?)<([^>]+)>$`)
	// uidCommentPattern is '/^(.+?) \(([^\)]+)\)$/' of the colon record parser.
	uidCommentPattern = regexp.MustCompile(`^(?s)(.+?) \(([^)]+)\)$`)
	// bareEmailPattern is '/^[\S]+@[\S]+$/' of the colon record parser.
	bareEmailPattern = regexp.MustCompile(`^[^\s]+@[^\s]+$`)
)

