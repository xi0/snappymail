package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

const indexFixture = `<!DOCTYPE html>
<html class="rl-started-trigger">
<head>
	<script nonce="">
	/* boot */
	</script>
	<style id="app-boot-css">{{BaseAppBootCss}}</style>
	<link type="text/css" rel="stylesheet" href="{{BaseAppMainCssLink}}">
	<style id="app-theme-style" data-name="{{BaseAppThemeName}}">{{BaseAppThemeCss}}</style>
</head>
<body id="rl-app" data-admin='{{BaseAppAdmin}}' spellcheck="false">
	<div id="rl-loading-desc">{{LoadingDescriptionEsc}}</div>
	<div id="no-cookie">{{NO_COOKIE_TITLE}}</div>
	{{BaseTemplates}}
	<script nonce="" type="text/javascript">{{BaseAppBootScript}}{{BaseLanguage}}</script>
	<script>var cookie = "samesite=strict";</script>
</body>
</html>
`

// newTree builds a minimal install tree with the index assets.
func newTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"app/templates/Index.html":                   indexFixture,
		"app/templates/Error.html":                   `<!DOCTYPE html><title>{{ErrorTitle}}</title><div>{{ErrorDesc}}</div><a href="{{BackHref}}">{{BackLink}}</a>`,
		"app/templates/Views/Components/Widget.html": `<div>widget</div><script>var x=1;</script>`,
		"app/templates/Views/User/Page.html":         `<section>user</section>`,
		"app/templates/Views/Common/Footer.html":     `<footer>common</footer>`,
		"static/css/boot.min.css":                    `body{color:#000}`,
		"static/css/app.min.css":                     `body{}`,
		"static/js/min/boot.min.js":                  `var boot=true;`,
		"static/favicon.png":                         `png`,
		"static/manifest.json":                       `{}`,
		"themes/Gazellemail/styles.css":              "body { color : #fff }",
	}

	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); nil != err {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return root
}

// fakeConfig is configs/application.ini for the renderer.
type fakeConfig struct {
	strings map[string]string
	bools   map[string]bool
}

// GetString returns the configured value or the default.
func (c fakeConfig) GetString(section, key, defaultValue string) string {
	if value, ok := c.strings[section+"."+key]; ok {
		return value
	}
	return defaultValue
}

// GetBool returns the configured value or the default.
func (c fakeConfig) GetBool(section, key string, defaultValue bool) bool {
	if value, ok := c.bools[section+"."+key]; ok {
		return value
	}
	return defaultValue
}

// fakeActions is webmail.Actions.
type fakeActions struct{ loggedIn bool }

// Language returns the language of the request.
func (fakeActions) Language() string { return "en" }

// Theme returns the theme name.
func (fakeActions) Theme() string { return "Gazellemail" }

// CompileCSS returns the theme stylesheet.
func (fakeActions) CompileCSS(string) (string, error) { return "body { color : #fff }", nil }

// CompileLanguage returns a minimal language script.
func (fakeActions) CompileLanguage(language string) (string, error) {
	return "document.documentElement.lang = '" + language + "';rl.I18N={};", nil
}

// StaticI18N returns the key text.
func (fakeActions) StaticI18N(key string) string { return "text:" + key }

// LoggedIn reports the account state.
func (a fakeActions) LoggedIn() bool { return a.loggedIn }

// memoryCache is the TMPL: cache.
type memoryCache map[string]string

// Get returns a cached entry.
func (c memoryCache) Get(key string) (string, bool) {
	value, ok := c[key]
	return value, ok
}

// Set stores an entry.
func (c memoryCache) Set(key, value string) error {
	c[key] = value
	return nil
}

func newRenderer(t *testing.T, root string, cache Cache, actions Actions) *Renderer {
	t.Helper()

	return New(Options{
		Config: fakeConfig{
			strings: map[string]string{"security.cookie_samesite": "Lax"},
		},
		Version:    "0.0.0",
		Paths:      httpx.WebPaths{Version: "0.0.0", VersionRoot: root},
		DataFolder: filepath.Dir(root),
		Cache:      cache,
		Actions: func(http.ResponseWriter, *http.Request, bool) Actions {
			if nil == actions {
				return fakeActions{}
			}
			return actions
		},
	})
}

func TestIndexRendersTheShell(t *testing.T) {
	root := newTree(t)
	renderer := newRenderer(t, root, nil, nil)

	recorder := httptest.NewRecorder()
	renderer.Index(recorder, httptest.NewRequest(http.MethodGet, "/", nil), &web.Route{})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "text/html; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	body := recorder.Body.String()

	// Every placeholder is replaced, including the nonce ones.
	if strings.Contains(body, "{{") {
		t.Errorf("body still holds a placeholder: %s", body)
	}
	if strings.Contains(body, `nonce=""`) {
		t.Errorf("body has an empty nonce: %s", body)
	}
	if !strings.Contains(body, `nonce="`) {
		t.Errorf("body has no nonce: %s", body)
	}

	// Boot assets and the main stylesheet.
	if !strings.Contains(body, "body{color:#000}") {
		t.Errorf("body does not inline the boot CSS: %s", body)
	}
	if !strings.Contains(body, "var boot=true;") {
		t.Errorf("body does not inline the boot script: %s", body)
	}
	if !strings.Contains(body, "/snappymail/v/0.0.0/static/css/app.min.css?v=") {
		t.Errorf("body has no main CSS link: %s", body)
	}

	// The theme CSS is whitespace-minified and the language script is inlined.
	if !strings.Contains(body, "body{color:#fff}") {
		t.Errorf("body does not inline the minified theme CSS: %s", body)
	}
	if !strings.Contains(body, "rl.I18N={}") {
		t.Errorf("body does not inline the language: %s", body)
	}

	// The compiled templates, with the script tags renamed for safety.
	if !strings.Contains(body, `<template id="WidgetComponent">`) {
		t.Errorf("body does not inline the component template: %s", body)
	}
	if !strings.Contains(body, "<x-script>") {
		t.Errorf("body does not escape the template script tag: %s", body)
	}
	if !strings.Contains(body, `<template id="Page">`) || !strings.Contains(body, `<template id="Footer">`) {
		t.Errorf("body does not inline the user/common templates: %s", body)
	}

	// data-admin reflects the admin flag.
	if !strings.Contains(body, `data-admin='0'`) {
		t.Errorf("body has the wrong data-admin: %s", body)
	}

	// security.cookie_samesite replaces the boot script default.
	if !strings.Contains(body, "samesite=lax") {
		t.Errorf("body does not lower the samesite: %s", body)
	}
	if strings.Contains(body, "samesite=strict") {
		t.Errorf("body still has samesite=strict: %s", body)
	}

	// StaticI18N texts.
	if !strings.Contains(body, "text:NO_COOKIE_TITLE") {
		t.Errorf("body does not use StaticI18N: %s", body)
	}
}

func TestIndexUsesTheCache(t *testing.T) {
	root := newTree(t)
	cache := memoryCache{}
	renderer := newRenderer(t, root, cache, nil)

	request := httptest.NewRequest(http.MethodGet, "/", nil)

	recorder := httptest.NewRecorder()
	renderer.Index(recorder, request, &web.Route{})
	if strings.Contains(recorder.Body.String(), "<!--cached-->") {
		t.Fatal("the first request must not be marked as cached")
	}
	if 0 == len(cache) {
		t.Fatal("the first request must fill the cache")
	}

	recorder = httptest.NewRecorder()
	renderer.Index(recorder, request, &web.Route{})
	if !strings.Contains(recorder.Body.String(), "<!--cached-->") {
		t.Error("the second request must be served from the cache")
	}
}

func TestIndexPermissionError(t *testing.T) {
	renderer := New(Options{
		Version:    "0.0.0",
		Paths:      httpx.WebPaths{Version: "0.0.0", VersionRoot: t.TempDir()},
		DataFolder: filepath.Join(t.TempDir(), "missing"),
	})

	recorder := httptest.NewRecorder()
	renderer.Index(recorder, httptest.NewRequest(http.MethodGet, "/", nil), &web.Route{})

	body := recorder.Body.String()
	if !strings.Contains(body, "Permission denied!") {
		t.Errorf("body = %q, want the permission error", body)
	}
	if !strings.Contains(body, "SnappyMail can not access the data folder") {
		t.Errorf("body = %q, want the data folder description", body)
	}
}

func TestIndexCustomLoginLinkRedirectsGuests(t *testing.T) {
	root := newTree(t)
	renderer := New(Options{
		Config: fakeConfig{strings: map[string]string{"labs.custom_login_link": "/login"}},
		Paths:  httpx.WebPaths{Version: "0.0.0", VersionRoot: root},
		Actions: func(http.ResponseWriter, *http.Request, bool) Actions {
			return fakeActions{loggedIn: false}
		},
	})

	recorder := httptest.NewRecorder()
	renderer.Index(recorder, httptest.NewRequest(http.MethodGet, "/", nil), &web.Route{})

	if http.StatusFound != recorder.Code {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); "/login" != got {
		t.Errorf("Location = %q, want /login", got)
	}

	// A logged in user still gets the page.
	renderer = New(Options{
		Config: fakeConfig{strings: map[string]string{"labs.custom_login_link": "/login"}},
		Paths:  httpx.WebPaths{Version: "0.0.0", VersionRoot: root},
		Actions: func(http.ResponseWriter, *http.Request, bool) Actions {
			return fakeActions{loggedIn: true}
		},
	})
	recorder = httptest.NewRecorder()
	renderer.Index(recorder, httptest.NewRequest(http.MethodGet, "/", nil), &web.Route{})
	if http.StatusOK != recorder.Code {
		t.Errorf("a logged in user must not be redirected, status = %d", recorder.Code)
	}
}

func TestClearHTMLOutput(t *testing.T) {
	input := "  <div>\n\t<p>a &nbsp; b</p>\n</div>  "
	// The leading whitespace and "&nbsp;" become a single NBSP; the space after
	// it survives because the second pattern only matches the literal "&nbsp;".
	want := "<div> <p>a\xC2\xA0 b</p> </div>"
	if got := ClearHTMLOutput(input); got != want {
		t.Errorf("ClearHTMLOutput(%q) = %q, want %q", input, got, want)
	}
}

func TestNL2AndEscape(t *testing.T) {
	if got := nl2br("a\nb"); "a<br />\nb" != got {
		t.Errorf("nl2br = %q", got)
	}
	if got := escapeHTML(`<a href="x">'&'</a>`); "&lt;a href=&quot;x&quot;&gt;&#039;&amp;&#039;&lt;/a&gt;" != got {
		t.Errorf("escapeHTML = %q", got)
	}
}

// TestIndexRendersTheShippedTree renders the real app/templates/Index.html of
// this checkout: every placeholder must be replaced and the real view
// templates must be compiled.
func TestIndexRendersTheShippedTree(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if nil != err {
		t.Fatalf("resolve repository root: %v", err)
	}

	versionRoot := filepath.Join(repoRoot, "snappymail", "v", "0.0.0")
	if _, statErr := os.Stat(filepath.Join(versionRoot, "app", "templates", "Index.html")); nil != statErr {
		t.Skipf("the shipped install tree is not available: %v", statErr)
	}

	renderer := New(Options{
		Config:     fakeConfig{},
		Version:    "0.0.0",
		Paths:      httpx.WebPaths{Version: "0.0.0", VersionRoot: versionRoot},
		DataFolder: filepath.Join(repoRoot, "data"),
		Actions: func(http.ResponseWriter, *http.Request, bool) Actions {
			return fakeActions{}
		},
	})

	recorder := httptest.NewRecorder()
	renderer.Index(recorder, httptest.NewRequest(http.MethodGet, "/", nil), &web.Route{})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	body := recorder.Body.String()
	if strings.Contains(body, "{{") {
		t.Error("the shipped index template still holds a placeholder")
	}
	if !strings.Contains(body, "rl.I18N=") {
		t.Error("the shipped index page has no language script")
	}
	if !strings.Contains(body, `id="app-theme-style" data-name="Gazellemail"`) {
		t.Error("the shipped index page has no theme style")
	}
	if !strings.Contains(body, `<template id="MessageList">`) &&
		!strings.Contains(body, `<template id="`) {
		t.Error("the shipped index page has no compiled templates")
	}
	if !strings.Contains(body, `nonce="`) || strings.Contains(body, `nonce=""`) {
		t.Error("the shipped index page has no CSP nonce")
	}
	if 10000 > len(body) {
		t.Errorf("the shipped index page is only %d bytes", len(body))
	}
}
