package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
)

// testCrypter is SnappyMail\Crypt with the test APP_SALT.
func testCrypter() *cryptox.Crypter {
	return cryptox.New("salt", testVersion, cryptox.DefaultCipher)
}

// newTestConfig builds the configuration of the test plugin.
func newTestConfig(t *testing.T, dir string) *Config {
	t.Helper()

	properties := []*Property{
		NewProperty("text").SetType(StringValue).SetDefaultValue("value"),
		NewProperty("count").SetType(IntValue).SetDefaultValue(3),
		NewProperty("enabled").SetType(BoolValue).SetDefaultValue(false),
		NewProperty("ratio").SetType(StringValue).SetDefaultValue(1.5),
		NewProperty("options").SetDefaultValue([]Option{{ID: "a", Name: "A"}}),
		NewProperty("password").SetType(PasswordValue).SetEncrypted(true),
	}

	return newConfig("test", dir, properties, testCrypter(), "salt")
}

func TestConfigDefaults(t *testing.T) {
	config := newTestConfig(t, t.TempDir())

	if "value" != config.Get("text", "") {
		t.Errorf("text = %v", config.Get("text", ""))
	}
	if 3 != config.Get("count", 0) {
		t.Errorf("count = %v, want 3", config.Get("count", 0))
	}
	if false != config.Get("enabled", true) {
		t.Errorf("enabled = %v, want false", config.Get("enabled", true))
	}
	// Config\Plugin stores '' for an array default (the options are not a value).
	if "" != config.Get("options", "missing") {
		t.Errorf("options = %v, want the empty string", config.Get("options", "missing"))
	}
	// A key that is not declared keeps its default.
	if "fallback" != config.Get("unknown", "fallback") {
		t.Errorf("unknown = %v", config.Get("unknown", "fallback"))
	}
}

func TestConfigSetKeepsTheDeclaredType(t *testing.T) {
	config := newTestConfig(t, t.TempDir())

	config.Set("count", "7")
	if 7 != config.Get("count", 0) {
		t.Errorf("count = %v, want the int 7", config.Get("count", 0))
	}
	config.Set("enabled", "1")
	if true != config.Get("enabled", false) {
		t.Errorf("enabled = %v, want true", config.Get("enabled", false))
	}
	config.Set("text", 12)
	if "12" != config.Get("text", "") {
		t.Errorf("text = %v, want \"12\"", config.Get("text", ""))
	}
	// Undeclared keys are ignored by AbstractConfig::Set().
	config.Set("unknown", "x")
	if _, ok := config.Values()["unknown"]; ok {
		t.Error("an undeclared key must not be stored")
	}
}

func TestConfigSaveWritesThePHPJSONLayout(t *testing.T) {
	dir := t.TempDir()
	config := newTestConfig(t, dir)
	config.Set("text", "hello/world")

	if !config.Save() {
		t.Fatal("Save() must succeed")
	}

	content, err := os.ReadFile(filepath.Join(dir, "plugin-test.json"))
	if nil != err {
		t.Fatalf("read the saved file: %v", err)
	}

	// json_encode(..., JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES): four space
	// indentation and a plain "/" in the value.
	if !strings.Contains(string(content), "    \"plugin\": {") {
		t.Errorf("the document is not pretty printed: %s", content)
	}
	if !strings.Contains(string(content), "hello/world") {
		t.Errorf("the slash is escaped: %s", content)
	}
	if strings.Contains(string(content), `"unknown"`) {
		t.Errorf("undeclared keys leaked into the file: %s", content)
	}

	// The file is 0600 like every other private data file.
	if info, err := os.Stat(filepath.Join(dir, "plugin-test.json")); nil == err && 0o600 != info.Mode().Perm() {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestConfigLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	config := newTestConfig(t, dir)
	config.Set("text", "stored")
	config.Set("count", 11)
	if !config.Save() {
		t.Fatal("Save() must succeed")
	}

	reloaded := newTestConfig(t, dir)
	if !reloaded.Load() {
		t.Fatal("Load() must succeed")
	}
	if "stored" != reloaded.Get("text", "") {
		t.Errorf("text = %v, want the stored value", reloaded.Get("text", ""))
	}
	if 11 != reloaded.Get("count", 0) {
		t.Errorf("count = %v, want 11", reloaded.Get("count", 0))
	}
	if !reloaded.Loaded() {
		t.Error("Loaded() must report the read")
	}
}

func TestConfigLoadsTheLegacyINI(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin-test.ini"),
		"; SnappyMail plugin (test)\n"+
			"[plugin]\n"+
			"text = \"from ini\"\n"+
			"count = 9\n"+
			"enabled = On\n"+
			"[other]\n"+
			"count = 99\n")

	config := newTestConfig(t, dir)
	if !config.Load() {
		t.Fatal("Load() must accept the legacy .ini")
	}
	if "from ini" != config.Get("text", "") {
		t.Errorf("text = %v", config.Get("text", ""))
	}
	if 9 != config.Get("count", 0) {
		t.Errorf("count = %v, want the [plugin] section value 9", config.Get("count", 0))
	}
	if true != config.Get("enabled", false) {
		t.Errorf("enabled = %v, want On to be true", config.Get("enabled", false))
	}

	// Saving migrates the values to the .json file and removes the .ini.
	if !config.Save() {
		t.Fatal("Save() must succeed")
	}
	if isFile(filepath.Join(dir, "plugin-test.ini")) {
		t.Error("the legacy .ini must be removed")
	}
	if !isFile(filepath.Join(dir, "plugin-test.json")) {
		t.Error("the .json file must exist")
	}
}

func TestConfigEncryptedValue(t *testing.T) {
	dir := t.TempDir()
	config := newTestConfig(t, dir)

	config.SetEncrypted("password", "s3cret")

	sealed := phpString(config.Get("password", ""))
	if "" == sealed || strings.Contains(sealed, "s3cret") {
		t.Fatalf("the value is not sealed: %q", sealed)
	}
	if "s3cret" != config.GetDecrypted("password", "") {
		t.Errorf("GetDecrypted() = %v", config.GetDecrypted("password", ""))
	}

	// A broken blob falls back to the default, like the swallowed exception.
	config.Set("password", "not a blob")
	if "fallback" != config.GetDecrypted("password", "fallback") {
		t.Errorf("GetDecrypted(broken) = %v", config.GetDecrypted("password", "fallback"))
	}
}

func TestParseLegacyINI(t *testing.T) {
	values := parseLegacyINI(strings.Join([]string{
		"; comment",
		"# comment",
		"[plugin]",
		`quoted = "a;b"`,
		"plain = text ; trailing comment",
		"off = Off",
		"multi = two words",
		"[other]",
		"ignored = 1",
		"malformed",
	}, "\n"), "plugin")

	if "a;b" != values["quoted"] {
		t.Errorf("quoted = %v", values["quoted"])
	}
	if "text" != values["plain"] {
		t.Errorf("plain = %v", values["plain"])
	}
	if false != values["off"] {
		t.Errorf("off = %v", values["off"])
	}
	if "two words" != values["multi"] {
		t.Errorf("multi = %v", values["multi"])
	}
	if _, ok := values["ignored"]; ok {
		t.Error("only the requested section must be read")
	}
	if _, ok := values["malformed"]; ok {
		t.Error("a line without '=' must be ignored")
	}
}

func TestPropertySetValue(t *testing.T) {
	integer := NewProperty("i").SetType(IntValue).SetDefaultValue(0)
	integer.SetValue("12abc")
	if 12 != integer.Value() {
		t.Errorf("INT = %v, want 12 (intval)", integer.Value())
	}

	boolean := NewProperty("b").SetType(BoolValue)
	boolean.SetValue("0")
	if false != boolean.Value() {
		t.Errorf("BOOL('0') = %v, want false", boolean.Value())
	}

	selectProperty := NewProperty("s").SetType(SelectValue).
		SetDefaultValue([]Option{{ID: "a", Name: "A"}})
	selectProperty.SetValue("b")
	if nil != selectProperty.Value() {
		t.Errorf("SELECT(unknown) = %v, want nil", selectProperty.Value())
	}
	selectProperty.SetValue("a")
	if "a" != selectProperty.Value() {
		t.Errorf("SELECT(a) = %v", selectProperty.Value())
	}

	selection := NewProperty("l").SetType(SelectionValue).SetDefaultValue([]string{"x", "y"})
	selection.SetValue("y")
	if "y" != selection.Value() {
		t.Errorf("SELECTION(y) = %v", selection.Value())
	}

	// GROUP has no value at all.
	group := NewProperty("g").SetType(GroupValue)
	group.SetValue("ignored")
	if nil != group.Value() {
		t.Errorf("GROUP = %v, want nil", group.Value())
	}
}

func TestValidateWildcardValues(t *testing.T) {
	// PHP also reports the matched entry for an exact match.
	if matched, found := ValidateWildcardValues("user@example.com", "user@example.com"); !matched ||
		"user@example.com" != found {
		t.Errorf("exact match = %v, %q", matched, found)
	}
	if matched, found := ValidateWildcardValues("user@example.com", "*@example.com"); !matched ||
		"*@example.com" != found {
		t.Errorf("wildcard match = %v, %q", matched, found)
	}
	if matched, _ := ValidateWildcardValues("user@other.com", "*@example.com"); matched {
		t.Error("a non matching address must be refused")
	}
	// An empty wildcard list allows everything.
	if matched, _ := ValidateWildcardValues("anything", ""); !matched {
		t.Error("an empty wildcard list must allow the value")
	}
}
