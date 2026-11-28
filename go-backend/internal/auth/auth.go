// Package auth ports the session and connection token layer of SnappyMail:
// the token helpers of \RainLoop\Utils and the account resolution of
// \RainLoop\Actions\UserAuth (getMainAccountFromToken, getAccountFromToken,
// SetAuthToken, SetAdditionalAuthToken, Logout and switchAccount).
//
// A request authenticates with two cookies: smsession (the raw session token)
// and smaccount (the encrypted account). The raw token is never part of the
// storage path; the storage key is sha1('Session' . APP_SALT . token . 'Token' .
// APP_SALT), written as "true" into the SESSION storage of the account on login.
// Deleting that entry from the PHP backend therefore kicks the Go session and
// vice versa, which is exactly the server side session control the two backends
// need while they run side by side.
//
// The login itself (credentials, IMAP validation, sign-me) is step 8: it plugs
// into this package through the SignMe callback and SwitchOptions.Verify, and
// the additional account list of switchAccount comes from step 24. Everything
// else is a one-to-one port, including the quirks:
//
//   - a swallowed failure is cached, so a later GetMainAccountFromToken(true)
//     still reports "no account" instead of the original error;
//   - Logout() clears the cookies but keeps the smsession cookie and the SESSION
//     storage entry;
//   - a missing SESSION value logs get_debug_type()'s "null" while an unusable
//     one logs "string", like the PHP notices.
package auth

import (
	"net/http"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The cookie names of \RainLoop\Utils and \RainLoop\Actions.
const (
	// SessionTokenName is Utils::SESSION_TOKEN.
	SessionTokenName = "smsession"
	// ConnectionTokenName is Utils::CONNECTION_TOKEN.
	ConnectionTokenName = "smtoken"
	// AuthSpecTokenKey is Actions::AUTH_SPEC_TOKEN_KEY (the main account).
	AuthSpecTokenKey = "smaccount"
	// AuthAdditionalTokenKey is Actions::AUTH_ADDITIONAL_TOKEN_KEY.
	AuthAdditionalTokenKey = "smadditional"
	// AuthSignMeTokenKey is Actions::AUTH_SIGN_ME_TOKEN_KEY.
	AuthSignMeTokenKey = "smremember"
	// AuthMailtoTokenKey is Actions::AUTH_MAILTO_TOKEN_KEY.
	AuthMailtoTokenKey = "smmailtoauth"
)

// ConnectionTokenLifetime is the 30 day lifetime the smtoken cookie gets.
const ConnectionTokenLifetime = 30 * 24 * time.Hour

// logName is the "TOKENS" log category of the token helpers.
const logName = "TOKENS"

// Logger is the \SnappyMail\Log surface the token helpers write to;
// *logx.Logger satisfies it.
type Logger interface {
	Write(desc string, level logx.Level, name string) bool
}

// Authenticator resolves the accounts of one request. It caches the resolved
// main and additional account, like the private properties of
// \RainLoop\Actions.
//
// An Authenticator is scoped to one request and not safe for concurrent use.
type Authenticator struct {
	// Context is the request environment (salt, storage, crypt, domains, hooks).
	Context *account.Context
	// Cookies is the cookie jar of the request.
	Cookies *cryptox.Jar
	// Response receives the cookie writes; nil keeps them in memory only, which
	// is what the tests use.
	Response http.ResponseWriter
	// Logger writes the SnappyMail\Log lines of the token checks.
	Logger Logger
	// SignMe resolves a sign-me ("remember me") cookie into a main account
	// (step 8). A nil callback disables the fallback, which is what PHP does
	// when there is no smremember cookie.
	SignMe func() (*account.MainAccount, error)
	// Rand is the randomness source of new tokens (nil: crypto/rand).
	Rand randomSource

	// mainResolved mirrors "false !== oMainAuthAccount".
	mainResolved bool
	main         *account.MainAccount
	// additionalResolved mirrors "false !== oAdditionalAuthAccount".
	additionalResolved bool
	additional         *account.AdditionalAccount
}

// New builds the authenticator of one request and wires Context.MainAccount, so
// the account models can resolve the parent account of an additional account
// exactly like Actions::getMainAccountFromToken() does. An already set callback
// (a test double) is left alone.
func New(ctx *account.Context, cookies *cryptox.Jar, response http.ResponseWriter, logger Logger) *Authenticator {
	authenticator := &Authenticator{
		Context:  ctx,
		Cookies:  cookies,
		Response: response,
		Logger:   logger,
	}

	if nil != ctx && nil == ctx.MainAccount {
		ctx.MainAccount = func() (*account.MainAccount, error) {
			return authenticator.GetMainAccountFromToken(false)
		}
	}

	return authenticator
}

// MainAccount returns the resolved main account, or nil.
func (a *Authenticator) MainAccount() *account.MainAccount { return a.main }

// AccountContext is the account Context of the request (APP_SALT, the storage
// and crypt providers). It is the Go stand-in for the parts of
// \RainLoop\Actions the account models reach through \RainLoop\Api globals, and
// the handler and plugin layers resolve their copy through it.
func (a *Authenticator) AccountContext() *account.Context {
	if nil == a {
		return nil
	}
	return a.Context
}

// AdditionalAccount returns the resolved additional account, or nil.
func (a *Authenticator) AdditionalAccount() *account.AdditionalAccount { return a.additional }

// GetMainAccountFromToken is Actions::getMainAccountFromToken().
//
// The result (including "there is no account") is cached for the request. Like
// the PHP method, a failure that was swallowed with throw=false is *not*
// reported again by a later call with throw=true.
func (a *Authenticator) GetMainAccountFromToken(throw bool) (*account.MainAccount, error) {
	if a.mainResolved {
		return a.main, nil
	}

	a.mainResolved = true
	a.main = nil

	if err := a.resolveMainAccount(throw); nil != err {
		if throw {
			return nil, err
		}
		return nil, nil
	}

	return a.main, nil
}

// resolveMainAccount is the try block of getMainAccountFromToken().
func (a *Authenticator) resolveMainAccount(throw bool) error {
	data := a.secureCookie(AuthSpecTokenKey)

	if truthyToken(data) {
		// Server side control/kickout of logged in sessions.
		token := a.SessionToken(false)
		if "" == token {
			if a.hasCookie(SessionTokenName) {
				a.notice("SESSION_TOKEN invalid")
			} else {
				a.notice("SESSION_TOKEN not set")
			}
		} else {
			resolved, err := account.NewMainAccountFromToken(a.Context, data, throw)
			if nil != err {
				return err
			}

			if nil != resolved {
				if value, ok := a.storageGet(resolved, storage.TypeSession, token); ok && tokenValueValid(value) {
					a.main = resolved
				} else {
					a.storageClear(resolved, storage.TypeSession, token)
					a.notice("SESSION_TOKEN value invalid: " + debugType(value, ok))
				}
			} else {
				a.notice("AUTH_SPEC_TOKEN_KEY invalid")
			}
		}

		if nil == a.main {
			a.clearCookie(SessionTokenName)
			a.Logout(true)
			return appctx.NewClientError(appctx.InvalidToken, nil, "Session gone")
		}

		return nil
	}

	// No smaccount cookie: try the "remember me" token of step 8.
	if nil != a.SignMe {
		resolved, err := a.SignMe()
		if nil != err {
			return err
		}
		if nil != resolved {
			a.storagePut(resolved, storage.TypeSession, a.SessionToken(true), "true")
			if err := a.SetAuthToken(resolved); nil != err {
				return err
			}
		}
	}

	if nil == a.main {
		return appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	return nil
}

// GetAccountFromToken is Actions::getAccountFromToken(): the additional account
// when one is selected, else the main account, else nil.
func (a *Authenticator) GetAccountFromToken(throw bool) (*account.Account, error) {
	if _, err := a.GetMainAccountFromToken(throw); nil != err {
		return nil, err
	}

	if !a.additionalResolved && a.hasCookie(AuthAdditionalTokenKey) {
		data := a.secureCookie(AuthAdditionalTokenKey)

		resolved, err := account.NewAdditionalAccountFromToken(a.Context, data, throw)
		if nil != err {
			return nil, err
		}

		a.additionalResolved = true
		a.additional = resolved

		if nil == resolved {
			a.clearCookie(AuthAdditionalTokenKey)
		}
	}

	if nil != a.additional {
		return &a.additional.Account, nil
	}
	if nil != a.main {
		return &a.main.Account, nil
	}

	return nil, nil
}

// SetMainAuthAccount is Actions::SetMainAuthAccount(): selecting the main
// account drops a previously selected additional account.
func (a *Authenticator) SetMainAuthAccount(main *account.MainAccount) {
	a.additionalResolved = true
	a.additional = nil
	a.mainResolved = true
	a.main = main
}

// SetAuthToken is Actions::SetAuthToken(): store the main account in the
// encrypted smaccount cookie.
func (a *Authenticator) SetAuthToken(main *account.MainAccount) error {
	a.SetMainAuthAccount(main)

	if nil == main {
		return a.setSecureCookie(AuthSpecTokenKey, nil)
	}
	return a.setSecureCookie(AuthSpecTokenKey, main)
}

// SetAdditionalAuthToken is Actions::SetAdditionalAuthToken().
func (a *Authenticator) SetAdditionalAuthToken(additional *account.AdditionalAccount) error {
	a.additionalResolved = true
	a.additional = additional

	if nil == additional {
		return a.setSecureCookie(AuthAdditionalTokenKey, nil)
	}
	return a.setSecureCookie(AuthAdditionalTokenKey, additional)
}

// Logout is Actions\UserAuth::Logout(): it clears the cookies of the selected
// additional account and, for $main, of the main account.
//
// The smsession cookie and the SESSION storage entry stay, exactly like PHP
// (the comment there notes the sign-me data is deliberately kept as well).
func (a *Authenticator) Logout(main bool) {
	a.clearCookie(AuthAdditionalTokenKey)
	if main {
		a.clearCookie(AuthSpecTokenKey)
	}
}

// SwitchOptions carries the parts of Actions\UserAuth::switchAccount() that
// belong to other steps.
type SwitchOptions struct {
	// AdditionalAccountsEnabled is GetCapa(Capa::ADDITIONAL_ACCOUNTS).
	AdditionalAccountsEnabled bool
	// Accounts is Actions::GetAccounts() (step 24): the token arrays of the
	// additional accounts, keyed by address.
	Accounts func(main *account.MainAccount) (map[string]any, error)
	// Verify is the IMAP login test of the new account (step 8).
	Verify func(additional *account.AdditionalAccount) error
	// NoCache is Http()->ServerNoCache(), called before anything else.
	NoCache func()
}

// SwitchAccount is Actions\UserAuth::switchAccount().
//
// Partial: the accounts list (step 24) and the IMAP login test (step 8) are
// injected; without them switching to another account reports
// AccountDoesNotExist like a missing entry does in PHP.
func (a *Authenticator) SwitchAccount(email string, options SwitchOptions) (bool, error) {
	if nil != options.NoCache {
		options.NoCache()
	}

	main, _ := a.GetMainAccountFromToken(false)
	if "" == email || nil == main || !options.AdditionalAccountsEnabled {
		return false, nil
	}

	var additional *account.AdditionalAccount

	if main.Email() != email {
		email = asciiEmail(email)

		var accounts map[string]any
		if nil != options.Accounts {
			var err error
			if accounts, err = options.Accounts(main); nil != err {
				return false, err
			}
		}

		token, ok := accounts[email]
		if !ok {
			return false, appctx.NewClientError(appctx.AccountDoesNotExist, nil, "")
		}

		resolved, err := account.NewAdditionalAccountFromToken(a.Context, token, true)
		if nil != err {
			return false, appctx.NewClientError(appctx.AccountSwitchFailed, err, "")
		}
		if nil == resolved {
			return false, appctx.NewClientError(appctx.AccountSwitchFailed, nil, "")
		}

		if nil != options.Verify {
			if err := options.Verify(resolved); nil != err {
				return false, err
			}
		}

		additional = resolved
	}

	if err := a.SetAdditionalAuthToken(additional); nil != err {
		return false, err
	}

	return true, nil
}
