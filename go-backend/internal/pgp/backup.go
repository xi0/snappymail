package pgp

// The server side key escrow of SnappyMail\PGP\Backup (Step 25).
//
// The frontend (OpenPGP.js) can ask the backend to remember key material so a
// second browser sees the same keys. PHP stores it in the PGP folder of the main
// account:
//
//   - a public key is written verbatim to "<id>_public.asc";
//   - a key pair is encrypted with the account crypt key and written to
//     "<id>.key" as the four element JSON array [method, base64 nonce,
//     base64 payload, hmac_sha1(payload, cryptKey)].
//
// Both names and the blob layout are a compatibility contract, so they are
// ported as-is: a key stored by the Go backend decrypts in PHP and vice versa.

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The file name suffixes of Backup::PGPKey()/getKeys().
const (
	publicKeySuffix  = "_public.asc"
	privateKeySuffix = ".key"
	// privateKeyMarker/publicKeyMarker are the str_contains() tests that decide
	// which branch a pasted block takes.
	privateKeyMarker = "PGP PRIVATE KEY"
	publicKeyMarker  = "PGP PUBLIC KEY"
	// maxBlobParts is the element count of the stored blob (method, nonce,
	// payload, hmac).
	maxBlobParts = 4
)

// PathProvider is the part of Providers\Storage Backup needs:
// GenerateFilePath($oAccount, StorageType::PGP, true).
//
// *storage.Provider satisfies it.
type PathProvider interface {
	GenerateFilePath(account any, storageType storage.StorageType, mkDir bool) string
}

// StoredKey is one entry of getKeys(): the file name and the key block.
type StoredKey struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// StoredKeys is the getKeys() document, with the two PHP arrays.
type StoredKeys struct {
	Public  []StoredKey `json:"public"`
	Private []StoredKey `json:"private"`
}

// Store is SnappyMail\PGP\Backup for one request.
//
// Context is the account context (APP_SALT, the storage provider and the
// crypt engine) the account models use; its MainAccount callback is
// Actions::getMainAccountFromToken(), wired by internal/auth.
type Store struct {
	// Context is the account context of the request (crypt engine, salt).
	Context *account.Context
	// Provider resolves the PGP folder of the main account.
	Provider PathProvider
}

// NewStore builds the backup store of one request. A nil context or provider
// makes every call answer "no keys", like an installation without an account.
func NewStore(context *account.Context, provider PathProvider) *Store {
	return &Store{Context: context, Provider: provider}
}

// mainAccount is Actions::getMainAccountFromToken().
func (s *Store) mainAccount() (*account.MainAccount, error) {
	if nil == s || nil == s.Context || nil == s.Context.MainAccount {
		return nil, nil
	}
	main, err := s.Context.MainAccount()
	if nil != err || nil == main {
		return nil, err
	}
	return main, nil
}

// directory is the "<storage>/<domain>/<localpart>/.pgp/" folder of the main
// account, or "" when there is none.
func (s *Store) directory(main *account.MainAccount) string {
	if nil == s || nil == s.Provider || nil == main {
		return ""
	}
	return s.Provider.GenerateFilePath(main, storage.TypePGP, true)
}

// cryptKey is $oAccount->CryptKey().
func (s *Store) cryptKey(main *account.MainAccount) (string, error) {
	if nil == s || nil == s.Context || nil == main {
		return "", errors.New("pgp: no account crypt key")
	}
	return main.CryptKey(s.Context)
}

// StoreKey is Backup::PGPKey(): remember key (a key pair when keyID is empty,
// or the given id) and report whether it was written.
func (s *Store) StoreKey(key, keyID string) (bool, error) {
	main, err := s.mainAccount()
	if nil != err || nil == main {
		return false, err
	}

	directory := s.directory(main)
	if "" == directory {
		return false, nil
	}

	if "" == keyID {
		keyID = storage.SHA1(key)
	} else {
		keyID = "0x" + keyID
	}

	if strings.Contains(key, privateKeyMarker) {
		cryptKey, err := s.cryptKey(main)
		if nil != err {
			return false, err
		}

		blob, err := s.Context.Crypt.Encrypt(key, cryptKey)
		if nil != err {
			return false, err
		}

		nonce := base64.StdEncoding.EncodeToString(blob.Nonce)
		payload := base64.StdEncoding.EncodeToString(blob.Payload)

		document := phpJSONArray([]string{
			blob.Method,
			nonce,
			payload,
			hmacSHA1(payload, cryptKey),
		})

		return nil == storage.WriteString(filepath.Join(directory, keyID+privateKeySuffix), document), nil
	}

	if strings.Contains(key, publicKeyMarker) {
		return nil == storage.WriteString(filepath.Join(directory, keyID+publicKeySuffix), key), nil
	}

	return false, nil
}

// Keys is Backup::getKeys(): the stored public keys and the key pairs that
// authenticate against the account crypt key.
func (s *Store) Keys() (StoredKeys, error) {
	result := StoredKeys{Public: []StoredKey{}, Private: []StoredKey{}}

	main, err := s.mainAccount()
	if nil != err || nil == main {
		return result, err
	}

	directory := s.directory(main)
	if "" == directory {
		return result, nil
	}

	entries, err := os.ReadDir(directory)
	if nil != err {
		// PHP: foreach (glob(...)) — a missing folder iterates nothing.
		return result, nil
	}

	var cryptKey string
	keyLoaded := false

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		full := filepath.Join(directory, name)

		switch {
		case strings.HasSuffix(name, publicKeySuffix):
			content, readErr := os.ReadFile(full)
			if nil != readErr {
				continue
			}
			result.Public = append(result.Public, StoredKey{ID: name, Value: string(content)})

		case strings.HasSuffix(name, privateKeySuffix):
			value, ok, keyErr := s.readPrivateKey(full, main, &cryptKey, &keyLoaded)
			if nil != keyErr {
				return result, keyErr
			}
			if ok {
				result.Private = append(result.Private, StoredKey{ID: name, Value: value})
			}
		}
	}

	return result, nil
}

// readPrivateKey decrypts one stored key pair, the
// "if (is_array($key)) { $mac = array_pop($key); … }" block of getKeys().
func (s *Store) readPrivateKey(
	file string,
	main *account.MainAccount,
	cryptKey *string,
	keyLoaded *bool,
) (string, bool, error) {
	content, err := os.ReadFile(file)
	if nil != err {
		return "", false, nil
	}

	var parts []string
	if err := json.Unmarshal(content, &parts); nil != err {
		return "", false, nil
	}
	if maxBlobParts != len(parts) {
		return "", false, nil
	}

	mac := parts[maxBlobParts-1]
	payload := parts[2]
	// PHP: !empty($key[2]) && hash_hmac('sha1', $key[2], $hash) === $mac.
	if "" == payload || "0" == payload {
		return "", false, nil
	}

	if !*keyLoaded {
		*keyLoaded = true
		resolved, err := s.cryptKey(main)
		if nil != err {
			return "", false, err
		}
		*cryptKey = resolved
	}

	if !hmac.Equal([]byte(hmacSHA1(payload, *cryptKey)), []byte(mac)) {
		return "", false, nil
	}

	nonce, _ := base64.StdEncoding.DecodeString(parts[1])
	ciphertext, _ := base64.StdEncoding.DecodeString(payload)

	plain, err := s.Context.Crypt.Decrypt(cryptox.Blob{
		Method:  parts[0],
		Nonce:   nonce,
		Payload: ciphertext,
	}, *cryptKey)
	if nil != err {
		// PHP logs the Crypt error and skips the file.
		if nil != s.Context.Log {
			s.Context.Log("Crypt", err.Error())
		}
		return "", false, nil
	}

	value, ok := plain.(string)
	if !ok {
		return "", false, nil
	}

	return value, true, nil
}

// hmacSHA1 is hash_hmac('sha1', data, key) as the lower case hex digest.
func hmacSHA1(data, key string) string {
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(data))
	return strings.ToLower(hexUpper(mac.Sum(nil)))
}

// phpJSONArray renders a JSON array of strings the way json_encode() does by
// default: slashes are escaped as "\/", so the stored blob keeps the byte
// layout PHP writes.
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
					builder.WriteString(`\u00`)
					builder.WriteByte("0123456789abcdef"[character>>4])
					builder.WriteByte("0123456789abcdef"[character&0x0f])
					continue
				}
				builder.WriteByte(character)
			}
		}
		builder.WriteByte('"')
	}
	builder.WriteByte(']')
	return builder.String()
}
