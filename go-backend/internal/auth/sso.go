package auth

// The single-use SSO login of ServiceActions::ServiceSso().
//
// A hash produced by \RainLoop\API::CreateUserSsoHash() (a PHP application that
// embeds SnappyMail) is not a session: it is a cache entry
// (/Sso/Data/<hash>/Login/) holding the credentials encrypted with the hash
// itself, valid for 10 seconds unless "Time" is 0. ServiceSso() reads it, drops
// it again (single use), logs the user in and redirects to "./" - the redirect
// is the caller's job, so this method only reports the account.
//
// PHP swallows every failure (it logs the exception and still redirects), so
// DoSso() reports nil instead of an error.

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// ssoTimeWindow is the \time() - 10 < $aData['Time'] window of ServiceSso().
const ssoTimeWindow = 10

// DoSso is ServiceActions::ServiceSso(): log in with the credentials behind an
// SSO hash, and report the account (nil when the hash is unknown, expired,
// unreadable or the login failed).
//
// The language of the AdditionalOptions is stored in the account settings, like
// the $aAdditionalOptions block of the PHP method.
func (l *Login) DoSso(hash string) *account.MainAccount {
	if nil == l || "" == hash || nil == l.Cache {
		return nil
	}

	crypter := l.crypter()
	if nil == crypter {
		return nil
	}

	key := storage.SsoCacheKey(hash)
	stored, ok := l.Cache.Get(key)
	if !ok || "" == stored {
		return nil
	}

	value, err := crypter.DecryptFromJSON(stored, hash)

	// Single use: the entry is dropped before the credentials are used.
	_ = l.Cache.Delete(key)

	if nil != err {
		return nil
	}

	document, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	email := valueString(document["Email"])
	password, hasPassword := document["Password"]
	created, hasTime := document["Time"]
	if "" == email || !hasPassword || !hasTime {
		return nil
	}

	// (0 === $aData['Time'] || \time() - 10 < $aData['Time'])
	if 0 != valueInt(created) && !(valueInt(created)+ssoTimeWindow > l.now().Unix()) {
		return nil
	}

	main, err := l.LoginProcess(trimValue(email), valueString(password))
	if nil != err {
		l.logf(logx.Warning, err.Error())
		return nil
	}

	if options, ok := document["AdditionalOptions"].(map[string]any); ok && 0 < len(options) {
		// isset($aAdditionalOptions['language']) ? ... : ''
		if language, exists := options["language"]; exists {
			l.persistLanguage(main, valueString(language))
		}
	}

	return main
}
