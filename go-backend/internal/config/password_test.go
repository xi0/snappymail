package config

// Tests of the admin password hashing (SetPassword/ValidatePassword) and the
// [value, comment] dump the admin settings form reads.

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPasswordRoundTrip checks the bcrypt hash and its verification, including a
// PHP "$2y$" hash written by the other backend.
func TestPasswordRoundTrip(t *testing.T) {
	application := NewApplication(filepath.Join(t.TempDir(), "application.ini"), "0.0.0", Defaults{})

	if application.ValidatePassword("") {
		t.Error("an empty password must not validate")
	}
	if application.ValidatePassword("secret") {
		t.Error("an unset password must not validate")
	}

	application.SetPassword("secret")

	hash := application.GetString("security", "admin_password", "")
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("admin_password = %q, want a bcrypt hash", hash)
	}
	if !application.ValidatePassword("secret") {
		t.Error("the correct password must validate")
	}
	if application.ValidatePassword("wrong") {
		t.Error("a wrong password must not validate")
	}
	if application.ValidatePassword("") {
		t.Error("an empty password must not validate")
	}

	// A PHP-style "$2y$" hash verifies with Go's bcrypt as well.
	phpHash := "$2y$" + strings.TrimPrefix(hash, "$2a$")
	application.Set("security", "admin_password", phpHash)
	if !application.ValidatePassword("secret") {
		t.Error("a $2y$ hash must validate like the $2a$ one")
	}
}

// TestDumpExtended checks the [value, comment] shape of jsonSerialize().
func TestDumpExtended(t *testing.T) {
	application := NewApplication(filepath.Join(t.TempDir(), "application.ini"), "0.0.0", Defaults{})

	dump := application.DumpExtended()

	webmail, ok := dump["webmail"]
	if !ok {
		t.Fatal("webmail section is missing")
	}
	title, ok := webmail["title"]
	if !ok || 2 != len(title) {
		t.Fatalf("webmail.title = %#v, want a two element list", webmail["title"])
	}
	if "SnappyMail Webmail" != title[0] {
		t.Errorf("webmail.title value = %v", title[0])
	}
	if "Text displayed as page title" != title[1] {
		t.Errorf("webmail.title comment = %v", title[1])
	}

	// version is present here (the caller drops it for the admin form).
	if _, ok := dump["version"]; !ok {
		t.Error("the version section must be part of the raw dump")
	}

	// The canonical values stay typed.
	security := dump["security"]
	if allow, _ := security["allow_admin_panel"]; true != allow[0] {
		t.Errorf("security.allow_admin_panel = %v, want true", allow[0])
	}
}
