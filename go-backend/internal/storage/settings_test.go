package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// phpSettings is a settings document as the PHP backend writes it: escaped "/",
// \uXXXX for non-ASCII, no whitespace and the keys in insertion order.
const phpSettings = `{"MessagesPerPage":25,"Layout":1,"language":"en","ViewImages":"ask",` +
	`"KeyPass":"\/path\/to\/key","Unicode":"Gr\u00fc\u00dfe","Refresh":true,"Hidden":null,` +
	`"Nested":{"plugin":"value","count":2},"List":[1,"two",false]}`

func TestParseSettingsKeepsOrderAndForm(t *testing.T) {
	settings := ParseSettings(phpSettings)
	if nil == settings {
		t.Fatal("ParseSettings returned nil")
	}

	wantKeys := []string{
		"MessagesPerPage", "Layout", "language", "ViewImages", "KeyPass",
		"Unicode", "Refresh", "Hidden", "Nested", "List",
	}
	gotKeys := settings.Keys()
	if len(wantKeys) != len(gotKeys) {
		t.Fatalf("keys = %v, want %v", gotKeys, wantKeys)
	}
	for index := range wantKeys {
		if wantKeys[index] != gotKeys[index] {
			t.Fatalf("keys = %v, want %v", gotKeys, wantKeys)
		}
	}

	// Re-encoding a PHP written document must produce the same bytes.
	encoded, err := settings.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	if string(encoded) != phpSettings {
		t.Errorf("Encode() = %s, want %s", encoded, phpSettings)
	}
}

func TestSettingsAccessors(t *testing.T) {
	settings := ParseSettings(phpSettings)

	if got := settings.GetConf("MessagesPerPage", 0); nil == got || "25" != phpString(got) {
		t.Errorf("GetConf(MessagesPerPage) = %v", got)
	}
	if got := settings.GetConf("missing", "fallback"); "fallback" != got {
		t.Errorf("GetConf(missing) = %v, want fallback", got)
	}
	// isset() semantics: a stored null falls back to the default.
	if got := settings.GetConf("Hidden", "fallback"); "fallback" != got {
		t.Errorf("GetConf(Hidden) = %v, want the default", got)
	}

	// An existing key keeps its position, a new one is appended.
	settings.SetConf("Layout", 2)
	settings.SetConf("NewKey", "x")
	keys := settings.Keys()
	if "Layout" != keys[1] || "NewKey" != keys[len(keys)-1] {
		t.Errorf("keys after SetConf = %v", keys)
	}

	if !settings.Has("NewKey") || settings.Has("other") {
		t.Error("Has() must report the stored keys")
	}

	encoded, err := settings.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(encoded), `"Layout":2`) {
		t.Errorf("Encode() = %s, want Layout 2", encoded)
	}

	// ToMap exposes the plain values for the AppData payloads.
	plain := settings.ToMap()
	if nested, ok := plain["Nested"].(map[string]any); !ok || "value" != nested["plugin"] {
		t.Errorf("ToMap(Nested) = %#v", plain["Nested"])
	}
	if list, ok := plain["List"].([]any); !ok || 3 != len(list) {
		t.Errorf("ToMap(List) = %#v", plain["List"])
	}
}

func TestSettingsNormalisations(t *testing.T) {
	// RainLoop\Settings::__construct() appends JunkFolder/DraftsFolder below the
	// legacy keys and moves "Language" to "language" at the end.
	settings := ParseSettings(`{"SpamFolder":"Spam","Language":"da","DraftFolder":"Drafts"}`)
	if nil == settings {
		t.Fatal("ParseSettings returned nil")
	}

	encoded, err := settings.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	want := `{"SpamFolder":"Spam","DraftFolder":"Drafts","JunkFolder":"Spam","DraftsFolder":"Drafts","language":"da"}`
	if string(encoded) != want {
		t.Errorf("Encode() = %s, want %s", encoded, want)
	}

	// The rename only happens when "language" is not set yet.
	settings = ParseSettings(`{"Language":"da","language":"en"}`)
	encoded, err = settings.Encode()
	if nil != err {
		t.Fatalf("Encode: %v", err)
	}
	if string(encoded) != `{"Language":"da","language":"en"}` {
		t.Errorf("Encode() = %s", encoded)
	}
}

func TestParseSettingsRejectsNonObjects(t *testing.T) {
	for _, content := range []string{"", "[1,2]", `"text"`, "null", "{invalid"} {
		if nil != ParseSettings(content) {
			t.Errorf("ParseSettings(%q) must return nil", content)
		}
	}
}

func TestSettingsStorageRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	provider := NewProvider(NewFileStorage(dir, false))
	store := NewSettingsStorage(provider)

	if name := store.FileName(); SettingsFileName != name {
		t.Errorf("FileName = %q, want %q", name, SettingsFileName)
	}

	// A missing file loads as an empty document.
	settings := store.Load(account("user@example.com"))
	if nil == settings || 0 != len(settings.Keys()) {
		t.Fatalf("Load of a missing file = %#v", settings)
	}

	file := filepath.Join(dir, "example.com", "user", SettingsFileName)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	// A settings file written by PHP must load and re-save unchanged.
	if err := os.WriteFile(file, []byte(phpSettings), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	settings = store.Load(account("user@example.com"))
	if nil == settings {
		t.Fatal("Load returned nil")
	}
	if err := store.Save(account("user@example.com"), settings); nil != err {
		t.Fatalf("Save: %v", err)
	}

	content, err := os.ReadFile(file)
	if nil != err {
		t.Fatalf("read: %v", err)
	}
	if string(content) != phpSettings {
		t.Errorf("re-saved settings = %s, want %s", content, phpSettings)
	}

	info, err := os.Stat(file)
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); 0o600 != mode {
		t.Errorf("file mode = %04o, want 0600", mode)
	}

	if !store.Delete(account("user@example.com")) {
		t.Error("Delete must report true")
	}
	if _, err := os.Stat(file); nil == err {
		t.Error("Delete must remove the settings file")
	}
}

func TestSettingsStorageLocalFileName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "storage")
	store := NewSettingsStorage(NewProvider(NewFileStorage(dir, true)))

	if name := store.FileName(); SettingsFileNameLocal != name {
		t.Errorf("FileName = %q, want %q", name, SettingsFileNameLocal)
	}

	settings := NewSettings()
	settings.SetConf("UseThreads", true)
	if err := store.Save(account("user@example.com"), settings); nil != err {
		t.Fatalf("Save: %v", err)
	}

	file := filepath.Join(dir, "example.com", "user", SettingsFileNameLocal)
	content, err := os.ReadFile(file)
	if nil != err {
		t.Fatalf("read %s: %v", file, err)
	}
	if `{"UseThreads":true}` != string(content) {
		t.Errorf("content = %s", content)
	}
}

func TestEncodeJSONTypes(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "true"},
		{0, "0"},
		{int64(-7), "-7"},
		{1.5, "1.5"},
		{2.0, "2.0"},
		{"", `""`},
		{"a/b", `"a\/b"`},
		{"tab\t", `"tab\t"`},
		{"ß", `"\u00df"`},
		{"🙂", `"\ud83d\ude42"`},
		{object{}, "{}"},
		{array{}, "[]"},
		{object{{"a", 1}, {"b", array{true, nil}}}, `{"a":1,"b":[true,null]}`},
		// A Go map has no order, so its keys are sorted.
		{map[string]any{"b": 1, "a": 2}, `{"a":2,"b":1}`},
	}

	for _, test := range tests {
		encoded, err := encodeJSON(test.value)
		if nil != err {
			t.Errorf("encodeJSON(%#v): %v", test.value, err)
			continue
		}
		if string(encoded) != test.want {
			t.Errorf("encodeJSON(%#v) = %s, want %s", test.value, encoded, test.want)
		}
	}
}
