package service

// The three small endpoints of step 13a, which need no new infrastructure:
//
//	GET  ?/Mailto&to=mailto:…   ServiceActions::ServiceMailto()
//	GET  ?/Sso&hash=…           ServiceActions::ServiceSso()
//	POST ?/CspReport            ServiceActions::ServiceCspReport()
//
// plus the ?/Test diagnostics report of step 13b (ServiceActions::ServiceTest →
// SnappyMail\Integrity::test()), which probes the running server through
// internal/httpclient, and the ?/ProxyExternal image proxy of step 13c
// (ServiceActions::ServiceProxyExternal), which fetches remote images through
// the same client for the "use_local_proxy_for_external_images" mode.
//
// ServiceMailto() puts the "mailto:" URL into the single-use smmailtoauth cookie
// (Actions::AUTH_MAILTO_TOKEN_KEY), which AppData() reports once as "mailToEmail"
// and then drops (appdata_user.go: mailTo). ServiceSso() logs in with the
// single-use hash of \RainLoop\API::CreateUserSsoHash() through the existing
// auth.DoSso() (internal/auth/sso.go) and ServiceCspReport() records a
// Content-Security-Policy violation report through the logx logger
// (SnappyMail\HTTP\CSP::logReport(): a 204, the raw body logged unless it is
// empty or reports the bare 'moz-extension' source).
//
// PHP swallows every failure of the first two and always redirects to "./", so
// the handlers report the request as handled in every case.
//
// The URL shapes look odd but are PHP's: Service::RunResult() splits the query
// string on "&" and "/" only, so a "?" inside it stays part of the path vector.
// The frontend therefore registers the protocol handler as "?mailto&to=%s"
// (app.js registerMailto()) and the bundled login-external-sso plugin documents
// "?Sso&hash="; a literal "?/Sso?hash=…" would address the index page in PHP as
// well. The parameters are read through queryParam()/requestParam(), so both the
// "?/Mailto&to=…" and the "?mailto&to=…" spelling reach the endpoints.

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/httpclient"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// cspLogName is the log category of the CSP reports: SnappyMail\Log::error()
// with the "CSP" prefix, which Logger()->Write() receives as the name.
const cspLogName = "CSP"

// mailtoPattern is the '/^mailto:/i' test of ServiceMailto().
var mailtoPattern = regexp.MustCompile(`(?i)^mailto:`)

// queryParam is $_GET[$name] as a string.
//
// A repeated or array-shaped parameter would be an array in PHP (and a fatal
// trim() TypeError for ServiceMailto()), so only the first value is used.
func queryParam(r *http.Request, name string) string {
	if nil == r || nil == r.URL {
		return ""
	}
	return r.URL.Query().Get(name)
}

// requestParam is $_REQUEST[$name]: the query string first, then the POST body,
// which wins with PHP's default request_order = "GP".
func requestParam(r *http.Request, name string) string {
	value := queryParam(r, name)

	if nil != r && httpx.IsPost(r) {
		if err := r.ParseForm(); nil == err {
			if values, ok := r.PostForm[name]; ok && 0 < len(values) {
				value = values[0]
			}
		}
	}

	return value
}

// requestBody is php://input; a nil body reads as empty.
func requestBody(r *http.Request) io.Reader {
	if nil == r || nil == r.Body {
		return strings.NewReader("")
	}
	return r.Body
}

// microtime is PHP's microtime(true): the current time as a float.
func microtime() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Mailto is ServiceActions::ServiceMailto(): hand a "mailto:" URL over to the
// webmail client through the single-use smmailtoauth cookie.
func (a *AppData) Mailto(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	httpx.NoStore(w)

	// PHP: $sTo = \trim($_GET['to'] ?? '');
	if to := strings.TrimSpace(queryParam(r, "to")); mailtoPattern.MatchString(to) {
		a.setMailtoCookie(w, r, to)
	}

	// \MailSo\Base\Http::Location('./');
	httpx.Location(w, "./", http.StatusFound)

	return true
}

// setMailtoCookie is the Cookies::set() call of ServiceMailto(): the timestamp
// and the mailto URL sealed with Utils::EncodeKeyValuesQ() into a session
// cookie (no expiry, HttpOnly, like the PHP default arguments).
//
// A request without a cookie stack (no session factory, no crypter) simply drops
// the cookie, which is what an unwritable cookie jar amounts to.
func (a *AppData) setMailtoCookie(w http.ResponseWriter, r *http.Request, to string) {
	session := a.session(w, r)
	if nil == session || nil == session.Jar || nil == session.Authenticator {
		return
	}

	encoded, err := session.Authenticator.EncodeKeyValuesQ(map[string]any{
		"Time":   microtime(),
		"MailTo": "MailTo",
		"To":     to,
	}, "")
	if nil != err {
		return
	}

	session.Jar.Set(w, auth.AuthMailtoTokenKey, encoded, time.Time{}, true)
}

// Sso is ServiceActions::ServiceSso(): log in with the
// \RainLoop\API::CreateUserSsoHash() generated hash and redirect to "./".
//
// auth.DoSso() already reads the /Sso/Data/<hash>/Login/ cache entry, drops it
// (single use), decrypts it, logs in and applies AdditionalOptions.language, so
// this is the redirect plus the session wiring around it.
func (a *AppData) Sso(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	httpx.NoStore(w)

	// PHP: $sSsoHash = $_REQUEST['hash'] ?? ''; if (!empty($sSsoHash)) …
	if hash := requestParam(r, "hash"); !phpEmpty(hash) {
		if login := a.newLogin(w, r); nil != login {
			// PHP catches every Throwable of LoginProcess() and still redirects.
			_ = login.DoSso(hash)
		}
	}

	// \MailSo\Base\Http::Location('./');
	httpx.Location(w, "./", http.StatusFound)

	return true
}

// newLogin builds the login flow (step 8) of the request on top of the request
// session, so ServiceSso() reuses exactly the cookie/account stack of the
// AppData and Json endpoints.
//
// The result is nil without a session: there is no account to log in to and no
// cookie jar to write the login cookies to.
func (a *AppData) newLogin(w http.ResponseWriter, r *http.Request) *auth.Login {
	session := a.session(w, r)
	if nil == session || nil == session.Authenticator {
		return nil
	}

	authenticator := session.Authenticator

	// Actions::SettingsProvider(): DoSso() persists AdditionalOptions.language.
	// The provider only has to be a SettingsStore; a Load-only provider leaves
	// the language unpersisted, like a missing settings provider in PHP.
	settings, _ := a.options.Settings.(auth.SettingsStore)

	// A typed-nil *logx.Logger would be a non-nil auth.Logger and panic on the
	// first log line, so it is only set when usable.
	var logger auth.Logger
	if nil != a.options.Logger {
		logger = a.options.Logger
	}

	host := ""
	if nil != r {
		host = r.Host
	}

	return &auth.Login{
		Context:          authenticator.Context,
		Session:          authenticator,
		Config:           a.config(),
		Logger:           logger,
		Host:             httpx.Host(r, false, true),
		HTTPHost:         host,
		Settings:         settings,
		ValidateLanguage: func(language string) string { return a.validateLanguage(language, "", false) },
		Cache:            a.options.Cache,
		ClientIP:         httpx.ClientIP(r,
			a.config().GetBool("labs", "http_client_ip_check_proxy", false)),
	}
}

// CspReport is ServiceActions::ServiceCspReport() /
// SnappyMail\HTTP\CSP::logReport(): reply 204 and record the violation report.
func (a *AppData) CspReport(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	body, _ := io.ReadAll(requestBody(r))

	var report any
	_ = json.Unmarshal(body, &report)

	w.WriteHeader(http.StatusNoContent)

	// PHP: if ($json && $report && 'moz-extension' !== $report['csp-report']
	//           ['source-file']) { \SnappyMail\Log::error('CSP', $json); }
	// A report without "csp-report"/"source-file" is logged, like the undefined
	// array key comparison of PHP evaluates to true.
	if !phpEmpty(string(body)) && !phpEmpty(report) && !cspReportFromExtension(report) {
		if nil != a.options.Logger {
			a.options.Logger.Write(string(body), logx.Error, cspLogName)
		}
	}

	return true
}

// cspReportFromExtension is the 'moz-extension' === source-file test of
// logReport(): a report from a browser extension has no useful source, so it is
// not logged.
//
// PHP compares the whole source-file against the bare string 'moz-extension',
// which is what Firefox sends for an extension injected script; a full
// "moz-extension://<uuid>/content.js" URL compares unequal and is logged. The
// comparison is kept exactly as-is instead of being turned into a prefix match.
func cspReportFromExtension(report any) bool {
	document, ok := report.(map[string]any)
	if !ok {
		return false
	}

	details, ok := document["csp-report"].(map[string]any)
	if !ok {
		return false
	}

	source, _ := details["source-file"].(string)

	return "moz-extension" == source
}

// proxyExpirySeconds is the 30 day lifetime ServiceProxyExternal() gives a
// proxied image (2592000 seconds added to time()).
const proxyExpirySeconds = 2592000

// proxySniffSize is how many leading bytes MimeType::fromStream() inspects
// (stream_get_contents($stream, 265, 0)).
const proxySniffSize = 265

// proxyImagePrefix is the '/^image\//' test of ServiceProxyExternal().
const proxyImagePrefix = "image/"

// ProxyExternal is ServiceActions::ServiceProxyExternal(): the local proxy for
// external images of ?/ProxyExternal/<url-safe-base64>.
//
// It only answers when labs.use_local_proxy_for_external_images is on and an
// account is logged in (PHP: getAccountFromToken()); every other case, a fetch
// that is not a 200 image response, and a transport error end in a 404, which is
// the "StatusHeader(404)" tail of the PHP method. The 200 branch sets the cache
// headers, streams the body and stops the request like the PHP "exit".
func (a *AppData) ProxyExternal(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	data := ""
	if nil != route && 1 < len(route.Paths) {
		data = route.Paths[1]
	}

	if a.proxyExternalAllowed(w, r, data) {
		// Actions::verifyCacheByKey(); the ETag handling is step 33.
		a.verifyCacheByKey(w, data)

		if sURL, ok := proxyURL(data); ok {
			// PHP sets the header before fetching, so it is present even when
			// the fetch or the content type check fails.
			w.Header().Set("X-Content-Location", sURL)

			if a.serveProxiedImage(w, data, sURL) {
				return true
			}
		}
	}

	w.WriteHeader(http.StatusNotFound)

	return true
}

// proxyExternalAllowed is the "$sData && config && getAccountFromToken()" gate
// of ServiceProxyExternal(). A nil account (a guest) maps the PHP exception on
// getAccountFromToken() onto the same 404 the endpoint reports for a guest.
func (a *AppData) proxyExternalAllowed(w http.ResponseWriter, r *http.Request, data string) bool {
	if phpEmpty(data) {
		return false
	}

	if !a.config().GetBool("labs", "use_local_proxy_for_external_images", false) {
		return false
	}

	resolved, _ := accounts(a.session(w, r))

	return nil != resolved
}

// proxyURL is the UrlSafeBase64Decode()/$sUrl check of ServiceProxyExternal():
// the decoded target URL, or ok=false for an empty/invalid blob.
func proxyURL(data string) (string, bool) {
	decoded, err := cryptox.URLSafeBase64Decode(data)
	if nil != err {
		return "", false
	}

	url := string(decoded)
	if "" == url {
		return "", false
	}

	return url, true
}

// serveProxiedImage performs the GET with MaxRedirects=2, streamed to a temp
// file (the PHP tmpfile()), and serves it when it is a 200 image response.
//
// On any failure it sets "X-Content-Error" (except for a transport error, which
// leaves the header off like a null $oResponse in PHP) and reports false so the
// caller answers 404.
func (a *AppData) serveProxiedImage(w http.ResponseWriter, data, sURL string) bool {
	tmp, err := os.CreateTemp("", "snappymail-proxy-*")
	if nil != err {
		return false
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	client := httpclient.New(a.options.Version)
	// PHP: $HTTP->max_redirects = 2;
	client.MaxRedirects = 2
	client.StreamBodyTo(tmp)

	response, err := client.Do(http.MethodGet, sURL, nil, nil)
	client.StreamBodyTo(nil)

	if nil != err || nil == response {
		return false
	}

	// PHP: MimeType::fromStream($tmp) ?: $oResponse->getHeader('content-type')
	contentType := proxyContentType(tmp)
	if "" == contentType {
		contentType = response.GetHeader("content-type")
	}

	// PHP: if (200 === status && str_starts_with($contentType, 'image/'))
	if http.StatusOK != response.Status || !strings.HasPrefix(contentType, proxyImagePrefix) {
		w.Header().Set("X-Content-Error", fmt.Sprintf("%d %s", response.Status, contentType))
		return false
	}

	// Actions::cacheByKey(); the ETag handling is step 33.
	a.cacheByKey(data)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public")
	w.Header().Set("Expires", proxyExpires())
	w.Header().Set("X-Content-Redirect-Location", response.FinalURI)
	w.WriteHeader(http.StatusOK)

	if _, err := tmp.Seek(0, io.SeekStart); nil == err {
		_, _ = io.Copy(w, tmp)
	}

	return true
}

// proxyContentType is MimeType::fromStream() on the temp file: the leading
// proxySniffSize bytes decide the type, using the detectDeeper() fallback with no
// name (the PHP fromStream() passes no name).
func proxyContentType(tmp *os.File) string {
	header := make([]byte, proxySniffSize)

	read, err := tmp.ReadAt(header, 0)
	if nil != err && io.EOF != err {
		return ""
	}
	if 0 >= read {
		return ""
	}

	return mime.FromContent(header[:read], "")
}

// proxyExpires is the "Expires" header of a proxied image:
// gmdate('D, j M Y H:i:s', 2592000 + time()) . ' UTC'.
func proxyExpires() string {
	expires := time.Now().Add(proxyExpirySeconds * time.Second).UTC()
	return expires.Format("Mon, 2 Jan 2006 15:04:05") + " UTC"
}

// integrityAssets are the files Integrity::test() probes: the bundles the
// frontend loads (the unminified variants are commented out in PHP).
var integrityAssets = []string{
	"static/css/app.min.css",
	"static/js/min/libs.min.js",
	"static/js/min/app.min.js",
	"static/js/min/openpgp.min.js",
}

// integrityEncodings are the content codings Integrity::test() checks, in the
// order the report lists them.
var integrityEncodings = []string{"gzip", "br"}

// Test is ServiceActions::ServiceTest(): the Integrity::test() diagnostics
// report of the running server.
//
// ServerNoCache() runs first, like in PHP. Integrity::test() echoes its report
// (no Content-Type header, so it stays text/html) and ServiceTest() returns an
// empty string; the handler writes the report directly so it stays composable,
// and answers the same HTML.
func (a *AppData) Test(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	httpx.NoStore(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, a.integrityReport(r))

	return true
}

// integrityReport is Integrity::test().
func (a *AppData) integrityReport(r *http.Request) string {
	var report strings.Builder

	// Integrity::phpVersion() and Integrity::phpExtensions() are the two error
	// blocks of the PHP report. A Go binary has no PHP version to check, and
	// openssl/mbstring/zlib/json/libxml/dom/fileinfo are part of the binary or
	// the standard library, so both checks are always green and print nothing.

	// MailSo\Base\Http::GetHost(false): the report has to address the server it
	// is rendered by.
	host := httpx.Host(r, false, false)
	if "" == host {
		report.WriteString(`<p style="color: red">Invalid Host header</p>`)
		return report.String()
	}

	base := httpx.Scheme(r) + "://" + host + a.options.Paths.VersionPath()

	// PHP appends ".gz"/".br" to the asset and HEADs that pre-compressed
	// sibling (Apache/mod_deflate); the Go backend compresses on the fly
	// (webmail.compress_output) and serves no sibling files, so the encoded
	// variants are requested with an Accept-Encoding header instead.
	report.WriteString(`<p>Assets are served from <code>` + html.EscapeString(base) +
		`</code>. The gzip/brotli variants are compressed on the fly by ` +
		`<code>webmail.compress_output</code> instead of being read from a ` +
		`pre-compressed <code>.gz</code>/<code>.br</code> sibling.</p>`)

	client := httpclient.New(a.options.Version)

	for _, asset := range integrityAssets {
		a.integrityAsset(&report, client, base, asset)
	}

	return report.String()
}

// integrityAsset runs the three probes of one file: the plain asset (size on
// disk versus content-length) and the two content codings.
func (a *AppData) integrityAsset(report *strings.Builder, client *httpclient.Client, base, asset string) {
	uri := base + asset

	fmt.Fprintf(report, "<h2>%s</h2>", html.EscapeString(uri))

	response, err := client.Do(http.MethodHead, uri, nil, nil)
	if a.integrityResponse(report, response, err) {
		size, found := a.integritySize(asset)

		switch {
		case !found:
			fmt.Fprintf(report, `<div style="color: red">asset file not found: %s</div>`,
				html.EscapeString(a.options.Paths.VersionFile(asset)))
		case size == int64(phpInt(response.GetHeader("content-length"))):
			fmt.Fprintf(report, "<div>content-length matches size %d</div>", size)
		default:
			fmt.Fprintf(report, `<div style="color: red">content-length mismatch, should be: %d</div>`,
				size)
		}
	}

	for _, encoding := range integrityEncodings {
		fmt.Fprintf(report, "<h3>%s encoded</h3>", integrityEncodingName(encoding))

		headers := http.Header{}
		headers.Set("Accept-Encoding", encoding)

		// The pipeline leaves HEAD requests uncompressed, so the coding can
		// only be observed with a GET; its body is discarded.
		client.StreamBodyTo(io.Discard)
		response, err := client.Do(http.MethodGet, uri, nil, headers)
		client.StreamBodyTo(nil)

		if !a.integrityResponse(report, response, err) {
			continue
		}

		if got := strings.ToLower(response.GetHeader("content-encoding")); got == encoding {
			report.WriteString("<div>content-encoding matches</div>")
		} else {
			fmt.Fprintf(report, `<div style="color: red">content-encoding mismatch, should be: %s</div>`,
				encoding)
		}
	}
}

// integrityResponse renders the "<details>" block of one probe and reports
// whether the response can be compared.
func (a *AppData) integrityResponse(
	report *strings.Builder,
	response *httpclient.Response,
	err error,
) bool {
	if nil != err || nil == response {
		message := "no response"
		if nil != err {
			message = err.Error()
		}

		fmt.Fprintf(report, `<div style="color: red">request failed: %s</div>`,
			html.EscapeString(message))
		return false
	}

	fmt.Fprintf(report, "<details><summary>Status: %d</summary><pre>%s</pre></details>",
		response.Status, html.EscapeString(integrityHeaderLines(response)))

	return true
}

// integrityHeaderLines renders $response->headers for the report: one lower
// case "name: value" line per header, sorted by name.
//
// Deviation from PHP: print_r() shows the order the headers arrived in, which
// net/http does not keep once the response is parsed into a map.
func integrityHeaderLines(response *httpclient.Response) string {
	names := make([]string, 0, len(response.Headers))
	for name := range response.Headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var lines strings.Builder
	for _, name := range names {
		for _, value := range response.Headers[name] {
			lines.WriteString(strings.ToLower(name) + ": " + value + "\n")
		}
	}

	return lines.String()
}

// integritySize is filesize(APP_VERSION_ROOT_PATH . $file); the boolean is
// false where PHP warns and returns false.
func (a *AppData) integritySize(asset string) (int64, bool) {
	info, err := os.Stat(a.options.Paths.VersionFile(asset))
	if nil != err || !info.Mode().IsRegular() {
		return 0, false
	}

	return info.Size(), true
}

// integrityEncodingName is the label of an encoding in the report: PHP writes
// "gzip encoded" and "brotli encoded".
func integrityEncodingName(encoding string) string {
	if "br" == encoding {
		return "brotli"
	}
	return encoding
}
