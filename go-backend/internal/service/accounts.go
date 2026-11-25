package service

// The additional account surface of \RainLoop\Actions\Accounts (Step 24),
// implemented on top of the request's auth.Authenticator and the storage
// providers the AppData endpoints already use.
//
// main builds one of these per request (see handler.Options.Accounts) so the
// dispatcher stays independent of the cookie/auth stack: the action handlers in
// internal/handler only see the small Accounts interface.

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// AccountsOptions is the installation-level state of the accounts provider.
type AccountsOptions struct {
	// Config is configs/application.ini.
	Config Config
	// Logger receives the login/connect lines of DoAccountSetup.
	Logger *logx.Logger
	// Plugins runs the login.* / imap.* hooks (step 28); nil runs none.
	Plugins account.Hooks
	// Settings is Actions::SettingsProvider() (the login language persistence).
	Settings auth.SettingsStore
	// Storage is Actions::StorageProvider(): the main account's CONFIG folder
	// that keeps the "additionalaccounts" document.
	Storage *storage.Provider
	// ValidateLanguage is Actions::ValidateLanguage().
	ValidateLanguage func(language string) string
	// IMAPLogin is Account::ImapConnectAndLogin(): validates the credentials of
	// an additional account. A nil callback skips the check.
	IMAPLogin func(target *account.Account) error
	// Unread is loadAdditionalAccountImapClient() + FolderStatus('INBOX'): the
	// UNSEEN counter of an additional account. A nil callback disables the
	// DoAccountUnread action.
	Unread func(target *account.Account) (int, error)
}

// Accounts is the handler.Accounts implementation of one request.
type Accounts struct {
	authenticator *auth.Authenticator
	options       AccountsOptions
}

// NewAccounts builds the additional account surface of one request. A nil
// authenticator makes every method report the InvalidToken failure, which is
// what a request without a session amounts to.
func NewAccounts(authenticator *auth.Authenticator, options AccountsOptions) *Accounts {
	return &Accounts{authenticator: authenticator, options: options}
}

// Main is Actions::getMainAccountFromToken().
func (a *Accounts) Main() (*account.MainAccount, error) {
	if nil == a || nil == a.authenticator {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	return a.authenticator.GetMainAccountFromToken(false)
}

// Current is Actions::getAccountFromToken().
func (a *Accounts) Current() (*account.Account, error) {
	if nil == a || nil == a.authenticator {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	return a.authenticator.GetAccountFromToken(false)
}

// GetAccounts is Actions::GetAccounts().
func (a *Accounts) GetAccounts(main *account.MainAccount) (*account.Accounts, error) {
	if nil == a {
		return account.NewAccounts(), nil
	}
	return account.LoadAccounts(a.options.Storage, main), nil
}

// SetAccounts is Actions::SetAccounts().
func (a *Accounts) SetAccounts(main *account.MainAccount, accounts *account.Accounts) error {
	if nil == a {
		return nil
	}
	return account.SaveAccounts(a.options.Storage, main, accounts)
}

// NewAdditional is AdditionalAccount::NewInstanceFromTokenArray().
func (a *Accounts) NewAdditional(data any, throw bool) (*account.AdditionalAccount, error) {
	if nil == a || nil == a.authenticator {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	return account.NewAdditionalAccountFromToken(a.authenticator.Context, data, throw)
}

// Seal is AdditionalAccount::asTokenArray($main).
func (a *Accounts) Seal(additional *account.AdditionalAccount, main *account.MainAccount) (map[string]any, error) {
	if nil == a || nil == a.authenticator {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	if nil == additional {
		return nil, nil
	}
	return additional.AsTokenArray(a.authenticator.Context, main)
}

// Login is Actions::LoginProcess() with $bMainAccount = false.
func (a *Accounts) Login(email, password string) (*account.AdditionalAccount, error) {
	login := a.newLogin()
	if nil == login {
		return nil, appctx.NewClientError(appctx.InvalidInputArgument, nil, "")
	}
	return login.LoginAdditionalProcess(email, password)
}

// Unread is Actions\Accounts::loadAdditionalAccountImapClient()'s
// FolderStatus('INBOX') lookup.
func (a *Accounts) Unread(additional *account.AdditionalAccount) (int, error) {
	if nil == a || nil == a.options.Unread || nil == additional {
		return 0, appctx.NewClientError(appctx.ConnectionError, nil, "")
	}
	return a.options.Unread(&additional.Account)
}

// Switch is Actions\UserAuth::switchAccount().
func (a *Accounts) Switch(email string) (bool, error) {
	if nil == a || nil == a.authenticator {
		return false, nil
	}

	options := auth.SwitchOptions{
		AdditionalAccountsEnabled: a.additionalAccountsEnabled(),
		Accounts: func(main *account.MainAccount) (map[string]any, error) {
			return account.LoadAccounts(a.options.Storage, main).ToMap(), nil
		},
		NoCache: func() {
			if nil != a.authenticator.Response {
				httpx.NoStore(a.authenticator.Response)
			}
		},
	}

	if nil != a.options.IMAPLogin {
		options.Verify = func(additional *account.AdditionalAccount) error {
			return a.options.IMAPLogin(&additional.Account)
		}
	}

	return a.authenticator.SwitchAccount(email, options)
}

// ClearAdditional is Cookies::clear(AUTH_ADDITIONAL_TOKEN_KEY).
func (a *Accounts) ClearAdditional() error {
	if nil == a || nil == a.authenticator {
		return nil
	}
	return a.authenticator.SetAdditionalAuthToken(nil)
}

// newLogin builds the login flow of DoAccountSetup on the request's
// authenticator.
func (a *Accounts) newLogin() *auth.Login {
	if nil == a || nil == a.authenticator {
		return nil
	}

	var logger auth.Logger
	if nil != a.options.Logger {
		logger = a.options.Logger
	}

	return &auth.Login{
		Context:          a.authenticator.Context,
		Session:          a.authenticator,
		Config:           a.options.Config,
		Logger:           logger,
		Plugins:          a.options.Plugins,
		Settings:         a.options.Settings,
		ValidateLanguage: a.options.ValidateLanguage,
		IMAPLogin:        a.options.IMAPLogin,
	}
}

// additionalAccountsEnabled is GetCapa(Capa::ADDITIONAL_ACCOUNTS).
func (a *Accounts) additionalAccountsEnabled() bool {
	if nil == a || nil == a.options.Config {
		return false
	}
	return a.options.Config.GetBool("webmail", "allow_additional_accounts", false)
}
