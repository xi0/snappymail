package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesPathShape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	files := NewFiles(dir)

	name, err := files.LocalFullFileName(account("user@example.com"), "upload-post-abc.eml")
	if nil != err {
		t.Fatalf("LocalFullFileName: %v", err)
	}
	want := filepath.Join(dir, "example.com", "user", ".files", SHA1("upload-post-abc.eml"))
	if name != want {
		t.Errorf("LocalFullFileName = %q, want %q", name, want)
	}

	// The ".files" folder is created with 0700.
	info, err := os.Stat(filepath.Dir(name))
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); 0o700 != mode {
		t.Errorf("folder mode = %04o, want 0700", mode)
	}

	// An additional account keeps its files in the parent's subfolder.
	additional := additionalAccount{email: "child@example.com", parent: "parent@example.com"}
	name, err = files.LocalFullFileName(additional, "key")
	if nil != err {
		t.Fatalf("LocalFullFileName: %v", err)
	}
	want = filepath.Join(dir, "example.com", "parent", "child@example.com", ".files", SHA1("key"))
	if name != want {
		t.Errorf("additional LocalFullFileName = %q, want %q", name, want)
	}
}

func TestFilesWriteReadClear(t *testing.T) {
	files := NewFiles(filepath.Join(t.TempDir(), "storage"))

	if err := files.PutFile(account("user@example.com"), "key", strings.NewReader("content")); nil != err {
		t.Fatalf("PutFile: %v", err)
	}

	if !files.FileExists(account("user@example.com"), "key") {
		t.Error("FileExists must report true")
	}
	if size, ok := files.FileSize(account("user@example.com"), "key"); !ok || 7 != size {
		t.Errorf("FileSize = %d, %v, want 7, true", size, ok)
	}

	if _, ok := files.FileName(account("user@example.com"), "key"); !ok {
		t.Fatal("FileName reported a missing file")
	}

	handle, err := files.Open(account("user@example.com"), "key")
	if nil != err {
		t.Fatalf("Open: %v", err)
	}
	content := make([]byte, 7)
	if _, err := handle.Read(content); nil != err {
		t.Fatalf("read: %v", err)
	}
	handle.Close()
	if "content" != string(content) {
		t.Errorf("content = %q", content)
	}

	// A second write truncates the entry.
	if err := files.PutFile(account("user@example.com"), "key", strings.NewReader("x")); nil != err {
		t.Fatalf("PutFile: %v", err)
	}
	if size, _ := files.FileSize(account("user@example.com"), "key"); 1 != size {
		t.Errorf("size after rewrite = %d, want 1", size)
	}

	if name, ok := files.FileName(account("user@example.com"), "missing"); ok || "" != name {
		t.Errorf("FileName(missing) = %q, %v", name, ok)
	}
	if size, ok := files.FileSize(account("user@example.com"), "missing"); ok || 0 != size {
		t.Errorf("FileSize(missing) = %d, %v", size, ok)
	}

	if !files.Clear(account("user@example.com"), "key") {
		t.Error("Clear must report true for an existing file")
	}
	if files.Clear(account("user@example.com"), "key") {
		t.Error("Clear must report false for a missing file")
	}
}

func TestFilesMoveUploadedFile(t *testing.T) {
	files := NewFiles(filepath.Join(t.TempDir(), "storage"))

	source := filepath.Join(t.TempDir(), "tmp-upload")
	if err := os.WriteFile(source, []byte("uploaded"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	if err := files.MoveUploadedFile(account("user@example.com"), "key", source); nil != err {
		t.Fatalf("MoveUploadedFile: %v", err)
	}
	if _, err := os.Stat(source); nil == err {
		t.Error("MoveUploadedFile must move the source away")
	}

	handle, err := files.Open(account("user@example.com"), "key")
	if nil != err {
		t.Fatalf("Open: %v", err)
	}
	defer handle.Close()

	content := make([]byte, 8)
	if n, _ := handle.Read(content); "uploaded" != string(content[:n]) {
		t.Errorf("content = %q", content[:n])
	}
}

func TestFilesGC(t *testing.T) {
	files := NewFiles(filepath.Join(t.TempDir(), "storage"))

	if err := files.PutFile(account("user@example.com"), "old", strings.NewReader("x")); nil != err {
		t.Fatalf("PutFile: %v", err)
	}
	if err := files.PutFile(account("user@example.com"), "fresh", strings.NewReader("x")); nil != err {
		t.Fatalf("PutFile: %v", err)
	}

	oldName, _ := files.FileName(account("user@example.com"), "old")
	past := time.Now().Add(-49 * time.Hour)
	if err := os.Chtimes(oldName, past, past); nil != err {
		t.Fatalf("chtimes: %v", err)
	}

	if files.GC(0) {
		t.Error("GC(0) must report false")
	}
	if !files.GC(48) {
		t.Error("GC(48) must report true")
	}
	if files.FileExists(account("user@example.com"), "old") {
		t.Error("GC must remove the old entry")
	}
	if !files.FileExists(account("user@example.com"), "fresh") {
		t.Error("GC must keep the fresh entry")
	}

	if !files.CloseAllOpenedFiles() {
		t.Error("CloseAllOpenedFiles must report true")
	}
}
