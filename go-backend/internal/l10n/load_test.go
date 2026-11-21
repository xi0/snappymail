package l10n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLocalization creates <root>/app/localization/<relative> with content.
func writeLocalization(t *testing.T, root, relative, content string) {
	t.Helper()

	path := filepath.Join(root, "app", "localization", filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadMergesEnBaseAndExact(t *testing.T) {
	root := t.TempDir()
	writeLocalization(t, root, "en/user.json",
		`{"A":"en","OnlyEn":"en","Nested":{"x":"en","y":"en"}}`)
	writeLocalization(t, root, "sv/user.json",
		`{"A":"sv","Nested":{"y":"sv"}}`)
	writeLocalization(t, root, "sv-SE/user.json",
		`{"B":"sv-SE","Nested":{"y":"sv-SE"}}`)

	catalog := NewCatalog(root)
	document := catalog.Load("sv_SE", "user")

	if "sv" != document["A"] {
		t.Errorf("A = %v, want sv (base overrides en)", document["A"])
	}
	if "en" != document["OnlyEn"] {
		t.Errorf("OnlyEn = %v, want en (kept from the base file)", document["OnlyEn"])
	}
	if "sv-SE" != document["B"] {
		t.Errorf("B = %v, want sv-SE", document["B"])
	}

	nested, ok := document["Nested"].(map[string]any)
	if !ok {
		t.Fatalf("Nested = %#v", document["Nested"])
	}
	if "en" != nested["x"] {
		t.Errorf("Nested.x = %v, want en", nested["x"])
	}
	if "sv-SE" != nested["y"] {
		t.Errorf("Nested.y = %v, want sv-SE", nested["y"])
	}

	// A missing language only yields the en texts.
	fallback := catalog.Load("xx", "user")
	if "en" != fallback["A"] {
		t.Errorf("A = %v, want the en fallback", fallback["A"])
	}
	if _, ok := fallback["B"]; ok {
		t.Error("B must not exist for an unknown language")
	}
}

func TestMergeRecursiveReplacesSlices(t *testing.T) {
	dst := map[string]any{"list": []any{"a", "b"}}
	src := map[string]any{"list": []any{"c"}}

	merged := MergeRecursive(dst, src)
	list, _ := merged["list"].([]any)
	if 2 != len(list) || "c" != list[0] || "b" != list[1] {
		t.Errorf("list = %#v, want [c b]", list)
	}
}

func TestCompileLanguageRendersJavaScript(t *testing.T) {
	root := t.TempDir()
	writeLocalization(t, root, "langs.json",
		`{"LANGS_NAMES":{"en":"English","de":"Deutsch"},"LANGS_NAMES_EN":{"en":"English"}}`)
	writeLocalization(t, root, "en/user.json",
		`{"ONE":"1","VERSION":"1.0.0","TEXT":"hello","PLURAL":"2"}`)
	writeLocalization(t, root, "en/relativetimeformat.js", "\n  {plural: n => 1 == n ? 'one' : 'other'}")

	catalog := NewCatalog(root)

	js, err := catalog.CompileLanguage("en", false, nil)
	if nil != err {
		t.Fatalf("CompileLanguage: %v", err)
	}

	if !strings.HasPrefix(js, "document.documentElement.lang = 'en';") {
		t.Errorf("js = %q, want the lang assignment first", js)
	}
	if !strings.Contains(js, "\nrl.I18N=") || !strings.Contains(js, "\nrl.relativeTime = ") {
		t.Errorf("js = %q, want the rl.I18N and rl.relativeTime parts", js)
	}

	// JSON_NUMERIC_CHECK turns numeric strings into numbers; version strings
	// that are not numeric stay strings.
	if !strings.Contains(js, `"ONE":1`) {
		t.Errorf("js = %q, want ONE as a number", js)
	}
	if !strings.Contains(js, `"PLURAL":2`) {
		t.Errorf("js = %q, want PLURAL as a number", js)
	}
	if !strings.Contains(js, `"VERSION":"1.0.0"`) {
		t.Errorf("js = %q, want VERSION to stay a string", js)
	}

	// The leading whitespace of relativetimeformat.js is stripped.
	if !strings.Contains(js, "rl.relativeTime = {plural:") {
		t.Errorf("js = %q, want the trimmed relativetimeformat.js", js)
	}

	// LANGS_NAMES is intersected with the available languages (de is not
	// available here).
	if !strings.Contains(js, `"LANGS_NAMES":{"en":"English"}`) {
		t.Errorf("js = %q, want only the available LANGS_NAMES", js)
	}
}

func TestCompileLanguageCallsPluginReader(t *testing.T) {
	root := t.TempDir()
	writeLocalization(t, root, "en/user.json", `{"A":"en"}`)

	catalog := NewCatalog(root)

	reader := &fakeReader{}
	if _, err := catalog.CompileLanguage("en", false, reader); nil != err {
		t.Fatalf("CompileLanguage: %v", err)
	}

	if "en" != reader.language {
		t.Errorf("ReadLang language = %q, want en", reader.language)
	}
	if _, ok := reader.lang["A"]; !ok {
		t.Error("ReadLang must receive the merged document")
	}
}

// fakeReader is the LangReader plugin surface.
type fakeReader struct {
	language string
	lang     map[string]any
}

// ReadLang is Plugins\Manager::ReadLang().
func (f *fakeReader) ReadLang(language string, lang map[string]any) {
	f.language = language
	f.lang = lang
}
