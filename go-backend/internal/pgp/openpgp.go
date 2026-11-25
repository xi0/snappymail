package pgp

// The pure Go OpenPGP engine (Step 25).
//
// PHP reaches GnuPG through the gpg binary (SnappyMail\GPG\PGP) or the pecl
// gnupg extension (disabled). A Go binary must not depend on a host executable,
// so this engine implements the same PGPInterface subset on
// github.com/ProtonMail/go-crypto and keeps its keyring in
// <homedir>/snappymail-keys.asc (one armored block, entity after entity).
//
// The information shape of keyInfo()/allKeysInfo() is the `gpg --with-colons`
// field set the frontend reads (disabled/expired/revoked/is_secret/can_sign/
// can_encrypt/uids/subkeys), including the string typed timestamp/length/
// algorithm fields, so both engines are interchangeable for the actions.

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The engine identity and the armor block types it writes.
const (
	// openpgpVersion is getEngineInfo()['version'] of the Go engine.
	openpgpVersion = "go-openpgp"
	// openpgpKeyringFile is the keyring inside the gnupg home directory.
	openpgpKeyringFile = "snappymail-keys.asc"
	// The armor headers of the exported blocks.
	armorPrivateKey = "PGP PRIVATE KEY BLOCK"
	armorPublicKey  = "PGP PUBLIC KEY BLOCK"
	armorMessage    = "PGP MESSAGE"
)

// The OpenPGP public key algorithm numbers of RFC 4880 §9.1, used instead of the
// packet constants so the capability mapping stays explicit.
const (
	algoRSA               = 1
	algoRSAEncryptOnly    = 2
	algoRSASignOnly       = 3
	algoElgamal           = 16
	algoDSA               = 17
	algoECDH              = 18
	algoECDSA             = 19
	algoEdDSA             = 22
	algoX25519            = 25
	algoX448              = 26
	algoEd25519           = 27
	algoEd448             = 28
)

// OpenPGP is the Go implementation of the Engine interface.
//
// It is not safe for concurrent use: one request creates one engine, like the
// PHP GnuPG instance.
type OpenPGP struct {
	homedir    string
	encryptKey []string
	pinentries map[string]string
}

// NewOpenPGP builds the engine for a gnupg home directory, creating it when
// needed.
func NewOpenPGP(homedir string) (Engine, error) {
	homedir = strings.TrimRight(strings.TrimSpace(homedir), `/\`)
	if "" == homedir {
		return nil, errors.New("pgp: empty gnupg home directory")
	}
	if err := storage.Mkdir(homedir); nil != err {
		return nil, err
	}
	return &OpenPGP{homedir: homedir, pinentries: map[string]string{}}, nil
}

// keyringPath is the keyring file of the home directory.
func (e *OpenPGP) keyringPath() string { return filepath.Join(e.homedir, openpgpKeyringFile) }

// EngineInfo is getEngineInfo().
func (e *OpenPGP) EngineInfo() map[string]any {
	return map[string]any{
		"protocol":  0, // GPGME_PROTOCOL_OpenPGP
		"file_name": "",
		"home_dir":  e.homedir,
		"version":   e.Version(),
	}
}

// Version is getEngineInfo()['version'].
func (e *OpenPGP) Version() string { return openpgpVersion }

// ClearKeys is clear().
func (e *OpenPGP) ClearKeys() bool {
	e.encryptKey = nil
	e.pinentries = map[string]string{}
	return true
}

// AddEncryptKey is addEncryptKey().
func (e *OpenPGP) AddEncryptKey(fingerprint string) bool {
	if "" == fingerprint {
		return false
	}
	e.encryptKey = append(e.encryptKey, fingerprint)
	return true
}

// AddDecryptKey is addDecryptKey(): the passphrase is remembered for the next
// Decrypt()/Export() call, keyed like the PHP pinentry list.
func (e *OpenPGP) AddDecryptKey(fingerprint, passphrase string) bool {
	if nil == e.pinentries {
		e.pinentries = map[string]string{}
	}
	e.pinentries[fingerprint] = passphrase
	return true
}

// passphraseFor is Base::getPassphrase(): the pinentry whose key id ends with
// the last characters of the requested id.
func (e *OpenPGP) passphraseFor(keyID string) (string, bool) {
	for fingerprint, passphrase := range e.pinentries {
		length := len(fingerprint)
		if len(keyID) < length {
			length = len(keyID)
		}
		if 0 < length && strings.EqualFold(fingerprint[len(fingerprint)-length:], keyID[len(keyID)-length:]) {
			return passphrase, true
		}
	}
	return "", false
}

// load reads the keyring; a missing file is an empty keyring.
func (e *OpenPGP) load() (openpgp.EntityList, error) {
	content, err := os.ReadFile(e.keyringPath())
	if nil != err {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if 0 == len(bytes.TrimSpace(content)) {
		return nil, nil
	}
	return readKeyBlocks(string(content))
}

// save writes the keyring as one armored block (or removes it when empty).
func (e *OpenPGP) save(list openpgp.EntityList) error {
	if 0 == len(list) {
		_ = os.Remove(e.keyringPath())
		return nil
	}

	buffer := &bytes.Buffer{}
	writer, err := armor.Encode(buffer, armorPrivateKey, nil)
	if nil != err {
		return err
	}
	for _, entity := range list {
		// The private keys are stored encrypted (when they have a passphrase),
		// so the user ids may not be re-signed: the encrypted key packet has no
		// signer left. SerializePrivateWithoutSigning() writes the packets as
		// they are. A public-only key (an imported public block) has no private
		// part at all.
		var err error
		if nil != entity.PrivateKey {
			err = entity.SerializePrivateWithoutSigning(writer, nil)
		} else {
			err = entity.Serialize(writer)
		}
		if nil != err {
			_ = writer.Close()
			return err
		}
	}
	if err := writer.Close(); nil != err {
		return err
	}

	return storage.WriteString(e.keyringPath(), buffer.String())
}

// readKeyBlocks parses one or more armored key blocks, or a binary keyring.
func readKeyBlocks(data string) (openpgp.EntityList, error) {
	trimmed := strings.TrimSpace(data)
	if "" == trimmed {
		return nil, nil
	}

	if !strings.Contains(trimmed, "-----BEGIN PGP") {
		return openpgp.ReadKeyRing(strings.NewReader(trimmed))
	}

	var result openpgp.EntityList
	reader := strings.NewReader(trimmed)
	for {
		block, err := armor.Decode(reader)
		if io.EOF == err {
			break
		}
		if nil != err {
			return nil, err
		}
		entities, err := openpgp.ReadKeyRing(block.Body)
		if nil != err {
			return nil, err
		}
		result = append(result, entities...)
	}

	if 0 == len(result) {
		return nil, errors.New("no OpenPGP key found")
	}

	return result, nil
}

// findEntity locates a key by fingerprint or key id (an optional "0x" prefix and
// the case are ignored, like gpg does).
func findEntity(list openpgp.EntityList, keyID string) *openpgp.Entity {
	keyID = strings.TrimPrefix(strings.TrimSpace(keyID), "0x")
	keyID = strings.TrimPrefix(keyID, "0X")
	keyID = strings.ToUpper(keyID)
	if "" == keyID {
		return nil
	}

	for _, entity := range list {
		if nil == entity.PrimaryKey {
			continue
		}
		if keyID == hexUpper(entity.PrimaryKey.Fingerprint) ||
			keyID == keyIDFromFingerprint(entity.PrimaryKey.Fingerprint) ||
			strings.HasSuffix(hexUpper(entity.PrimaryKey.Fingerprint), keyID) {
			return entity
		}
	}
	return nil
}

// KeyInfo is keyInfo().
func (e *OpenPGP) KeyInfo(pattern string, private bool) []Key {
	list, err := e.load()
	if nil != err {
		return nil
	}

	result := []Key{}
	for _, entity := range list {
		if private && nil == entity.PrivateKey {
			continue
		}
		if !entityMatches(entity, pattern) {
			continue
		}
		result = append(result, openPGPKeyInfo(entity))
	}
	return result
}

// AllKeysInfo is allKeysInfo(): the public and the private listing with the
// renamed capabilities.
func (e *OpenPGP) AllKeysInfo(pattern string) KeySet {
	result := KeySet{Public: []Key{}, Private: []Key{}}

	for _, key := range e.KeyInfo(pattern, false) {
		key.CanVerify = key.CanSign
		key.CanSign = false
		result.Public = append(result.Public, key)
	}
	for _, key := range e.KeyInfo(pattern, true) {
		key.CanDecrypt = key.CanEncrypt
		key.CanEncrypt = false
		result.Private = append(result.Private, key)
	}

	return result
}

// entityMatches is the "gpg --list-keys $pattern" filter: an empty pattern
// matches every key, otherwise the pattern has to be part of a uid, the
// fingerprint or the key id.
func entityMatches(entity *openpgp.Entity, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if "" == pattern {
		return true
	}

	if nil != entity.PrimaryKey {
		if strings.Contains(strings.ToLower(hexUpper(entity.PrimaryKey.Fingerprint)), pattern) ||
			strings.Contains(strings.ToLower(keyIDFromFingerprint(entity.PrimaryKey.Fingerprint)), pattern) {
			return true
		}
	}

	for uid := range entity.Identities {
		if strings.Contains(strings.ToLower(uid), pattern) {
			return true
		}
	}

	return false
}

// openPGPKeyInfo converts one entity into the keyInfo() document.
func openPGPKeyInfo(entity *openpgp.Entity) Key {
	key := Key{UIDs: []UID{}, Subkeys: []Subkey{}}

	if nil == entity.PrimaryKey {
		return key
	}

	key.IsSecret = false // PHP quirk: keyInfo() sets is_secret to ("ssb" === record)
	key.CanSign = canEncryptOrSign(uint8(entity.PrimaryKey.PubKeyAlgo), true)
	key.CanEncrypt = canEncryptOrSign(uint8(entity.PrimaryKey.PubKeyAlgo), false)

	// gpg lists the uids in keyring order; a Go map has none, so they are
	// sorted to keep the output stable.
	uids := make([]string, 0, len(entity.Identities))
	for uid := range entity.Identities {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	for _, uid := range uids {
		key.UIDs = append(key.UIDs, parseUID(uid))
	}

	key.Subkeys = append(key.Subkeys, subkeyInfo(entity.PrimaryKey, nil != entity.PrivateKey, true))
	for _, subkey := range entity.Subkeys {
		if nil == subkey.PublicKey {
			continue
		}
		key.Subkeys = append(key.Subkeys, subkeyInfo(subkey.PublicKey, nil != subkey.PrivateKey, false))
	}

	return key
}

// subkeyInfo converts one key packet into a 'sub'/'ssb' record.
func subkeyInfo(pk *packet.PublicKey, secret bool, primary bool) Subkey {
	algorithm := uint8(pk.PubKeyAlgo)

	return Subkey{
		Fingerprint:     hexUpper(pk.Fingerprint),
		KeyID:           keyIDFromFingerprint(pk.Fingerprint),
		Timestamp:       strconv.FormatInt(pk.CreationTime.Unix(), 10),
		Expires:         "",
		IsSecret:        secret,
		CanEncrypt:      canEncryptOrSign(algorithm, false),
		CanSign:         canEncryptOrSign(algorithm, true),
		CanCertify:      primary,
		CanAuthenticate: true,
		Revoked:         false,
		Length:          strconv.Itoa(publicKeyBits(pk)),
		Algorithm:       strconv.Itoa(int(algorithm)),
	}
}

// canEncryptOrSign maps an algorithm number onto its capability.
func canEncryptOrSign(algorithm uint8, sign bool) bool {
	if sign {
		switch algorithm {
		case algoRSA, algoRSASignOnly, algoDSA, algoECDSA, algoEdDSA, algoEd25519, algoEd448:
			return true
		}
		return false
	}

	switch algorithm {
	case algoRSA, algoRSAEncryptOnly, algoElgamal, algoECDH, algoX25519, algoX448:
		return true
	}
	return false
}

// publicKeyBits is the "length" field of the colon records.
func publicKeyBits(pk *packet.PublicKey) int {
	if public, ok := pk.PublicKey.(*rsa.PublicKey); ok {
		return public.N.BitLen()
	}
	// Elliptic curve keys report their field size; gpg prints the same number.
	switch uint8(pk.PubKeyAlgo) {
	case algoEdDSA, algoECDSA, algoECDH, algoX25519:
		return 256
	case algoEd25519:
		return 256
	case algoX448, algoEd448:
		return 448
	}
	return 0
}

// Import is import(): add the blocks to the keyring and report the counters.
func (e *OpenPGP) Import(keydata string) (ImportStats, error) {
	stats := ImportStats{}

	if "" == strings.TrimSpace(keydata) {
		return stats, errors.New("No valid input data found.")
	}

	incoming, err := readKeyBlocks(keydata)
	if nil != err {
		return stats, fmt.Errorf("pgp: invalid key data: %w", err)
	}
	if 0 == len(incoming) {
		return stats, errors.New("No valid input data found.")
	}

	current, err := e.load()
	if nil != err {
		return stats, err
	}

	merged := openpgp.EntityList{}
	merged = append(merged, current...)

	for _, entity := range incoming {
		fingerprint := hexUpper(entity.PrimaryKey.Fingerprint)

		var existing *openpgp.Entity
		for _, candidate := range merged {
			if hexUpper(candidate.PrimaryKey.Fingerprint) == fingerprint {
				existing = candidate
				break
			}
		}

		if nil == existing {
			merged = append(merged, entity)
			stats.Imported++
			if nil != entity.PrivateKey {
				stats.SecretImported++
			}
			continue
		}

		stats.Unchanged++
		if nil == existing.PrivateKey && nil != entity.PrivateKey {
			existing.PrivateKey = entity.PrivateKey
			existing.Subkeys = append(existing.Subkeys, entity.Subkeys...)
			stats.SecretImported++
		} else if nil != entity.PrivateKey {
			stats.SecretUnchanged++
		}
	}

	if err := e.save(merged); nil != err {
		return stats, err
	}

	return stats, nil
}

// Export is export(): the armored public key, or the armored key pair when a
// passphrase is given.
func (e *OpenPGP) Export(fingerprint string, passphrase *string) (string, error) {
	list, err := e.load()
	if nil != err {
		return "", err
	}

	entity := findEntity(list, fingerprint)
	if nil == entity {
		return "", errors.New("Public key not found: " + fingerprint)
	}

	if nil == passphrase {
		return armoredEntity(entity, false)
	}

	if nil == entity.PrivateKey {
		return "", errors.New("Private key not found: " + fingerprint)
	}
	if err := e.decryptEntity(entity, *passphrase); nil != err {
		return "", err
	}

	return armoredEntity(entity, true)
}

// armoredEntity serializes one entity as an armored block.
func armoredEntity(entity *openpgp.Entity, private bool) (string, error) {
	blockType := armorPublicKey
	if private {
		blockType = armorPrivateKey
	}

	buffer := &bytes.Buffer{}
	writer, err := armor.Encode(buffer, blockType, nil)
	if nil != err {
		return "", err
	}

	if private {
		// No re-signing: the stored self-signatures stay as they are (and an
		// encrypted key packet has no signer to re-sign with).
		err = entity.SerializePrivateWithoutSigning(writer, nil)
	} else {
		err = entity.Serialize(writer)
	}
	if nil != err {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); nil != err {
		return "", err
	}

	return buffer.String(), nil
}

// decryptEntity unlocks the private keys of an entity, using the given
// passphrase or a remembered pinentry.
func (e *OpenPGP) decryptEntity(entity *openpgp.Entity, passphrase string) error {
	keys := make([]*packet.PrivateKey, 0, len(entity.Subkeys)+1)
	if nil != entity.PrivateKey {
		keys = append(keys, entity.PrivateKey)
	}
	for _, subkey := range entity.Subkeys {
		if nil != subkey.PrivateKey {
			keys = append(keys, subkey.PrivateKey)
		}
	}

	for _, key := range keys {
		if !key.Encrypted {
			continue
		}

		candidate := passphrase
		if "" == candidate {
			if remembered, ok := e.passphraseFor(hexUpper(entity.PrimaryKey.Fingerprint)); ok {
				candidate = remembered
			}
		}
		if err := key.Decrypt([]byte(candidate)); nil != err {
			return fmt.Errorf("pgp: wrong passphrase: %w", err)
		}
	}

	return nil
}

// DeleteKey is deleteKey(): drop the secret key (private) or the whole key.
func (e *OpenPGP) DeleteKey(keyID string, private bool) (bool, error) {
	list, err := e.load()
	if nil != err {
		return false, err
	}

	entity := findEntity(list, keyID)
	if nil == entity {
		if private {
			return false, errors.New("Private key not found: " + keyID)
		}
		return false, errors.New("Public key not found: " + keyID)
	}

	remaining := openpgp.EntityList{}
	for _, candidate := range list {
		if candidate != entity {
			remaining = append(remaining, candidate)
			continue
		}
		if !private {
			continue
		}

		entity.PrivateKey = nil
		for index := range entity.Subkeys {
			entity.Subkeys[index].PrivateKey = nil
		}
		remaining = append(remaining, entity)
	}

	if err := e.save(remaining); nil != err {
		return false, err
	}

	return true, nil
}

// GenerateKey is generateKey(): a new RSA entity whose private keys are sealed
// with passphrase.
func (e *OpenPGP) GenerateKey(uid, passphrase string) (string, error) {
	name, email := splitUID(uid)

	entity, err := openpgp.NewEntity(name, "", email, nil)
	if nil != err {
		return "", err
	}

	if "" != passphrase {
		if nil != entity.PrivateKey && !entity.PrivateKey.Encrypted {
			if err := entity.PrivateKey.Encrypt([]byte(passphrase)); nil != err {
				return "", err
			}
		}
		for index := range entity.Subkeys {
			key := entity.Subkeys[index].PrivateKey
			if nil != key && !key.Encrypted {
				if err := key.Encrypt([]byte(passphrase)); nil != err {
					return "", err
				}
			}
		}
	}

	list, err := e.load()
	if nil != err {
		return "", err
	}
	if err := e.save(append(list, entity)); nil != err {
		return "", err
	}

	fingerprint := hexUpper(entity.PrimaryKey.Fingerprint)
	// The engine knows the passphrase it just used, like an addSignKey() call of
	// the frontend would tell it.
	if "" != passphrase {
		e.AddDecryptKey(fingerprint, passphrase)
	}

	return fingerprint, nil
}

// splitUID turns "Name <email>" into its parts (PGPKeySettings::uid() writes
// exactly that form).
func splitUID(uid string) (name, email string) {
	uid = strings.TrimSpace(uid)

	open := strings.LastIndex(uid, "<")
	close := strings.LastIndex(uid, ">")
	if 0 <= open && open < close {
		email = strings.TrimSpace(uid[open+1 : close])
		name = strings.TrimSpace(uid[:open])
		if "" == name {
			name = email
		}
		return name, email
	}

	if strings.Contains(uid, "@") {
		return uid, uid
	}
	return uid, uid
}

// Sign is sign(): a clearsigned document of the first usable private key.
func (e *OpenPGP) Sign(plaintext string) (string, error) {
	list, err := e.load()
	if nil != err {
		return "", err
	}

	var signer *openpgp.Entity
	var signerErr error
	for _, entity := range list {
		if nil == entity.PrivateKey {
			continue
		}
		if err := e.decryptEntity(entity, ""); nil != err {
			signerErr = err
			continue
		}
		signer = entity
		break
	}
	if nil == signer {
		if nil != signerErr {
			return "", signerErr
		}
		return "", errors.New("No signing keys specified.")
	}

	buffer := &bytes.Buffer{}
	plaintextWriter, err := clearsign.Encode(buffer, signer.PrivateKey, nil)
	if nil != err {
		return "", err
	}
	if _, err := plaintextWriter.Write([]byte(plaintext)); nil != err {
		_ = plaintextWriter.Close()
		return "", err
	}
	if err := plaintextWriter.Close(); nil != err {
		return "", err
	}

	return buffer.String(), nil
}

// Verify is verify(): the signatures found in signedText (a detached signature
// is passed separately).
func (e *OpenPGP) Verify(signedText, signature string) ([]Signature, error) {
	list, err := e.load()
	if nil != err {
		return nil, err
	}

	if "" != signature {
		return verifyDetached(list, []byte(signedText), []byte(signature)), nil
	}

	block, _ := clearsign.Decode([]byte(signedText))
	if nil == block || nil == block.ArmoredSignature {
		return nil, nil
	}

	detached, readErr := io.ReadAll(block.ArmoredSignature.Body)
	if nil != readErr {
		return nil, nil
	}

	return verifyDetached(list, block.Bytes, detached), nil
}

// verifyDetached checks a detached signature against every key of the keyring.
//
// gpg reports ERRSIG (status 1, summary 4) when the signing key is not in the
// keyring; that single "invalid" entry is reproduced so DoPgpVerifyMessage
// answers a document instead of false.
func verifyDetached(list openpgp.EntityList, signed, signature []byte) []Signature {
	for _, entity := range list {
		if nil == entity.PrimaryKey {
			continue
		}
		_, err := openpgp.CheckDetachedSignature(
			openpgp.EntityList{entity},
			bytes.NewReader(signed),
			bytes.NewReader(signature),
			nil,
		)
		if nil != err {
			continue
		}
		return []Signature{{
			Fingerprint: hexUpper(entity.PrimaryKey.Fingerprint),
			KeyID:       keyIDFromFingerprint(entity.PrimaryKey.Fingerprint),
			UID:         firstUID(entity),
			Status:      0,
			Summary:     0,
		}}
	}

	return []Signature{{Status: 1, Summary: 4}}
}

// firstUID is the first user id of a key, the 'uid' field of the GOODSIG line.
func firstUID(entity *openpgp.Entity) string {
	uids := make([]string, 0, len(entity.Identities))
	for uid := range entity.Identities {
		uids = append(uids, uid)
	}
	if 0 == len(uids) {
		return ""
	}
	sort.Strings(uids)
	return uids[0]
}

// Decrypt is decrypt(): the plaintext of an encrypted message in the keyring.
func (e *OpenPGP) Decrypt(text string) (string, error) {
	list, err := e.load()
	if nil != err {
		return "", err
	}

	// The remembered passphrases unlock the key packets first, exactly like the
	// pinentries an addDecryptKey() call hands to the gpg agent. go-crypto does
	// not get a prompt: an encrypted key that no pinentry opens is a failure,
	// not a retry.
	for _, entity := range list {
		_ = e.decryptEntity(entity, "")
	}

	// A message from a mail client is armored; ReadMessage() wants the packets.
	if strings.Contains(text, "-----BEGIN PGP MESSAGE-----") {
		block, err := armor.Decode(strings.NewReader(text))
		if nil != err {
			return "", err
		}
		message, err := openpgp.ReadMessage(block.Body, list, nil, nil)
		if nil != err {
			return "", err
		}
		return readMessageBody(message.UnverifiedBody)
	}

	message, err := openpgp.ReadMessage(strings.NewReader(text), list, nil, nil)
	if nil != err {
		return "", err
	}

	return readMessageBody(message.UnverifiedBody)
}

// readMessageBody reads the decrypted body of a message.
func readMessageBody(body io.Reader) (string, error) {
	if nil == body {
		return "", errors.New("pgp: no message body")
	}

	plaintext, err := io.ReadAll(body)
	if nil != err {
		return "", err
	}

	return string(plaintext), nil
}

// Encrypt is encrypt(): the armored message for the AddEncryptKey() recipients.
func (e *OpenPGP) Encrypt(plaintext string) (string, error) {
	if 0 == len(e.encryptKey) {
		return "", errors.New("No encryption keys specified.")
	}

	list, err := e.load()
	if nil != err {
		return "", err
	}

	recipients := make([]*openpgp.Entity, 0, len(e.encryptKey))
	for _, key := range e.encryptKey {
		entity := findEntity(list, key)
		if nil == entity {
			return "", errors.New("encryption key not found: " + key)
		}
		recipients = append(recipients, entity)
	}

	buffer := &bytes.Buffer{}
	armorWriter, err := armor.Encode(buffer, armorMessage, nil)
	if nil != err {
		return "", err
	}

	plaintextWriter, err := openpgp.Encrypt(armorWriter, recipients, nil, nil, nil)
	if nil != err {
		_ = armorWriter.Close()
		return "", err
	}
	if _, err := plaintextWriter.Write([]byte(plaintext)); nil != err {
		_ = plaintextWriter.Close()
		_ = armorWriter.Close()
		return "", err
	}
	if err := plaintextWriter.Close(); nil != err {
		return "", err
	}
	if err := armorWriter.Close(); nil != err {
		return "", err
	}

	return buffer.String(), nil
}

// EncryptedMessageKeys is getEncryptedMessageKeys().
//
// Partial: PHP parses the ENC_TO status lines of `gpg --decrypt --skip-verify`,
// which the Go engine reports as an empty list. Only the mail view consumes it
// and only when the gpg binary is available (see the step notes).
func (e *OpenPGP) EncryptedMessageKeys(string) []string { return []string{} }

// parseUID is the uid record parser of SnappyMail\GPG\PGP::keyInfo():
// "Name (Comment) <email>" with every part optional.
func parseUID(raw string) UID {
	uid := UID{UID: raw, Name: "", Comment: "", Email: ""}

	text := unescapeColonValue(raw)

	if matches := uidEmailPattern.FindStringSubmatch(text); 3 <= len(matches) {
		text = strings.TrimSpace(matches[1])
		uid.Email = matches[2]
	}
	if matches := uidCommentPattern.FindStringSubmatch(text); 3 <= len(matches) {
		text = matches[1]
		uid.Comment = matches[2]
	}

	if "" == uid.Email && bareEmailPattern.MatchString(text) {
		uid.Email = text
	} else {
		uid.Name = text
	}

	return uid
}

// unescapeColonValue is PHP's stripcslashes() for the colon records, which
// escape a uid as a quoted, \xNN encoded string.
func unescapeColonValue(value string) string {
	value = strings.Trim(value, `"`)
	if !strings.Contains(value, `\`) {
		return value
	}

	// gpg writes a uid as a quoted string with \xNN escapes; only the escapes
	// that can appear in a uid are decoded.
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if '\\' != character || index+1 >= len(value) {
			builder.WriteByte(character)
			continue
		}
		index++
		switch value[index] {
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		case '\\':
			builder.WriteByte('\\')
		case '"':
			builder.WriteByte('"')
		case 'x':
			decoded := 0
			digits := 0
			for digits < 2 && index+1 < len(value) {
				digit, ok := hexDigit(value[index+1])
				if !ok {
					break
				}
				decoded = decoded*16 + digit
				index++
				digits++
			}
			if 0 < digits {
				builder.WriteByte(byte(decoded))
			}
		default:
			builder.WriteByte('\\')
			builder.WriteByte(value[index])
		}
	}

	return builder.String()
}

// hexDigit is the value of one hex character.
func hexDigit(character byte) (int, bool) {
	switch {
	case '0' <= character && '9' >= character:
		return int(character - '0'), true
	case 'a' <= character && 'f' >= character:
		return int(character-'a') + 10, true
	case 'A' <= character && 'F' >= character:
		return int(character-'A') + 10, true
	}
	return 0, false
}
