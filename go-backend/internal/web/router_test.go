package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

const testVersion = "0.0.0"

// errorTemplateFixture is a trimmed copy of app/templates/Error.html: the
// placeholder names are what ServiceActions::ErrorTemplates() replaces.
const errorTemplateFixture = `<!DOCTYPE html>
<html lang="en" dir="ltr">
<head><meta charset="utf-8"><title>{{ErrorTitle}}</title></head>
<body>
<div class="error-header">{{ErrorHeader}}</div>
<div class="error-desc">{{ErrorDesc}}</div>
<div style="{{BackLinkVisibilityStyle}}"><a href="{{BackHref}}">{{BackLink}}</a></div>
</body>
</html>`

// testTree builds a minimal install tree with the whitelisted assets and a few
// files that must stay unavailable.
func testTree(t *testing.T) httpx.WebPaths {
	t.Helper()

	root := t.TempDir()
	versionRoot := filepath.Join(root, "snappymail", "v", testVersion)

	writeTreeFile(t, versionRoot, "static/js/boot.js", "// boot")
	writeTreeFile(t, versionRoot, "static/js/min/app.min.js", "// app.min.js")
	writeTreeFile(t, versionRoot, "static/css/app.min.css", "body{}")
	writeTreeFile(t, versionRoot, "static/css/fonts/snappymail.woff2", "font")
	writeTreeFile(t, versionRoot, "static/images/snappymail-logo.png", "png")
	writeTreeFile(t, versionRoot, "static/sounds/new-mail.mp3", "mp3")
	writeTreeFile(t, versionRoot, "static/favicon.png", "png")
	writeTreeFile(t, versionRoot, "static/manifest.json", "{}")
	writeTreeFile(t, versionRoot, "themes/Gazellemail/styles.css", "body{}")
	writeTreeFile(t, versionRoot, "themes/Gazellemail/images/logo.png", "png")
	writeTreeFile(t, versionRoot, "app/templates/Error.html", errorTemplateFixture)

	// Never available: PHP sources, private data, non-whitelisted assets.
	writeTreeFile(t, versionRoot, "app/libraries/RainLoop/Api.php", "<?php")
	writeTreeFile(t, versionRoot, "include.php", "<?php")
	writeTreeFile(t, versionRoot, "static/js/secret.js", "// not whitelisted")
	writeTreeFile(t, versionRoot, "themes/Gazellemail/README.txt", "not whitelisted")
	writeTreeFile(t, root, "include.php", "<?php")
	writeTreeFile(t, root, "data/_data_/_default_/configs/application.ini", "[webmail]")
	writeTreeFile(t, root, "plugins/caldav/index.php", "<?php")

	return httpx.WebPaths{Version: testVersion, VersionRoot: versionRoot}
}

// writeTreeFile creates a file (and its directories) below root.
func writeTreeFile(t *testing.T, root, relative, content string) {
	t.Helper()

	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		t.Fatalf("mkdir %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newTestRouter builds the router with the given configuration.
func newTestRouter(t *testing.T, config Config) (*Router, httpx.WebPaths) {
	t.Helper()

	paths := testTree(t)
	router := NewRouter(config, nil, paths, nil)
	router.Register("Ping", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		httpx.NoStore(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Pong")
		return true
	})

	return router, paths
}

// serve runs a single request through the router.
func serve(router *Router, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func get(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestRouterServesWhitelistedStaticAssets(t *testing.T) {
	tests := []struct {
		target      string
		contentType string
		body        string
	}{
		{"/snappymail/v/0.0.0/static/js/min/app.min.js", "application/javascript", "// app.min.js"},
		{"/snappymail/v/0.0.0/static/js/boot.js", "application/javascript", "// boot"},
		{"/snappymail/v/0.0.0/static/css/app.min.css", "text/css", "body{}"},
		{"/snappymail/v/0.0.0/static/css/fonts/snappymail.woff2", "font/woff2", "font"},
		{"/snappymail/v/0.0.0/static/images/snappymail-logo.png", "image/png", "png"},
		{"/snappymail/v/0.0.0/static/sounds/new-mail.mp3", "audio/mpeg", "mp3"},
		{"/snappymail/v/0.0.0/static/favicon.png", "image/png", "png"},
		{"/snappymail/v/0.0.0/static/manifest.json", "application/json", "{}"},
		{"/snappymail/v/0.0.0/themes/Gazellemail/styles.css", "text/css", "body{}"},
		{"/snappymail/v/0.0.0/themes/Gazellemail/images/logo.png", "image/png", "png"},
		// The ?v=<mtime> cache buster is ignored.
		{"/snappymail/v/0.0.0/static/js/boot.js?v=1699999999", "application/javascript", "// boot"},
	}

	router, _ := newTestRouter(t, DefaultConfig())

	for _, test := range tests {
		recorder := serve(router, get(test.target))

		if http.StatusOK != recorder.Code {
			t.Errorf("GET %s = %d, want 200", test.target, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); test.contentType != got {
			t.Errorf("GET %s Content-Type = %q, want %q", test.target, got, test.contentType)
		}
		if got := recorder.Body.String(); test.body != got {
			t.Errorf("GET %s body = %q, want %q", test.target, got, test.body)
		}
		if "" == recorder.Header().Get("ETag") {
			t.Errorf("GET %s has no ETag", test.target)
		}
		if "" == recorder.Header().Get("Cache-Control") {
			t.Errorf("GET %s has no Cache-Control", test.target)
		}
		if "/" != recorder.Header().Get("Service-Worker-Allowed") {
			t.Errorf("GET %s Service-Worker-Allowed = %q, want /",
				test.target, recorder.Header().Get("Service-Worker-Allowed"))
		}
	}
}

func TestRouterUnavailableFilesAre404(t *testing.T) {
	targets := []string{
		// PHP sources
		"/snappymail/v/0.0.0/app/libraries/RainLoop/Api.php",
		"/snappymail/v/0.0.0/include.php",
		"/include.php",
		// private data
		"/data/_data_/_default_/configs/application.ini",
		"/data/",
		// build time plugins
		"/plugins/caldav/index.php",
		"/plugins/caldav/callback.js",
		// assets outside the whitelist
		"/snappymail/v/0.0.0/static/js/secret.js",
		"/snappymail/v/0.0.0/static/css/other.css",
		"/snappymail/v/0.0.0/themes/Gazellemail/README.txt",
		"/snappymail/v/0.0.0/.htaccess",
		// another version directory
		"/snappymail/v/9.9.9/static/js/boot.js",
	}

	router, _ := newTestRouter(t, DefaultConfig())

	for _, target := range targets {
		if recorder := serve(router, get(target)); http.StatusNotFound != recorder.Code {
			t.Errorf("GET %s = %d, want 404", target, recorder.Code)
		}
	}
}

func TestRouterStaticRangeAndConditionalRequests(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())
	target := "/snappymail/v/0.0.0/static/js/boot.js"

	recorder := serve(router, get(target))
	etag := recorder.Header().Get("ETag")
	if "" == etag {
		t.Fatal("no ETag on the static response")
	}

	request := get(target)
	request.Header.Set("If-None-Match", etag)
	if recorder = serve(router, request); http.StatusNotModified != recorder.Code {
		t.Errorf("If-None-Match = %d, want 304", recorder.Code)
	}

	request = get(target)
	request.Header.Set("Range", "bytes=0-3")
	recorder = serve(router, request)
	if http.StatusPartialContent != recorder.Code {
		t.Fatalf("Range = %d, want 206", recorder.Code)
	}
	if "// b" != recorder.Body.String() {
		t.Errorf("Range body = %q, want %q", recorder.Body.String(), "// b")
	}

	request = httptest.NewRequest(http.MethodPost, target, nil)
	if recorder = serve(router, request); http.StatusMethodNotAllowed != recorder.Code {
		t.Errorf("POST static = %d, want 405", recorder.Code)
	}
}

func TestRouterPingEndpoint(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())

	recorder := serve(router, get("/?/Ping/0/"))

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if "Pong" != recorder.Body.String() {
		t.Errorf("body = %q, want Pong", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); "text/plain; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestRouterServesIndexForEverythingElse(t *testing.T) {
	targets := []string{"/", "/?", "/whatever", "/?/index/0/", "/?/Unknown/0/", "/index.php"}

	router, _ := newTestRouter(t, DefaultConfig())

	for _, target := range targets {
		recorder := serve(router, get(target))

		if http.StatusOK != recorder.Code {
			t.Errorf("GET %s = %d, want 200", target, recorder.Code)
			continue
		}
		if got := recorder.Header().Get("Content-Type"); "text/html; charset=utf-8" != got {
			t.Errorf("GET %s Content-Type = %q, want text/html; charset=utf-8", target, got)
		}
		if !strings.Contains(recorder.Body.String(), "SnappyMail") {
			t.Errorf("GET %s body = %q, want the index page", target, recorder.Body.String())
		}
	}
}

func TestRouterPostIsNotCached(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())

	recorder := serve(router, httptest.NewRequest(http.MethodPost, "/?/Unknown/", nil))

	if got := recorder.Header().Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Pragma"); "no-cache" != got {
		t.Errorf("Pragma = %q, want no-cache", got)
	}
}

func TestRouterRouteParsing(t *testing.T) {
	paths := testTree(t)

	config := DefaultConfig()
	config.AdminKey = "admin"

	router := NewRouter(config, nil, paths, nil)

	var captured *Route
	router.Register("Probe", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		captured = route
		return true
	})

	tests := []struct {
		target string
		host   string
		query  string
		paths  []string
		name   string
		extra  string
		admin  bool
	}{
		{target: "/?/Probe/0/1/", query: "Probe/0/1", paths: []string{"Probe", "0", "1"}, name: "Probe"},
		{target: "/?/Probe@extra/0/", query: "Probe@extra/0", paths: []string{"Probe@extra", "0"},
			name: "Probe", extra: "extra"},
		{target: "/?/admin/Probe/0/", query: "admin/Probe/0", paths: []string{"Probe", "0"},
			name: "Probe", admin: true},
		{target: "/index.php/Probe/0/", query: "Probe/0", paths: []string{"Probe", "0"}, name: "Probe"},
	}

	for _, test := range tests {
		captured = nil

		request := get(test.target)
		if "" != test.host {
			request.Host = test.host
		}

		serve(router, request)

		if nil == captured {
			t.Fatalf("GET %s did not reach the Probe endpoint", test.target)
		}
		if captured.Query != test.query {
			t.Errorf("GET %s Query = %q, want %q", test.target, captured.Query, test.query)
		}
		if strings.Join(captured.Paths, "|") != strings.Join(test.paths, "|") {
			t.Errorf("GET %s Paths = %q, want %q", test.target, captured.Paths, test.paths)
		}
		if captured.Name != test.name {
			t.Errorf("GET %s Name = %q, want %q", test.target, captured.Name, test.name)
		}
		if captured.Extra != test.extra {
			t.Errorf("GET %s Extra = %q, want %q", test.target, captured.Extra, test.extra)
		}
		if captured.Admin != test.admin {
			t.Errorf("GET %s Admin = %v, want %v", test.target, captured.Admin, test.admin)
		}
	}
}

func TestRouterAdminPanelByHost(t *testing.T) {
	paths := testTree(t)

	config := DefaultConfig()
	config.AdminHost = "admin.example.com"

	router := NewRouter(config, nil, paths, nil)

	var captured *Route
	router.Register("Probe", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		captured = route
		return true
	})

	request := get("/?/Probe/0/")
	request.Host = "admin.example.com"
	serve(router, request)

	if nil == captured || !captured.Admin {
		t.Fatal("a request on admin_panel.host must be an admin request")
	}
	if strings.Join(captured.Paths, "|") != "Probe|0" {
		t.Errorf("Paths = %q, want [Probe 0] (the host form does not shift a key)", captured.Paths)
	}

	captured = nil
	request = get("/?/Probe/0/")
	request.Host = "www.example.com"
	serve(router, request)

	if nil == captured || captured.Admin {
		t.Fatal("a request on another host must not be an admin request")
	}
}

func TestRouterAdminPanelGate(t *testing.T) {
	config := DefaultConfig()
	config.AllowAdminPanel = false

	router, _ := newTestRouter(t, config)

	recorder := serve(router, get("/?/admin/Ping/0/"))
	if http.StatusForbidden != recorder.Code {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Access to the SnappyMail Admin Panel is not allowed!") {
		t.Errorf("body = %q, want the admin panel error", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); "text/html; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	// Allowing the panel lets the request through to the endpoint.
	config.AllowAdminPanel = true
	router, _ = newTestRouter(t, config)

	if recorder = serve(router, get("/?/admin/Ping/0/")); http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if "Pong" != recorder.Body.String() {
		t.Errorf("body = %q, want Pong", recorder.Body.String())
	}
}

func TestRouterSecFetchGate(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())

	tests := []struct {
		name   string
		site   string
		target string
		rules  string
		status int
	}{
		{name: "no header", target: "/?/Ping/0/", status: http.StatusOK},
		{name: "same-origin", site: "same-origin", target: "/?/Ping/0/", status: http.StatusOK},
		{name: "none", site: "none", target: "/?/Ping/0/", status: http.StatusOK},
		{name: "cross-site", site: "cross-site", target: "/?/Ping/0/", status: http.StatusForbidden},
		{name: "same-site", site: "same-site", target: "/?/Ping/0/", status: http.StatusForbidden},
		{name: "allowed by rule", site: "cross-site", target: "/?/Ping/0/",
			rules: "site=cross-site", status: http.StatusOK},
		// mailto is exempt, but not registered, so it falls through to the index
		{name: "mailto is exempt", site: "cross-site", target: "/?/mailto", status: http.StatusOK},
	}

	for _, test := range tests {
		config := DefaultConfig()
		config.SecFetchAllow = test.rules

		router, _ = newTestRouter(t, config)

		request := get(test.target)
		if "" != test.site {
			request.Header.Set("Sec-Fetch-Site", test.site)
		}

		recorder := serve(router, request)
		if test.status != recorder.Code {
			t.Errorf("%s: status = %d, want %d", test.name, recorder.Code, test.status)
		}

		if http.StatusForbidden == test.status &&
			!strings.Contains(recorder.Body.String(), "Disallowed Sec-Fetch") {
			t.Errorf("%s: body = %q, want the Sec-Fetch report", test.name, recorder.Body.String())
		}
	}
}

func TestRouterForceHTTPS(t *testing.T) {
	config := DefaultConfig()
	config.ForceHTTPS = true

	router, _ := newTestRouter(t, config)

	recorder := serve(router, get("http://www.example.com/?/Ping/0/"))
	if http.StatusFound != recorder.Code {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	// "www." is stripped, like Http::GetHost(false).
	if got := recorder.Header().Get("Location"); "https://example.com/?/Ping/0/" != got {
		t.Errorf("Location = %q, want https://example.com/?/Ping/0/", got)
	}

	if recorder = serve(router, get("https://example.com/?/Ping/0/")); http.StatusOK != recorder.Code {
		t.Errorf("secure request = %d, want 200", recorder.Code)
	}
}

func TestRouterSecurityHeaders(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())

	recorder := serve(router, get("/?/Ping/0/"))
	header := recorder.Header()

	if "SnappyMail" != header.Get("Server") {
		t.Errorf("Server = %q, want SnappyMail", header.Get("Server"))
	}
	if "no-referrer" != header.Get("Referrer-Policy") {
		t.Errorf("Referrer-Policy = %q", header.Get("Referrer-Policy"))
	}
	if "nosniff" != header.Get("X-Content-Type-Options") {
		t.Errorf("X-Content-Type-Options = %q", header.Get("X-Content-Type-Options"))
	}
	if "1; mode=block" != header.Get("X-XSS-Protection") {
		t.Errorf("X-XSS-Protection = %q", header.Get("X-XSS-Protection"))
	}
	if "DENY" != header.Get("X-Frame-Options") {
		t.Errorf("X-Frame-Options = %q, want DENY", header.Get("X-Frame-Options"))
	}
	if "" == header.Get("Content-Security-Policy") {
		t.Error("no Content-Security-Policy")
	}
	if "" != header.Get("Strict-Transport-Security") {
		t.Errorf("Strict-Transport-Security = %q, want nothing on plain http",
			header.Get("Strict-Transport-Security"))
	}

	// HTTPS adds HSTS.
	recorder = serve(router, get("https://example.com/?/Ping/0/"))
	if got := recorder.Header().Get("Strict-Transport-Security"); "max-age=31536000" != got {
		t.Errorf("Strict-Transport-Security = %q, want max-age=31536000", got)
	}

	// An empty custom_server_signature removes the header.
	config := DefaultConfig()
	config.ServerSignature = ""
	router, _ = newTestRouter(t, config)

	if got := serve(router, get("/?/Ping/0/")).Header().Get("Server"); "" != got {
		t.Errorf("Server = %q, want nothing", got)
	}
}

func TestRouterContentSecurityPolicy(t *testing.T) {
	// Defaults: remote images are proxied, so img-src stays restrictive.
	router, _ := newTestRouter(t, DefaultConfig())
	policy := serve(router, get("/?/Ping/0/")).Header().Get("Content-Security-Policy")

	for _, want := range []string{
		"base-uri 'self'",
		"default-src 'self' data:",
		"script-src 'self' 'unsafe-eval'",
		"img-src 'self' data:",
		"style-src 'self' 'unsafe-inline'",
		"connect-src 'self' data: keys.openpgp.org",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("CSP %q does not contain %q", policy, want)
		}
	}

	// Without the local image proxy remote images have to be allowed.
	config := DefaultConfig()
	config.LocalProxyExternalImages = false
	config.ContentSecurityPolicy = "img-src https://cdn.example.com"
	config.FaviconURL = "https://favicon.example.com/favicon.ico"
	router, _ = newTestRouter(t, config)
	policy = serve(router, get("/?/Ping/0/")).Header().Get("Content-Security-Policy")

	for _, want := range []string{"https:", "http:", "https://cdn.example.com", "favicon.example.com"} {
		if !strings.Contains(policy, want) {
			t.Errorf("CSP %q does not contain %q", policy, want)
		}
	}

	// csp_report adds the report-uri, debug.enable makes the whole policy
	// report-only.
	config = DefaultConfig()
	config.CSPReport = true
	config.Debug = true
	router, _ = newTestRouter(t, config)

	recorder := serve(router, get("/?/Ping/0/"))
	if "" != recorder.Header().Get("Content-Security-Policy") {
		t.Error("debug.enable must send the policy as report-only")
	}
	policy = recorder.Header().Get("Content-Security-Policy-Report-Only")
	if !strings.Contains(policy, "report-uri /?/CspReport") {
		t.Errorf("CSP %q does not contain the report-uri", policy)
	}
}

// TestRouterCompressesOutput verifies that the pipeline applies output
// compression; the coding negotiation matrix lives in compress_test.go.
func TestRouterCompressesOutput(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true

	router, _ := newTestRouter(t, config)

	request := get("/whatever")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br")
	recorder := serve(router, request)

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if encodingBrotli != recorder.Header().Get("Content-Encoding") {
		t.Fatalf("Content-Encoding = %q, want br", recorder.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(readBody(t, recorder), "SnappyMail") {
		t.Error("the decompressed body must be the index page")
	}

	// webmail.compress_output is off by default.
	router, _ = newTestRouter(t, DefaultConfig())
	request = get("/whatever")
	request.Header.Set("Accept-Encoding", "gzip, br")
	if got := serve(router, request).Header().Get("Content-Encoding"); "" != got {
		t.Errorf("Content-Encoding = %q, want nothing when compression is disabled", got)
	}
}

func TestRouterErrorTemplateEscapesRequestHeaders(t *testing.T) {
	router, _ := newTestRouter(t, DefaultConfig())

	request := get("/?/Ping/0/")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Sec-Fetch-Dest", "<script>alert(1)</script>")

	recorder := serve(router, request)
	body := recorder.Body.String()

	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("body %q does not escape the Sec-Fetch headers", body)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("body %q contains unescaped request data", body)
	}
	if !strings.Contains(body, "<title>Access Denied.</title>") {
		t.Errorf("body %q does not use the Error.html template", body)
	}
}
