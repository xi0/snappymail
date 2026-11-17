package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedNow is the clock the golden fixture was written with (gmdate('r')).
var fixedNow = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

// goldenVersion is APP_VERSION of the golden fixture.
const goldenVersion = "0.0.0"

func TestLoadApplicationIniMatchesPHPGolden(t *testing.T) {
	app := NewApplication(copyFixture(t, "application.ini"), goldenVersion, Defaults{})
	app.Now = fixedClock

	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}

	assertDumpMatchesGolden(t, app)
}

func TestLoadLegacyApplicationIni(t *testing.T) {
	app := NewApplication(copyFixture(t, "legacy.ini"), goldenVersion, Defaults{})
	app.Now = fixedClock

	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		section string
		key     string
		want    any
	}{
		// Plain values.
		{"webmail", "title", "My SnappyMail"},
		{"webmail", "allow_themes", false},
		{"webmail", "messages_per_page", 35},
		// An unknown language falls back to "en" (Application::Set()).
		{"webmail", "language", "en"},
		// webmail.language_admin → admin_panel.language.
		{"admin_panel", "language", "nl"},
		// security.admin_panel_* → admin_panel.*.
		{"admin_panel", "host", "admin.example.com"},
		{"admin_panel", "key", "manage"},
		{"security", "custom_server_signature", `My "quoted" server\name`},
		{"security", "encrypt_cipher", "aes-256-cbc"},
		// labs.* → their new homes.
		{"imap", "use_expunge_all_on_delete", true},
		{"imap", "fetch_new_messages", false},
		{"security", "force_https", true},
		{"login", "fault_delay", 3},
		{"cache", "system_data", false},
		{"debug", "javascript", true},
		{"logs", "json_response_write_limit", 42},
		// Unknown keys are only accepted in [custom].
		{"custom", "Foo", "bar"},
	}

	for _, test := range tests {
		got, ok := app.Lookup(test.section, test.key)
		if !ok {
			t.Errorf("%s.%s is missing after the load", test.section, test.key)
			continue
		}
		if got != test.want {
			t.Errorf("%s.%s = %#v, want %#v", test.section, test.key, got, test.want)
		}
	}

	// Save() writes the migrated keys in the canonical layout.
	if err := app.Save(); nil != err {
		t.Fatalf("Save: %v", err)
	}

	saved := readFile(t, app.File())
	for _, want := range []string{
		"[custom]\nFoo = \"bar\"",
		`custom_server_signature = "My \"quoted\" server\\name"`,
		`encrypt_cipher = "aes-256-cbc"`,
		"force_https = On",
	} {
		if !strings.Contains(saved, want) {
			t.Errorf("the saved file does not contain %q:\n%s", want, saved)
		}
	}
}

func TestSaveIsByteIdenticalToPHP(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "application.ini")

	app := NewApplication(file, goldenVersion, Defaults{})
	app.Now = fixedClock

	if err := app.Save(); nil != err {
		t.Fatalf("Save: %v", err)
	}

	if got, want := readFile(t, file), readFixture(t, "application.ini"); got != want {
		t.Errorf("Save() differs from the PHP layout: %s", difference(want, got))
	}

	// configs/ holds the admin password hash, so RainLoop\Utils::saveFile()
	// restricts it.
	info, err := os.Stat(file)
	if nil != err {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); 0o600 != mode {
		t.Errorf("file mode = %04o, want 0600", mode)
	}

	info, err = os.Stat(filepath.Dir(file))
	if nil != err {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := info.Mode().Perm(); 0o700 != mode {
		t.Errorf("directory mode = %04o, want 0700", mode)
	}
}

func TestLoadSaveRoundTripIsIdempotent(t *testing.T) {
	file := copyFixture(t, "application.ini")

	app := NewApplication(file, goldenVersion, Defaults{})
	app.Now = fixedClock

	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}
	if err := app.Save(); nil != err {
		t.Fatalf("Save: %v", err)
	}

	if got, want := readFile(t, file), readFixture(t, "application.ini"); got != want {
		t.Errorf("load + save is not a no-op: %s", difference(want, got))
	}
}

func TestLoadCreatesDefaultsForMissingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "application.ini")

	app := NewApplication(file, goldenVersion, Defaults{})
	app.Now = fixedClock

	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}

	if got, want := readFile(t, file), readFixture(t, "application.ini"); got != want {
		t.Errorf("the created file differs from the PHP layout: %s", difference(want, got))
	}

	assertDumpMatchesGolden(t, app)
}

func TestLoadOverwritesBrokenFile(t *testing.T) {
	for _, content := range []string{"", "; nothing here\n", "[unclosed\nkey = value\n"} {
		file := filepath.Join(t.TempDir(), "configs", "application.ini")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); nil != err {
			t.Fatalf("write: %v", err)
		}

		var logged []string
		app := NewApplication(file, goldenVersion, Defaults{})
		app.Now = fixedClock
		app.Logf = func(category, message string) { logged = append(logged, category+": "+message) }

		if err := app.Load(); nil != err {
			t.Fatalf("Load(%q): %v", content, err)
		}

		if got, want := readFile(t, file), readFixture(t, "application.ini"); got != want {
			t.Errorf("Load(%q) did not restore the defaults: %s", content, difference(want, got))
		}
	}
}

func TestLoadServerBusyGuard(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "application.ini")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	content := "[security]\nmax_sys_getloadavg = 2\n"
	if err := os.WriteFile(file, []byte(content), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	app := NewApplication(file, goldenVersion, Defaults{})
	app.LoadAverage = func() (float64, bool) { return 4.5, true }

	err := app.Load()

	var busy *ServerBusy
	if !errors.As(err, &busy) {
		t.Fatalf("Load error = %v, want a *ServerBusy", err)
	}
	if 4.5 != busy.Load {
		t.Errorf("busy.Load = %v, want 4.5", busy.Load)
	}
	if want := "Mailserver too busy (4.5). Please try again later."; want != busy.Message {
		t.Errorf("busy.Message = %q, want %q", busy.Message, want)
	}

	// The guard fires before any save, so the file is untouched.
	if got := readFile(t, file); content != got {
		t.Errorf("file = %q, want it untouched", got)
	}

	// Under the limit the load succeeds.
	app.LoadAverage = func() (float64, bool) { return 1.5, true }
	if err := app.Load(); nil != err {
		t.Errorf("Load under the limit: %v", err)
	}

	// A platform without sys_getloadavg() skips the guard.
	app.LoadAverage = func() (float64, bool) { return 99, false }
	if err := app.Load(); nil != err {
		t.Errorf("Load without a load average: %v", err)
	}
}

func TestLoadReplacesUnsupportedCipher(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "application.ini")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(file, []byte("[security]\nencrypt_cipher = \"des-ede3\"\n"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	var logged []string
	app := NewApplication(file, goldenVersion, Defaults{})
	app.Now = fixedClock
	app.Logf = func(category, message string) { logged = append(logged, category+": "+message) }

	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}

	if got := app.GetString("security", "encrypt_cipher", ""); DefaultCipher != got {
		t.Errorf("encrypt_cipher = %q, want %q", got, DefaultCipher)
	}

	want := "Crypt: OpenSSL no support for cipher 'des-ede3'"
	if 0 == len(logged) || want != logged[0] {
		t.Errorf("warnings = %v, want %q first", logged, want)
	}

	// PHP saves the replacement cipher.
	if got := readFile(t, file); !strings.Contains(got, `encrypt_cipher = "`+DefaultCipher+`"`) {
		t.Errorf("the replaced cipher was not saved:\n%s", got)
	}
}

func TestLoadResetsUnknownTimeZone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "configs", "application.ini")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(file, []byte("[logs]\ntime_zone = \"Mars/Olympus\"\n"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	app := NewApplication(file, goldenVersion, Defaults{})
	if err := app.Load(); nil != err {
		t.Fatalf("Load: %v", err)
	}

	if got := app.GetString("logs", "time_zone", ""); "UTC" != got {
		t.Errorf("time_zone = %q, want UTC", got)
	}

	// PHP only fixes the value in memory.
	if got := readFile(t, file); !strings.Contains(got, "Mars/Olympus") {
		t.Errorf("the file must not be rewritten:\n%s", got)
	}
}

func TestDefaultsFromEnvironment(t *testing.T) {
	// ini_get('upload_max_filesize') arithmetic of defaultValues().
	tests := []struct {
		upload string
		want   float64
	}{
		{"", 2},
		{"2M", 2},
		{"64M", 25},
		{"1G", 25},
		{"512K", 0.5},
		{"0", 0},
	}
	for _, test := range tests {
		got := Defaults{UploadMaxFilesize: test.upload}.attachmentSizeLimit()
		if got != test.want {
			t.Errorf("attachmentSizeLimit(%q) = %v, want %v", test.upload, got, test.want)
		}
	}

	// contacts.sqlite_global defaults to whether the address book exists.
	withBook := NewApplication(filepath.Join(t.TempDir(), "application.ini"), goldenVersion,
		Defaults{AddressBookExists: true})
	if !withBook.GetBool("contacts", "sqlite_global", false) {
		t.Error("sqlite_global must default to true when AddressBook.sqlite exists")
	}
}

// fixedClock is the app.Now client the goldens use.
func fixedClock() time.Time { return fixedNow }

// readFixture returns the content of a testdata file.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

// copyFixture copies a testdata file into a temporary directory, so a test never
// rewrites the checked-in fixture.
func copyFixture(t *testing.T, name string) string {
	t.Helper()

	target := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(readFixture(t, name)), 0o600); nil != err {
		t.Fatalf("write %s: %v", target, err)
	}

	return target
}

// readFile returns the content of name.
func readFile(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(name)
	if nil != err {
		t.Fatalf("read %s: %v", name, err)
	}

	return string(content)
}

// assertDumpMatchesGolden compares Dump() with the PHP golden.
func assertDumpMatchesGolden(t *testing.T, app *Application) {
	t.Helper()

	got, err := json.MarshalIndent(app.Dump(), "", "  ")
	if nil != err {
		t.Fatalf("marshal dump: %v", err)
	}

	if want := readFixture(t, "application.dump.json"); string(got)+"\n" != want {
		t.Errorf("configuration dump differs from PHP: %s",
			difference(want, string(got)+"\n"))
	}
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
			return fmt.Sprintf("line %d:\n want %q\n  got %q\n(lines: want %d, got %d)",
				index+1, wantLine, gotLine, len(wantLines), len(gotLines))
		}
	}

	return "no difference"
}
