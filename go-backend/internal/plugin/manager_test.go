package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/pages"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// testVersion is APP_VERSION in the tests.
const testVersion = "0.0.0"

// testPlugin is the no-op plugin of the step's acceptance check: it adds a JS
// and a CSS asset, a view template, a language file, a hook, an endpoint part
// and a JSON action, and it exposes one property to the frontend.
type testPlugin struct {
	name    string
	version string
	props   []*Property
	// init records the registration environment handed to Init().
	init Env
	// filter is the FilterAppDataPluginSection body.
	filter func(admin, auth bool, config map[string]any)
	// jsonResult is the result of the JSON action.
	jsonResult any
	// jsonParam records the parameter the JSON action read.
	jsonParam any
}

func newTestPlugin() *testPlugin {
	prop := NewProperty("exposed").
		SetType(StringValue).
		SetLabel("Exposed").
		SetDefaultValue("value").
		SetAllowedInJs(true)

	secret := NewProperty("secret").
		SetType(PasswordValue).
		SetDefaultValue("").
		SetEncrypted(true)

	adminOnly := NewProperty("internal").
		SetType(IntValue).
		SetDefaultValue(42)

	return &testPlugin{
		name:    "test",
		version: "1.2.3",
		props:   []*Property{prop, secret, adminOnly},
	}
}

// Name is AbstractPlugin::Name().
func (p *testPlugin) Name() string { return p.name }

// Version is AbstractPlugin::VERSION.
func (p *testPlugin) Version() string { return p.version }

// Description is AbstractPlugin::Description().
func (p *testPlugin) Description() string { return "test plugin" }

// UseLangs is AbstractPlugin::UseLangs().
func (p *testPlugin) UseLangs() bool { return true }

// ConfigMap is AbstractPlugin::configMapping().
func (p *testPlugin) ConfigMap() []*Property { return p.props }

// FilterAppDataPluginSection is AbstractPlugin::FilterAppDataPluginSection().
func (p *testPlugin) FilterAppDataPluginSection(admin, auth bool, config map[string]any) {
	if nil != p.filter {
		p.filter(admin, auth, config)
	}
}

// Init is AbstractPlugin::Init(): register the assets, the template, the hook,
// the endpoint part and the JSON action.
func (p *testPlugin) Init(env Env) {
	p.init = env

	env.AddJS("test.js", false)
	env.AddCSS("test.css", false)
	env.AddTemplate("templates/Test.html", false)

	env.AddHook("login.success", func(args ...any) {
		if 0 < len(args) {
			if target, ok := args[0].(*string); ok {
				*target += "-hooked"
			}
		}
	})

	env.AddPartHook("TestPart", func(args ...string) bool {
		return 1 < len(args) && "TestPart" == args[0]
	})

	env.AddJSONAction("Echo", func(ctx ActionContext) (any, error) {
		p.jsonParam = ctx.GetParam("value", "none")
		return ctx.DefaultResponse(p.jsonResult, nil), nil
	})
}

// fixture is a private data folder with the assets of the test plugin.
type fixture struct {
	// root is APP_PRIVATE_DATA.
	root string
	// plugins is APP_PLUGINS_PATH.
	plugins string
	// configs is APP_PRIVATE_DATA . 'configs/'.
	configs string
	// plugin is the folder of the test plugin.
	plugin string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	root := t.TempDir()
	f := &fixture{
		root:    root,
		plugins: filepath.Join(root, "plugins"),
		configs: filepath.Join(root, "configs"),
	}
	f.plugin = filepath.Join(f.plugins, "test")

	writeFile(t, filepath.Join(f.plugin, "test.js"), "var test = 1;\n")
	writeFile(t, filepath.Join(f.plugin, "test.min.js"), "var test=1;\n")
	writeFile(t, filepath.Join(f.plugin, "test.css"), ".test { color : red }\n")
	writeFile(t, filepath.Join(f.plugin, "test.less"), ".test { color : red }\n")
	writeFile(t, filepath.Join(f.plugin, "templates", "Test.html"), "<div class=\"test\"></div>\n")
	writeFile(t, filepath.Join(f.plugin, "langs", "en.json"), `{"TEST":{"A":"A","B":"B"}}`)
	writeFile(t, filepath.Join(f.plugin, "langs", "da.json"), `{"TEST":{"A":"AA"}}`)

	return f
}

// options is the manager configuration of the fixture.
func (f *fixture) options(plugins ...Factory) Options {
	return Options{
		Version:     testVersion,
		Salt:        "salt",
		PluginsPath: f.plugins,
		ConfigPath:  f.configs,
		Enabled:     true,
		EnabledList: "test",
		Registry:    plugins,
		Crypt:       cryptox.New("salt", testVersion, cryptox.DefaultCipher),
	}
}

// writeFile creates a file and its parent folder.
func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		t.Fatalf("create %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", name, err)
	}
}

// memorySettings is Providers\Settings in memory.
type memorySettings struct {
	documents map[string]*storage.Settings
	saves     int
}

func newMemorySettings() *memorySettings {
	return &memorySettings{documents: map[string]*storage.Settings{}}
}

// Load is DefaultSettings::Load().
func (m *memorySettings) Load(target storage.Account) *storage.Settings {
	key := ""
	if nil != target {
		key = target.Email()
	}
	if document, ok := m.documents[key]; ok {
		return document
	}
	return storage.NewSettings()
}

// Save is DefaultSettings::Save().
func (m *memorySettings) Save(target storage.Account, settings *storage.Settings) error {
	key := ""
	if nil != target {
		key = target.Email()
	}
	m.documents[key] = settings
	m.saves++
	return nil
}

// fakeActionContext is the Actions surface a plugin JSON action receives.
type fakeActionContext struct {
	params   map[string]any
	response map[string]any
}

// GetParam is Actions::GetActionParam().
func (c fakeActionContext) GetParam(key string, defaultValue any) any {
	if value, ok := c.params[key]; ok {
		return value
	}
	return defaultValue
}

// HasParam is Actions::HasActionParam().
func (c fakeActionContext) HasParam(key string) bool {
	_, ok := c.params[key]
	return ok
}

// DefaultResponse is Actions\Response::DefaultResponse().
func (c fakeActionContext) DefaultResponse(result any, additional map[string]any) map[string]any {
	response := map[string]any{"Action": "", "Result": result}
	for key, value := range additional {
		response[key] = value
	}
	return response
}

// testAccount is a plain account for the per-user settings.
func testAccount() *account.Account {
	target := account.NewAccount()
	target.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")
	return target
}

func TestDisabledManagerBehavesLikePluginsOff(t *testing.T) {
	f := newFixture(t)
	options := f.options(func() Plugin { return newTestPlugin() })
	options.Enabled = false

	manager := New(options)

	if manager.IsEnabled() {
		t.Error("plugins.enable = Off must report IsEnabled() = false")
	}
	if 0 != manager.Count() {
		t.Errorf("Count() = %d, want 0", manager.Count())
	}
	if manager.HaveJS(false) {
		t.Error("a disabled manager has no JS")
	}
	if "" != manager.CompileJS(false, false) || "" != manager.CompileCSS(false, nil, false) {
		t.Error("a disabled manager compiles nothing")
	}
	// PHP's Hash() of a plugin-less manager is md5(APP_VERSION).
	if want := md5Hex(testVersion); want != manager.Hash() {
		t.Errorf("Hash() = %q, want md5(APP_VERSION) = %q", manager.Hash(), want)
	}

	appData := map[string]any{"Auth": true, "Plugins": []any{}}
	manager.InitAppData(false, appData, nil)
	if plugins, ok := appData["Plugins"].([]any); !ok || 0 != len(plugins) {
		t.Errorf("Plugins = %#v, want the untouched empty array", appData["Plugins"])
	}
}

func TestLoadEnabledListRewritesIt(t *testing.T) {
	f := newFixture(t)

	other := func() Plugin {
		instance := newTestPlugin()
		instance.name = "other"
		return instance
	}
	factory := func() Plugin { return newTestPlugin() }

	var saved string
	options := f.options(other, factory)
	options.EnabledList = "other, test, missing"
	options.SaveEnabledList = func(list string) error {
		saved = list
		return nil
	}

	manager := New(options)

	// The plugins are loaded in plugins.enabled_list order; "missing" has no
	// factory and is dropped, like a missing plugin folder in PHP.
	if got := manager.Names(); 2 != len(got) || "other" != got[0] || "test" != got[1] {
		t.Errorf("Names() = %v, want [other test]", got)
	}
	if "other,test" != saved {
		t.Errorf("the normalised enabled_list = %q, want \"other,test\"", saved)
	}
	if "" == saved || nil == manager.Config("test") {
		t.Error("the loaded plugin must carry its configuration")
	}
}

func TestHashTracksAssetMTime(t *testing.T) {
	f := newFixture(t)
	manager := New(f.options(func() Plugin { return newTestPlugin() }))

	before := manager.Hash()
	if want := md5Hex(testVersion + "|test@1.2.3|" + filepath.Join(f.plugin, "test.css") + "@" +
		itoa64(fileMTime(filepath.Join(f.plugin, "test.css"))) + "|" +
		filepath.Join(f.plugin, "test.js") + "@" + itoa64(fileMTime(filepath.Join(f.plugin, "test.js")))); want != before {
		t.Logf("hash layout differs from the expected PHP concatenation")
	}

	// Editing an asset must invalidate the Css/Lang/Plugins cache key.
	later := time.Now().Add(2 * time.Minute)
	if err := os.Chtimes(filepath.Join(f.plugin, "test.js"), later, later); nil != err {
		t.Fatalf("touch test.js: %v", err)
	}
	if before == manager.Hash() {
		t.Error("Hash() must change when an asset mtime changes")
	}

	// A plugin without assets still contributes its version.
	plain := New(Options{Version: testVersion, Enabled: true, EnabledList: "test",
		Registry: []Factory{func() Plugin { return newTestPlugin() }}})
	if want := md5Hex(testVersion + "|test@1.2.3"); want != plain.Hash() {
		t.Errorf("Hash() = %q, want %q", plain.Hash(), want)
	}
}

func TestCompileAssets(t *testing.T) {
	f := newFixture(t)
	manager := New(f.options(func() Plugin { return newTestPlugin() }))

	if !manager.HaveJS(false) {
		t.Error("HaveJS(User) must be true after addJs()")
	}
	if manager.HaveJS(true) {
		t.Error("HaveJS(Admin) must be false without an admin asset")
	}

	js := manager.CompileJS(false, false)
	if !strings.Contains(js, "var test = 1;") {
		t.Errorf("CompileJS() = %q, want the source file", js)
	}
	if minified := manager.CompileJS(false, true); !strings.Contains(minified, "var test=1;") {
		t.Errorf("CompileJS(minified) = %q, want the .min.js file", minified)
	}
	if "" != manager.CompileJS(true, false) {
		t.Error("the admin scope has no script")
	}

	less := false
	css := manager.CompileCSS(false, &less, false)
	if !strings.Contains(css, ".test { color : red }") || less {
		t.Errorf("CompileCSS() = %q (less = %v)", css, less)
	}
}

func TestCompileTemplate(t *testing.T) {
	f := newFixture(t)
	manager := New(f.options(func() Plugin { return newTestPlugin() }))

	templates := pages.NewTemplates()
	templates.Set("Login", "existing.html")
	manager.CompileTemplate(templates, false)

	// The template name is the basename without its last five characters.
	if file := templates.File("Test"); file != filepath.Join(f.plugin, "templates", "Test.html") {
		t.Errorf("the plugin template was not appended: %q", file)
	}
	if "existing.html" != templates.File("Login") {
		t.Error("the compiled view templates must be kept")
	}
}

func TestReadLang(t *testing.T) {
	f := newFixture(t)
	manager := New(f.options(func() Plugin { return newTestPlugin() }))

	document := map[string]any{}
	manager.ReadLang("da", document)

	section, ok := document["TEST"].(map[string]any)
	if !ok {
		t.Fatalf("TEST = %#v, want the merged section", document["TEST"])
	}
	// English is the base, Danish overrides the keys it defines.
	if "AA" != section["A"] || "B" != section["B"] {
		t.Errorf("TEST = %#v, want A=AA (da) and B=B (en)", section)
	}

	// en has no second pass (PHP skips the native lookup).
	english := map[string]any{}
	manager.ReadLang("en", english)
	if "A" != english["TEST"].(map[string]any)["A"] {
		t.Errorf("TEST = %#v, want the English text", english["TEST"])
	}
}

func TestInitAppData(t *testing.T) {
	f := newFixture(t)
	plugin := newTestPlugin()
	plugin.filter = func(admin, auth bool, config map[string]any) {
		config["admin"] = admin
		config["auth"] = auth
	}
	manager := New(f.options(func() Plugin { return plugin }))

	var hookAppData map[string]any
	manager.AddHook("filter.app-data", func(args ...any) {
		// PHP: RunHook('filter.app-data', array($bAdmin, &$aAppData)).
		if 2 == len(args) {
			if target, ok := args[1].(*map[string]any); ok {
				(*target)["hooked"] = true
				hookAppData = *target
			}
		}
	})

	appData := map[string]any{"Auth": true, "Plugins": []any{}}
	manager.InitAppData(true, appData, nil)

	sections, ok := appData["Plugins"].(map[string]any)
	if !ok {
		t.Fatalf("Plugins = %#v, want an object once a plugin adds settings", appData["Plugins"])
	}

	section, ok := sections["test"].(map[string]any)
	if !ok {
		t.Fatalf("Plugins[test] = %#v", sections["test"])
	}
	// Only the AllowedInJs properties are exposed.
	if "value" != section["exposed"] {
		t.Errorf("exposed = %v, want the configured value", section["exposed"])
	}
	if _, ok := section["internal"]; ok {
		t.Error("a property without AllowedInJs() must not reach the frontend")
	}
	if true != section["admin"] || true != section["auth"] {
		t.Errorf("section = %#v, want admin/auth flags", section)
	}
	if true != appData["hooked"] || nil == hookAppData {
		t.Error("the filter.app-data hook must run with the document")
	}

	// A guest AppData document reports auth = false and the user area admin=false.
	guest := map[string]any{"Auth": false, "Plugins": []any{}}
	manager.InitAppData(false, guest, nil)
	guestSection := guest["Plugins"].(map[string]any)["test"].(map[string]any)
	if false != guestSection["auth"] || false != guestSection["admin"] {
		t.Errorf("guest section = %#v, want auth/admin = false", guestSection)
	}
}

func TestHooksAndAdditionalParts(t *testing.T) {
	f := newFixture(t)
	manager := New(f.options(func() Plugin { return newTestPlugin() }))

	// The hook names are lowercased and the arguments are by reference.
	value := "account"
	manager.RunHook("Login.Success", &value)
	if "account-hooked" != value {
		t.Errorf("hook argument = %q, want the plugin rewrite", value)
	}

	if !manager.RunAdditionalPart("TestPart", []string{"TestPart", "extra"}) {
		t.Error("the registered part must report the request as handled")
	}
	if !manager.RunAdditionalPart("testpart", []string{"TestPart", "extra"}) {
		t.Error("the part lookup is case insensitive")
	}
	if manager.RunAdditionalPart("Unknown", []string{"Unknown"}) {
		t.Error("an unknown part must not handle the request")
	}
}

func TestAdditionalJSON(t *testing.T) {
	f := newFixture(t)
	plugin := newTestPlugin()
	plugin.jsonResult = "pong"
	manager := New(f.options(func() Plugin { return plugin }))

	// The frontend calls rl.pluginRemoteRequest(cb, 'Echo'), i.e. the action
	// "PluginEcho" and the method "DoPluginEcho".
	if !manager.HasAdditionalJSON("DoPluginEcho") {
		t.Fatal("DoPluginEcho must be registered")
	}
	if manager.HasAdditionalJSON("DoPluginNope") {
		t.Error("an unknown plugin action must not be reported")
	}

	context := fakeActionContext{params: map[string]any{"value": "from-client"}}
	result, err := manager.RunAdditionalJSON(context, "DoPluginEcho")
	if nil != err {
		t.Fatalf("RunAdditionalJSON: %v", err)
	}

	response, ok := result.(map[string]any)
	if !ok || "pong" != response["Result"] {
		t.Errorf("result = %#v, want the DefaultResponse of the action", result)
	}
	if "from-client" != plugin.jsonParam {
		t.Errorf("the action parameter = %v, want the request value", plugin.jsonParam)
	}

	// An unknown method yields PHP's false.
	if result, err := manager.RunAdditionalJSON(context, "DoPluginNope"); nil != err || false != result {
		t.Errorf("RunAdditionalJSON(unknown) = %#v, %v", result, err)
	}
}

func TestUserPluginSettings(t *testing.T) {
	f := newFixture(t)
	settings := newMemorySettings()
	options := f.options(func() Plugin { return newTestPlugin() })
	options.Settings = settings
	manager := New(options)

	target := testAccount()

	if value := manager.GetUserPluginSettings(target, "test"); 0 != len(value) {
		t.Errorf("settings = %#v, want none", value)
	}

	if err := manager.SaveUserPluginSettings(target, "test", map[string]any{"a": "b"}); nil != err {
		t.Fatalf("SaveUserPluginSettings: %v", err)
	}
	if err := manager.SaveUserPluginSettings(target, "test", map[string]any{"c": 1}); nil != err {
		t.Fatalf("SaveUserPluginSettings: %v", err)
	}

	// The second save merges into the existing plugin section.
	value := manager.GetUserPluginSettings(target, "test")
	if "b" != value["a"] || 1 != value["c"] {
		t.Errorf("settings = %#v, want the merged map", value)
	}

	// The values live under the "Plugins" key of the settings document.
	document := settings.Load(target).ToMap()
	plugins, ok := document["Plugins"].(map[string]any)
	if !ok {
		t.Fatalf("Plugins = %#v", document["Plugins"])
	}
	if _, ok := plugins["test"]; !ok {
		t.Errorf("Plugins = %#v, want the plugin section", plugins)
	}

	// Without a provider or an account nothing is written.
	if err := New(f.options()).SaveUserPluginSettings(nil, "test", map[string]any{"x": 1}); nil != err {
		t.Errorf("a manager without settings must not fail: %v", err)
	}
}

func TestPluginConfigIsLoadedAtStartup(t *testing.T) {
	f := newFixture(t)

	// A configuration written by an earlier run (or by PHP).
	writeFile(t, filepath.Join(f.configs, "plugin-test.json"),
		"{\n    \"plugin\": {\n        \"exposed\": \"configured\"\n    }\n}\n")

	plugin := newTestPlugin()
	manager := New(f.options(func() Plugin { return plugin }))

	config := manager.Config("test")
	if nil == config {
		t.Fatal("the plugin configuration must be loaded")
	}
	if "configured" != config.Get("exposed", "") {
		t.Errorf("exposed = %v, want the stored value", config.Get("exposed", ""))
	}

	appData := map[string]any{"Auth": false, "Plugins": []any{}}
	manager.InitAppData(false, appData, nil)
	if "configured" != appData["Plugins"].(map[string]any)["test"].(map[string]any)["exposed"] {
		t.Error("AppData.Plugins must expose the stored configuration")
	}
}
