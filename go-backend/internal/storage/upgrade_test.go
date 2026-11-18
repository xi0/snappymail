package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// legacyTree builds the pre-2.x layout: data/cfg/<shard>/<email>/<files>.
func legacyTree(t *testing.T, storageDir string) {
	t.Helper()

	old := filepath.Join(storageDir, "cfg", "ex", "user@example.com")
	if err := os.MkdirAll(old, 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "settings"), []byte(`{"a":1}`), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "sign_me"), []byte("credentials"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	// The obsolete top level folders the migration drops.
	for _, folder := range []string{"data", "files"} {
		path := filepath.Join(storageDir, folder, "something")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o600); nil != err {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestUpgradeFileStorage(t *testing.T) {
	dir := t.TempDir()
	legacyTree(t, dir)

	UpgradeFileStorage(dir)

	migrated := filepath.Join(dir, "example.com", "user", "settings")
	content, err := os.ReadFile(migrated)
	if nil != err {
		t.Fatalf("read %s: %v", migrated, err)
	}
	if `{"a":1}` != string(content) {
		t.Errorf("migrated settings = %q", content)
	}

	// The insecure sign_me file is deleted, not moved.
	if isFile(filepath.Join(dir, "example.com", "user", "sign_me")) {
		t.Error("the legacy sign_me file must be deleted")
	}

	for _, folder := range []string{"cfg", "data", "files"} {
		if _, err := os.Stat(filepath.Join(dir, folder)); nil == err {
			t.Errorf("UpgradeFileStorage must remove %s", folder)
		}
	}
}

func TestUpgradeFileStorageWithoutLegacyFolders(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "example.com", "user"), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "example.com", "user", "settings"), []byte("{}"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	// A data folder without "cfg" is left untouched.
	UpgradeFileStorage(dir)
	if !isFile(filepath.Join(dir, "example.com", "user", "settings")) {
		t.Error("UpgradeFileStorage must not touch the current layout")
	}

	// An empty path is ignored.
	UpgradeFileStorage("")
}

func TestFilePathMigratesLegacyLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	legacyTree(t, dir)

	storage := NewFileStorage(dir, false)

	// Reading a path triggers the migration while "cfg" exists.
	path, err := storage.FilePath("user@example.com", TypeConfig, false)
	if nil != err {
		t.Fatalf("FilePath: %v", err)
	}
	if want := filepath.Join(dir, "example.com", "user") + string(filepath.Separator); path != want {
		t.Errorf("FilePath = %q, want %q", path, want)
	}
	if !isFile(filepath.Join(dir, "example.com", "user", "settings")) {
		t.Error("FilePath must trigger the legacy migration")
	}
	if _, err := os.Stat(filepath.Join(dir, "cfg")); nil == err {
		t.Error("the legacy cfg folder must be gone")
	}
}
