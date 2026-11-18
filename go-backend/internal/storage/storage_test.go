package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// account is a minimal storage.Account for the tests.
type account string

func (a account) Email() string { return string(a) }

// additionalAccount is a minimal storage.AdditionalAccount for the tests.
type additionalAccount struct {
	email  string
	parent string
}

func (a additionalAccount) Email() string       { return a.email }
func (a additionalAccount) ParentEmail() string { return a.parent }

func TestSecureFileName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"user", "user"},
		{"user@example.com", "user@example.com"},
		{".sign_me", ".sign_me"},
		{"a|b", "a-b"},
		{`back\slash`, "back-slash"},
		{"../etc/passwd", "..-etc-passwd"},
		{"a?b*c<d>e:f\"g+h[i]j&k", "a-b-c-d-e-f-g-h-i-j-k"},
		{"tab\there", "tab-here"},
		{"new\nline", "new-line"},
		{"nul\x00byte", "nul-byte"},
		{"Grüße", "Grüße"},
		{"emoji🙂", "emoji🙂"},
	}

	for _, test := range tests {
		if got := SecureFileName(test.in); got != test.want {
			t.Errorf("SecureFileName(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSplitEmail(t *testing.T) {
	tests := []struct{ in, local, domain string }{
		{"user@example.com", "user", "example.com"},
		{"a@b@c.example", "a@b", "c.example"},
		{"no-domain", "no-domain", ""},
		{"trailing@", "trailing", ""},
		{"@leading", "", "leading"},
	}

	for _, test := range tests {
		local, domain := splitEmail(test.in)
		if local != test.local || domain != test.domain {
			t.Errorf("splitEmail(%q) = %q, %q, want %q, %q",
				test.in, local, domain, test.local, test.domain)
		}
	}
}

func TestStoragePaths(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir, false)

	tests := []struct {
		name    string
		account any
		kind    StorageType
		want    string
	}{
		{"config", "user@example.com", TypeConfig, "example.com/user/"},
		{"config upper case", "User@Example.COM", TypeConfig, "Example.COM/User/"},
		{"sign me", "user@example.com", TypeSignMe, "example.com/user/.sign_me/"},
		{"session", "user@example.com", TypeSession, "example.com/user/.sessions/"},
		{"pgp", "user@example.com", TypePGP, "example.com/user/.pgp/"},
		{"root", "user@example.com", TypeRoot, "example.com/user/"},
		{"account", account("user@example.com"), TypeConfig, "example.com/user/"},
		{"local part without domain", "user", TypeConfig, "unknown.tld/user/"},
		{"sanitised domain", "user@evil.com!x", TypeConfig, "evil.com!x/user/"},
		{"sanitised local part", "we|ird@example.com", TypeConfig, "example.com/we-ird/"},
		{"nobody", nil, TypeConfig, "__nobody__/"},
		{"nobody type", "user@example.com", TypeNobody, "__nobody__/"},
		{"empty account", account(""), TypeConfig, ""},
	}

	for _, test := range tests {
		got, err := storage.FilePath(test.account, test.kind, false)
		if nil != err {
			t.Errorf("%s: FilePath: %v", test.name, err)
			continue
		}
		want := ""
		if "" != test.want {
			want = filepath.Join(dir, filepath.FromSlash(test.want)) + string(filepath.Separator)
		}
		if got != want {
			t.Errorf("%s: FilePath = %q, want %q", test.name, got, want)
		}
	}

	// An unsupported storage type is refused, like the PHP exception.
	if _, err := storage.FilePath("user@example.com", StorageType(9), false); nil == err {
		t.Error("FilePath must reject an invalid storage type")
	}
}

func TestStoragePathsForAdditionalAccount(t *testing.T) {
	dir := t.TempDir()
	additional := additionalAccount{email: "child@example.com", parent: "parent@example.com"}

	shared := NewFileStorage(dir, false)
	path, err := shared.FilePath(additional, TypeConfig, false)
	if nil != err {
		t.Fatalf("FilePath: %v", err)
	}
	if want := filepath.Join(dir, "example.com", "parent") + string(filepath.Separator); path != want {
		t.Errorf("shared config path = %q, want %q", path, want)
	}

	local := NewFileStorage(dir, true)
	path, err = local.FilePath(additional, TypeConfig, false)
	if nil != err {
		t.Fatalf("FilePath: %v", err)
	}
	if want := filepath.Join(dir, "example.com", "parent", "child@example.com") + string(filepath.Separator); path != want {
		t.Errorf("local config path = %q, want %q", path, want)
	}

	// The type specific subfolders win over the additional account address.
	path, err = local.FilePath(additional, TypeSignMe, false)
	if nil != err {
		t.Fatalf("FilePath: %v", err)
	}
	if want := filepath.Join(dir, "example.com", "parent", ".sign_me") + string(filepath.Separator); path != want {
		t.Errorf("sign-me path = %q, want %q", path, want)
	}
}

func TestNobodyKeysAreHashed(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir, false)

	name, err := storage.FileName(nil, TypeNobody, "Cache/Timers", false)
	if nil != err {
		t.Fatalf("FileName: %v", err)
	}
	if want := filepath.Join(dir, "__nobody__", SHA1("Cache/Timers")); name != want {
		t.Errorf("FileName = %q, want %q", name, want)
	}

	if err := storage.Put(nil, TypeNobody, "Cache/Timers", "1,2"); nil != err {
		t.Fatalf("Put: %v", err)
	}
	if value, ok, err := storage.Get(nil, TypeNobody, "Cache/Timers"); nil != err || !ok || "1,2" != value {
		t.Errorf("Get = %q, %v, %v", value, ok, err)
	}
}

func TestPutGetClearPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	storage := NewFileStorage(dir, false)

	if err := storage.Put("user@example.com", TypeConfig, "settings", `{"a":1}`); nil != err {
		t.Fatalf("Put: %v", err)
	}

	file := filepath.Join(dir, "example.com", "user", "settings")
	info, err := os.Stat(file)
	if nil != err {
		t.Fatalf("stat %s: %v", file, err)
	}
	if mode := info.Mode().Perm(); 0o600 != mode {
		t.Errorf("file mode = %04o, want 0600", mode)
	}
	for _, directory := range []string{dir, filepath.Join(dir, "example.com"), filepath.Dir(file)} {
		info, err := os.Stat(directory)
		if nil != err {
			t.Fatalf("stat %s: %v", directory, err)
		}
		if mode := info.Mode().Perm(); 0o700 != mode {
			t.Errorf("directory %s mode = %04o, want 0700", directory, mode)
		}
	}

	value, ok, err := storage.Get("user@example.com", TypeConfig, "settings")
	if nil != err || !ok || `{"a":1}` != value {
		t.Errorf("Get = %q, %v, %v", value, ok, err)
	}

	if value, ok, err := storage.Get("user@example.com", TypeConfig, "missing"); nil != err || ok || "" != value {
		t.Errorf("Get(missing) = %q, %v, %v", value, ok, err)
	}

	if !storage.Clear("user@example.com", TypeConfig, "settings") {
		t.Error("Clear must report true for an existing entry")
	}
	if storage.Clear("user@example.com", TypeConfig, "settings") {
		t.Error("Clear must report false for a missing entry")
	}
}

func TestSessionReadTouchesFile(t *testing.T) {
	storage := NewFileStorage(t.TempDir(), false)

	if err := storage.Put("user@example.com", TypeSession, "token", "1"); nil != err {
		t.Fatalf("Put: %v", err)
	}
	name, err := storage.FileName("user@example.com", TypeSession, "token", false)
	if nil != err {
		t.Fatalf("FileName: %v", err)
	}

	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(name, old, old); nil != err {
		t.Fatalf("chtimes: %v", err)
	}

	if _, _, err := storage.Get("user@example.com", TypeSession, "token"); nil != err {
		t.Fatalf("Get: %v", err)
	}

	info, err := os.Stat(name)
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if info.ModTime().Before(time.Now().Add(-time.Minute)) {
		t.Error("a SESSION read must update the modification time")
	}
}

func TestDeleteStorage(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir, false)

	if err := storage.Put("user@example.com", TypeConfig, "settings", "{}"); nil != err {
		t.Fatalf("Put: %v", err)
	}
	if !storage.DeleteStorage("user@example.com") {
		t.Error("DeleteStorage must report true")
	}
	if _, err := os.Stat(filepath.Join(dir, "example.com", "user")); nil == err {
		t.Error("DeleteStorage must remove the account folder")
	}

	// A missing folder is not an error either.
	if !storage.DeleteStorage("other@example.com") {
		t.Error("DeleteStorage of a missing account must report true")
	}
}

func TestStorageGC(t *testing.T) {
	dir := t.TempDir()
	storage := NewFileStorage(dir, false)

	local := filepath.Join(dir, "example.com", "user")
	signMe := filepath.Join(local, ".sign_me")
	sessions := filepath.Join(local, ".sessions")

	for _, folder := range []string{signMe, sessions} {
		if err := os.MkdirAll(folder, 0o700); nil != err {
			t.Fatalf("mkdir %s: %v", folder, err)
		}
	}

	oldSignMe := filepath.Join(signMe, "old")
	freshSignMe := filepath.Join(signMe, "fresh")
	oldSession := filepath.Join(sessions, "old")
	freshSession := filepath.Join(sessions, "fresh")

	for _, file := range []string{oldSignMe, freshSignMe, oldSession, freshSession} {
		if err := os.WriteFile(file, []byte("x"), 0o600); nil != err {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	// The sign-me tokens live 30 days, the sessions 3 hours.
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(oldSignMe, old, old); nil != err {
		t.Fatalf("chtimes: %v", err)
	}
	old = time.Now().Add(-4 * time.Hour)
	if err := os.Chtimes(oldSession, old, old); nil != err {
		t.Fatalf("chtimes: %v", err)
	}

	storage.GC()

	if isFile(oldSignMe) {
		t.Error("GC must remove sign-me entries older than 30 days")
	}
	if !isFile(freshSignMe) {
		t.Error("GC must keep fresh sign-me entries")
	}
	if isFile(oldSession) {
		t.Error("GC must remove session entries older than 3 hours")
	}
	if !isFile(freshSession) {
		t.Error("GC must keep fresh session entries")
	}
}

func TestProviderVerifiesAccount(t *testing.T) {
	dir := t.TempDir()
	provider := NewProvider(NewFileStorage(dir, false))

	// A CONFIG entry needs an account or an e-mail address.
	if err := provider.Put(nil, TypeConfig, "settings", "{}"); nil != err {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := provider.Get(nil, TypeConfig, "settings"); ok {
		t.Error("Get without an account must not read anything")
	}
	if provider.Clear(nil, TypeConfig, "settings") {
		t.Error("Clear without an account must report false")
	}

	// A NOBODY entry does not.
	if err := provider.Put(nil, TypeNobody, "Cache/Timers", "1,1"); nil != err {
		t.Fatalf("Put(nobody): %v", err)
	}
	if got := provider.GetDefault(nil, TypeNobody, "Cache/Timers", ""); "1,1" != got {
		t.Errorf("GetDefault(nobody) = %q, want 1,1", got)
	}

	// The default is returned for a missing entry.
	if got := provider.GetDefault("user@example.com", TypeConfig, "missing", "fallback"); "fallback" != got {
		t.Errorf("GetDefault(missing) = %q, want fallback", got)
	}

	if provider.IsLocal() {
		t.Error("a shared provider must not report itself local")
	}
}

func TestMkdirAndRecTimeDirRemove(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")

	if err := Mkdir(nested); nil != err {
		t.Fatalf("Mkdir: %v", err)
	}
	info, err := os.Stat(nested)
	if nil != err || !info.IsDir() {
		t.Fatalf("Mkdir did not create %s", nested)
	}
	if mode := info.Mode().Perm(); 0o700 != mode {
		t.Errorf("mode = %04o, want 0700", mode)
	}
	// An existing directory is left alone.
	if err := Mkdir(nested); nil != err {
		t.Fatalf("Mkdir(existing): %v", err)
	}

	old := filepath.Join(nested, "old")
	fresh := filepath.Join(nested, "fresh")
	for _, file := range []string{old, fresh} {
		if err := os.WriteFile(file, []byte("x"), 0o600); nil != err {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); nil != err {
		t.Fatalf("chtimes: %v", err)
	}

	if RecTimeDirRemove(filepath.Join(root, "a"), time.Hour) {
		t.Error("RecTimeDirRemove must not remove a directory that still holds files")
	}
	if isFile(old) {
		t.Error("RecTimeDirRemove must remove the old file")
	}
	if !isFile(fresh) {
		t.Error("RecTimeDirRemove must keep the fresh file")
	}

	// A zero age removes everything, including the directory tree.
	if !RecRmDir(filepath.Join(root, "a")) {
		t.Error("RecRmDir must remove the directory tree")
	}
	if _, err := os.Stat(filepath.Join(root, "a")); nil == err {
		t.Error("RecRmDir left the tree behind")
	}
}
