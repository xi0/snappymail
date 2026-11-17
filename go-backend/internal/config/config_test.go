package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestPHPBooleanCast(t *testing.T) {
	tests := []struct {
		value any
		want  bool
	}{
		{nil, false},
		{true, true},
		{false, false},
		{"", false},
		{"0", false},
		{"1", true},
		{"On", true},
		{"Off", true}, // the INI parser already turned it into ""
		{"false", true},
		{0, false},
		{2, true},
		{0.0, false},
		{0.5, true},
		{[]string{"a"}, false}, // not a scalar
	}

	for _, test := range tests {
		if got := castLike(false, test.value); got != test.want {
			t.Errorf("castLike(bool, %#v) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestPHPNumericCasts(t *testing.T) {
	tests := []struct {
		value    any
		wantInt  int
		wantReal float64
	}{
		{"20", 20, 20},
		{" 20 ", 20, 20},
		{"1.9", 1, 1.9},
		{"-1.9", -1, -1.9},
		{"1.5abc", 1, 1.5},
		{"1e3", 1000, 1000},
		{"1e", 1, 1},
		{"abc", 0, 0},
		{"0x1A", 0, 0},
		{".5", 0, 0.5}, // no leading digit, PHP: (int)".5" is 0, (float) is 0.5
		{"", 0, 0},
		{true, 1, 1},
		{false, 0, 0},
		{"2M", 2, 2},
	}

	for _, test := range tests {
		if got := castLike(0, test.value); got != test.wantInt {
			t.Errorf("castLike(int, %q) = %v, want %v", test.value, got, test.wantInt)
		}
		if got := castLike(0.0, test.value); got != test.wantReal {
			t.Errorf("castLike(float, %q) = %v, want %v", test.value, got, test.wantReal)
		}
	}
}

func TestPHPStringCast(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{true, "1"},
		{false, ""},
		{20, "20"},
		{2.0, "2"},
		{1.5, "1.5"},
		{0.0, "0"},
		{"text", "text"},
		{nil, ""},
	}

	for _, test := range tests {
		if got := castLike("", test.value); got != test.want {
			t.Errorf("castLike(string, %#v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestSetKeepsDefaultType(t *testing.T) {
	app := testApplication(t)

	// Strings from the INI parser are cast to the type of the default.
	app.Set("webmail", "messages_per_page", "35")
	app.Set("webmail", "allow_themes", "0")
	app.Set("security", "max_sys_getloadavg", "2.5")
	app.Set("webmail", "title", "Other")

	if got := app.GetInt("webmail", "messages_per_page", 0); 35 != got {
		t.Errorf("messages_per_page = %d, want 35", got)
	}
	if app.GetBool("webmail", "allow_themes", true) {
		t.Error(`allow_themes must be false for the string "0" like PHP's (bool) cast`)
	}
	if got := app.GetFloat("security", "max_sys_getloadavg", 0); 2.5 != got {
		t.Errorf("max_sys_getloadavg = %v, want 2.5", got)
	}
	if got := app.GetString("webmail", "title", ""); "Other" != got {
		t.Errorf("title = %q, want Other", got)
	}

	// An unknown key outside [custom] is ignored.
	app.Set("webmail", "no_such_key", "x")
	if _, ok := app.Lookup("webmail", "no_such_key"); ok {
		t.Error("unknown keys must not be added outside the custom section")
	}
}

func TestSetLegacyRewrites(t *testing.T) {
	app := testApplication(t)

	tests := []struct {
		section string
		key     string
		value   any
		want    [2]string
	}{
		{"webmail", "language_admin", "nl", [2]string{"admin_panel", "language"}},
		{"security", "admin_panel_host", "admin.example.com", [2]string{"admin_panel", "host"}},
		{"security", "admin_panel_key", "manage", [2]string{"admin_panel", "key"}},
		{"labs", "imap_use_expunge_all_on_delete", "On", [2]string{"imap", "use_expunge_all_on_delete"}},
		{"labs", "use_app_debug_js", "On", [2]string{"debug", "javascript"}},
		{"labs", "use_app_debug_css", "On", [2]string{"debug", "css"}},
		{"labs", "cache_system_data", "Off", [2]string{"cache", "system_data"}},
		{"labs", "force_https", "On", [2]string{"security", "force_https"}},
		{"labs", "check_new_messages", "Off", [2]string{"imap", "fetch_new_messages"}},
		{"labs", "login_fault_delay", "3", [2]string{"login", "fault_delay"}},
		{"labs", "log_ajax_response_write_limit", "42", [2]string{"logs", "json_response_write_limit"}},
		// The PHP if-chain also rewrites a rewritten key.
		{"labs", "imap_force_https", "On", [2]string{"security", "force_https"}},
	}

	for _, test := range tests {
		app.Set(test.section, test.key, test.value)

		want, ok := app.Lookup(test.want[0], test.want[1])
		if !ok {
			t.Errorf("%s.%s was not rewritten to %s.%s", test.section, test.key, test.want[0], test.want[1])
			continue
		}
		// Run the value through the same cast the target key uses.
		if test.value == "On" && want != true {
			t.Errorf("%s.%s = %#v, want true", test.want[0], test.want[1], want)
		}
	}
}

func TestSetValidatesLanguage(t *testing.T) {
	app := testApplication(t)

	app.Set("webmail", "language", "da")
	if got := app.GetString("webmail", "language", ""); "da" != got {
		t.Errorf("language = %q, want da", got)
	}

	app.Set("webmail", "language", "klingon")
	if got := app.GetString("webmail", "language", ""); "en" != got {
		t.Errorf("language = %q, want the en fallback", got)
	}

	// An unknown regional code falls back to the bare language, like
	// SnappyMail\L10n::validLanguage() does ("sv-SE" → "sv", there is only a
	// "sv" localization folder). Step 11 replaces this placeholder with the
	// real folder list, so "pt-BR" then survives as "pt-BR".
	app.Set("admin_panel", "language", "sv-SE")
	if got := app.GetString("admin_panel", "language", ""); "sv" != got {
		t.Errorf("admin_panel language = %q, want sv", got)
	}

	app.Set("admin_panel", "language", "pt_BR")
	if got := app.GetString("admin_panel", "language", ""); "pt" != got {
		t.Errorf("admin_panel language = %q, want pt", got)
	}
}

func TestSetCustomSection(t *testing.T) {
	app := testApplication(t)

	app.Set("custom", "Foo", "bar")
	if got := app.GetString("custom", "Foo", ""); "bar" != got {
		t.Errorf("custom.Foo = %q, want bar", got)
	}

	// Keys that could inject a new section or parameter are refused
	// (AbstractConfig::isValidIniKey()).
	app.Set("custom", "bad;key", "x")
	app.Set("custom", "bad]key", "x")
	app.Set("custom", " bad key", "x")
	for _, key := range []string{"bad;key", "bad]key", " bad key"} {
		if _, ok := app.Lookup("custom", key); ok {
			t.Errorf("custom.%s must be refused", key)
		}
	}

	// Save writes the custom section last.
	app.Set("custom", "Foo", "bar")
	content := app.render()
	if index := strings.Index(content, "[custom]"); 0 > index {
		t.Fatal("the custom section is missing from the output")
	} else if index < strings.Index(content, "[version]") {
		t.Error("the custom section must follow the defaults, like PHP's appended array entry")
	}
}

func TestReplaceEnvInConfiguration(t *testing.T) {
	app := testApplication(t)
	app.Env = map[string]string{
		"MY_TITLE":  "Env title",
		"MY_PROXY":  "http://proxy.example.com:3128",
		"ZERO":      "0",
		"UNRELATED": "ignored",
	}

	app.Set("labs", "replace_env_in_configuration", " webmail.title , labs.curl_proxy ")
	app.prepareReplaceEnv()

	app.Set("webmail", "title", "Title: $MY_TITLE")
	app.Set("labs", "curl_proxy", "$MY_PROXY")
	app.Set("webmail", "loading_description", "$UNRELATED")

	if got := app.GetString("webmail", "title", ""); "Title: Env title" != got {
		t.Errorf("title = %q, want the substituted value", got)
	}
	if got := app.GetString("labs", "curl_proxy", ""); "http://proxy.example.com:3128" != got {
		t.Errorf("curl_proxy = %q, want the substituted value", got)
	}
	// Not listed, so it is left alone.
	if got := app.GetString("webmail", "loading_description", ""); "$UNRELATED" != got {
		t.Errorf("loading_description = %q, want it untouched", got)
	}

	// PHP's empty(): a "0" env value is not substituted, and an unknown name
	// keeps the whole match.
	app.Set("labs", "curl_proxy", "$ZERO $MISSING")
	if got := app.GetString("labs", "curl_proxy", ""); "$ZERO $MISSING" != got {
		t.Errorf("curl_proxy = %q, want the placeholders kept", got)
	}

	// Without an environment the replacement is off.
	app.Env = nil
	app.prepareReplaceEnv()
	if got := app.GetString("labs", "curl_proxy", ""); "$ZERO $MISSING" != got {
		t.Errorf("curl_proxy = %q, want it untouched without an environment", got)
	}
}

func TestDumpIsACopy(t *testing.T) {
	app := testApplication(t)

	dump := app.Dump()
	dump["webmail"]["title"] = "mutated"

	if got := app.GetString("webmail", "title", ""); "SnappyMail Webmail" != got {
		t.Errorf("title = %q, the dump must not alias the configuration", got)
	}
}

// testApplication builds an in-memory configuration with the defaults.
func testApplication(t *testing.T) *Application {
	t.Helper()
	return NewApplication(fmt.Sprintf("%s/application.ini", t.TempDir()), "0.0.0", Defaults{})
}
