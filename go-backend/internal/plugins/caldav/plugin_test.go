package caldav

// The step 29 acceptance checks: the plugin metadata and configuration mapping,
// the "calendar_sync" blob AutoConfigureCalDAV() writes (through the real
// plugin.Manager, so the hook registration is exercised too), and the
// translations of the plugin's own langs/ folder.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/handler"
	"github.com/xi0/snappymail/go-backend/internal/plugin"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

const (
	// testVersion is APP_VERSION in the tests.
	testVersion = "0.0.0"
	// testSalt is APP_SALT in the tests.
	testSalt = "test-salt"
)

// testActions is the request scoped \RainLoop\Actions surface a plugin action
// receives (Actions).
type testActions struct {
	params   map[string]any
	current  *account.Account
	main     *account.MainAccount
	env      *account.Context
	language string
}

// GetParam is Actions::GetActionParam().
func (a *testActions) GetParam(key string, defaultValue any) any {
	if value, ok := a.params[key]; ok && nil != value {
		return value
	}
	return defaultValue
}

// HasParam is Actions::HasActionParam().
func (a *testActions) HasParam(key string) bool {
	_, ok := a.params[key]
	return ok
}

// DefaultResponse is Actions\Response::DefaultResponse().
func (a *testActions) DefaultResponse(result any, additional map[string]any) map[string]any {
	response := map[string]any{"Action": "", "Result": result}
	for key, value := range additional {
		response[key] = value
	}
	return response
}

// Account is Actions::getAccountFromToken().
func (a *testActions) Account(throw bool) (*account.Account, error) {
	if nil == a.current && throw {
		return nil, errNoAccount
	}
	return a.current, nil
}

// MainAccount is Actions::getMainAccountFromToken().
func (a *testActions) MainAccount(bool) (*account.MainAccount, error) {
	return a.main, nil
}

// AccountContext is the provider environment of the request.
func (a *testActions) AccountContext() *account.Context { return a.env }

// Language is Actions::GetLanguage().
func (a *testActions) Language() string { return a.language }

// errNoAccount is the "there is no account" failure of the fake actions.
var errNoAccount = accountError("account undefined")

// accountError is a minimal error for the fake actions.
type accountError string

// Error implements error.
func (e accountError) Error() string { return string(e) }

// testEnv is the plugin.Env the helper functions of the package receive. The
// registration methods are only used by Init(), so they stay nil here.
type testEnv struct {
	plugin.Env
	path   string
	config *plugin.Config
}

// Path is AbstractPlugin::Path().
func (e testEnv) Path() string { return e.path }

// Config is AbstractPlugin::Config().
func (e testEnv) Config() *plugin.Config { return e.config }

// fixture is a private data folder plus the state the plugin runs against.
type fixture struct {
	// root is APP_PRIVATE_DATA.
	root string
	// plugins is APP_PLUGINS_PATH.
	plugins string
	// configs is APP_PRIVATE_DATA . 'configs/'.
	configs string
	// pluginDir is the folder of the caldav plugin.
	pluginDir string
	// storage is the shared (non local) storage provider.
	storage *storage.Provider
	// crypter is SnappyMail\Crypt with APP_SALT/APP_VERSION.
	crypter *cryptox.Crypter
	// env is the account Context of the request.
	env *account.Context
	// main is the logged in main account.
	main *account.MainAccount
	// instance is the plugin the manager built.
	instance *Plugin
	// manager is the real plugin manager.
	manager *plugin.Manager
}

// newFixture builds the private data folder, the plugin languages and the plugin
// manager. configure may add settings to configs/plugin-caldav.json (e.g. the
// caldav_server of the mock server) before the manager loads it.
func newFixture(t *testing.T, configure func(config map[string]any)) *fixture {
	t.Helper()

	root := t.TempDir()

	f := &fixture{
		root:      root,
		plugins:   filepath.Join(root, "plugins"),
		configs:   filepath.Join(root, "configs"),
		pluginDir: filepath.Join(root, "plugins", "caldav"),
		crypter:   cryptox.New(testSalt, testVersion, cryptox.DefaultCipher),
	}

	f.storage = storage.NewProvider(storage.NewFileStorage(filepath.Join(root, "storage"), false))
	f.env = &account.Context{Salt: testSalt, Storage: f.storage, Crypt: f.crypter}

	f.main = account.NewMainAccount()
	f.main.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")

	// The plugin assets: only the translations matter for step 29.
	writeTestFile(t, filepath.Join(f.pluginDir, "langs", "en.json"),
		`{"CALDAV":{"CALENDAR":"Calendar","ERROR_NOT_LOGGED_IN":"Please log in first",`+
			`"ERROR_NOT_CONFIGURED_MSG":"Calendar not configured yet. Please check settings.",`+
			`"ERROR_NO_ENCRYPTION_KEY":"Cannot access encryption key"}}`)
	writeTestFile(t, filepath.Join(f.pluginDir, "langs", "da.json"),
		`{"CALDAV":{"ERROR_NOT_LOGGED_IN":"Log venligst ind foerst"}}`)

	config := map[string]any{}
	if nil != configure {
		configure(config)
	}
	if 0 < len(config) {
		encoded, err := json.Marshal(map[string]any{"plugin": config})
		if nil != err {
			t.Fatalf("encode plugin-caldav.json: %v", err)
		}
		writeTestFile(t, filepath.Join(f.configs, "plugin-caldav.json"), string(encoded)+"\n")
	}

	f.instance = &Plugin{}
	f.manager = plugin.New(plugin.Options{
		Version:     testVersion,
		Salt:        testSalt,
		PluginsPath: f.plugins,
		ConfigPath:  f.configs,
		Enabled:     true,
		EnabledList: name,
		Registry:    []plugin.Factory{func() plugin.Plugin { return f.instance }},
		Crypt:       f.crypter,
	})

	return f
}

// writeTestFile creates a file and its parent folder.
func writeTestFile(t *testing.T, name, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		t.Fatalf("create %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", name, err)
	}
}

// actions builds the request surface of the fixture for an account.
func (f *fixture) actions(current *account.Account) *testActions {
	return &testActions{
		params:  map[string]any{},
		current: current,
		main:    f.main,
		env:     f.env,
	}
}

// pluginEnv is the registration environment of the fixture: the plugin folder and
// the configuration document the manager loaded.
func (f *fixture) pluginEnv(t *testing.T) plugin.Env {
	t.Helper()

	config := f.manager.Config(name)
	if nil == config {
		t.Fatal("the manager did not load the plugin configuration")
	}

	return testEnv{path: f.pluginDir, config: config}
}

// calendarSyncDocument reads and decodes the "calendar_sync" blob of an account.
func (f *fixture) calendarSyncDocument(t *testing.T, target any) map[string]any {
	t.Helper()

	document, ok := f.storage.Get(target, storage.TypeConfig, calendarSyncKey)
	if !ok || "" == document {
		t.Fatalf("the calendar_sync blob of %v is missing", target)
	}

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(document), &decoded); nil != err {
		t.Fatalf("calendar_sync is not JSON (%v): %s", err, document)
	}
	return decoded
}

func TestPluginMetadata(t *testing.T) {
	// The dispatcher hands its request context (*handler.Context) to the plugin
	// actions; this fails to compile when that surface drifts.
	var _ Actions = (*handler.Context)(nil)

	instance := New()

	if name != instance.Name() {
		t.Errorf("Name() = %q, want the plugin id %q", instance.Name(), name)
	}

	custom, ok := instance.(*Plugin)
	if !ok {
		t.Fatalf("New() = %T, want *Plugin", instance)
	}
	if displayName != custom.DisplayName() {
		t.Errorf("DisplayName() = %q", custom.DisplayName())
	}
	if "1.19" != custom.Version() {
		t.Errorf("Version() = %q, want 1.19", custom.Version())
	}
	if "2026-02-01" != custom.Release() {
		t.Errorf("Release() = %q", custom.Release())
	}
	if "Calendar" != custom.Category() {
		t.Errorf("Category() = %q", custom.Category())
	}
	if "2.0.0" != custom.Required() {
		t.Errorf("Required() = %q", custom.Required())
	}
	if !strings.Contains(custom.Description(), "CalDAV") {
		t.Errorf("Description() = %q", custom.Description())
	}
	// AbstractPlugin::UseLangs(true) in Init().
	if !custom.UseLangs() {
		t.Error("UseLangs() must be true")
	}
}

func TestConfigMapMatchesThePHPDeclaration(t *testing.T) {
	properties := (&Plugin{}).ConfigMap()

	want := []struct {
		name         string
		typ          plugin.PropertyType
		defaultValue any
	}{
		{"caldav_server", plugin.StringValue, "https://my.mailbux.com/dav/cal"},
		{"jmap_server", plugin.StringValue, "https://my.mailbux.com/jmap"},
		{"default_protocol", plugin.SelectionValue, "caldav"},
		{"auto_sync", plugin.BoolValue, true},
		{"sync_interval", plugin.IntValue, 5},
		{"allow_invites", plugin.BoolValue, true},
	}

	if len(want) != len(properties) {
		t.Fatalf("ConfigMap() has %d properties, want %d", len(properties), len(want))
	}

	for index, expected := range want {
		property := properties[index]
		if expected.name != property.Name() {
			t.Errorf("property %d name = %q, want %q", index, property.Name(), expected.name)
		}
		if expected.typ != property.Type() {
			t.Errorf("%s type = %d, want %d", expected.name, property.Type(), expected.typ)
		}
		if expected.defaultValue != property.DefaultValue() {
			t.Errorf("%s default = %#v, want %#v", expected.name, property.DefaultValue(), expected.defaultValue)
		}
		if "" == property.Label() {
			t.Errorf("%s has no label", expected.name)
		}
	}

	// The SELECTION values of default_protocol (SetOptions() in PHP).
	protocol := properties[2]
	protocol.SetValue("jmap")
	if "jmap" != protocol.Value() {
		t.Errorf("default_protocol(jmap) = %v", protocol.Value())
	}
	protocol.SetValue("other")
	if nil != protocol.Value() {
		t.Errorf("default_protocol(other) = %v, want nil", protocol.Value())
	}
}

func TestInitRegistersTheStep29Surface(t *testing.T) {
	f := newFixture(t, nil)

	// The JSON action: rl.pluginRemoteRequest(cb, 'GetCalendars') addresses
	// "PluginGetCalendars" and the method "DoPluginGetCalendars".
	if !f.manager.HasAdditionalJSON("DoPluginGetCalendars") {
		t.Error("DoPluginGetCalendars must be registered")
	}

	// The hooks are recorded by the manager itself; run them to prove the
	// registration points at the plugin (a missing hook is a silent no-op, so
	// the AutoConfigure effect is what is asserted here).
	f.manager.RunHook("login.success", f.main, f.env)

	if _, ok := f.storage.Get(f.main, storage.TypeConfig, calendarSyncKey); !ok {
		t.Error("login.success must auto-configure the calendar_sync blob")
	}
}

func TestAutoConfigureWritesTheCalendarSyncBlob(t *testing.T) {
	f := newFixture(t, func(config map[string]any) {
		config["caldav_server"] = "https://dav.example.com/cal/"
	})

	f.manager.RunHook("login.success", f.main, f.env)

	document := f.calendarSyncDocument(t, f.main)

	// The key set and the values of AutoConfigureCalDAV().
	if len(document) != 5 {
		t.Errorf("calendar_sync = %#v, want the five document keys", document)
	}
	if 1.0 != document["Mode"] {
		t.Errorf("Mode = %v, want 1", document["Mode"])
	}
	if "user@example.com" != document["User"] {
		t.Errorf("User = %v", document["User"])
	}
	// rtrim($sServer, '/') . '/' . $sEmail
	if "https://dav.example.com/cal/user@example.com" != document["Url"] {
		t.Errorf("Url = %v", document["Url"])
	}

	// The password is a Crypt blob of the IMAP password, sealed with the main
	// account's crypt key, and the HMAC signs the sealed value with that key.
	cryptKey, err := f.main.CryptKey(f.env)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}
	sealed := phpString(document["Password"])
	if "" == sealed || strings.Contains(sealed, "secret") {
		t.Fatalf("Password = %q, want a sealed blob", sealed)
	}
	plain, err := f.crypter.DecryptFromJSON(sealed, cryptKey)
	if nil != err {
		t.Fatalf("the sealed password does not decrypt: %v", err)
	}
	if "secret" != phpString(plain) {
		t.Errorf("Password decrypts to %v, want the IMAP password", plain)
	}
	if want := account.HMAC(sealed, cryptKey); want != phpString(document["PasswordHMAC"]) {
		t.Errorf("PasswordHMAC = %v, want %q", document["PasswordHMAC"], want)
	}

	// The stored document uses the PHP key order and its escaped slashes.
	raw, _ := f.storage.Get(f.main, storage.TypeConfig, calendarSyncKey)
	order := []string{`"Mode":`, `,"User":`, `,"Password":`, `,"PasswordHMAC":`, `,"Url":`}
	position := -1
	for _, key := range order {
		found := strings.Index(raw, key)
		if 0 > found || found < position {
			t.Errorf("calendar_sync key %s is missing or out of order: %s", key, raw)
		}
		position = found
	}
	if !strings.Contains(raw, `\/`) {
		t.Errorf("json_encode() escaping is missing (\\/): %s", raw)
	}
}

func TestAutoConfigureHonoursAutoSyncOff(t *testing.T) {
	f := newFixture(t, func(config map[string]any) {
		config["auto_sync"] = false
	})

	f.manager.RunHook("login.success", f.main, f.env)

	if _, ok := f.storage.Get(f.main, storage.TypeConfig, calendarSyncKey); ok {
		t.Error("auto_sync = false must not write the calendar_sync blob")
	}
}

func TestAutoConfigureWithoutLoggedInAccount(t *testing.T) {
	f := newFixture(t, nil)

	// PHP: if (!$oAccount || !$oAccount->Email()) return;
	f.manager.RunHook("login.success", account.NewMainAccount(), f.env)

	if _, ok := f.storage.Get(f.main, storage.TypeConfig, calendarSyncKey); ok {
		t.Error("an account without an address must not be configured")
	}
}

func TestAutoConfigureAdditionalAccount(t *testing.T) {
	f := newFixture(t, nil)

	cryptKey, err := f.main.CryptKey(f.env)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// The additional account list of Actions\GetAccounts(): the password is a
	// Crypt::EncryptUrlSafe() blob sealed with the main crypt key.
	sealed, err := f.crypter.EncryptURLSafe("add-secret", cryptKey)
	if nil != err {
		t.Fatalf("EncryptURLSafe: %v", err)
	}
	document, err := json.Marshal(map[string]any{
		"add@example.com": map[string]any{
			"email": "add@example.com",
			"login": "add@example.com",
			"pass":  sealed,
		},
	})
	if nil != err {
		t.Fatalf("encode additionalaccounts: %v", err)
	}
	if err := f.storage.Put(f.main, storage.TypeConfig, additionalAccountsKey, string(document)); nil != err {
		t.Fatalf("store additionalaccounts: %v", err)
	}

	additional := account.NewAdditionalAccount()
	additional.SetCredentials(nil, "add@example.com", "add@example.com", "ignored", "", "")
	additional.SetParentEmail("user@example.com")

	// The account switch selects the additional account, so the hook sees it as
	// getAccountFromToken().
	response := map[string]any{"Result": map[string]any{"Email": "add@example.com"}}
	f.manager.RunHook("json.after-AccountSwitch", &response, f.actions(&additional.Account))

	// Providers\Storage resolves an additional account to its parent folder, so
	// the blob lives with the main account.
	stored := f.calendarSyncDocument(t, f.main)

	if "add@example.com" != stored["User"] {
		t.Errorf("User = %v, want the additional address", stored["User"])
	}

	// The additional password was converted: opened with the main crypt key it
	// yields the additional account password.
	plain, err := f.crypter.DecryptFromJSON(phpString(stored["Password"]), cryptKey)
	if nil != err {
		t.Fatalf("the sealed password does not decrypt: %v", err)
	}
	if "add-secret" != phpString(plain) {
		t.Errorf("Password decrypts to %v, want the additional password", plain)
	}
	if want := account.HMAC(phpString(stored["Password"]), cryptKey); want != phpString(stored["PasswordHMAC"]) {
		t.Errorf("PasswordHMAC = %v, want %q", stored["PasswordHMAC"], want)
	}
}

func TestAccountSwitchHookIgnoresAFailedSwitch(t *testing.T) {
	f := newFixture(t, nil)

	// PHP: if (!empty($aResponse['Result'])).
	for _, response := range []map[string]any{
		{"Result": false},
		{"Result": nil},
		{},
	} {
		f.manager.RunHook("json.after-AccountSwitch", &response, f.actions(&f.main.Account))

		if _, ok := f.storage.Get(f.main, storage.TypeConfig, calendarSyncKey); ok {
			t.Errorf("a failed switch (%v) must not configure the account", response)
		}
	}
}

func TestMsgUsesThePluginTranslations(t *testing.T) {
	f := newFixture(t, nil)

	english := &request{actions: &testActions{language: "en"}}
	danish := &request{actions: &testActions{language: "da"}}

	env := f.pluginEnv(t)

	if got := msg(env, english, "ERROR_NOT_LOGGED_IN", nil, "fallback"); "Please log in first" != got {
		t.Errorf("en message = %q", got)
	}
	// Danish overrides the English value; languages without a file fall back to
	// English (da.json only translates the one key).
	if got := msg(env, danish, "ERROR_NOT_LOGGED_IN", nil, "fallback"); "Log venligst ind foerst" != got {
		t.Errorf("da message = %q", got)
	}
	if got := msg(env, danish, "CALENDAR", nil, "fallback"); "Calendar" != got {
		t.Errorf("da fallback = %q, want the English text", got)
	}

	// A key without a translation uses the default, then the key itself, and the
	// placeholders are replaced.
	if got := msg(env, english, "MISSING", map[string]string{"CODE": "405"}, "CalDAV error: %CODE%"); "CalDAV error: 405" != got {
		t.Errorf("placeholder message = %q", got)
	}
	if got := msg(env, english, "MISSING", nil, ""); "MISSING" != got {
		t.Errorf("missing message = %q, want the key", got)
	}

	// A request without actions (a hook) uses English.
	if got := msg(env, nil, "ERROR_NO_ENCRYPTION_KEY", nil, ""); "Cannot access encryption key" != got {
		t.Errorf("hook message = %q", got)
	}
}

func TestJSONQuoteMatchesPHPJsonEncode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", `"plain"`},
		{"/var/cal", `"\/var\/cal"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"line\nbreak", `"line\nbreak"`},
		{"tab\t", `"tab\t"`},
		{"\x01", `"\u0001"`},
		{"Møder", `"M\u00f8der"`},
		{"😀", `"\ud83d\ude00"`},
	}

	for _, test := range cases {
		if got := jsonQuote(test.in); test.want != got {
			t.Errorf("jsonQuote(%q) = %s, want %s", test.in, got, test.want)
		}
	}
}

func TestCalendarSyncEncodeKeepsThePHPKeyOrder(t *testing.T) {
	document := calendarSync{
		Mode:         calendarSyncMode,
		User:         "user@example.com",
		Password:     `["sodium","a","b/c"]`,
		PasswordHMAC: "0123",
		Url:          "https://dav.example.com/cal/user@example.com",
	}

	want := `{"Mode":1,"User":"user@example.com","Password":"[\"sodium\",\"a\",\"b\/c\"]",` +
		`"PasswordHMAC":"0123","Url":"https:\/\/dav.example.com\/cal\/user@example.com"}`

	if got := document.encode(); want != got {
		t.Errorf("encode() =\n%s\nwant\n%s", got, want)
	}
}
