package pgp

// The gpg binary engine (Step 25), the port of SnappyMail\GPG\PGP.
//
// Go has no proc_open() with extra file descriptors, so the status lines of
// `--status-fd` are collected through `--status-file` and the passphrase of a
// `--pinentry-mode loopback` run is handed over in a private temporary file
// instead of the PINENTRY_USER_DATA environment variable PHP uses. Everything
// else — the argument set, the `--with-colons` parser, the GOODSIG/VALIDSIG
// handling, the IMPORT_RES counters — follows the PHP class so a host with gpg
// produces the same documents as the PHP backend.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gpgTimeout is the process budget; the PHP driver aborts after 5 seconds, Go
// gives a slow key server or a big import a bit more room.
const gpgTimeout = 30 * time.Second

// The status line prefix of gpg (the PHP "[GNUPG:] " magic).
const statusPrefix = "[GNUPG:] "

// GnuPG is SnappyMail\GPG\PGP for one home directory.
type GnuPG struct {
	binary  string
	homedir string

	version     string
	versionDone bool

	encryptKeys []string
	pinentries  map[string]string
	armor       bool
	signMode    int
}

// The GNUPG_SIG_MODE_* values of the PGP interface.
const (
	signModeNormal = 0
	signModeDetach = 1
	signModeClear  = 2
)

// NewGnuPG builds the binary engine.
func NewGnuPG(binary, homedir string) *GnuPG {
	return &GnuPG{
		binary:     binary,
		homedir:    homedir,
		pinentries: map[string]string{},
		armor:      true,
		signMode:   signModeClear,
	}
}

// EngineInfo is getEngineInfo().
func (g *GnuPG) EngineInfo() map[string]any {
	return map[string]any{
		"protocol":  0,
		"file_name": g.binary,
		"home_dir":  g.homedir,
		"version":   g.Version(),
	}
}

// Version is getEngineInfo()['version']: the "cfg:version" line of
// `gpg --with-colons --list-config`.
func (g *GnuPG) Version() string {
	if g.versionDone {
		return g.version
	}
	g.versionDone = true

	result, _ := g.run([]string{"--with-colons", "--list-config"}, "")
	for _, line := range strings.Split(result.output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if 3 <= len(fields) && "cfg" == fields[0] && "version" == fields[1] {
			g.version = fields[2]
			break
		}
	}

	return g.version
}

// ClearKeys is clear().
func (g *GnuPG) ClearKeys() bool {
	g.encryptKeys = nil
	g.pinentries = map[string]string{}
	return true
}

// AddEncryptKey is addEncryptKey().
func (g *GnuPG) AddEncryptKey(fingerprint string) bool {
	if "" == fingerprint {
		return false
	}
	g.encryptKeys = append(g.encryptKeys, fingerprint)
	return true
}

// AddDecryptKey is addDecryptKey().
func (g *GnuPG) AddDecryptKey(fingerprint, passphrase string) bool {
	g.pinentries[fingerprint] = passphrase
	return true
}

// gpgRun is the result of one gpg invocation.
type gpgRun struct {
	output string
	status []string
	errors []string
}

// passphrase is Base::getPassphrase() for the key ids of a status line.
func (g *GnuPG) passphrase(keyID string) string {
	for fingerprint, passphrase := range g.pinentries {
		length := len(fingerprint)
		if len(keyID) < length {
			length = len(keyID)
		}
		if 0 < length && strings.HasSuffix(keyID, fingerprint[len(fingerprint)-length:]) {
			return passphrase
		}
	}
	return ""
}

// firstPassphrase is the passphrase to unlock the (only) operation key.
func (g *GnuPG) firstPassphrase() string {
	for _, passphrase := range g.pinentries {
		return passphrase
	}
	return ""
}

// run executes gpg with input on stdin. The status lines of the run are
// collected from a temporary --status-file, errors from stderr.
func (g *GnuPG) run(arguments []string, input string) (*gpgRun, error) {
	statusFile, err := os.CreateTemp("", "sm-gpg-status-")
	if nil != err {
		return &gpgRun{}, err
	}
	statusName := statusFile.Name()
	_ = statusFile.Close()
	defer func() { _ = os.Remove(statusName) }()

	args := []string{
		"--status-file", statusName,
	}
	if "" != g.homedir {
		args = append(args, "--homedir", g.homedir)
	}
	args = append(args,
		"--no-tty",
		"--no-options",
		"--no-secmem-warning",
		"--no-permission-warning",
		"--trust-model", "always",
		"--batch",
	)
	args = append(args, arguments...)

	passphraseFile, cleanup, err := g.passphraseArgs(argumentIsPassphrase(arguments))
	if nil != err {
		return &gpgRun{}, err
	}
	defer cleanup()
	args = append(args, passphraseFile...)

	ctx, cancel := context.WithTimeout(context.Background(), gpgTimeout)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	command := exec.CommandContext(ctx, g.binary, args...)
	command.Stdin = strings.NewReader(input)
	command.Stdout = &stdout
	command.Stderr = &stderr
	// Don't localize the results.
	command.Env = append(os.Environ(), "LC_ALL=C")

	runErr := command.Run()

	result := &gpgRun{output: stdout.String(), errors: splitLines(stderr.String())}

	if content, readErr := os.ReadFile(statusName); nil == readErr {
		for _, line := range strings.Split(string(content), "\n") {
			if strings.HasPrefix(line, statusPrefix) {
				result.status = append(result.status, strings.TrimPrefix(line, statusPrefix))
				continue
			}
			// Older gpg versions write the prefix without the trailing space.
			if trimmed := strings.TrimPrefix(line, "[GNUPG:]"); trimmed != line {
				result.status = append(result.status, strings.TrimSpace(trimmed))
			}
		}
	}

	if nil != runErr && ctx.Err() != nil {
		return result, errors.New("gpg: timeout")
	}

	// PHP throws when gpg reported an error and the exit code is non-zero.
	if nil != runErr && argumentIsStrict(arguments) {
		return result, fmt.Errorf("gpg: %s", strings.Join(result.errors, ". "))
	}

	return result, nil
}

// passphraseArgs adds the loopback pinentry arguments when the operation needs a
// passphrase, and returns the file to remove afterwards.
func (g *GnuPG) passphraseArgs(needed bool) ([]string, func(), error) {
	cleanup := func() {}

	passphrase := g.firstPassphrase()
	if !needed || "" == passphrase {
		return []string{"--pinentry-mode", "cancel"}, cleanup, nil
	}

	file, err := os.CreateTemp("", "sm-gpg-pass-")
	if nil != err {
		return nil, cleanup, err
	}
	name := file.Name()
	cleanup = func() { _ = os.Remove(name) }

	if _, err := file.WriteString(passphrase + "\n"); nil != err {
		_ = file.Close()
		return nil, cleanup, err
	}
	if err := file.Close(); nil != err {
		return nil, cleanup, err
	}

	return []string{"--pinentry-mode", "loopback", "--passphrase-file", name}, cleanup, nil
}

// argumentIsPassphrase reports whether the operation needs the private key: the
// decrypt, sign, export-secret-keys, generate and delete-secret-key runs.
func argumentIsPassphrase(arguments []string) bool {
	for _, argument := range arguments {
		switch argument {
		case "--decrypt", "--clearsign", "--sign", "--detach-sign",
			"--export-secret-keys", "--quick-gen-key", "--quick-add-key",
			"--delete-secret-key":
			return true
		}
	}
	return false
}

// argumentIsStrict reports whether a non-zero exit code is a failure. The
// inspection commands (key listing, config listing) succeed with an empty
// keyring and report the condition through the exit code.
func argumentIsStrict(arguments []string) bool {
	for _, argument := range arguments {
		switch argument {
		case "--list-config", "--list-public-keys", "--list-secret-keys":
			return false
		}
	}
	return true
}

// KeyInfo is keyInfo().
func (g *GnuPG) KeyInfo(pattern string, private bool) []Key {
	arguments := []string{
		"--with-colons",
		"--with-fingerprint",
		"--with-fingerprint",
		"--fixed-list-mode",
	}
	if private {
		arguments = append(arguments, "--list-secret-keys")
	} else {
		arguments = append(arguments, "--list-public-keys")
	}
	if "" != strings.TrimSpace(pattern) {
		arguments = append(arguments, "--utf8-strings", pattern)
	}

	result, err := g.run(arguments, "")
	if nil != err && "" == result.output {
		return nil
	}

	return parseColons(result.output)
}

// AllKeysInfo is allKeysInfo().
func (g *GnuPG) AllKeysInfo(pattern string) KeySet {
	result := KeySet{Public: []Key{}, Private: []Key{}}

	for _, key := range g.KeyInfo(pattern, false) {
		key.CanVerify = key.CanSign
		key.CanSign = false
		result.Public = append(result.Public, key)
	}
	for _, key := range g.KeyInfo(pattern, true) {
		key.CanDecrypt = key.CanEncrypt
		key.CanEncrypt = false
		result.Private = append(result.Private, key)
	}

	return result
}

// parseColons is the state machine of keyInfo(): 'pub'/'sec' start a key,
// 'sub'/'ssb' add a subkey, 'fpr'/'grp'/'uid' fill it.
func parseColons(output string) []Key {
	var keys []Key
	var current *Key

	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, ":")
		if 2 > len(fields) {
			continue
		}

		switch fields[0] {
		case "sec", "pub":
			if nil != current {
				keys = append(keys, *current)
			}
			current = &Key{
				IsSecret:   "ssb" == fields[0],
				CanSign:    capability(fields, 's'),
				CanEncrypt: capability(fields, 'e'),
				UIDs:       []UID{},
				Subkeys:    []Subkey{},
			}
			fallthrough

		case "ssb", "sub":
			if nil == current {
				continue
			}
			current.Subkeys = append(current.Subkeys, colonSubkey(fields))
			continue

		case "fpr":
			if nil == current || 0 == len(current.Subkeys) || 10 > len(fields) {
				continue
			}
			current.Subkeys[len(current.Subkeys)-1].Fingerprint = fields[9]
			continue

		case "grp":
			if nil == current || 0 == len(current.Subkeys) || 10 > len(fields) {
				continue
			}
			current.Subkeys[len(current.Subkeys)-1].Keygrip = fields[9]
			continue

		case "uid":
			if nil == current || 10 > len(fields) {
				continue
			}
			uid := parseUID(fields[9])
			uid.Revoked = "r" == fieldAt(fields, 1)
			current.UIDs = append(current.UIDs, uid)
			continue
		}
	}

	if nil != current {
		keys = append(keys, *current)
	}

	return keys
}

// colonSubkey builds one 'sub'/'ssb' record.
func colonSubkey(fields []string) Subkey {
	return Subkey{
		KeyID:           fieldAt(fields, 4),
		Timestamp:       fieldAt(fields, 5),
		Expires:         fieldAt(fields, 6),
		IsSecret:        "ssb" == fieldAt(fields, 0),
		CanEncrypt:      capability(fields, 'e'),
		CanSign:         capability(fields, 's'),
		CanCertify:      capability(fields, 'c'),
		CanAuthenticate: capability(fields, 'a'),
		Revoked:         "r" == fieldAt(fields, 1),
		Length:          fieldAt(fields, 2),
		Algorithm:       fieldAt(fields, 3),
	}
}

// fieldAt returns field index or "" (the PHP undefined offset warning branch).
func fieldAt(fields []string, index int) string {
	if index >= len(fields) {
		return ""
	}
	return fields[index]
}

// capability is str_contains($tokens[11], $letter): the capability string of a
// colon record ("esca", "es", …).
func capability(fields []string, letter byte) bool {
	return strings.ContainsRune(fieldAt(fields, 11), rune(letter))
}

// Import is import().
func (g *GnuPG) Import(keydata string) (ImportStats, error) {
	stats := ImportStats{}

	if "" == strings.TrimSpace(keydata) {
		return stats, errors.New("No valid input data found.")
	}

	result, err := g.run([]string{"--import"}, keydata)
	if nil != err {
		return stats, err
	}

	for _, line := range result.status {
		if !strings.HasPrefix(line, "IMPORT_RES") {
			continue
		}
		// IMPORT_RES <count> <no_user_id> <imported> <imported_rsa> <unchanged>
		//   <n_uids> <n_subk> <n_sigs> <n_revoc> <sec_read> <sec_imported> …
		fields := strings.Fields(strings.TrimPrefix(line, "IMPORT_RES"))
		return ImportStats{
			Imported:        importField(fields, 2),
			Unchanged:       importField(fields, 4),
			NewUserIDs:      importField(fields, 5),
			NewSubkeys:      importField(fields, 6),
			NewSignatures:   importField(fields, 7),
			SecretImported:  importField(fields, 10),
			SecretUnchanged: importField(fields, 11),
			SkippedKeys:     importField(fields, 12),
			Fingerprint:     "",
		}, nil
	}

	// PHP: "return false" when the status block carries no IMPORT_RES (the run
	// itself succeeded); the action reports that as a false import result.
	return stats, nil
}

// importField reads one IMPORT_RES counter.
func importField(fields []string, index int) int {
	if index >= len(fields) {
		return 0
	}
	number, _ := strconv.Atoi(fields[index])
	return number
}

// Export is export().
func (g *GnuPG) Export(fingerprint string, passphrase *string) (string, error) {
	private := nil != passphrase

	keys := g.KeyInfo(fingerprint, private)
	if 0 == len(keys) {
		if private {
			return "", errors.New("Private key not found: " + fingerprint)
		}
		return "", errors.New("Public key not found: " + fingerprint)
	}

	target := fingerprint
	if 0 < len(keys[0].Subkeys) {
		if "" != keys[0].Subkeys[0].Fingerprint {
			target = keys[0].Subkeys[0].Fingerprint
		} else if "" != keys[0].Subkeys[0].KeyID {
			target = keys[0].Subkeys[0].KeyID
		}
	}

	if private {
		g.AddDecryptKey(target, *passphrase)
	}

	arguments := []string{"--export", "--armor"}
	if private {
		arguments[0] = "--export-secret-keys"
	}
	if "" != target {
		arguments = append(arguments, target)
	}

	result, err := g.run(arguments, "")
	if nil != err {
		return "", err
	}
	if "" == strings.TrimSpace(result.output) {
		return "", errors.New("gpg: export failed")
	}

	return result.output, nil
}

// DeleteKey is deleteKey().
func (g *GnuPG) DeleteKey(keyID string, private bool) (bool, error) {
	keys := g.KeyInfo(keyID, private)
	if 0 == len(keys) {
		if private {
			return false, errors.New("Private key not found: " + keyID)
		}
		return false, errors.New("Public key not found: " + keyID)
	}

	target := keyID
	if 0 < len(keys[0].Subkeys) && "" != keys[0].Subkeys[0].Fingerprint {
		target = keys[0].Subkeys[0].Fingerprint
	}

	operation := "--delete-key"
	if private {
		operation = "--delete-secret-key"
	}

	result, err := g.run([]string{"--yes", operation, target}, "")
	if nil != err && "" == result.output {
		// gpg reports a missing secret key on stderr; the PHP method only
		// reports the boolean of the run.
		return false, nil
	}

	return nil == err, nil
}

// GenerateKey is generateKey(): `--quick-gen-key "<name> <email>" algo usage`.
func (g *GnuPG) GenerateKey(uid, passphrase string) (string, error) {
	if "" != passphrase {
		g.AddDecryptKey("", passphrase)
	}

	result, err := g.run([]string{
		"--yes",
		"--quick-gen-key", uid, "default", "default", "0",
	}, "")
	if nil != err {
		return "", err
	}

	for _, line := range result.status {
		fields := strings.Fields(line)
		if 3 <= len(fields) && "KEY_CREATED" == fields[0] {
			return fields[2], nil
		}
	}

	return "", errors.New("gpg: key generation failed")
}

// Sign is sign(): clearsigning by default, like the PGP interface does.
func (g *GnuPG) Sign(plaintext string) (string, error) {
	arguments := []string{}
	switch g.signMode {
	case signModeNormal:
		arguments = append(arguments, "--sign")
	case signModeDetach:
		arguments = append(arguments, "--detach-sign")
	default:
		arguments = append(arguments, "--clearsign")
	}
	if g.armor {
		arguments = append(arguments, "--armor")
	}
	arguments = append(arguments, "--textmode")

	if 0 == len(g.pinentries) {
		return "", errors.New("No signing keys specified.")
	}
	for fingerprint := range g.pinentries {
		if "" != fingerprint {
			arguments = append(arguments, "--local-user", fingerprint)
		}
	}

	result, err := g.run(arguments, plaintext)
	if nil != err {
		return "", err
	}

	return result.output, nil
}

// Verify is verify().
func (g *GnuPG) Verify(signedText, signature string) ([]Signature, error) {
	arguments := []string{"--verify"}

	if "" != signature {
		file, err := os.CreateTemp("", "sm-gpg-sig-")
		if nil != err {
			return nil, err
		}
		name := file.Name()
		defer func() { _ = os.Remove(name) }()

		if _, err := file.WriteString(signature); nil != err {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); nil != err {
			return nil, err
		}

		arguments = append(arguments, name, "-")
	}

	result, _ := g.run(arguments, signedText)

	return parseVerifyStatus(result.status), nil
}

// parseVerifyStatus is the status switch of _verify().
func parseVerifyStatus(status []string) []Signature {
	var signatures []Signature

	for _, line := range status {
		fields := strings.Fields(line)
		if 0 == len(fields) {
			continue
		}

		switch fields[0] {
		case "EXPSIG", "EXPKEYSIG", "REVKEYSIG", "BADSIG", "ERRSIG", "GOODSIG":
			good := "GOODSIG" == fields[0]
			signature := Signature{
				Status:  1,
				Summary: 4,
				KeyID:   fieldAt(fields, 1),
				UID:     strings.Join(fields[minInt(2, len(fields)):], " "),
			}
			if good {
				signature.Status = 0
				signature.Summary = 0
			}
			signatures = append(signatures, signature)

		case "VALIDSIG":
			if 0 == len(signatures) {
				continue
			}
			last := &signatures[len(signatures)-1]
			last.Fingerprint = fieldAt(fields, 1)
			last.Timestamp = int64(importField(fields, 3))
			last.Expires = int64(importField(fields, 4))
			last.Version = importField(fields, 5)
		}
	}

	return signatures
}

// Decrypt is decrypt().
func (g *GnuPG) Decrypt(text string) (string, error) {
	result, err := g.run([]string{"--decrypt", "--skip-verify"}, text)
	if nil != err {
		return "", err
	}

	return result.output, nil
}

// Encrypt is encrypt().
func (g *GnuPG) Encrypt(plaintext string) (string, error) {
	if 0 == len(g.encryptKeys) {
		return "", errors.New("No encryption keys specified.")
	}

	arguments := []string{"--encrypt"}
	if g.armor {
		arguments = append(arguments, "--armor")
	}
	for _, fingerprint := range g.encryptKeys {
		arguments = append(arguments, "--recipient", fingerprint)
	}

	result, err := g.run(arguments, plaintext)
	if nil != err {
		return "", err
	}

	return result.output, nil
}

// EncryptedMessageKeys is getEncryptedMessageKeys(): the ENC_TO status lines of
// a decrypt attempt (which fails without the private key, and that is fine).
func (g *GnuPG) EncryptedMessageKeys(data string) []string {
	result, _ := g.run([]string{"--decrypt", "--skip-verify"}, data)

	keys := []string{}
	for _, line := range result.status {
		fields := strings.Fields(line)
		if 2 <= len(fields) && "ENC_TO" == fields[0] {
			keys = append(keys, fields[1])
		}
	}

	return keys
}

// splitLines is preg_split('/\R/') for the stderr output.
func splitLines(value string) []string {
	lines := []string{}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if "" != line {
			lines = append(lines, line)
		}
	}
	return lines
}

// minInt is PHP's min() for two ints.
func minInt(first, second int) int {
	if first < second {
		return first
	}
	return second
}
