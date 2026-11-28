package caldav

// The ported plugin actions reach the request through \RainLoop\Actions in PHP
// ($this->Manager()->Actions()). The Go plugin manager is built once at start-up
// and shared by all requests, so the request is handed to a plugin instead:
//
//   - a plugin JSON action receives the handler context (*handler.Context),
//     which is the Actions surface of the request (parameters, response helper,
//     account resolution, storage/crypt environment);
//   - the hooks receive the values their PHP counterpart receives plus the
//     environment as a trailing argument ("login.success" gets the account
//     Context, "json.after-*" the request context).
//
// Actions is the surface both cases are reduced to. It is satisfied
// structurally by *handler.Context, which keeps this package independent of the
// request pipeline (internal/handler must not import a plugin).

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

// Actions is the \RainLoop\Actions surface the ported CalDAV code uses.
type Actions interface {
	plugin.ActionContext

	// Account is Actions::getAccountFromToken(): the selected additional
	// account, else the main account, else nil.
	Account(throw bool) (*account.Account, error)
	// MainAccount is Actions::getMainAccountFromToken(): the account that owns
	// the crypt key.
	MainAccount(throw bool) (*account.MainAccount, error)
	// AccountContext is the provider environment of the request (APP_SALT, the
	// storage provider and the crypt engine).
	AccountContext() *account.Context
	// Language is Actions::GetLanguage() (the code the plugin's translations are
	// looked up with).
	Language() string
}

// request is the per-call state of the ported actions: the \RainLoop\Actions
// surface of a JSON action, or the values a hook was called with.
type request struct {
	// actions is the request context of a JSON action (nil in a hook).
	actions Actions
	// current is Actions::getAccountFromToken().
	current *account.Account
	// main is Actions::getMainAccountFromToken().
	main *account.MainAccount
	// env is the account Context: APP_SALT, the storage and crypt providers.
	env *account.Context
}

// requestFromActions builds the state of a plugin JSON action. It returns nil
// when the context does not offer the Actions surface, which makes the action
// report "not logged in" like an unauthenticated request does in PHP.
func requestFromActions(ctx plugin.ActionContext) *request {
	actions, ok := ctx.(Actions)
	if !ok || nil == actions {
		return nil
	}

	resolved := &request{actions: actions, env: actions.AccountContext()}

	if current, err := actions.Account(false); nil == err && nil != current {
		resolved.current = current
	}
	if main, err := actions.MainAccount(false); nil == err && nil != main {
		resolved.main = main
	}

	return resolved
}

// requestFromHook extracts the request state from the arguments of a hook.
//
// Both hook shapes are accepted, so a caller may pass the environment in any
// order: the account Context of "login.success" and the request context (an
// Actions) of "json.after-*".
func requestFromHook(args []any) *request {
	resolved := &request{}

	for _, arg := range args {
		switch typed := arg.(type) {
		case *account.MainAccount:
			// A *MainAccount satisfies account.Account as well, so it has to be
			// matched first (MainAccount::CryptKey() is the hook argument).
			resolved.main = typed
			if nil == resolved.current {
				resolved.current = &typed.Account
			}
		case *account.Context:
			resolved.env = typed
		case Actions:
			resolved.actions = typed
			if nil == resolved.env {
				resolved.env = typed.AccountContext()
			}
			if nil == resolved.current {
				if current, err := typed.Account(false); nil == err && nil != current {
					resolved.current = current
				}
			}
			if nil == resolved.main {
				if main, err := typed.MainAccount(false); nil == err && nil != main {
					resolved.main = main
				}
			}
		}
	}

	return resolved
}

// language is the two-letter code the plugin's translations are read for:
// CaldavPlugin::msg() uses substr(GetLanguage(), 0, 2) and only "da" has a
// dedicated translation, everything else falls back to English.
func (r *request) language() string {
	code := ""
	if nil != r && nil != r.actions {
		code = strings.ToLower(r.actions.Language())
	}
	if 2 < len(code) {
		code = code[:2]
	}
	if "da" == code {
		return "da"
	}
	return "en"
}
