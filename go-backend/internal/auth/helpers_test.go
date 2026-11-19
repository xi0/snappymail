package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
// the crypt engine and the domain configs.
type testEnv struct {
	t       *testing.T
	storage *storage.Provider
	crypt   *cryptox.Crypter
	domains *fakeDomains
	logger  *logRecorder
}

// fakeDomains is a DomainProvider without a domains/ folder.
type fakeDomains struct {
	configs map[string]*domain.Domain
}

func (f *fakeDomains) Load(name string, _ ...domain.LoadOption) *domain.Domain {
	return f.configs[name]
}

func (f *fakeDomains) GetByEmailAddress(email string) (*domain.Domain, error) {
	if config, ok := f.configs[account.EmailDomain(email)]; ok {
		return config, nil
	}
	return nil, appctx.NewClientError(appctx.DomainNotAllowed, nil, email)
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
			"example.com":  testDomain("example.com"),
			"second.example": testDomain("second.example"),
		}},
		logger: &logRecorder{},
	}
}

// context builds a fresh account.Context, like a new request would.
func (e *testEnv) context() *account.Context {
	return &account.Context{
		Salt:    testSalt,
		Storage: e.storage,
		Domains: e.domains,
		Crypt:   e.crypt,
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
