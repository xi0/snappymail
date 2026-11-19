package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The APP_SALT/APP_VERSION/cipher and smctoken values of the tests. They must
// match the PHP cross-check script.
const (
	testSalt    = "b7c1d0f8f0d94c0ab6f0a3a1f0c0ffee"
	testVersion = "0.0.0"
	testCipher  = "aes-256-cbc"
	testToken   = "testtoken"

	testEmail    = "user@example.com"
	testPassword = "secret"
)

// testEnv is the per-installation state the requests share: the storage folder,
// the crypt engine, the domain configs, application.ini and the settings.
type testEnv struct {
	t        *testing.T
	storage  *storage.Provider
	crypt    *cryptox.Crypter
	domains  *fakeDomains
	logger   *logRecorder
	config   *testConfig
	settings *fakeSettings
	hooks    *hookRecorder
	// now is the fixed clock of the login flow (login.fault_delay, SSO).
	now time.Time
	// slept records the pauses of login.fault_delay.
	slept []time.Duration
}

// fakeDomains is a DomainProvider without a domains/ folder.
//
// A wildcard config is returned for any name (or for wildName only), so the
// "determine with wildcard" fallback of resolveLoginCredentials() can be
// exercised without the domain provider's Load() options.
type fakeDomains struct {
	configs  map[string]*domain.Domain
	wildcard *domain.Domain
	wildName string
}

func (f *fakeDomains) Load(name string, options ...domain.LoadOption) *domain.Domain {
	if config, ok := f.configs[name]; ok {
		return config
	}
	// A wildcard config is only used when the caller asks for it, like
	// DefaultDomain::Load($name, $bFindWithWildCard).
	if 0 < len(options) && nil != f.wildcard && ("" == f.wildName || f.wildName == name) {
		return f.wildcard
	}
	return nil
}

func (f *fakeDomains) GetByEmailAddress(email string) (*domain.Domain, error) {
	if config := f.Load(account.EmailDomain(email), domain.WithWildcard()); nil != config {
		return config, nil
	}
	return nil, appctx.NewClientError(appctx.DomainNotAllowed, nil, email)
}

// testConfig is a minimal application.ini for the login tests: the values are
// keyed by "section.key" and a missing entry yields the PHP default.
type testConfig struct {
	strings map[string]string
	bools   map[string]bool
	ints    map[string]int
}

func newTestConfig() *testConfig {
	return &testConfig{
		strings: map[string]string{},
		bools:   map[string]bool{},
		ints:    map[string]int{},
	}
}

func (c *testConfig) GetString(sectionName, key, defaultValue string) string {
	if value, ok := c.strings[sectionName+"."+key]; ok {
		return value
	}
	return defaultValue
}

func (c *testConfig) GetBool(sectionName, key string, defaultValue bool) bool {
	if value, ok := c.bools[sectionName+"."+key]; ok {
		return value
	}
	return defaultValue
}

func (c *testConfig) GetInt(sectionName, key string, defaultValue int) int {
	if value, ok := c.ints[sectionName+"."+key]; ok {
		return value
	}
	return defaultValue
}

// fakeSettings is Actions::SettingsProvider(): the settings document per
// account, kept in memory.
type fakeSettings struct {
	documents map[string]*storage.Settings
	saves     int
}

func newFakeSettings() *fakeSettings {
	return &fakeSettings{documents: map[string]*storage.Settings{}}
}

func (s *fakeSettings) Load(account storage.Account) *storage.Settings {
	if document, ok := s.documents[account.Email()]; ok {
		return document
	}
	document := storage.NewSettings()
	s.documents[account.Email()] = document
	return document
}

func (s *fakeSettings) Save(account storage.Account, settings *storage.Settings) error {
	s.documents[account.Email()] = settings
	s.saves++
	return nil
}

// logRecorder captures the SnappyMail\Log lines of the authenticator.
type logRecorder struct {
	lines []string
}

func (l *logRecorder) Write(desc string, level logx.Level, name string) bool {
	l.lines = append(l.lines, name+": "+desc)
	return true
}

func (l *logRecorder) has(substring string) bool {
	for _, line := range l.lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

// newTestEnv builds the shared installation state in a temporary folder.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	crypter := cryptox.New(testSalt, testVersion, testCipher)
	crypter.DefaultPassphrase = func() (string, error) { return testToken, nil }

	return &testEnv{
		t:       t,
		storage: storage.NewProvider(storage.NewFileStorage(t.TempDir(), false)),
		crypt:   crypter,
		domains: &fakeDomains{configs: map[string]*domain.Domain{
			"example.com":    testDomain("example.com"),
			"second.example": testDomain("second.example"),
		}},
		logger:   &logRecorder{},
		config:   newTestConfig(),
		settings: newFakeSettings(),
		now:      time.Unix(1700000000, 0),
	}
}

// context builds a fresh account.Context, like a new request would.
func (e *testEnv) context() *account.Context {
	return &account.Context{
		Salt:    testSalt,
		Storage: e.storage,
		Domains: e.domains,
		Crypt:   e.crypt,
		Plugins: e.plugins(),
	}
}

// plugins is the hook recorder every request shares.
func (e *testEnv) plugins() *hookRecorder {
	if nil == e.hooks {
		e.hooks = &hookRecorder{}
	}
	return e.hooks
}

// hookRecorder records the plugin hooks the login flow runs and lets a test
// rewrite hook arguments like a real plugin does.
type hookRecorder struct {
	runs     []string
	rewriter func(name string, args ...any)
}

func (h *hookRecorder) RunHook(name string, args ...any) {
	h.runs = append(h.runs, name)
	if nil != h.rewriter {
		h.rewriter(name, args...)
	}
}

// has reports whether a hook ran at least once.
func (h *hookRecorder) has(name string) bool {
	for _, run := range h.runs {
		if run == name {
			return true
		}
	}
	return false
}

// loginFor builds the login flow of one request.
func (e *testEnv) loginFor(request *http.Request) (*Login, *httptest.ResponseRecorder) {
	authenticator, recorder := e.authenticator(request)
	return e.loginWith(authenticator), recorder
}

// loginWith builds the login flow on top of an existing authenticator.
func (e *testEnv) loginWith(authenticator *Authenticator) *Login {
	return &Login{
		Context:  authenticator.Context,
		Session:  authenticator,
		Config:   e.config,
		Logger:   e.logger,
		Host:     "webmail.example.com",
		HTTPHost: "webmail.example.com:1443",
		Settings: e.settings,
		Start:    e.now,
		Now:      func() time.Time { return e.now },
		Sleep:    func(d time.Duration) { e.slept = append(e.slept, d) },
	}
}

// authenticator builds the authenticator of one request.
func (e *testEnv) authenticator(request *http.Request) (*Authenticator, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()

	var jar *cryptox.Jar
	if nil != request {
		jar = cryptox.NewJar(request, cryptox.CookieOptions{})
	} else {
		jar = cryptox.NewJar(nil, cryptox.CookieOptions{})
	}

	return New(e.context(), jar, recorder, e.logger), recorder
}

// testDomain builds a domain config with IMAP/SMTP hosts.
func testDomain(name string) *domain.Domain {
	config := domain.New(name, domain.Defaults{})
	config.IMAP.Host = "imap." + name
	config.IMAP.Port = 993
	config.SMTP.Host = "smtp." + name
	config.SMTP.Port = 465
	return config
}

// mainAccount builds the account of user@example.com.
func (e *testEnv) mainAccount() *account.MainAccount {
	main := account.NewMainAccount()
	main.SetCredentials(testDomain("example.com"), testEmail, testEmail, testPassword, "", "")
	return main
}

// additionalAccount builds an additional account of add@second.example.
func (e *testEnv) additionalAccount() *account.AdditionalAccount {
	additional := account.NewAdditionalAccount()
	additional.SetCredentials(testDomain("second.example"), "add@second.example", "add@second.example", "add-secret", "", "")
	return additional
}

// login runs the parts of a login that matter here: the SESSION storage entry
// and the smaccount cookie. Step 8 calls exactly these two.
//
// It returns the request of the next page load, the response of the login and
// the session storage key.
func (e *testEnv) login(main *account.MainAccount) (*http.Request, *httptest.ResponseRecorder, string) {
	e.t.Helper()

	authenticator, recorder := e.authenticator(nil)
	if err := authenticator.SetAuthToken(main); nil != err {
		e.t.Fatalf("SetAuthToken: %v", err)
	}
	if err := authenticator.SetAdditionalAuthToken(nil); nil != err {
		e.t.Fatalf("SetAdditionalAuthToken: %v", err)
	}

	// PHP: StorageProvider()->Put($oAccount, SESSION, Utils::GetSessionToken(), 'true')
	session := authenticator.SessionToken(true)
	if err := e.storage.Put(main, storage.TypeSession, session, "true"); nil != err {
		e.t.Fatalf("store the session: %v", err)
	}

	return requestFromCookies(recorder), recorder, session
}

// requestFromCookies builds the next request from the cookies a response set.
func requestFromCookies(recorder *httptest.ResponseRecorder) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	for _, cookie := range recorder.Result().Cookies() {
		if "" != cookie.Value {
			request.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}

	return request
}

// requestWith builds a request carrying the given cookies.
func requestWith(cookies map[string]string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range cookies {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	return request
}

// cookieLine reports whether the recorder wrote a Set-Cookie line starting with
// prefix (an empty value means the cookie was cleared).
func cookieLine(recorder *httptest.ResponseRecorder, prefix string) bool {
	for _, line := range recorder.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// clientCode returns the notification code of a client error.
func clientCode(t *testing.T, err error) int {
	t.Helper()

	var clientError *appctx.ClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("error is not a ClientError: %v", err)
	}
	return clientError.Code
}
