// Package storage implements the on-disk state layer of SnappyMail: the paths,
// permissions and file formats that RainLoop\Providers\Storage,
// RainLoop\Providers\Settings\DefaultSettings and RainLoop\Providers\Files
// read and write below data/_data_/<name>/.
//
// The layout is a compatibility contract, because the PHP backend may run
// against the same data folder at any time, so the observable behaviour is
// ported as-is instead of being "improved":
//
//   - a stored value lives at storage/<domain>/<localpart>[/<subfolder>]/<key>,
//     with the subfolders ".sign_me", ".sessions" and ".pgp" and the global
//     "__nobody__/<sha1(key)>" folder;
//   - keys, domain and local parts are sanitised with
//     MailSo\Base\Utils::SecureFileName semantics;
//   - files are 0600 and directories 0700, like RainLoop\Utils::saveFile();
//   - an uploaded file is hashed into
//     storage/<domain>/<localpart>[/<subemail>]/.files/<sha1(key)>;
//   - account settings are the JSON documents "settings" and "settings_local",
//     written the way PHP's json_encode() writes them (escaped "/", \uXXXX and
//     no pretty printing) so a file written by either backend stays identical.
//
// WriteFile() is deliberately the only difference: PHP writes with
// file_put_contents(), Go writes a temporary file next to the target and
// renames it, so a concurrent reader never sees a half written settings file.
// The resulting bytes and permissions are the same.
package storage

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// StorageType is RainLoop\Providers\Storage\Enumerations\StorageType.
type StorageType int

// The storage types of the PHP class; the numbers are part of the contract
// because they select the folder layout.
const (
	// TypeConfig is StorageType::CONFIG: the account configuration folder.
	TypeConfig StorageType = 1
	// TypeNobody is StorageType::NOBODY: the global, account independent folder.
	TypeNobody StorageType = 2
	// TypeSignMe is StorageType::SIGN_ME: the 30 day "remember me" tokens.
	TypeSignMe StorageType = 3
	// TypeSession is StorageType::SESSION: the per browser session tokens.
	TypeSession StorageType = 4
	// TypePGP is StorageType::PGP: the OpenPGP backups.
	TypePGP StorageType = 5
	// TypeRoot is StorageType::ROOT: the account root (gnupg home, .cryptkey).
	TypeRoot StorageType = 6
)

// String returns the PHP constant name of the storage type.
func (t StorageType) String() string {
	switch t {
	case TypeConfig:
		return "CONFIG"
	case TypeNobody:
		return "NOBODY"
	case TypeSignMe:
		return "SIGN_ME"
	case TypeSession:
		return "SESSION"
	case TypePGP:
		return "PGP"
	case TypeRoot:
		return "ROOT"
	}
	return fmt.Sprintf("StorageType(%d)", int(t))
}

// Account is the part of RainLoop\Model\Account the providers need. Step 7
// (internal/account) implements it.
type Account interface {
	// Email is Account::Email(), the address the storage folder is named after.
	Email() string
}

// AdditionalAccount is RainLoop\Model\AdditionalAccount: its configuration is
// stored below the parent account, with the additional address as subfolder
// when the provider is the local one.
type AdditionalAccount interface {
	Account
	// ParentEmail is AdditionalAccount::ParentEmail().
	ParentEmail() string
}

// accountPath resolves the $mAccount argument of the PHP providers, which
// accepts a Model\Account, a plain e-mail address or null.
//
// email is the address that names the storage folder and subFolder is the
// additional account address when the caller asked for the local provider.
type accountPath struct {
	email     string
	subFolder string
	ok        bool
}

// resolveAccount maps the account argument onto the PHP branches of
// FileStorage::GenerateFilePath().
func resolveAccount(account any) accountPath {
	switch typed := account.(type) {
	case nil:
		return accountPath{}
	case string:
		return accountPath{email: typed, ok: true}
	case AdditionalAccount:
		return accountPath{email: typed.ParentEmail(), subFolder: typed.Email(), ok: true}
	case Account:
		return accountPath{email: typed.Email(), ok: true}
	}
	return accountPath{}
}

// FileStorage is RainLoop\Providers\Storage\FileStorage: the storage/ folder of
// the private data folder.
type FileStorage struct {
	// dir is sDataPath: data/_data_/<name>/storage, without a trailing
	// separator.
	dir string
	// local is the bLocal constructor argument ("storage-local").
	local bool
	// upgraded guards the one-shot legacy layout migration of
	// GenerateFilePath().
	upgraded sync.Once
}

// NewFileStorage builds the driver for a storage folder. dir is
// APP_PRIVATE_DATA . 'storage'; local is PHP's bLocal (the "storage-local"
// provider, which keeps additional accounts in their own subfolder).
func NewFileStorage(dir string, local bool) *FileStorage {
	return &FileStorage{
		dir:   strings.TrimRight(strings.TrimSpace(dir), `\/`),
		local: local,
	}
}

// Dir returns the storage folder of the driver.
func (s *FileStorage) Dir() string { return s.dir }

// IsLocal is FileStorage::IsLocal().
func (s *FileStorage) IsLocal() bool { return s.local }

// FilePath is FileStorage::GenerateFilePath(): the folder that holds the keyed
// files of a storage type, with mkDir creating it as 0700.
//
// The empty string means "no folder for this account", which PHP reports for a
// CONFIG/SIGN_ME/SESSION/PGP/ROOT path without an e-mail address.
func (s *FileStorage) FilePath(account any, storageType StorageType, mkDir bool) (string, error) {
	path := ""

	if nil == account || TypeNobody == storageType {
		path = s.dir + "/__nobody__/"
	} else {
		resolved := resolveAccount(account)
		email := resolved.email
		subFolder := resolved.subFolder
		if !s.local {
			subFolder = ""
		}
		if "" != email {
			// The type specific subfolders win over the additional account
			// address, exactly like the PHP if/else-if chain.
			switch storageType {
			case TypeSignMe:
				subFolder = ".sign_me"
			case TypeSession:
				subFolder = ".sessions"
			case TypePGP:
				subFolder = ".pgp"
			case TypeRoot:
				subFolder = ""
			}
		}

		switch storageType {
		case TypeConfig, TypeSignMe, TypeSession, TypePGP, TypeRoot:
			if "" == email {
				return "", nil
			}

			s.upgraded.Do(func() { UpgradeFileStorage(s.dir) })

			localPart, domain := splitEmail(email)
			if "" == domain {
				domain = "unknown.tld"
			}
			if "" == localPart {
				localPart = ".unknown"
			}

			path = s.dir + "/" + SecureFileName(domain) + "/" + SecureFileName(localPart) + "/"
			if "" != subFolder {
				path += SecureFileName(subFolder) + "/"
			}
		default:
			return "", fmt.Errorf("storage: invalid storage type %d", int(storageType))
		}
	}

	if mkDir && "" != path {
		if err := Mkdir(path); nil != err {
			return "", err
		}
	}

	return path, nil
}

// FileName is FileStorage::generateFileName(): the full name of a keyed entry.
//
// A NOBODY entry is hashed, because the key is shared between accounts; any
// other key is sanitised and appended as-is.
func (s *FileStorage) FileName(account any, storageType StorageType, key string, mkDir bool) (string, error) {
	path, err := s.FilePath(account, storageType, mkDir)
	if nil != err || "" == path {
		return path, err
	}

	if TypeNobody == storageType {
		if "" == key {
			// PHP: sha1($sKey ?: time())
			key = fmt.Sprintf("%d", time.Now().Unix())
		}
		return path + SHA1(key), nil
	}

	if "" == key {
		return path, nil
	}
	return path + SecureFileName(key), nil
}

// Put is FileStorage::Put(): store value under key.
func (s *FileStorage) Put(account any, storageType StorageType, key, value string) error {
	name, err := s.FileName(account, storageType, key, true)
	if nil != err || "" == name {
		return err
	}
	return WriteFile(name, []byte(value))
}

// Get is FileStorage::Get(): the stored value and whether it exists. A SESSION
// read touches the file so the garbage collector keeps the session alive.
func (s *FileStorage) Get(account any, storageType StorageType, key string) (string, bool, error) {
	name, err := s.FileName(account, storageType, key, false)
	if nil != err || "" == name {
		return "", false, err
	}

	content, readErr := os.ReadFile(name)
	if nil != readErr {
		if os.IsNotExist(readErr) {
			return "", false, nil
		}
		return "", false, readErr
	}

	if TypeSession == storageType {
		// PHP: touch($sFileName) to prevent garbage collection.
		now := time.Now()
		_ = os.Chtimes(name, now, now)
	}

	return string(content), true, nil
}

// Clear is FileStorage::Clear(): remove one entry; the result reports whether a
// file was removed.
func (s *FileStorage) Clear(account any, storageType StorageType, key string) bool {
	name, err := s.FileName(account, storageType, key, false)
	if nil != err || "" == name {
		return false
	}
	return nil == os.Remove(name)
}

// DeleteStorage is FileStorage::DeleteStorage(): remove the whole account
// configuration folder.
func (s *FileStorage) DeleteStorage(account any) bool {
	path, err := s.FileName(account, TypeConfig, "", false)
	if nil != err || "" == path {
		return true
	}

	if info, statErr := os.Stat(path); nil == statErr && info.IsDir() {
		RecRmDir(path)
	}

	return true
}

// GC is FileStorage::GC(): drop sign-me entries older than 30 days and session
// entries older than 3 hours.
func (s *FileStorage) GC() {
	for _, domain := range subDirectories(s.dir) {
		for _, local := range subDirectories(domain) {
			RecTimeDirRemove(filepath.Join(local, ".sign_me"), 30*24*time.Hour)
			RecTimeDirRemove(filepath.Join(local, ".sessions"), 3*time.Hour)
		}
	}
}

// Provider is RainLoop\Providers\Storage: the driver plus the account
// verification of the PHP wrapper.
type Provider struct {
	driver *FileStorage
}

// NewProvider wraps a storage driver.
func NewProvider(driver *FileStorage) *Provider {
	return &Provider{driver: driver}
}

// IsLocal is Providers\Storage::IsLocal().
func (p *Provider) IsLocal() bool {
	return nil != p.driver && p.driver.IsLocal()
}

// Dir returns the storage folder of the wrapped driver.
func (p *Provider) Dir() string {
	if nil == p.driver {
		return ""
	}
	return p.driver.Dir()
}

// verifyAccount is Providers\Storage::verifyAccount(): a NOBODY entry needs no
// account, everything else needs an Account or an e-mail address.
func verifyAccount(account any, storageType StorageType) bool {
	if TypeNobody == storageType {
		return true
	}
	if _, ok := account.(Account); ok {
		return true
	}
	_, ok := account.(string)
	return ok
}

// Put is Providers\Storage::Put().
func (p *Provider) Put(account any, storageType StorageType, key, value string) error {
	if nil == p.driver || !verifyAccount(account, storageType) {
		return nil
	}
	return p.driver.Put(account, storageType, key, value)
}

// Get is Providers\Storage::Get().
func (p *Provider) Get(account any, storageType StorageType, key string) (string, bool) {
	if nil == p.driver || !verifyAccount(account, storageType) {
		return "", false
	}
	value, ok, err := p.driver.Get(account, storageType, key)
	if nil != err {
		return "", false
	}
	return value, ok
}

// GetDefault is Providers\Storage::Get() with the $mDefault of the PHP method.
func (p *Provider) GetDefault(account any, storageType StorageType, key, defaultValue string) string {
	if value, ok := p.Get(account, storageType, key); ok {
		return value
	}
	return defaultValue
}

// Clear is Providers\Storage::Clear().
func (p *Provider) Clear(account any, storageType StorageType, key string) bool {
	if nil == p.driver || !verifyAccount(account, storageType) {
		return false
	}
	return p.driver.Clear(account, storageType, key)
}

// DeleteStorage is Providers\Storage::DeleteStorage().
func (p *Provider) DeleteStorage(account any) bool {
	if nil == p.driver {
		return false
	}
	return p.driver.DeleteStorage(account)
}

// GenerateFilePath is Providers\Storage::GenerateFilePath().
func (p *Provider) GenerateFilePath(account any, storageType StorageType, mkDir bool) string {
	if nil == p.driver {
		return ""
	}
	path, _ := p.driver.FilePath(account, storageType, mkDir)
	return path
}

// GC is Providers\Storage::GC().
func (p *Provider) GC() {
	if nil != p.driver {
		p.driver.GC()
	}
}

// SecureFileName ports MailSo\Base\Utils::SecureFileName(): replace control
// characters, format characters, the Win95 reserved characters and the path
// separators with "-".
func SecureFileName(name string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case '|', '\\', '?', '*', '<', '"', ':', '>', '+', '[', ']', '/', '&':
			return '-'
		}
		// \pC is Cc, Cf, Cs, Co and Cn. Go has no table for the unassigned
		// code points (Cn), which only matters for names that PHP would not
		// produce either.
		if unicode.In(character, unicode.Cc, unicode.Cf, unicode.Cs, unicode.Co) {
			return '-'
		}
		return character
	}, name)
}

// Mkdir ports MailSo\Base\Utils::mkdir(): create the directory and its parents
// with 0700 when it does not exist yet.
func Mkdir(directory string) error {
	if "" == directory {
		return nil
	}
	if info, err := os.Stat(directory); nil == err && info.IsDir() {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); nil != err {
		return fmt.Errorf("storage: failed to create directory %s: %w", directory, err)
	}
	return nil
}

// WriteFile ports RainLoop\Utils::saveFile(): create the parent folder with
// 0700 and write the file with 0600.
//
// Unlike PHP the content is written to a temporary file in the same folder and
// renamed, so a concurrent reader (the PHP backend) never observes a partially
// written settings file.
func WriteFile(name string, content []byte) error {
	directory := filepath.Dir(name)
	if err := Mkdir(directory); nil != err {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".tmp-storage-")
	if nil != err {
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}
	temporaryName := temporary.Name()

	if _, err := temporary.Write(content); nil != err {
		temporary.Close()
		os.Remove(temporaryName)
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}

	// os.CreateTemp() already uses 0600; chmod keeps the mode explicit for file
	// systems that do not honour it.
	if err := temporary.Chmod(0o600); nil != err {
		temporary.Close()
		os.Remove(temporaryName)
		return fmt.Errorf("storage: failed to restrict file %s: %w", name, err)
	}

	if err := temporary.Close(); nil != err {
		os.Remove(temporaryName)
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}

	if err := os.Rename(temporaryName, name); nil != err {
		os.Remove(temporaryName)
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}

	return nil
}

// WriteString is WriteFile for a string value.
func WriteString(name, content string) error { return WriteFile(name, []byte(content)) }

// RecRmDir is MailSo\Base\Utils::RecRmDir(): remove a directory tree.
func RecRmDir(directory string) bool { return RecTimeDirRemove(directory, 0) }

// RecTimeDirRemove is MailSo\Base\Utils::RecTimeDirRemove(): remove the files
// of a directory tree that are older than maxAge and then every directory that
// became empty, bottom-up.
//
// A zero maxAge removes everything. The boolean result reports whether the root
// directory itself was removed.
func RecTimeDirRemove(directory string, maxAge time.Duration) bool {
	info, err := os.Stat(directory)
	if nil != err || !info.IsDir() {
		return false
	}

	cutoff := time.Now().Add(-maxAge)

	removeEntry(directory, cutoff, 0 < maxAge)

	return nil == os.Remove(directory)
}

// removeEntry handles one directory of a recursive removal; childFirst tells
// whether only old files and already empty directories are candidates.
func removeEntry(directory string, cutoff time.Time, childFirst bool) {
	entries, err := os.ReadDir(directory)
	if nil != err {
		return
	}

	for _, entry := range entries {
		name := filepath.Join(directory, entry.Name())
		info, infoErr := entry.Info()
		if nil != infoErr {
			continue
		}

		if info.IsDir() {
			removeEntry(name, cutoff, childFirst)
			if !childFirst || isEmptyDirectory(name) {
				_ = os.Remove(name)
			}
			continue
		}

		if !childFirst || info.ModTime().Before(cutoff) {
			_ = os.Remove(name)
		}
	}
}

// isEmptyDirectory reports whether a directory holds no entries.
func isEmptyDirectory(directory string) bool {
	entries, err := os.ReadDir(directory)
	return nil == err && 0 == len(entries)
}

// subDirectories lists the directories of parent, like glob("parent/*",
// GLOB_ONLYDIR) sorted by name.
func subDirectories(parent string) []string {
	entries, err := os.ReadDir(parent)
	if nil != err {
		return nil
	}

	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			result = append(result, filepath.Join(parent, entry.Name()))
		}
	}
	return result
}

// splitEmail splits an address on its last "@", mirroring PHP's
// explode('@', $sEmail) + array_pop() + implode('@', $aEmail).
//
// An address without "@" has no domain, exactly like the count() check of
// FileStorage::GenerateFilePath().
func splitEmail(email string) (localPart, domain string) {
	index := strings.LastIndex(email, "@")
	if 0 > index {
		return email, ""
	}
	return email[:index], strings.TrimSpace(email[index+1:])
}

// SHA1 returns the lowercase hex sha1 of value, PHP's sha1().
func SHA1(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
