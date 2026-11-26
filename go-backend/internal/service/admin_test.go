package service

// Tests of the Step 27 admin session: the smadmin cookie + file cache marker
// round-trip across requests, the password check and the logout.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/config"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// adminFixture is one admin session installation under test.
type adminFixture struct {
	t           *testing.T
	application *config.Application
	crypter     *cryptox.Crypter
	cache       *storage.Cache
}

// newAdminFixture builds the configuration, the crypt engine and the file cache.
func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()

	application := config.NewApplication(
		filepath.Join(t.TempDir(), "application.ini"), testVersion, config.Defaults{})
	application.SetPassword("secret")

	driver, err := storage.NewCacheFileDriver(t.TempDir())
	if nil != err {
		t.Fatalf("cache driver: %v", err)
	}
	cache := storage.NewCache(driver)
	cache.SetCacheIndex("v1")

	return &adminFixture{
		t:           t,
		application: application,
		crypter:     cryptox.New(testSalt, testVersion, "aes-256-cbc"),
		cache:       cache,
	}
}

// admin builds an Admin for a request with the given cookies.
func (f *adminFixture) admin(cookies ...*http.Cookie) (*Admin, *httptest.ResponseRecorder) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	jar := cryptox.NewJar(request, cryptox.CookieOptions{})
	context := &account.Context{Salt: testSalt, Crypt: f.crypter}
	authenticator := auth.New(context, jar, recorder, nil)

	return NewAdmin(AdminOptions{
		Config:           f.application,
		Cache:            f.cache,
		Authenticator:    authenticator,
		ValidatePassword: f.application.ValidatePassword,
	}), recorder
}

// TestAdminLoginLogout checks the login, the persistence across requests and the
// logout.
func TestAdminLoginLogout(t *testing.T) {
	fixture := newAdminFixture(t)

	// A fresh request is not logged in.
	admin, recorder := fixture.admin()
	if loggedIn, _ := admin.IsLoggedIn(false); loggedIn {
		t.Fatal("a fresh request must not be logged in")
	}

	// The login sets the cookie and the cache marker.
	if err := admin.Login("admin", "secret", ""); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if loggedIn, _ := admin.IsLoggedIn(false); !loggedIn {
		t.Fatal("the login must be recognised in the same request")
	}

	// The next request carries the smadmin/smsession cookies, so a new Admin
	// recognises the session (the cookie + cache round-trip).
	cookies := recorder.Result().Cookies()
	next, nextRecorder := fixture.admin(cookies...)
	if loggedIn, _ := next.IsLoggedIn(false); !loggedIn {
		t.Fatalf("the session must survive a new request (%d cookies)", len(cookies))
	}

	// Logout drops the marker and clears the cookie.
	if err := next.Logout(); nil != err {
		t.Fatalf("Logout: %v", err)
	}
	if loggedIn, _ := next.IsLoggedIn(false); loggedIn {
		t.Fatal("the logout must end the session")
	}

	// A request built from the post-logout response is not logged in either.
	after, _ := fixture.admin(nextRecorder.Result().Cookies()...)
	if loggedIn, _ := after.IsLoggedIn(false); loggedIn {
		t.Fatal("a logged out session must not come back")
	}
}

// TestAdminLoginRejectsBadCredentials checks the AuthError of a wrong password
// and the allow_admin_panel gate.
func TestAdminLoginRejectsBadCredentials(t *testing.T) {
	fixture := newAdminFixture(t)
	admin, _ := fixture.admin()

	if err := admin.Login("admin", "wrong", ""); nil == err {
		t.Error("a wrong password must be rejected")
	}
	if err := admin.Login("", "secret", ""); nil == err {
		t.Error("an empty login must be rejected")
	}
	if err := admin.Login("nobody", "secret", ""); nil == err {
		t.Error("an unknown login must be rejected")
	}

	fixture.application.Set("security", "allow_admin_panel", false)
	if err := admin.Login("admin", "secret", ""); nil == err {
		t.Error("the login must be refused when the admin panel is disabled")
	}
}

// TestAdminLoginTOTP checks that a configured TOTP secret requires a code.
func TestAdminLoginTOTP(t *testing.T) {
	fixture := newAdminFixture(t)
	fixture.application.Set("security", "admin_totp", "JBSWY3DPEHPK3PXP")
	admin, _ := fixture.admin()

	if err := admin.Login("admin", "secret", ""); nil == err {
		t.Error("a missing TOTP code must be rejected")
	}
	if err := admin.Login("admin", "secret", "000000"); nil == err {
		t.Error("a wrong TOTP code must be rejected")
	}
}

// TestAdminAppDataBootstrapsPassword checks the first-run password seed of the
// not-logged-in AdminAppData branch.
func TestAdminAppDataBootstrapsPassword(t *testing.T) {
	application := config.NewApplication(
		filepath.Join(t.TempDir(), "application.ini"), testVersion, config.Defaults{})
	if "" != application.GetString("security", "admin_password", "") {
		t.Fatal("a fresh configuration must not have an admin password")
	}

	privateData := t.TempDir()
	appData := NewAppData(Options{
		Config:      application,
		Version:     testVersion,
		Salt:        testSalt,
		PrivateData: privateData,
		Admin:       fakeAdmin{loggedIn: false},
	})

	document := appData.Build(true, nil)
	if false != phpBool(document["Auth"]) {
		t.Fatal("Auth must be false for a logged out admin")
	}

	if _, err := os.Stat(filepath.Join(privateData, "admin_password.txt")); nil != err {
		t.Errorf("the weak password file was not written: %v", err)
	}
	first := application.GetString("security", "admin_password", "")
	if "" == first {
		t.Fatal("the admin password hash was not stored")
	}

	// A second visit keeps the generated password.
	appData.Build(true, nil)
	if second := application.GetString("security", "admin_password", ""); second != first {
		t.Error("the admin password must not be regenerated once set")
	}
}
