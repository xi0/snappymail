package auth

import (
	"crypto/rand"
	"io"
	"strconv"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// randomSource is the randomness source of generated tokens (io.Reader).
type randomSource interface {
	Read(p []byte) (n int, err error)
}

// randomTokenSize is the number of random bytes of MailSo\Base\Utils::Sha1Rand().
const randomTokenSize = 16

// salt is APP_SALT.
func (a *Authenticator) salt() string {
	if nil == a || nil == a.Context {
		return ""
	}
	return a.Context.Salt
}

// crypter is the SnappyMail\Crypt of the request.
func (a *Authenticator) crypter() *cryptox.Crypter {
	if nil == a || nil == a.Context {
		return nil
	}
	return a.Context.Crypt
}

// hasCookie is isset($_COOKIE[$name]).
func (a *Authenticator) hasCookie(name string) bool {
	return nil != a && nil != a.Cookies && a.Cookies.Has(name)
}

// cookie is Cookies::get(), "" when the cookie is absent.
func (a *Authenticator) cookie(name string) string {
	if nil == a || nil == a.Cookies {
		return ""
	}
	return a.Cookies.Get(name)
}

// setCookie is Cookies::set().
func (a *Authenticator) setCookie(name, value string, expires time.Time) {
	if nil == a || nil == a.Cookies {
		return
	}
	a.Cookies.Set(a.Response, name, value, expires, true)
}

// clearCookie is Cookies::clear().
func (a *Authenticator) clearCookie(name string) {
	if nil == a || nil == a.Cookies {
		return
	}
	a.Cookies.Clear(a.Response, name)
}

// secureCookie is Cookies::getSecure(): the decrypted data, or nil when the
// cookie is missing or cannot be decrypted (PHP's DecryptFromJSON() returns
// null in that case as well).
func (a *Authenticator) secureCookie(name string) any {
	if !a.hasCookie(name) {
		return nil
	}
	crypter := a.crypter()
	if nil == crypter {
		return nil
	}

	value, _, err := a.Cookies.GetSecure(crypter, name)
	if nil != err {
		return nil
	}
	return value
}

// setSecureCookie is Cookies::setSecure(); a nil data value clears the cookie.
func (a *Authenticator) setSecureCookie(name string, data any) error {
	if nil == a || nil == a.Cookies {
		return nil
	}
	crypter := a.crypter()
	if nil == crypter {
		return nil
	}
	return a.Cookies.SetSecure(crypter, a.Response, name, data)
}

// storageGet is StorageProvider()->Get().
func (a *Authenticator) storageGet(account any, storageType storage.StorageType, key string) (string, bool) {
	if nil == a.Context || nil == a.Context.Storage {
		return "", false
	}
	return a.Context.Storage.Get(account, storageType, key)
}

// storagePut is StorageProvider()->Put(); the PHP callers ignore the result.
func (a *Authenticator) storagePut(account any, storageType storage.StorageType, key, value string) {
	if nil == a.Context || nil == a.Context.Storage {
		return
	}
	_ = a.Context.Storage.Put(account, storageType, key, value)
}

// storageClear is StorageProvider()->Clear().
func (a *Authenticator) storageClear(account any, storageType storage.StorageType, key string) bool {
	if nil == a.Context || nil == a.Context.Storage {
		return false
	}
	return a.Context.Storage.Clear(account, storageType, key)
}

// notice writes a SnappyMail\Log::notice() line of the TOKENS category.
func (a *Authenticator) notice(message string) {
	if nil != a && nil != a.Logger {
		a.Logger.Write(message, logx.Notice, logName)
	}
}

// debug writes a SnappyMail\Log::debug() line of the TOKENS category.
func (a *Authenticator) debug(message string) {
	if nil != a && nil != a.Logger {
		a.Logger.Write(message, logx.Debug, logName)
	}
}

// SessionToken is Utils::GetSessionToken(): the storage key of the session.
//
// The cookie holds a random value, the returned token is its sha1 with APP_SALT
// on both sides. A missing cookie with generate=false yields "", PHP's null.
func (a *Authenticator) SessionToken(generate bool) string {
	raw := a.cookie(SessionTokenName)

	if "" == raw {
		if !generate {
			return ""
		}

		a.debug("New SESSION_TOKEN")
		raw = a.newRandomToken()
		// A session cookie, like Cookies::set() without an expire argument.
		a.setCookie(SessionTokenName, raw, time.Time{})
	}

	salt := a.salt()
	return storage.SHA1("Session" + salt + raw + "Token" + salt)
}

// ConnectionToken is Utils::GetConnectionToken().
//
// A logged in account gets a token derived from its hash ("1-" main, "2-"
// additional), a guest gets one derived from the 30 day smtoken cookie ("0-").
func (a *Authenticator) ConnectionToken() string {
	if resolved, err := a.GetAccountFromToken(false); nil == err && nil != resolved {
		hash := resolved.Hash(a.salt())
		if nil != a.additional {
			return "2-" + storage.SHA1(a.salt()+hash)
		}
		return "1-" + storage.SHA1(a.salt()+hash)
	}

	raw := a.cookie(ConnectionTokenName)
	if "" == raw {
		raw = a.newRandomToken()
		a.setCookie(ConnectionTokenName, raw, time.Now().Add(ConnectionTokenLifetime))
	}

	salt := a.salt()
	return "0-" + storage.SHA1("Connection"+salt+raw+"Token"+salt)
}

// CSRFToken is Utils::GetCsrfToken(), which is the connection token.
func (a *Authenticator) CSRFToken() string { return a.ConnectionToken() }

// UpdateConnectionToken is Utils::UpdateConnectionToken(): refresh the 30 day
// lifetime of an existing smtoken cookie.
func (a *Authenticator) UpdateConnectionToken() {
	raw := a.cookie(ConnectionTokenName)
	if "" == raw {
		return
	}
	a.setCookie(ConnectionTokenName, raw, time.Now().Add(ConnectionTokenLifetime))
}

// EncodeKeyValuesQ is Utils::EncodeKeyValuesQ(): the url-safe blob used by the
// Raw endpoints and the mailto token.
func (a *Authenticator) EncodeKeyValuesQ(values any, customKey string) (string, error) {
	crypter := a.crypter()
	if nil == crypter {
		return "", cryptox.ErrNoPassphrase
	}
	return crypter.EncryptURLSafe(values, storage.SHA1(a.salt()+customKey+"Q"+a.SessionToken(true)))
}

// DecodeKeyValuesQ is Utils::DecodeKeyValuesQ(); a blob that cannot be decoded
// yields an empty document, like the PHP "?: array()".
func (a *Authenticator) DecodeKeyValuesQ(encoded, customKey string) map[string]any {
	crypter := a.crypter()
	if nil == crypter {
		return map[string]any{}
	}

	value, err := crypter.DecryptURLSafe(encoded, storage.SHA1(a.salt()+customKey+"Q"+a.SessionToken(false)))
	if nil != err {
		return map[string]any{}
	}

	if document, ok := value.(map[string]any); ok {
		return document
	}
	return map[string]any{}
}

// newRandomToken is MailSo\Base\Utils::Sha1Rand(APP_SALT).
func (a *Authenticator) newRandomToken() string {
	reader := a.Rand
	if nil == reader {
		reader = rand.Reader
	}

	buffer := make([]byte, randomTokenSize)
	if _, err := io.ReadFull(reader, buffer); nil != err {
		// crypto/rand does not fail in practice. A time based fallback keeps
		// the token unique instead of handing out an empty one.
		return storage.SHA1(a.salt() + strconv.FormatInt(time.Now().UnixNano(), 10))
	}

	return storage.SHA1(a.salt() + string(buffer))
}

// asciiEmail is SnappyMail\IDN::emailToAscii().
func asciiEmail(email string) string { return domain.EmailToASCII(email) }

// truthyToken is PHP's "if ($aData)" for the decrypted cookie payload: an
// account document is a non-empty array, anything else is not usable.
func truthyToken(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return "" != typed && "0" != typed
	case bool:
		return typed
	case int:
		return 0 != typed
	case int64:
		return 0 != typed
	case float64:
		return 0 != typed
	case []any:
		return 0 < len(typed)
	case map[string]any:
		return 0 < len(typed)
	}
	return true
}

// tokenValueValid is the "if ($sTokenValue)" check of the SESSION storage read:
// the entry is written as the string "true".
func tokenValueValid(value string) bool {
	return "" != value && "0" != value
}

// debugType is PHP's get_debug_type() for the value of a storage read that the
// PHP provider reports as string or null.
func debugType(value string, found bool) string {
	if found {
		return "string"
	}
	return "null"
}
