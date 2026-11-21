package l10n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newVersionRoot builds an app/localization tree with the given files.
//
// The keys of files are "<lang>/<file>", so a language is available for an area
// exactly when its file exists.
func newVersionRoot(t *testing.T, files ...string) string {
	t.Helper()

	root := t.TempDir()
	for _, file := range files {
		path := filepath.Join(root, "app", "localization", filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); nil != err {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return root
}

func TestLanguagesFollowsTheExistingFiles(t *testing.T) {
	root := newVersionRoot(t,
		"en/user.json", "en/admin.json",
		"da/user.json", "da/admin.json",
		"de/user.json",        // no admin.json: webmail only
		"notalanguage/README", // no user.json: not a language
	)

	catalog := NewCatalog(root)

	if got, want := strings.Join(catalog.Languages(false), ","), "da,de,en"; got != want {
		t.Errorf("Languages(false) = %q, want %q", got, want)
	}
	if got, want := strings.Join(catalog.Languages(true), ","), "da,en"; got != want {
		t.Errorf("Languages(true) = %q, want %q", got, want)
	}

	// The list is cached per area: two calls agree.
	if again := catalog.Languages(false); strings.Join(again, ",") != "da,de,en" {
		t.Errorf("Languages(false) = %v, want the cached list", again)
	}

	// A missing tree yields an empty list, not nil-panic.
	empty := NewCatalog(filepath.Join(root, "missing"))
	if 0 != len(empty.Languages(false)) {
		t.Error("a missing localization tree must have no languages")
	}
}

func TestValidLanguage(t *testing.T) {
	languages := []string{"da", "en", "en-GB", "pt-BR"}

	tests := []struct {
		language string
		want     string
	}{
		{"en", "en"},
		{"en-GB", "en-GB"},
		// strtr('_', '-').
		{"en_GB", "en-GB"},
		// Unknown exact language falls back to its primary subtag.
		{"en-US", "en"},
		{"pt-PT", ""},
		{"pt_pt", ""},
		// The primary subtag itself is not available here.
		{"pt", ""},
		{"", ""},
		// PHP's strtok() skips leading delimiters.
		{"-en", "en"},
		{"--en", "en"},
		{"fr", ""},
		// Case sensitive, like in_array().
		{"EN", ""},
	}

	for _, test := range tests {
		if got := ValidLanguage(languages, test.language); got != test.want {
			t.Errorf("ValidLanguage(%q) = %q, want %q", test.language, got, test.want)
		}
	}
}

func TestAcceptLanguage(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"", ""},
		{"en", "en"},
		{"EN-us,en;q=0.9", "en-us,en"},
		{"da, en-gb;q=0.8, en;q=0.7", "da,en-gb,en"},
		// A missing q value counts as 1.
		{"de;q=0.5, fr", "fr,de"},
		// A repeated code keeps its first position but takes the last weight.
		{"de, fr, de;q=0.1", "fr,de"},
		{"*", ""},
	}

	for _, test := range tests {
		got := strings.Join(AcceptLanguage(test.header), ",")
		if got != test.want {
			t.Errorf("AcceptLanguage(%q) = %q, want %q", test.header, got, test.want)
		}
	}
}

func TestDetectOrderHelper(t *testing.T) {
	// The helper keeps the stable order of equal weights (PHP 8 sorts stably).
	codes := AcceptLanguage("a;q=0.5, b;q=0.5, c;q=0.5")
	if want := "a,b,c"; strings.Join(codes, ",") != want {
		t.Errorf("stable order = %v, want %s", codes, want)
	}
}
