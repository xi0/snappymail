package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCanonicalJSON(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "snappymail.eu.json"), readFixture(t, "default.json"))

	// setup.php creates the hostname config with shortLogin enabled.
	writeTestFile(t, filepath.Join(dir, "localhost.json"), `{
    "IMAP": {"host": "localhost", "shortLogin": true},
    "SMTP": {"host": "localhost", "shortLogin": true},
    "Sieve": {"host": "localhost"},
    "whiteList": ""
}`)

	d := p.Load("SnappyMail.EU")
	if nil == d {
		t.Fatal("Load returned nil")
	}
	if want := "snappymail.eu"; want != d.Name {
		t.Errorf("Name = %q, want %q", d.Name, want)
	}
	if 143 != d.IMAP.Port || !d.IMAP.FastSimpleSearch {
		t.Errorf("IMAP = %#v", d.IMAP)
	}

	// Keys that the file does not contain keep the class defaults.
	host := p.Load("localhost")
	if nil == host {
		t.Fatal("Load(localhost) returned nil")
	}
	if !host.IMAP.ShortLogin || !host.SMTP.ShortLogin {
		t.Error("shortLogin must be read from the file")
	}
	if want := DefaultMessageListLimit; want != host.IMAP.MessageListLimit {
		t.Errorf("message_list_limit = %d, want %d", host.IMAP.MessageListLimit, want)
	}
	if want := "localhost"; want != host.SMTP.Host {
		t.Errorf("SMTP host = %q, want %q", host.SMTP.Host, want)
	}

	if nil != p.Load("missing.example") {
		t.Error("Load of an unknown domain must return nil")
	}
}

func TestLoadLegacyIniFile(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "legacy.example.ini"), strings.Join([]string{
		"imap_host = \"imap.legacy.example\"",
		"imap_port = 993",
		"imap_secure = \"SSL\"",
		"imap_short_login = 1",
		"smtp_host = \"smtp.legacy.example\"",
		"smtp_port = 587",
		"smtp_secure = \"TLS\"",
		"smtp_auth = 1",
		"white_list = \"@legacy.example\"",
		"",
	}, "\n"))

	d := p.Load("legacy.example")
	if nil == d {
		t.Fatal("Load returned nil")
	}
	if "imap.legacy.example" != d.IMAP.Host || SecuritySSL != d.IMAP.Type || !d.IMAP.ShortLogin {
		t.Errorf("IMAP = %#v", d.IMAP)
	}
	if "smtp.legacy.example" != d.SMTP.Host || SecuritySTARTTLS != d.SMTP.Type || !d.SMTP.UseAuth {
		t.Errorf("SMTP = %#v", d.SMTP)
	}
	if want := "@legacy.example"; want != d.WhiteList {
		t.Errorf("whiteList = %q, want %q", d.WhiteList, want)
	}
}

func TestLoadAliasFile(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "target.example.json"), readFixture(t, "default.json"))
	writeTestFile(t, filepath.Join(dir, "alias.example.alias"), " Target.Example \n")

	d := p.Load("alias.example")
	if nil == d {
		t.Fatal("Load returned nil")
	}
	if want := "target.example"; want != d.Name {
		t.Errorf("Name = %q, want the target domain %q", d.Name, want)
	}
	if want := "alias.example"; want != d.AliasName() {
		t.Errorf("AliasName = %q, want %q", d.AliasName(), want)
	}

	// Without the alias check the name has no configuration at all.
	if nil != p.Load("alias.example", WithoutAliasCheck()) {
		t.Error("WithoutAliasCheck must not follow the alias")
	}

	// An empty alias file is ignored.
	writeTestFile(t, filepath.Join(dir, "empty.example.alias"), "\n")
	if nil != p.Load("empty.example") {
		t.Error("an empty alias file must be ignored")
	}
}

func TestLoadWildcardDomains(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "default.json"), readFixture(t, "default.json"))
	writeTestFile(t, filepath.Join(dir, "_wildcard_example.com.json"), `{
    "IMAP": {"host": "wildcard.example.com", "shortLogin": true},
    "SMTP": {"host": "wildcard.example.com"},
    "Sieve": {"host": ""},
    "whiteList": ""
}`)

	// The most specific wildcard wins: rsort() puts "*example.com" before "*".
	d := p.Load("sub.example.com", WithWildcard())
	if nil == d {
		t.Fatal("Load with wildcard returned nil")
	}
	if want := "*example.com"; want != d.Name {
		t.Errorf("Name = %q, want the wildcard %q", d.Name, want)
	}
	if "wildcard.example.com" != d.IMAP.Host {
		t.Errorf("IMAP host = %q, want the wildcard config", d.IMAP.Host)
	}

	// Everything else falls back to the "default" config ("*").
	other := p.Load("other.example", WithWildcard())
	if nil == other {
		t.Fatal("Load(other.example) with wildcard returned nil")
	}
	if want := "*"; want != other.Name {
		t.Errorf("Name = %q, want %q", other.Name, want)
	}

	// Without the wildcard search there is no match.
	if nil != p.Load("other.example") {
		t.Error("Load without WithWildcard must not use the wildcard configs")
	}
}

func TestWildcardLineIsCached(t *testing.T) {
	cache := newFakeCache()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "default.json"), readFixture(t, "default.json"))
	p := NewProvider(dir, cache, Defaults{})

	if nil == p.Load("anything.example", WithWildcard()) {
		t.Fatal("Load with wildcard returned nil")
	}
	if 1 != cache.gets || 1 != cache.sets {
		t.Errorf("cache gets/sets = %d/%d, want 1/1", cache.gets, cache.sets)
	}
	if want := "*"; want != cache.values[WildcardCacheKey] {
		t.Errorf("cached line = %q, want %q", cache.values[WildcardCacheKey], want)
	}

	// The second lookup is answered from the cache.
	p.Load("anything.example", WithWildcard())
	if 2 != cache.gets || 1 != cache.sets {
		t.Errorf("cache gets/sets = %d/%d, want 2/1", cache.gets, cache.sets)
	}

	// Saving an alias invalidates the cached line.
	if err := p.SaveAlias("alias.example", "example.com"); nil != err {
		t.Fatalf("SaveAlias: %v", err)
	}
	if 0 == cache.deletes {
		t.Error("SaveAlias must delete the cached wildcard line")
	}
	if _, ok := cache.values[WildcardCacheKey]; ok {
		t.Error("the cached wildcard line must be gone")
	}
}

func TestDisabledEntriesAreRefused(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "one.example.json"), readFixture(t, "default.json"))
	writeTestFile(t, filepath.Join(dir, "two.example.json"), readFixture(t, "default.json"))

	ok, err := p.Disable("One.Example", true)
	if nil != err || !ok {
		t.Fatalf("Disable = %v, %v, want true, nil", ok, err)
	}
	if want := "one.example"; want != readFile(t, filepath.Join(dir, "disabled")) {
		t.Errorf("disabled file = %q, want %q", readFile(t, filepath.Join(dir, "disabled")), want)
	}

	if nil != p.Load("one.example") {
		t.Error("a disabled domain must not be loaded")
	}
	if nil == p.Load("one.example", WithoutDisabledCheck()) {
		t.Error("WithoutDisabledCheck must load a disabled domain")
	}

	// The second name reports success, because it is appended as the last key.
	if ok, err := p.Disable("two.example", true); nil != err || !ok {
		t.Fatalf("Disable(two) = %v, %v, want true, nil", ok, err)
	}

	// Enabling the first name rewrites the file correctly but reports false:
	// array_filter() keeps the original array keys, so the comparison with the
	// freshly read list fails (ported PHP quirk).
	ok, err = p.Disable("one.example", false)
	if nil != err {
		t.Fatalf("Disable(one, false): %v", err)
	}
	if ok {
		t.Error("Disable(one, false) must report false, like array_filter() + === does")
	}
	if want := "two.example"; want != readFile(t, filepath.Join(dir, "disabled")) {
		t.Errorf("disabled file = %q, want %q", readFile(t, filepath.Join(dir, "disabled")), want)
	}
	if nil == p.Load("one.example") {
		t.Error("one.example must be loadable again")
	}

	// Removing the only entry empties the list and reports true.
	if ok, err := p.Disable("two.example", false); nil != err || !ok {
		t.Fatalf("Disable(two, false) = %v, %v, want true, nil", ok, err)
	}
	if want := ""; want != readFile(t, filepath.Join(dir, "disabled")) {
		t.Errorf("disabled file = %q, want it empty", readFile(t, filepath.Join(dir, "disabled")))
	}

	// A name that cannot be encoded is refused.
	if ok, err := p.Disable("-invalid", true); nil != err || ok {
		t.Errorf("Disable(-invalid) = %v, %v, want false, nil", ok, err)
	}
}

func TestDeleteRemovesEveryVariant(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "gone.example.json"), readFixture(t, "default.json"))
	writeTestFile(t, filepath.Join(dir, "gone.example.ini"), "imap_host = imap.gone.example\n")
	writeTestFile(t, filepath.Join(dir, "gone.example.alias"), "other.example\n")
	writeTestFile(t, filepath.Join(dir, "other.example.json"), readFixture(t, "default.json"))

	ok, err := p.Delete("Gone.Example")
	if nil != err || !ok {
		t.Fatalf("Delete = %v, %v, want true, nil", ok, err)
	}

	for _, extension := range []string{".json", ".ini", ".alias"} {
		if _, err := os.Stat(filepath.Join(dir, "gone.example"+extension)); nil == err {
			t.Errorf("gone.example%s was not deleted", extension)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.example.json")); nil != err {
		t.Error("Delete must only remove the requested domain")
	}

	// Delete() also writes the (empty) disabled list, like the PHP method does
	// through Disable($sName, !$bResult).
	if _, err := os.Stat(filepath.Join(dir, "disabled")); nil != err {
		t.Error("Delete must create the disabled file")
	}
}

func TestSaveIsByteIdenticalToPHP(t *testing.T) {
	// A folder that does not exist yet, so the 0700 creation is exercised.
	dir := filepath.Join(t.TempDir(), "domains")
	p := NewProvider(dir, nil, Defaults{})

	d := FromArray("example.com", readJSONFixture(t, "default.json"), Defaults{})
	if nil == d {
		t.Fatal("FromArray returned nil")
	}

	if err := p.Save(d); nil != err {
		t.Fatalf("Save: %v", err)
	}

	file := filepath.Join(dir, "example.com.json")
	// The fixture ends with a newline for the sake of the repository; PHP's
	// json_encode() does not.
	want := strings.TrimSuffix(readFixture(t, "default.saved.json"), "\n")
	if got := readFile(t, file); got != want {
		t.Errorf("Save() differs from the PHP layout: %s", difference(want, got))
	}

	info, err := os.Stat(file)
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); 0o600 != mode {
		t.Errorf("file mode = %04o, want 0600", mode)
	}

	info, err = os.Stat(dir)
	if nil != err {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := info.Mode().Perm(); 0o700 != mode {
		t.Errorf("directory mode = %04o, want 0700", mode)
	}
}

func TestSaveUsesTheEncodedFileName(t *testing.T) {
	p, dir := newProvider(t)
	writeTestFile(t, filepath.Join(dir, "example.com.ini"), "imap_host = imap.example.com\n")
	writeTestFile(t, filepath.Join(dir, "disabled"), "example.com")

	d := New("Example.COM", Defaults{})
	if err := p.Save(d); nil != err {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "example.com.json")); nil != err {
		t.Error("Save must write the lowercase punycode file name")
	}
	if _, err := os.Stat(filepath.Join(dir, "example.com.ini")); nil == err {
		t.Error("Save must remove the legacy INI file first")
	}
	// Save() runs Delete() first, which re-enables the domain.
	if got := readFile(t, filepath.Join(dir, "disabled")); "" != got {
		t.Errorf("disabled file = %q, want the domain enabled", got)
	}

	// A name that does not encode cannot be saved.
	if err := p.Save(New("-bad", Defaults{})); ErrInvalidName != err {
		t.Errorf("Save(-bad) = %v, want ErrInvalidName", err)
	}
}

func TestSaveAlias(t *testing.T) {
	p, dir := newProvider(t)

	if err := p.SaveAlias("Alias.Example", "Target.Example"); nil != err {
		t.Fatalf("SaveAlias: %v", err)
	}
	if got, want := readFile(t, filepath.Join(dir, "alias.example.alias")), "target.example"; got != want {
		t.Errorf("alias file = %q, want %q", got, want)
	}

	if err := p.SaveAlias("alias.example", ""); ErrInvalidName != err {
		t.Errorf("SaveAlias without target = %v, want ErrInvalidName", err)
	}
	if err := p.SaveAlias("-invalid", "example.com"); ErrInvalidName != err {
		t.Errorf("SaveAlias with an invalid name = %v, want ErrInvalidName", err)
	}
}

func TestGetListOrder(t *testing.T) {
	p, dir := newProvider(t)
	for _, name := range []string{
		"b.example.json",
		"a.example.json",
		"z.example.alias",
		"y.example.alias",
		"_wildcard_a.example.json",
		"_wildcard_z.example.json",
		"default.json",
		"ignored.txt",
	} {
		writeTestFile(t, filepath.Join(dir, name), readFixture(t, "default.json"))
	}
	writeTestFile(t, filepath.Join(dir, "disabled"), "b.example\n")

	want := []ListEntry{
		// ksort() of the normal domains
		{Name: "a.example"},
		{Name: "b.example", Disabled: true},
		// ksort() of the aliases
		{Name: "y.example", Alias: true},
		{Name: "z.example", Alias: true},
		// krsort() of the wildcards
		{Name: "*z.example"},
		{Name: "*a.example"},
		{Name: "*"},
	}

	got := p.GetList(true)
	if len(got) != len(want) {
		t.Fatalf("GetList = %#v, want %d entries", got, len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("entry %d = %#v, want %#v", index, got[index], want[index])
		}
	}

	// Aliases are optional.
	for _, entry := range p.GetList(false) {
		if entry.Alias {
			t.Errorf("%#v must not be listed without aliases", entry)
		}
	}

	// A missing folder yields an empty list instead of a failure.
	empty := NewProvider(filepath.Join(t.TempDir(), "nope"), nil, Defaults{})
	if 0 != len(empty.GetList(true)) {
		t.Error("GetList of a missing folder must be empty")
	}
}

func TestValidateWildcardValues(t *testing.T) {
	tests := []struct {
		value  string
		values string
		ok     bool
		found  string
	}{
		{"example.com", "", true, ""},
		{"", "", false, ""},
		{"example.com", "*", true, "*"},
		{"example.com", "example.com", true, "example.com"},
		{"example.com", "other.com", false, ""},
		{"mail.example.com", "*.example.com", true, "*.example.com"},
		{"example.com", "*.example.com", false, ""},
		{"mail.example.com", "*example.com", true, "*example.com"},
		{"example.com", "***", true, "*"},
		{"mail.example.com", "*example.com other.com", true, "*example.com"},
		{"other.com", "*example.com;other.com", true, "other.com"},
		// ".*.example.com" becomes the unanchored /.*\..*\.example\.com/.
		{"deep.mail.example.com", ".*.example.com", true, ".*.example.com"},
		{"deepmail.example.com", ".*.example.com", false, ""},
		{"a.b.c", "a*b*c", true, "a*b*c"},
		{"ab", "a.c", false, ""},
		// The pattern is not anchored, like preg_match().
		{"xa.bx.c", "a*b*c", true, "a*b*c"},
	}

	for _, test := range tests {
		ok, found := ValidateWildcardValues(test.value, test.values)
		if ok != test.ok || found != test.found {
			t.Errorf("ValidateWildcardValues(%q, %q) = %v, %q, want %v, %q",
				test.value, test.values, ok, found, test.ok, test.found)
		}
	}
}

// newProvider builds a provider in an empty temporary domains folder.
func newProvider(t *testing.T) (*Provider, string) {
	t.Helper()

	dir := t.TempDir()
	return NewProvider(dir, nil, Defaults{}), dir
}

// writeTestFile creates a file below dir, creating the folder with 0700.
func writeTestFile(t *testing.T, name, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", name, err)
	}
}

// readFixture returns the content of a checked-in testdata file.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

// fakeCache is an in-memory Cache that counts the calls.
type fakeCache struct {
	values  map[string]string
	gets    int
	sets    int
	deletes int
}

// newFakeCache builds an empty fakeCache.
func newFakeCache() *fakeCache {
	return &fakeCache{values: make(map[string]string)}
}

func (c *fakeCache) Get(key string) (string, bool) {
	c.gets++
	value, ok := c.values[key]
	return value, ok
}

func (c *fakeCache) Set(key, value string) error {
	c.sets++
	c.values[key] = value
	return nil
}

func (c *fakeCache) Delete(key string) error {
	c.deletes++
	delete(c.values, key)
	return nil
}

// difference describes the first differing line of two texts.
func difference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	for index := 0; index < len(wantLines) || index < len(gotLines); index++ {
		var wantLine, gotLine string
		if index < len(wantLines) {
			wantLine = wantLines[index]
		}
		if index < len(gotLines) {
			gotLine = gotLines[index]
		}
		if wantLine != gotLine {
			return fmt.Sprintf("\nline %d:\n want %q\n  got %q", index+1, wantLine, gotLine)
		}
	}

	return "no difference"
}
