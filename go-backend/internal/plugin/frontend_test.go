package plugin_test

// The step 28 acceptance check: a no-op plugin that adds a JS/CSS asset, a view
// template, a language file and a JSON action has to show up in the values the
// frontend consumes — AppData.PluginsLink, AppData.Plugins[<name>], the
// compiled plugin JavaScript of ?/Plugins and the merged translations of
// ?/Lang.
//
// The test lives outside the package on purpose: it wires the real manager into
// internal/service, which is what main.go does.

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/plugin"
	"github.com/xi0/snappymail/go-backend/internal/service"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

const frontendVersion = "0.0.0"

// frontendPlugin is a minimal build-time plugin.
type frontendPlugin struct{}

// Name is AbstractPlugin::Name().
func (frontendPlugin) Name() string { return "frontend" }

// Version is AbstractPlugin::VERSION.
func (frontendPlugin) Version() string { return "9.9.9" }

// Description is AbstractPlugin::Description().
func (frontendPlugin) Description() string { return "frontend fixture" }

// UseLangs is AbstractPlugin::UseLangs().
func (frontendPlugin) UseLangs() bool { return true }

// ConfigMap is AbstractPlugin::configMapping().
func (frontendPlugin) ConfigMap() []*plugin.Property {
	return []*plugin.Property{
		plugin.NewProperty("service_url").
			SetType(plugin.StringValue).
			SetDefaultValue("https://example.com/dav").
			SetAllowedInJs(true),
	}
}

// FilterAppDataPluginSection is AbstractPlugin::FilterAppDataPluginSection().
func (frontendPlugin) FilterAppDataPluginSection(bool, bool, map[string]any) {}

// Init is AbstractPlugin::Init().
func (frontendPlugin) Init(env plugin.Env) {
	env.AddJS("frontend.js", false)
	env.AddCSS("frontend.css", false)
	env.AddJSONAction("Frontend", func(ctx plugin.ActionContext) (any, error) {
		return ctx.DefaultResponse("ok", nil), nil
	})
}

// writeFixtureFile writes one file of the install tree.
func writeFixtureFile(t *testing.T, name, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		t.Fatalf("mkdir %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newFrontendFixture builds the manager and the AppData service on a temporary
// private data folder.
func newFrontendFixture(t *testing.T) (*plugin.Manager, *service.AppData) {
	t.Helper()

	private := t.TempDir()
	versionRoot := filepath.Join(private, "version")
	pluginDir := filepath.Join(private, "plugins", "frontend")

	writeFixtureFile(t, filepath.Join(pluginDir, "frontend.js"), "var frontendReady = true;\n")
	writeFixtureFile(t, filepath.Join(pluginDir, "frontend.css"), ".frontend { color : blue }\n")
	writeFixtureFile(t, filepath.Join(pluginDir, "langs", "en.json"),
		`{"FRONTEND":{"TITLE":"Frontend","OPEN":"Open"}}`)
	writeFixtureFile(t, filepath.Join(pluginDir, "langs", "da.json"),
		`{"FRONTEND":{"OPEN":"Aabn"}}`)

	pathCollection := httpx.WebPaths{Version: frontendVersion, VersionRoot: versionRoot}

	manager := plugin.New(plugin.Options{
		Version:     frontendVersion,
		Salt:        "salt",
		PluginsPath: filepath.Join(private, "plugins"),
		ConfigPath:  filepath.Join(private, "configs"),
		Enabled:     true,
		EnabledList: "frontend",
		Registry:    []plugin.Factory{func() plugin.Plugin { return frontendPlugin{} }},
		Crypt:       cryptox.New("salt", frontendVersion, cryptox.DefaultCipher),
	})

	appData := service.NewAppData(service.Options{
		Version: frontendVersion,
		Salt:    "salt",
		Paths:   pathCollection,
		Plugins: manager,
	})

	return manager, appData
}

func TestPluginReachesPluginsLinkAndAppData(t *testing.T) {
	manager, appData := newFrontendFixture(t)

	document := appData.Build(false, nil)

	// PluginsLink = Plugins/0/User/<md5(hash . cache.index . APP_VERSION)>/;
	// cache.index is empty in the fixture.
	wantLink := "Plugins/0/User/" + md5Hex(manager.Hash()+frontendVersion) + "/"
	if got := document["PluginsLink"]; wantLink != got {
		t.Errorf("PluginsLink = %v, want %q", got, wantLink)
	}

	// AppData.Plugins is an object keyed by plugin name with the AllowedInJs
	// properties.
	sections, ok := document["Plugins"].(map[string]any)
	if !ok {
		t.Fatalf("Plugins = %#v, want an object", document["Plugins"])
	}
	section, ok := sections["frontend"].(map[string]any)
	if !ok {
		t.Fatalf("Plugins[frontend] = %#v", sections["frontend"])
	}
	if "https://example.com/dav" != section["service_url"] {
		t.Errorf("service_url = %v, want the default value", section["service_url"])
	}
}

func TestPluginJavaScriptIsServed(t *testing.T) {
	_, appData := newFrontendFixture(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/Plugins/0/User/abc/", nil)
	route := &web.Route{Paths: []string{"Plugins", "0", "User", "abc"}}

	if !appData.Plugins(recorder, request, route) {
		t.Fatal("?/Plugins must be handled")
	}
	if got := recorder.Header().Get("Content-Type"); "application/javascript; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(recorder.Body.String(), "var frontendReady = true;") {
		t.Errorf("body = %q, want the compiled plugin script", recorder.Body.String())
	}
}

func TestPluginLanguageIsMerged(t *testing.T) {
	_, appData := newFrontendFixture(t)

	js, err := appData.CompileLanguage("da", false)
	if nil != err {
		t.Fatalf("CompileLanguage(da): %v", err)
	}
	// The English text is the base, Danish overrides OPEN.
	if !strings.Contains(js, `"TITLE":"Frontend"`) || !strings.Contains(js, `"OPEN":"Aabn"`) {
		t.Errorf("js = %q, want the plugin translations", js)
	}
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
