package themes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles creates the given files below root, with empty content.
func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()

	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("/* css */"), 0o600); nil != err {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func TestListFindsShippedCustomAndNextcloudThemes(t *testing.T) {
	versionRoot := t.TempDir()
	indexRoot := t.TempDir()
	nextcloudRoot := t.TempDir()

	writeFiles(t, versionRoot,
		"themes/Gazellemail/styles.css",
		"themes/NoStyle/other.txt",  // no stylesheet at all
		"themes/Old/styles.less",    // LESS theme
		"themes/.hidden/styles.css", // hidden folder
		"themes/Empty/other.txt",
	)
	writeFiles(t, indexRoot,
		"themes/Company/styles.css",
		// The same name as a shipped theme stays a distinct entry.
		"themes/Gazellemail/styles.css",
	)
	writeFiles(t, nextcloudRoot,
		"themes/Rainloop/snappymail/style.css",
	)

	set := Set{VersionRoot: versionRoot, IndexRoot: indexRoot, NextcloudRoot: nextcloudRoot}

	want := "Company@custom,Gazellemail,Gazellemail@custom,Old,Rainloop@nextcloud"
	if got := strings.Join(set.List(), ","); got != want {
		t.Errorf("List() = %q, want %q", got, want)
	}

	// Without the Nextcloud root (class_exists('OC', false) is false) the
	// Nextcloud themes are absent.
	set.NextcloudRoot = ""
	if got := strings.Join(set.List(), ","); strings.Contains(got, "@nextcloud") {
		t.Errorf("List() = %q, want no Nextcloud themes", got)
	}

	// A missing tree is not an error.
	empty := Set{VersionRoot: filepath.Join(versionRoot, "missing")}
	if 0 != len(empty.List()) {
		t.Errorf("List() = %v, want nothing", empty.List())
	}
}

func TestValidate(t *testing.T) {
	versionRoot := t.TempDir()
	writeFiles(t, versionRoot, "themes/Gazellemail/styles.css", "themes/Other/styles.css")

	set := Set{VersionRoot: versionRoot}

	if got := set.Validate("Other", "Gazellemail"); "Other" != got {
		t.Errorf("Validate(Other) = %q, want Other", got)
	}
	// An unknown theme falls back to the configured default.
	if got := set.Validate("Missing", "Other"); "Other" != got {
		t.Errorf("Validate(Missing) = %q, want Other", got)
	}
	// An unknown theme and an unknown default fall back to Gazellemail.
	if got := set.Validate("Missing", "AlsoMissing"); "Gazellemail" != got {
		t.Errorf("Validate(Missing, AlsoMissing) = %q, want Gazellemail", got)
	}
	// The fallback is not validated when the theme itself is known.
	if got := set.Validate("Other", "AlsoMissing"); "Other" != got {
		t.Errorf("Validate(Other, AlsoMissing) = %q, want Other", got)
	}
}
