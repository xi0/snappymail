package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Files is RainLoop\Providers\Files\FileStorage: uploaded attachments and other
// binary blobs below storage/<domain>/<localpart>[/<subemail>]/.files/<sha1>.
//
// Only the parent account folder is shared with the storage provider; the file
// entries themselves are always hashed, so a temporary upload name can never
// collide with a configuration key.
type Files struct {
	// dir is sDataPath: data/_data_/<name>/storage.
	dir string
}

// NewFiles builds the provider for a storage folder.
func NewFiles(dir string) *Files {
	return &Files{dir: strings.TrimRight(strings.TrimSpace(dir), `\/`)}
}

// Dir returns the storage folder of the provider.
func (f *Files) Dir() string { return f.dir }

// LocalFullFileName is FileStorage::GenerateLocalFullFileName(): the absolute
// name of key, creating the ".files" folder.
func (f *Files) LocalFullFileName(account Account, key string) (string, error) {
	return f.fullFileName(account, key, true)
}

// PutFile is FileStorage::PutFile(): copy source into the entry of key.
func (f *Files) PutFile(account Account, key string, source io.Reader) error {
	name, err := f.fullFileName(account, key, true)
	if nil != err {
		return err
	}

	handle, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if nil != err {
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}

	if _, err := io.Copy(handle, source); nil != err {
		handle.Close()
		return fmt.Errorf("storage: failed to save file %s: %w", name, err)
	}

	return handle.Close()
}

// MoveUploadedFile is FileStorage::MoveUploadedFile(): move an upload into the
// entry of key. A cross-device move falls back to a copy, which PHP's
// move_uploaded_file() does internally as well.
func (f *Files) MoveUploadedFile(account Account, key, source string) error {
	name, err := f.fullFileName(account, key, true)
	if nil != err {
		return err
	}

	if err := os.Rename(source, name); nil == err {
		// An uploaded temporary file arrives with the web server's mode; the
		// storage contract is 0600 for files.
		_ = os.Chmod(name, 0o600)
		return nil
	}

	input, err := os.Open(source)
	if nil != err {
		return fmt.Errorf("storage: failed to move file %s: %w", source, err)
	}
	defer input.Close()

	if err := f.PutFile(account, key, input); nil != err {
		return err
	}

	return nil
}

// Open is FileStorage::GetFile() with the read-only "rb" mode.
func (f *Files) Open(account Account, key string) (*os.File, error) {
	name, err := f.fullFileName(account, key, true)
	if nil != err {
		return nil, err
	}
	return os.Open(name)
}

// Create opens the entry of key for writing, like fopen(..., 'w+b').
func (f *Files) Create(account Account, key string) (*os.File, error) {
	name, err := f.fullFileName(account, key, true)
	if nil != err {
		return nil, err
	}
	return os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
}

// FileName is FileStorage::GetFileName(): the absolute name of key when it
// exists.
func (f *Files) FileName(account Account, key string) (string, bool) {
	name, err := f.fullFileName(account, key, false)
	if nil != err || "" == name {
		return "", false
	}
	if !isFile(name) {
		return "", false
	}
	return name, true
}

// Clear is FileStorage::Clear(): remove the entry of key; the result reports
// whether a file was removed.
func (f *Files) Clear(account Account, key string) bool {
	name, err := f.fullFileName(account, key, false)
	if nil != err || "" == name {
		return false
	}
	return nil == os.Remove(name)
}

// FileSize is FileStorage::FileSize().
func (f *Files) FileSize(account Account, key string) (int64, bool) {
	name, ok := f.FileName(account, key)
	if !ok {
		return 0, false
	}
	info, err := os.Stat(name)
	if nil != err {
		return 0, false
	}
	return info.Size(), true
}

// FileExists is FileStorage::FileExists().
func (f *Files) FileExists(account Account, key string) bool {
	_, ok := f.FileName(account, key)
	return ok
}

// GC is FileStorage::GC(): remove entries older than hours hours from every
// ".files" folder, plus the pre-2.x top level "files" folder.
func (f *Files) GC(hours int) bool {
	if 0 >= hours {
		return false
	}

	maxAge := time.Duration(hours) * time.Hour
	for _, domain := range subDirectories(f.dir) {
		for _, local := range subDirectories(domain) {
			RecTimeDirRemove(filepath.Join(local, ".files"), maxAge)
		}
	}

	RecTimeDirRemove(filepath.Join(f.dir, "files"), maxAge)

	return true
}

// CloseAllOpenedFiles is FileStorage::CloseAllOpenedFiles(). The Go provider
// hands out *os.File values that the caller closes, so there is nothing left to
// close here; the method exists for call-site parity.
func (f *Files) CloseAllOpenedFiles() bool { return true }

// fullFileName is FileStorage::generateFullFileName().
func (f *Files) fullFileName(account Account, key string, mkDir bool) (string, error) {
	email, subEmail := "", ""
	switch typed := account.(type) {
	case nil:
	case AdditionalAccount:
		email = typed.ParentEmail()
		subEmail = typed.Email()
	case Account:
		email = typed.Email()
	default:
		return "", fmt.Errorf("storage: unsupported account type %T", account)
	}

	localPart, domain := splitEmail(email)
	if "" == domain {
		domain = "unknown.tld"
	}
	if "" == localPart {
		localPart = ".unknown"
	}

	name := f.dir + "/" + SecureFileName(domain) + "/" + SecureFileName(localPart)
	if "" != subEmail {
		name += "/" + SecureFileName(subEmail)
	}
	name += "/.files/" + SHA1(key)

	if mkDir {
		if err := Mkdir(filepath.Dir(name)); nil != err {
			return "", err
		}
	}

	return name, nil
}

// isFile mirrors PHP's is_file().
func isFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}
