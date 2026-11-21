package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

func TestServiceLangCompilesTheLanguage(t *testing.T) {
	fixture := newFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/Lang/0/App/en/0.0.0/", nil)
	route := &web.Route{Paths: []string{"Lang", "0", "App", "en", "0.0.0"}}

	if !fixture.appData.Lang(recorder, request, route) {
		t.Fatal("Lang must report the request as handled")
	}
	if got := recorder.Header().Get("Content-Type"); "application/javascript; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, "document.documentElement.lang = 'en';") {
		t.Errorf("body = %q, want the compiled language", body)
	}
	if !strings.Contains(body, "rl.I18N=") {
		t.Errorf("body = %q, want the I18N document", body)
	}

	// Without a language segment the response is empty.
	recorder = httptest.NewRecorder()
	fixture.appData.Lang(recorder, request, &web.Route{Paths: []string{"Lang", "0", "App"}})
	if "" != recorder.Body.String() {
		t.Errorf("body = %q, want an empty language response", recorder.Body.String())
	}
}

func TestServiceCssReturnsTheTheme(t *testing.T) {
	fixture := newFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet,
		"/?/Css/0/User/-/Gazellemail/-/0/Hash/-/Json/", nil)
	route := &web.Route{Paths: []string{
		"Css", "0", "User", "-", "Gazellemail", "-", "0", "Hash", "-", "Json",
	}}

	if !fixture.appData.Css(recorder, request, route) {
		t.Fatal("Css must report the request as handled")
	}
	if got := recorder.Header().Get("Content-Type"); "application/json; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	var pair []any
	if err := json.Unmarshal(recorder.Body.Bytes(), &pair); nil != err {
		t.Fatalf("body is not JSON (%v): %s", err, recorder.Body.String())
	}
	if 2 != len(pair) {
		t.Fatalf("pair = %#v, want [theme, css]", pair)
	}
	if "Gazellemail" != pair[0] {
		t.Errorf("theme = %v, want Gazellemail", pair[0])
	}
	if _, ok := pair[1].(string); !ok {
		t.Errorf("css = %#v, want a string", pair[1])
	}

	// Without Json the theme stylesheet is served as text/css.
	recorder = httptest.NewRecorder()
	route = &web.Route{Paths: []string{"Css", "0", "User", "-", "Gazellemail", "-", "0", "Hash"}}
	fixture.appData.Css(recorder, request, route)
	if got := recorder.Header().Get("Content-Type"); "text/css; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if "" == recorder.Body.String() {
		t.Error("the stylesheet must not be empty")
	}

	// An unknown theme falls back to the configured default.
	recorder = httptest.NewRecorder()
	route = &web.Route{Paths: []string{"Css", "0", "User", "-", "Missing", "-", "0", "Hash", "-", "Json"}}
	fixture.appData.Css(recorder, request, route)
	if err := json.Unmarshal(recorder.Body.Bytes(), &pair); nil != err {
		t.Fatalf("body is not JSON (%v): %s", err, recorder.Body.String())
	}
	if "Gazellemail" != pair[0] {
		t.Errorf("theme = %v, want the Gazellemail fallback", pair[0])
	}
}

func TestServicePluginsWithoutPlugins(t *testing.T) {
	fixture := newFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/Plugins/0/User/abc/", nil)
	route := &web.Route{Paths: []string{"Plugins", "0", "User", "abc"}}

	if !fixture.appData.Plugins(recorder, request, route) {
		t.Fatal("Plugins must report the request as handled")
	}
	if got := recorder.Header().Get("Content-Type"); "application/javascript; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if "" != recorder.Body.String() {
		t.Errorf("body = %q, want nothing without plugins", recorder.Body.String())
	}
}

func TestValidateLanguageAndStaticI18N(t *testing.T) {
	fixture := newFixture(t)

	if "en" != fixture.appData.ValidateLanguage("en", false) {
		t.Errorf("ValidateLanguage(en) = %q", fixture.appData.ValidateLanguage("en", false))
	}
	// An unknown language falls back to the configured default.
	if "en" != fixture.appData.ValidateLanguage("xx", false) {
		t.Errorf("ValidateLanguage(xx) = %q, want the default en", fixture.appData.ValidateLanguage("xx", false))
	}

	// The test tree holds empty JSON documents, so StaticI18N returns the key.
	if "BACK_LINK" != fixture.appData.StaticI18N("en", "BACK_LINK") {
		t.Errorf("StaticI18N = %q, want the key", fixture.appData.StaticI18N("en", "BACK_LINK"))
	}
}

// TestCompileAgainstTheShippedTree exercises the real localization and theme
// files of this checkout: the compiled language and theme CSS must be produced
// without error.
func TestCompileAgainstTheShippedTree(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if nil != err {
		t.Fatalf("resolve repository root: %v", err)
	}

	versionRoot := filepath.Join(repoRoot, "snappymail", "v", "0.0.0")
	if _, statErr := os.Stat(filepath.Join(versionRoot, "app", "localization", "da", "user.json")); nil != statErr {
		t.Skipf("the shipped install tree is not available: %v", statErr)
	}

	appData := NewAppData(Options{
		Version: "0.0.0",
		Paths:   httpx.WebPaths{Version: "0.0.0", VersionRoot: versionRoot},
	})

	js, err := appData.CompileLanguage("da", false)
	if nil != err {
		t.Fatalf("CompileLanguage(da): %v", err)
	}
	if !strings.HasPrefix(js, "document.documentElement.lang = 'da';") {
		t.Errorf("js = %q, want the da language assignment", js)
	}
	if !strings.Contains(js, `"LANGS_NAMES":{`) || !strings.Contains(js, `"da":"Dansk"`) {
		t.Error("js does not carry the intersected language names")
	}
	if !strings.Contains(js, "rl.relativeTime = {") {
		t.Error("js does not carry the absolute relative time format")
	}

	css, err := appData.CompileCSS("Gazellemail", false, false)
	if nil != err {
		t.Fatalf("CompileCSS(Gazellemail): %v", err)
	}
	if 0 == len(css) {
		t.Error("the shipped theme CSS is empty")
	}
	if !strings.Contains(css, "--gazelle-blue") {
		t.Error("the shipped theme CSS does not contain the brand tokens")
	}

	if "Reload" != appData.StaticI18N("en", "BACK_LINK") {
		t.Errorf("StaticI18N(BACK_LINK) = %q, want Reload", appData.StaticI18N("en", "BACK_LINK"))
	}
}
