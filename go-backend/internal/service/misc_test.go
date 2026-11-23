package service

// Tests of step 13a: ?/Mailto, ?/Sso and ?/CspReport.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// miscTestHost is $_SERVER['HTTP_HOST'] of the requests.
const miscTestHost = "webmail.example.com"

// logSink captures the lines the endpoints write through logx.
type logSink struct {
	lines []string
}

// Write is the logx Driver interface.
func (s *logSink) Write(desc string, level logx.Level, name string) bool {
	s.lines = append(s.lines, name+"|"+level.Name()+"|"+desc)
	return true
}

// has reports whether a line contains substring.
func (s *logSink) has(substring string) bool {
	for _, line := range s.lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

// miscDomains is Providers\Domain for the SSO login flow.
type miscDomains struct {
	config *domain.Domain
}

// Load is Providers\Domain::Load().
func (d miscDomains) Load(name string, _ ...domain.LoadOption) *domain.Domain {
	if nil != d.config && d.config.Name == name {
		return d.config
	}
	return nil
}

// GetByEmailAddress is Providers\Domain::getByEmailAddress().
func (d miscDomains) GetByEmailAddress(email string) (*domain.Domain, error) {
	if nil != d.config && d.config.Name == account.EmailDomain(email) {
		return d.config, nil
	}
	return nil, appctx.NewClientError(appctx.DomainNotAllowed, nil, email)
}

// miscFixture is an AppData installation with a real cookie/account stack, the
// file storage/settings providers, the SSO cache and a capturing logger.
type miscFixture struct {
	t        *testing.T
	tree     testTree
	appData  *AppData
	crypter  *cryptox.Crypter
	cache    *storage.Cache
	storage  *storage.Provider
	settings *storage.SettingsStorage
	sink     *logSink
}

// newMiscFixture builds the installation of the endpoints.
func newMiscFixture(t *testing.T) *miscFixture {
	t.Helper()

	fixture := newFixture(t)

	crypter := cryptox.New(testSalt, testVersion, "aes-256-cbc")
	crypter.DefaultPassphrase = func() (string, error) { return "passphrase", nil }

	storageDir := filepath.Join(fixture.tree.root, "storage")
	provider := storage.NewProvider(storage.NewFileStorage(storageDir, false))

	driver, err := storage.NewCacheFileDriver(filepath.Join(fixture.tree.root, "cache"))
	if nil != err {
		t.Fatalf("NewCacheFileDriver: %v", err)
	}
	cache := storage.NewCache(driver).SetCacheIndex("")

	sink := &logSink{}
	logger := logx.New(logx.Debug).Append(sink)

	settings := storage.NewSettingsStorage(provider)
	domains := miscDomains{config: domain.New(account.EmailDomain(testEmail), domain.Defaults{})}

	fixture.options.Settings = settings
	fixture.options.Cache = cache
	fixture.options.Logger = logger
	fixture.options.NewSession = func(w http.ResponseWriter, r *http.Request) *Session {
		jar := cryptox.NewJar(r, cryptox.CookieOptions{})
		return &Session{
			Request:       r,
			Response:      w,
			Jar:           jar,
			Authenticator: auth.New(&account.Context{
				Salt:    testSalt,
				Storage: provider,
				Domains: domains,
				Crypt:   crypter,
			}, jar, w, nil),
		}
	}
	fixture.appData = NewAppData(fixture.options)

	return &miscFixture{
		t:        t,
		tree:     fixture.tree,
		appData:  fixture.appData,
		crypter:  crypter,
		cache:    cache,
		storage:  provider,
		settings: settings,
		sink:     sink,
	}
}

// mailtoRequest builds the "?mailto&to=…" request the frontend registers
// (app.js registerMailto()).
func mailtoRequest(target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/?mailto&to="+target, nil)
	request.Host = miscTestHost
	return request
}

// ssoRequest builds a "?/Sso&hash=…" request.
func ssoRequest(hash string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/?/Sso&hash="+hash, nil)
	request.Host = miscTestHost
	return request
}

// requestFrom builds the follow-up request carrying the cookies of a response.
func requestFrom(recorder *httptest.ResponseRecorder) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	for _, cookie := range recorder.Result().Cookies() {
		if "" != cookie.Value {
			request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}

	return request
}

// cookieValue returns the value of a cookie written by the response.
func cookieValue(t *testing.T, recorder *httptest.ResponseRecorder, name string) (string, bool) {
	t.Helper()

	for _, cookie := range recorder.Result().Cookies() {
		if name == cookie.Name {
			return cookie.Value, true
		}
	}
	return "", false
}

func TestMailtoSetsTheSingleUseCookie(t *testing.T) {
	fixture := newMiscFixture(t)

	const mailto = "mailto:user@example.com?subject=Hi"
	recorder := httptest.NewRecorder()

	if !fixture.appData.Mailto(recorder, mailtoRequest("mailto:user@example.com?subject=Hi"),
		&web.Route{Paths: []string{"Mailto", "0"}}) {
		t.Fatal("Mailto must report the request as handled")
	}

	if http.StatusFound != recorder.Code {
		t.Fatalf("status = %d, want 302", recorder.Code)
	}
	if "./" != recorder.Header().Get("Location") {
		t.Errorf("Location = %q, want ./", recorder.Header().Get("Location"))
	}
	// ServerNoCache() runs before the redirect.
	if "no-store" != recorder.Header().Get("Cache-Control") {
		t.Errorf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if "" != recorder.Body.String() {
		t.Errorf("body = %q, want nothing", recorder.Body.String())
	}

	token, ok := cookieValue(t, recorder, auth.AuthMailtoTokenKey)
	if !ok || "" == token {
		t.Fatalf("the %s cookie was not set", auth.AuthMailtoTokenKey)
	}

	// The cookie is the EncodeKeyValuesQ() blob AppData::mailTo() decodes; the
	// key includes the smsession token, so the next request has to send back the
	// cookies of this response (EncodeKeyValuesQ/GetSessionToken() create the
	// session cookie when it is missing).
	session := fixture.appData.NewSession(recorder, requestFrom(recorder))
	decoded := session.Authenticator.DecodeKeyValuesQ(token, "")
	if "MailTo" != decoded["MailTo"] {
		t.Errorf("MailTo = %v, want MailTo", decoded["MailTo"])
	}
	if mailto != decoded["To"] {
		t.Errorf("To = %v, want %s", decoded["To"], mailto)
	}
	if !phptimeIsMicrotime(t, decoded["Time"]) {
		t.Errorf("Time = %#v, want a microtime(true) value", decoded["Time"])
	}
}

// phptimeIsMicrotime reports whether value looks like microtime(true): an epoch
// timestamp, fractional like the PHP float.
func phptimeIsMicrotime(t *testing.T, value any) bool {
	t.Helper()

	var stamp float64
	switch typed := value.(type) {
	case float64:
		stamp = typed
	case int64:
		// decodeJSON() keeps an integral number as int64; microtime(true) only
		// loses its fraction when it is exactly on a second.
		stamp = float64(typed)
	default:
		return false
	}

	now := float64(time.Now().Unix())

	return stamp > now-60 && stamp < now+60
}

func TestMailtoIgnoresOtherTargets(t *testing.T) {
	fixture := newMiscFixture(t)

	for _, target := range []string{"", "http://example.com", "MAILTO", "mailto"} {
		recorder := httptest.NewRecorder()
		request := mailtoRequest(target)
		if "" == target {
			request = httptest.NewRequest(http.MethodGet, "/?mailto", nil)
		}

		fixture.appData.Mailto(recorder, request, &web.Route{Paths: []string{"Mailto"}})

		if http.StatusFound != recorder.Code || "./" != recorder.Header().Get("Location") {
			t.Errorf("%q: no redirect (%d %q)", target, recorder.Code, recorder.Header().Get("Location"))
		}
		// "mailto" alone does not match '/^mailto:/i'.
		if _, ok := cookieValue(t, recorder, auth.AuthMailtoTokenKey); ok {
			t.Errorf("%q must not set the mailto cookie", target)
		}
	}

	// The scheme prefix is case-insensitive.
	recorder := httptest.NewRecorder()
	fixture.appData.Mailto(recorder, mailtoRequest("MAILTO:user@example.com"), &web.Route{})
	if _, ok := cookieValue(t, recorder, auth.AuthMailtoTokenKey); !ok {
		t.Error("an upper case MAILTO: target must set the cookie")
	}
}

// storeSsoHash writes the cache entry API::CreateUserSsoHash() writes.
func storeSsoHash(t *testing.T, fixture *miscFixture, hash string, payload map[string]any) {
	t.Helper()

	sealed, err := fixture.crypter.EncryptToJSON(payload, hash)
	if nil != err {
		t.Fatalf("EncryptToJSON: %v", err)
	}
	if err := fixture.cache.Set(storage.SsoCacheKey(hash), sealed); nil != err {
		t.Fatalf("cache the SSO hash: %v", err)
	}
}

func TestSsoLogsInOnce(t *testing.T) {
	fixture := newMiscFixture(t)

	const hash = "sso-hash-1"
	storeSsoHash(t, fixture, hash, map[string]any{
		"Email":             testEmail,
		"Password":          "secret",
		"AdditionalOptions": map[string]any{"language": "da"},
		"Time":              time.Now().Unix(),
	})

	recorder := httptest.NewRecorder()
	if !fixture.appData.Sso(recorder, ssoRequest(hash), &web.Route{Paths: []string{"Sso", "0"}}) {
		t.Fatal("Sso must report the request as handled")
	}

	if http.StatusFound != recorder.Code || "./" != recorder.Header().Get("Location") {
		t.Fatalf("status = %d, Location = %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if "no-store" != recorder.Header().Get("Cache-Control") {
		t.Errorf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}

	// The login cookies were written (SetAuthToken -> smaccount).
	if _, ok := cookieValue(t, recorder, auth.AuthSpecTokenKey); !ok {
		t.Error("the login did not set the smaccount cookie")
	}

	// AdditionalOptions.language was stored in the account settings.
	if settings := fixture.settings.Load(mainAccount()); "da" != phpString(settings.GetConf("language", "")) {
		t.Errorf("the SSO language was not stored: %v", settings.GetConf("language", ""))
	}

	// Single use: the cache entry is gone and a second visit is a no-op.
	if fixture.cache.Exists(storage.SsoCacheKey(hash)) {
		t.Error("the SSO hash was not consumed")
	}

	second := httptest.NewRecorder()
	fixture.appData.Sso(second, ssoRequest(hash), &web.Route{Paths: []string{"Sso", "0"}})
	if http.StatusFound != second.Code {
		t.Errorf("the second visit must still redirect, status = %d", second.Code)
	}
	if _, ok := cookieValue(t, second, auth.AuthSpecTokenKey); ok {
		t.Error("a consumed SSO hash must not log in again")
	}
}

func TestSsoWithoutHashRedirects(t *testing.T) {
	fixture := newMiscFixture(t)

	for _, target := range []string{"/?/Sso", "/?/Sso&hash=", "/?/Sso&hash=0"} {
		recorder := httptest.NewRecorder()
		fixture.appData.Sso(recorder, httptest.NewRequest(http.MethodGet, target, nil), &web.Route{})

		if http.StatusFound != recorder.Code || "./" != recorder.Header().Get("Location") {
			t.Errorf("%s: no redirect (%d %q)", target, recorder.Code, recorder.Header().Get("Location"))
		}
		if _, ok := cookieValue(t, recorder, auth.AuthSpecTokenKey); ok {
			t.Errorf("%s must not log in", target)
		}
	}
}

func TestSsoAcceptsTheHashFromAPostBody(t *testing.T) {
	fixture := newMiscFixture(t)

	const hash = "sso-post"
	storeSsoHash(t, fixture, hash, map[string]any{
		"Email":    testEmail,
		"Password": "secret",
		"Time":     0, // API::CreateUserSsoHash($bUseTimeout = false)
	})

	request := httptest.NewRequest(http.MethodPost, "/?/Sso",
		strings.NewReader("hash="+hash))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder := httptest.NewRecorder()
	fixture.appData.Sso(recorder, request, &web.Route{Paths: []string{"Sso"}})

	if _, ok := cookieValue(t, recorder, auth.AuthSpecTokenKey); !ok {
		t.Error("the hash of the POST body was not used")
	}
}

func cspRequest(payload string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/?/CspReport", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/csp-report")
	return request
}

func TestCspReportLogsTheViolation(t *testing.T) {
	fixture := newMiscFixture(t)

	const body = `{"csp-report":{"document-uri":"https://mail.example.com/",` +
		`"violated-directive":"script-src","source-file":"https://evil.example.com/x.js"}}`

	recorder := httptest.NewRecorder()
	if !fixture.appData.CspReport(recorder, cspRequest(body), &web.Route{Paths: []string{"CspReport"}}) {
		t.Fatal("CspReport must report the request as handled")
	}

	if http.StatusNoContent != recorder.Code {
		t.Errorf("status = %d, want 204", recorder.Code)
	}
	if "" != recorder.Body.String() {
		t.Errorf("body = %q, want nothing", recorder.Body.String())
	}

	// SnappyMail\Log::error('CSP', $json): LOG_ERR with the CSP category.
	if !fixture.sink.has(cspLogName + "|" + logx.Error.Name() + "|" + body) {
		t.Errorf("the report was not logged: %v", fixture.sink.lines)
	}
}

func TestCspReportSkipsExtensionReports(t *testing.T) {
	fixture := newMiscFixture(t)

	// Firefox reports a violated extension script with the bare
	// "moz-extension" source, which CSP::logReport() compares exactly.
	const body = `{"csp-report":{"source-file":"moz-extension"}}`

	recorder := httptest.NewRecorder()
	fixture.appData.CspReport(recorder, cspRequest(body), &web.Route{})

	if http.StatusNoContent != recorder.Code {
		t.Errorf("status = %d, want 204", recorder.Code)
	}
	if 0 != len(fixture.sink.lines) {
		t.Errorf("a moz-extension report must not be logged: %v", fixture.sink.lines)
	}
}

func TestCspReportLogsEverythingElse(t *testing.T) {
	fixture := newMiscFixture(t)

	// An empty body is the "$json" false case.
	fixture.appData.CspReport(httptest.NewRecorder(), cspRequest(""), &web.Route{})
	if 0 != len(fixture.sink.lines) {
		t.Errorf("an empty body must not be logged: %v", fixture.sink.lines)
	}

	// A body that is not JSON is the "$report" false case.
	fixture.appData.CspReport(httptest.NewRecorder(), cspRequest("not json"), &web.Route{})
	if 0 != len(fixture.sink.lines) {
		t.Errorf("a non-JSON body must not be logged: %v", fixture.sink.lines)
	}

	// A report without "csp-report" is still logged (PHP: the undefined array
	// key compares unequal to 'moz-extension').
	fixture.appData.CspReport(httptest.NewRecorder(), cspRequest(`{"other":1}`), &web.Route{})
	if !fixture.sink.has(`{"other":1}`) {
		t.Errorf("a report without csp-report must be logged: %v", fixture.sink.lines)
	}

	// A "moz-extension://…" source is *not* the bare 'moz-extension' string, so
	// the exact comparison of logReport() logs it. The port keeps that quirk.
	const urlSource = `{"csp-report":{"source-file":"moz-extension://abc/content.js"}}`
	fixture.appData.CspReport(httptest.NewRecorder(), cspRequest(urlSource), &web.Route{})
	if !fixture.sink.has(urlSource) {
		t.Errorf("a moz-extension:// source was not logged: %v", fixture.sink.lines)
	}
}

func TestRegisterAddsTheStep13aEndpoints(t *testing.T) {
	fixture := newMiscFixture(t)

	router := web.NewRouter(web.DefaultConfig(), nil, newTestTree(t).paths(), nil)
	Register(router, fixture.appData)

	// ?mailto&to=… (the frontend spelling)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, mailtoRequest("mailto:user@example.com"))
	if http.StatusFound != recorder.Code || "./" != recorder.Header().Get("Location") {
		t.Fatalf("GET ?mailto&to=… = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if _, ok := cookieValue(t, recorder, auth.AuthMailtoTokenKey); !ok {
		t.Error("?mailto&to=… must set the mailto cookie")
	}

	// ?/Sso&hash=… without a known hash still redirects.
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, ssoRequest("unknown"))
	if http.StatusFound != recorder.Code || "./" != recorder.Header().Get("Location") {
		t.Fatalf("GET ?/Sso&hash=… = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}

	// ?/CspReport
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, cspRequest(`{"csp-report":{"source-file":"x.js"}}`))
	if http.StatusNoContent != recorder.Code {
		t.Fatalf("POST ?/CspReport = %d, want 204", recorder.Code)
	}
}
