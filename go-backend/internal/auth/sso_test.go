package auth

// Tests of the single-use SSO hash of ServiceActions::ServiceSso().

import (
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// withCache gives the login flow a file cache, like Actions::Cacher().
func (e *testEnv) withCache(t *testing.T) *Login {
	t.Helper()

	driver, err := storage.NewCacheFileDriver(t.TempDir())
	if nil != err {
		t.Fatalf("NewCacheFileDriver: %v", err)
	}

	login, _ := e.loginFor(nil)
	login.Cache = storage.NewCache(driver).SetCacheIndex("")
	return login
}

// storeSsoHash writes the cache entry API::CreateUserSsoHash() writes.
func storeSsoHash(t *testing.T, env *testEnv, login *Login, hash string, payload map[string]any) {
	t.Helper()

	sealed, err := env.crypt.EncryptToJSON(payload, hash)
	if nil != err {
		t.Fatalf("EncryptToJSON: %v", err)
	}
	if err := login.Cache.Set(storage.SsoCacheKey(hash), sealed); nil != err {
		t.Fatalf("cache the SSO hash: %v", err)
	}
}

func TestDoSso(t *testing.T) {
	env := newTestEnv(t)
	login := env.withCache(t)

	const hash = "sso-hash-1"
	storeSsoHash(t, env, login, hash, map[string]any{
		"Email":             testEmail,
		"Password":          testPassword,
		"AdditionalOptions": map[string]any{"language": "da"},
		"Time":              env.now.Unix(),
	})

	login.ValidateLanguage = func(language string) string {
		if "da" == language {
			return "da"
		}
		return ""
	}

	main := login.DoSso(hash)
	if nil == main {
		t.Fatal("DoSso did not log in")
	}
	if testEmail != main.Email() {
		t.Errorf("email = %q", main.Email())
	}

	// The session was started.
	if value, ok := env.storage.Get(main, storage.TypeSession, login.Session.SessionToken(false)); !ok || "true" != value {
		t.Errorf("the SESSION entry = %q, %v", value, ok)
	}

	// The AdditionalOptions language was stored.
	if settings := env.settings.documents[testEmail]; nil == settings || "da" != settings.GetConf("language", "") {
		t.Errorf("the SSO language was not stored: %v", settings)
	}

	// Single use: the cache entry is gone.
	if login.Cache.Exists(storage.SsoCacheKey(hash)) {
		t.Error("the SSO hash was not consumed")
	}
	if nil != login.DoSso(hash) {
		t.Error("a consumed SSO hash logged in again")
	}
}

func TestDoSsoTimeouts(t *testing.T) {
	env := newTestEnv(t)
	login := env.withCache(t)

	// An old hash is refused.
	storeSsoHash(t, env, login, "old", map[string]any{
		"Email":    testEmail,
		"Password": testPassword,
		"Time":     env.now.Add(-60 * time.Second).Unix(),
	})
	if nil != login.DoSso("old") {
		t.Error("an expired SSO hash must not log in")
	}

	// Time 0 never expires (API::CreateUserSsoHash($bUseTimeout = false)).
	storeSsoHash(t, env, login, "forever", map[string]any{
		"Email":    testEmail,
		"Password": testPassword,
		"Time":     0,
	})
	if nil == login.DoSso("forever") {
		t.Error("a hash without a timeout must log in")
	}

	// A hash that is still inside the 10 second window is accepted.
	storeSsoHash(t, env, login, "fresh", map[string]any{
		"Email":    testEmail,
		"Password": testPassword,
		"Time":     env.now.Add(-5 * time.Second).Unix(),
	})
	if nil == login.DoSso("fresh") {
		t.Error("a fresh SSO hash must log in")
	}
}

func TestDoSsoRejectsBrokenPayloads(t *testing.T) {
	env := newTestEnv(t)
	login := env.withCache(t)

	// An unknown hash is a no-op.
	if nil != login.DoSso("unknown") {
		t.Error("an unknown hash must not log in")
	}
	if nil != login.DoSso("") {
		t.Error("an empty hash must not log in")
	}

	// A blob that does not decrypt with the hash.
	if err := login.Cache.Set(storage.SsoCacheKey("broken"), `["c29kaXVt","",""]`); nil != err {
		t.Fatalf("cache: %v", err)
	}
	if nil != login.DoSso("broken") {
		t.Error("an undecryptable hash must not log in")
	}

	// A payload without a password or with an unknown domain.
	storeSsoHash(t, env, login, "nopass", map[string]any{
		"Email": testEmail,
		"Time":  env.now.Unix(),
	})
	if nil != login.DoSso("nopass") {
		t.Error("a payload without a password must not log in")
	}

	storeSsoHash(t, env, login, "nodomain", map[string]any{
		"Email":    "user@unknown.tld",
		"Password": testPassword,
		"Time":     env.now.Unix(),
	})
	if nil != login.DoSso("nodomain") {
		t.Error("a payload for an unknown domain must not log in")
	}
	if !env.logger.has("LOGIN: DomainNotAllowed[109]") {
		t.Errorf("the failed login was not logged: %v", env.logger.lines)
	}
}

func TestDoSsoWithoutCache(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// Actions::Cacher() is always available in PHP; a nil cache disables the
	// endpoint instead of panicking.
	if nil != login.DoSso("anything") {
		t.Error("DoSso without a cache must not log in")
	}
}
