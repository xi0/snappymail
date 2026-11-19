package account

import (
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The APP_SALT/APP_VERSION/cipher and smctoken values the tests run with. They
// must match the ones the PHP cross-check script uses.
const (
	testSalt    = "b7c1d0f8f0d94c0ab6f0a3a1f0c0ffee"
	testVersion = "0.0.0"
	testCipher  = "aes-256-cbc"
	testToken   = "testtoken"
)

// testCrypter builds the SnappyMail\Crypt of the tests: the smctoken cookie
// value is fixed, so an empty key (cookies) resolves to "testtoken" + version.
func testCrypter() *cryptox.Crypter {
	crypter := cryptox.New(testSalt, testVersion, testCipher)
	crypter.DefaultPassphrase = func() (string, error) { return testToken, nil }
	return crypter
}

// testStorage builds a file backed storage provider in a temporary folder.
func testStorage(t *testing.T) *storage.Provider {
	t.Helper()
	return storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))
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

// fakeDomains is a DomainProvider without a domains/ folder.
type fakeDomains struct {
	configs map[string]*domain.Domain
	err     error
}

func newFakeDomains(configs ...*domain.Domain) *fakeDomains {
	index := make(map[string]*domain.Domain, len(configs))
	for _, config := range configs {
		index[config.Name] = config
	}
	return &fakeDomains{configs: index}
}

func (f *fakeDomains) Load(name string, _ ...domain.LoadOption) *domain.Domain {
	return f.configs[name]
}

func (f *fakeDomains) GetByEmailAddress(email string) (*domain.Domain, error) {
	if nil != f.err {
		return nil, f.err
	}
	if config, ok := f.configs[EmailDomain(email)]; ok {
		return config, nil
	}
	return nil, appctx.NewClientError(appctx.DomainNotAllowed, nil, email)
}

// hookRecorder records the plugin hooks that ran.
type hookRecorder struct {
	names []string
	args  [][]any
	// on runs after a hook was recorded, so a test can mutate the argument.
	on func(name string, args []any)
}

func (h *hookRecorder) RunHook(name string, args ...any) {
	h.names = append(h.names, name)
	h.args = append(h.args, args)
	if nil != h.on {
		h.on(name, args)
	}
}

func (h *hookRecorder) called(name string) bool {
	for _, recorded := range h.names {
		if recorded == name {
			return true
		}
	}
	return false
}

// calledWith reports whether a hook ran with a single argument of type T.
func (h *hookRecorder) firstArg(name string) any {
	for index, recorded := range h.names {
		if recorded == name && 0 < len(h.args[index]) {
			return h.args[index][0]
		}
	}
	return nil
}

// logRecorder records the debug lines of the account models.
type logRecorder struct {
	lines []string
}

func (l *logRecorder) logf(category, message string) {
	l.lines = append(l.lines, category+": "+message)
}

func (l *logRecorder) has(substring string) bool {
	for _, line := range l.lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

// newTestContext builds a Context with a temporary storage provider, an
// in-memory domain provider and the test crypter.
func newTestContext(t *testing.T, configs ...*domain.Domain) (*Context, *hookRecorder, *logRecorder) {
	t.Helper()

	hooks := &hookRecorder{}
	logs := &logRecorder{}

	context := &Context{
		Salt:    testSalt,
		Storage: testStorage(t),
		Domains: newFakeDomains(configs...),
		Crypt:   testCrypter(),
		Plugins: hooks,
		Log:     logs.logf,
	}

	return context, hooks, logs
}

// newTestAccount builds a main account for user@example.com.
func newTestAccount(context *Context) *Account {
	account := NewAccount()
	account.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")
	return account
}
