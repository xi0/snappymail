package service

// The admin panel session of the Go backend, the port of
// \RainLoop\Actions\Admin (IsAdminLoggined / getAdminAuthKey) and the
// setAdminAuthToken()/Logout() halves of ActionsAdmin.
//
// The admin login is independent of a mail account: it is a random key written
// into the (unprefixed) file cache under KeyPathHelper::SessionAdminKey() and an
// encrypted ["token", key] blob in the smadmin cookie. Because both the cookie
// format (SnappyMail\Crypt) and the cache layout are shared with PHP, an admin
// session created by one backend is recognised by the other while they run side
// by side.

import (
	cryptorand "crypto/rand"
	"errors"
	"strconv"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// adminTokenKey is Actions\Admin::$AUTH_ADMIN_TOKEN_KEY.
const adminTokenKey = "smadmin"

// Errors reported by the token helpers, mirroring the RuntimeException messages
// of ActionsAdmin::setAdminAuthToken().
var (
	errAdminNoCache = errors.New("failed to store admin token")
	errAdminNoCrypt = errors.New("failed to encode admin token")
)

// AdminOptions is the per-request state of the admin session.
type AdminOptions struct {
	// Config is configs/application.ini (security.* / admin_panel.*).
	Config Config
	// Cache is Actions::Cacher(null, true): the unprefixed file cache that holds
	// the admin session markers.
	Cache *storage.Cache
	// Authenticator provides the cookie jar, the crypt engine, APP_SALT and the
	// session token the cookie blob is keyed with.
	Authenticator *auth.Authenticator
	// ValidatePassword is Application::ValidatePassword().
	ValidatePassword func(password string) bool
}

// Admin is the admin panel session of one request. It satisfies the handler's
// admin gate (IsLoggedIn) and offers the login/logout the admin actions need.
//
// An Admin is request scoped and not safe for concurrent use.
type Admin struct {
	options AdminOptions
}

// NewAdmin builds the admin session of one request.
func NewAdmin(options AdminOptions) *Admin { return &Admin{options: options} }

// IsLoggedIn is Actions\Admin::IsAdminLoggined():
//
//	if ($this->Config()->Get('security', 'allow_admin_panel', true)) {
//		$sAdminKey = $this->getAdminAuthKey();
//		if ($sAdminKey && $this->Cacher(null, true)->Get(SessionAdminKey($sAdminKey))) {
//			return true;
//		}
//	}
//	if ($bThrowExceptionOnFalse) throw new ClientException(Notifications::AuthError);
//	return false;
func (a *Admin) IsLoggedIn(throw bool) (bool, error) {
	if nil != a && nil != a.options.Config &&
		a.options.Config.GetBool("security", "allow_admin_panel", true) &&
		nil != a.options.Authenticator {
		if key := a.authKey(); "" != key && nil != a.options.Cache &&
			a.options.Cache.Exists(storage.SessionAdminKey(key)) {
			return true, nil
		}
	}

	if throw {
		return false, appctx.NewClientError(appctx.AuthError, nil, "")
	}
	return false, nil
}

// Login is the credential check of ActionsAdmin::DoAdminLogin() plus
// setAdminAuthToken().
//
// PHP:
//
//	if (!strlen($sLogin) || !strlen($oPassword) || !allow_admin_panel
//	 || $sLogin !== admin_login || !ValidatePassword($oPassword)
//	 || ($totp && !TOTP::Verify($totp, $TOTP))) {
//		LoggerAuthHelper(...); loginErrorDelay(); throw AuthError;
//	}
//	$sToken = $this->setAdminAuthToken();
func (a *Admin) Login(login, password, passcode string) error {
	if nil == a {
		return appctx.NewClientError(appctx.AuthError, nil, "")
	}

	config := a.options.Config
	secret := ""
	if nil != config {
		secret = config.GetString("security", "admin_totp", "")
	}

	valid := nil != config &&
		"" != login && "" != password &&
		config.GetBool("security", "allow_admin_panel", true) &&
		login == config.GetString("security", "admin_login", "") &&
		a.validatePassword(password) &&
		("" == secret || ("" != passcode && totp.Validate(passcode, secret)))

	if !valid {
		return appctx.NewClientError(appctx.AuthError, nil, "")
	}

	if _, err := a.setToken(); nil != err {
		return appctx.NewClientError(appctx.UnknownError, err, "")
	}

	return nil
}

// Logout is ActionsAdmin::DoAdminLogout(): drop the cache marker and clear the
// cookie.
func (a *Admin) Logout() error {
	if nil == a || nil == a.options.Authenticator {
		return nil
	}

	if key := a.authKey(); "" != key && nil != a.options.Cache {
		_ = a.options.Cache.Delete(storage.SessionAdminKey(key))
	}
	a.clearCookie()

	return nil
}

// ValidatePassword exposes Application::ValidatePassword() to the password
// update action.
func (a *Admin) ValidatePassword(password string) bool {
	return a.validatePassword(password)
}

// validatePassword calls the configured validator.
func (a *Admin) validatePassword(password string) bool {
	if nil == a || nil == a.options.ValidatePassword {
		return false
	}
	return a.options.ValidatePassword(password)
}

// setToken is ActionsAdmin::setAdminAuthToken(): store a random marker in the
// cache, encode ["token", marker] into the cookie.
func (a *Admin) setToken() (string, error) {
	if nil == a.options.Cache {
		return "", errAdminNoCache
	}

	marker := a.newMarker()
	if err := a.options.Cache.Set(storage.SessionAdminKey(marker),
		strconv.FormatInt(time.Now().Unix(), 10)); nil != err {
		return "", errAdminNoCache
	}

	token, err := a.encode([]any{"token", marker})
	if nil != err || "" == token {
		return "", errAdminNoCrypt
	}

	a.setCookie(token)
	return token, nil
}

// newMarker is MailSo\Base\Utils::Sha1Rand() without the salt: sha1(random).
func (a *Admin) newMarker() string {
	buffer := make([]byte, 16)
	if _, err := cryptorand.Read(buffer); nil != err {
		return storage.SHA1(strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	return storage.SHA1(string(buffer))
}

// authKey is Actions\Admin::getAdminAuthKey():
//
//	$cookie = Cookies::get('smadmin');
//	if ($cookie) {
//		$aAdminHash = Utils::DecodeKeyValuesQ($cookie);
//		if (!empty($aAdminHash[1]) && 'token' === $aAdminHash[0]) return $aAdminHash[1];
//		Cookies::clear('smadmin');
//	}
//	return '';
func (a *Admin) authKey() string {
	if nil == a || nil == a.options.Authenticator || nil == a.options.Authenticator.Cookies {
		return ""
	}

	cookie := a.options.Authenticator.Cookies.Get(adminTokenKey)
	if "" == cookie {
		return ""
	}

	if values, ok := a.decode(cookie).([]any); ok && 2 <= len(values) {
		if "token" == phpString(values[0]) && "" != phpString(values[1]) {
			return phpString(values[1])
		}
	}

	a.clearCookie()
	return ""
}

// encode is Utils::EncodeKeyValuesQ() with the empty custom key.
func (a *Admin) encode(values any) (string, error) {
	crypter := a.crypter()
	if nil == crypter {
		return "", errAdminNoCrypt
	}
	return crypter.EncryptURLSafe(values, a.cryptKey(true))
}

// decode is Utils::DecodeKeyValuesQ() returning the raw decoded value.
func (a *Admin) decode(encoded string) any {
	crypter := a.crypter()
	if nil == crypter {
		return nil
	}
	value, err := crypter.DecryptURLSafe(encoded, a.cryptKey(false))
	if nil != err {
		return nil
	}
	return value
}

// cryptKey is the sha1(APP_SALT . customKey . 'Q' . SessionToken()) key of
// Utils::EncodeKeyValuesQ()/DecodeKeyValuesQ() with the empty custom key.
func (a *Admin) cryptKey(generate bool) string {
	if nil == a || nil == a.options.Authenticator {
		return ""
	}
	salt := ""
	if nil != a.options.Authenticator.Context {
		salt = a.options.Authenticator.Context.Salt
	}
	return storage.SHA1(salt + "Q" + a.options.Authenticator.SessionToken(generate))
}

// crypter is SnappyMail\Crypt of the request, or nil.
func (a *Admin) crypter() cryptoCrypter {
	if nil == a || nil == a.options.Authenticator || nil == a.options.Authenticator.Context {
		return nil
	}
	return a.options.Authenticator.Context.Crypt
}

// setCookie is Cookies::set('smadmin', $value).
func (a *Admin) setCookie(value string) {
	if nil == a || nil == a.options.Authenticator || nil == a.options.Authenticator.Cookies {
		return
	}
	a.options.Authenticator.Cookies.Set(
		a.options.Authenticator.Response, adminTokenKey, value, time.Time{}, true)
}

// clearCookie is Cookies::clear('smadmin').
func (a *Admin) clearCookie() {
	if nil == a || nil == a.options.Authenticator || nil == a.options.Authenticator.Cookies {
		return
	}
	a.options.Authenticator.Cookies.Clear(a.options.Authenticator.Response, adminTokenKey)
}

// cryptoCrypter is the small SnappyMail\Crypt surface the admin cookie needs;
// *cryptox.Crypter satisfies it (kept as an interface so this file stays free of
// the cryptox import).
type cryptoCrypter interface {
	EncryptURLSafe(data any, key string) (string, error)
	DecryptURLSafe(data, key string) (any, error)
}
