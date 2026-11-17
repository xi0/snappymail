package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

func TestAllowedStatic(t *testing.T) {
	allowed := []string{
		"static/favicon.png",
		"static/manifest.json",
		"static/js/boot.js",
		"static/js/libs.js",
		"static/js/app.js",
		"static/js/admin.js",
		"static/js/sieve.js",
		"static/js/openpgp.js",
		"static/js/serviceworker.js",
		"static/js/min/app.min.js",
		"static/js/min/libs.min.js",
		"static/js/min/other.min.js",
		"static/css/boot.css",
		"static/css/boot.min.css",
		"static/css/app.css",
		"static/css/app.min.css",
		"static/css/admin.css",
		"static/css/admin.min.css",
		"static/css/fonts/snappymail.woff",
		"static/css/fonts/snappymail.woff2",
		"static/images/snappymail-logo.png",
		"static/images/icon-message-notification.png",
		"static/sounds/new-mail.mp3",
		"static/sounds/alert.ogg",
		"themes/Gazellemail/styles.css",
		"themes/Gazellemail/styles.less",
		"themes/Gazellemail/images/logo.png",
		"themes/@custom/styles.css",
	}

	denied := []string{
		"",
		"/static/js/boot.js",
		"static",
		"static/",
		"static/js",
		"static/js/",
		"static/js/other.js",
		"static/js/min/sub/app.min.js",
		"static/js/boot.jsx",
		"static/css/other.css",
		"static/css/boot.css.map",
		"static/css/fonts/../../js/boot.js",
		"static/images/../../include.php",
		"static/images/.hidden.png",
		"static/images/logo.php",
		"static/sounds/alert.txt",
		"static/.htaccess",
		"static/js\\boot.js",
		"themes/Gazellemail/other.css",
		"themes/Gazellemail/images/logo.css",
		"themes/../styles.css",
		"themes/Gazellemail/../../include.php",
		"app/templates/Index.html",
		"include.php",
		"localization/en/app.json",
	}

	for _, relative := range allowed {
		if !allowedStatic(relative) {
			t.Errorf("allowedStatic(%q) = false, want true", relative)
		}
	}

	for _, relative := range denied {
		if allowedStatic(relative) {
			t.Errorf("allowedStatic(%q) = true, want false", relative)
		}
	}
}

func TestForbiddenPath(t *testing.T) {
	forbidden := []string{
		"/include.php",
		"/data/_data_/_default_/configs/application.ini",
		"/data/",
		"/plugins/caldav/index.php",
		// Everything below the version root is answered by Static first, so
		// whatever is left there must not fall through to the index page.
		"/snappymail/v/0.0.0/app/libraries/RainLoop/Api.php",
		"/snappymail/v/0.0.0/include.php",
		"/snappymail/v/0.0.0/static/js/boot.js",
		"/.htaccess",
		"/snappymail/v/0.0.0/.htaccess",
		"/.git/config",
		"/tests/",
		"/build/",
		"/a/../../../etc/passwd",
		"/evil\\path",
	}

	allowed := []string{
		"",
		"/",
		"/index.php",
		"/index.php/Raw/0/",
		"/?",
		"/whatever",
	}

	for _, value := range forbidden {
		if !forbiddenPath(value) {
			t.Errorf("forbiddenPath(%q) = false, want true", value)
		}
	}

	for _, value := range allowed {
		if forbiddenPath(value) {
			t.Errorf("forbiddenPath(%q) = true, want false", value)
		}
	}
}

func TestSecFetchMatchAnyRuleTable(t *testing.T) {
	tests := []struct {
		name  string
		dest  string
		mode  string
		site  string
		user  string
		rules string
		want  bool
	}{
		{name: "no header at all", want: true},
		{name: "site none", site: "none", want: true},
		{name: "site same-origin", site: "same-origin", want: true},
		{name: "site same-site", site: "same-site", want: false},
		{name: "site cross-site", site: "cross-site", want: false},
		{name: "cross-site allowed by rule", site: "cross-site", rules: "site=cross-site", want: true},
		{name: "multiple rules", site: "cross-site", rules: "site=same-site;site=cross-site", want: true},
		{name: "dest must match", site: "cross-site", dest: "iframe",
			rules: "dest=iframe,site=cross-site", want: true},
		{name: "dest mismatch", site: "cross-site", dest: "image",
			rules: "dest=iframe,site=cross-site", want: false},
		{name: "mode must match", site: "cross-site", mode: "cors",
			rules: "mode=navigate,site=cross-site", want: false},
		{name: "mode matches", site: "cross-site", mode: "navigate",
			rules: "mode=navigate,site=cross-site", want: true},
		// The implicit "site=same-origin;site=none" rules only look at the
		// site, so they win before the dest/mode rules are considered.
		{name: "implicit rule ignores dest", site: "same-origin", dest: "iframe", want: true},
		{name: "user alone is not a rule", site: "cross-site", user: "?1", want: false},
	}

	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		if "" != test.site {
			request.Header.Set("Sec-Fetch-Site", test.site)
		}
		if "" != test.dest {
			request.Header.Set("Sec-Fetch-Dest", test.dest)
		}
		if "" != test.mode {
			request.Header.Set("Sec-Fetch-Mode", test.mode)
		}
		if "" != test.user {
			request.Header.Set("Sec-Fetch-User", test.user)
		}

		if got := SecFetchMatchAnyRule(request, test.rules); got != test.want {
			t.Errorf("%s: SecFetchMatchAnyRule = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestCSPDefaultPolicy(t *testing.T) {
	csp := NewCSP("", "/?/CspReport")

	want := "base-uri 'self'; default-src 'self' data:; " +
		"script-src 'self' 'unsafe-eval'; img-src 'self' data:; " +
		"media-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"connect-src 'self' data: keys.openpgp.org"

	if got := csp.String(); got != want {
		t.Errorf("CSP = %q, want %q", got, want)
	}
	if 0 < len(csp.Get("frame-ancestors")) {
		t.Error("frame-ancestors must not be set by default")
	}
}

func TestCSPMergesConfiguration(t *testing.T) {
	csp := NewCSP("img-src https://cdn.example.com; worker-src 'self'; frame-ancestors none", "/?/CspReport")

	imgSrc := csp.Get("img-src")
	if strings.Join(imgSrc, " ") != "'self' data: https://cdn.example.com" {
		t.Errorf("img-src = %q", imgSrc)
	}

	// Unknown directives are appended in policy order.
	if !strings.HasSuffix(csp.String(), "worker-src 'self'; frame-ancestors none") {
		t.Errorf("CSP = %q, want the configured directives at the end", csp.String())
	}

	// frame-ancestors "none" keeps X-Frame-Options: DENY.
	recorder := httptest.NewRecorder()
	csp.SetHeaders(recorder)
	if "DENY" != recorder.Header().Get("X-Frame-Options") {
		t.Errorf("X-Frame-Options = %q, want DENY", recorder.Header().Get("X-Frame-Options"))
	}
}

func TestCSPDuplicatesAndReportURI(t *testing.T) {
	csp := NewCSP("img-src 'self'", "/?/CspReport")
	csp.Add("img-src", "'self'")
	csp.Add("img-src", "https:")

	// Sources are merged like array_merge(), so duplicates only disappear when
	// the header is rendered (PHP: array_unique()).
	if got := strings.Join(csp.Get("img-src"), " "); "'self' data: 'self' 'self' https:" != got {
		t.Errorf("img-src = %q, want the merged sources", got)
	}
	if policy := csp.String(); !strings.Contains(policy, "img-src 'self' data: https:") {
		t.Errorf("CSP = %q, want the de-duplicated img-src", policy)
	}

	// report-uri is only added when reporting is enabled and always ends the
	// header.
	if strings.Contains(csp.String(), "report-uri") {
		t.Errorf("CSP %q must not contain a report-uri by default", csp.String())
	}

	csp.Report = true
	if !strings.HasSuffix(csp.String(), "; report-uri /?/CspReport") {
		t.Errorf("CSP = %q, want the report-uri at the end", csp.String())
	}

	recorder := httptest.NewRecorder()
	csp.ReportOnly = true
	csp.SetHeaders(recorder)
	if "" != recorder.Header().Get("Content-Security-Policy") {
		t.Error("report-only must not send Content-Security-Policy")
	}
	if "" == recorder.Header().Get("Content-Security-Policy-Report-Only") {
		t.Error("report-only must send Content-Security-Policy-Report-Only")
	}
}

func TestErrorTemplateUsesTheTemplateFile(t *testing.T) {
	paths := testTree(t)
	router := NewRouter(DefaultConfig(), nil, paths, nil)

	body := router.errorTemplate("Access Denied.", "Not <b>allowed</b>", true)

	if !strings.Contains(body, "<title>Access Denied.</title>") {
		t.Errorf("body = %q, want the rendered template", body)
	}
	if !strings.Contains(body, "Not &lt;b&gt;allowed&lt;/b&gt;") {
		t.Errorf("body = %q, want an escaped description", body)
	}
	if !strings.Contains(body, `href="./"`) {
		t.Errorf("body = %q, want the back link", body)
	}
	if !strings.Contains(body, "display:inline-block") {
		t.Errorf("body = %q, want the visible back link", body)
	}
	if strings.Contains(router.errorTemplate("t", "d", false), "display:inline-block") {
		t.Error("the back link must be hidden when it is disabled")
	}
}

func TestErrorTemplateFallsBackWithoutTemplate(t *testing.T) {
	paths := httpx.WebPaths{Version: testVersion, VersionRoot: t.TempDir()}
	router := NewRouter(DefaultConfig(), nil, paths, nil)

	body := router.errorTemplate("Oops", "missing", true)

	if !strings.Contains(body, "<title>Oops</title>") || !strings.Contains(body, "missing") {
		t.Errorf("body = %q, want the fallback markup", body)
	}
}
