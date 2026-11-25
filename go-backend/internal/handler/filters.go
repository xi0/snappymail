package handler

// The filter actions of \RainLoop\Actions\Filters (Step 23):
// DoFilters, DoFiltersScriptSave, DoFiltersScriptActivate and
// DoFiltersScriptDelete.
//
// Every action is gated by Actions::GetCapa(Capa::Sieve, $oAccount), which is
// true only when the domain of the account has ManageSieve enabled
// (SieveSettings.enabled) — a domain without a Sieve server therefore reports
// Capa.Sieve false in AppData and every filter action answers FalseResponse.
//
// The ManageSieve transport itself lives in internal/sieveclient; the provider
// already maps its failures onto the notification codes of
// Providers\Filters::handleException(), so the actions only forward the error.
//
// PHP quirks kept as-is:
//   - DoFiltersScriptSave() ignores the "active" parameter: the
//     ActivateScript() call is commented out in the PHP action, so saving never
//     activates the script.

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
)

// Filters is Actions\Filters::FiltersProvider(): the \RainLoop\Providers\Filters
// surface of the DoFilters* actions. *sieveclient.Provider satisfies it.
//
// A nil provider reports every account as having no filter driver, which makes
// Load() answer an empty document and the mutating actions answer false, i.e.
// exactly the PHP behaviour when no driver is wired.
type Filters interface {
	// Load is Providers\Filters::Load().
	Load(account *account.Account) (any, error)
	// Save is Providers\Filters::Save().
	Save(account *account.Account, scriptName, source string) (bool, error)
	// ActivateScript is Providers\Filters::ActivateScript().
	ActivateScript(account *account.Account, scriptName string) (bool, error)
	// DeleteScript is Providers\Filters::DeleteScript().
	DeleteScript(account *account.Account, scriptName string) (bool, error)
}

// RegisterFilters registers the DoFilters* actions.
func RegisterFilters(d *Dispatcher) {
	d.Register("Filters", doFilters)
	d.Register("FiltersScriptSave", doFiltersScriptSave)
	d.Register("FiltersScriptActivate", doFiltersScriptActivate)
	d.Register("FiltersScriptDelete", doFiltersScriptDelete)
}

// filters is Actions\Filters::FiltersProvider(). Without a wired factory the
// provider has no driver, which is noFilters.
func (c *Context) filters() Filters {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newFilters {
		return noFilters{}
	}
	if provider := c.dispatcher.newFilters(c.dispatcher.plugins); nil != provider {
		return provider
	}
	return noFilters{}
}

// noFilters is \RainLoop\Providers\Filters without a driver: IsActive() is
// false, so Load() answers array() and every mutation answers false.
type noFilters struct{}

// Load is Providers\Filters::Load() for a missing driver.
func (noFilters) Load(*account.Account) (any, error) { return []any{}, nil }

// Save is Providers\Filters::Save() for a missing driver.
func (noFilters) Save(*account.Account, string, string) (bool, error) { return false, nil }

// ActivateScript is Providers\Filters::ActivateScript() for a missing driver.
func (noFilters) ActivateScript(*account.Account, string) (bool, error) { return false, nil }

// DeleteScript is Providers\Filters::DeleteScript() for a missing driver.
func (noFilters) DeleteScript(*account.Account, string) (bool, error) { return false, nil }

// sieveCapa is Actions::GetCapa(Capa::Sieve, $oAccount): ManageSieve is
// available when the domain of the account enables it.
//
// Note that GetCapa() always calls Capa(false, …), so the admin flag of
// AppData's Capa() does not apply here (an admin without a Sieve domain gets
// FalseResponse, like PHP).
func (c *Context) sieveCapa(resolved *account.Account) bool {
	if nil == resolved {
		return false
	}
	config := resolved.Domain()
	if nil == config {
		return false
	}
	return config.Sieve.Enabled
}

// doFilters is Actions\Filters::DoFilters().
func doFilters(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	if !ctx.sieveCapa(resolved) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	result, err := ctx.filters().Load(resolved)
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doFiltersScriptSave is Actions\Filters::DoFiltersScriptSave().
func doFiltersScriptSave(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	if !ctx.sieveCapa(resolved) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	// PHP reads 'active' but never acts on it (the ActivateScript call is
	// commented out), so the parameter is not read here either.
	name := ctx.stringParam("name", "")
	body := ctx.stringParam("body", "")

	result, err := ctx.filters().Save(resolved, name, body)
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doFiltersScriptActivate is Actions\Filters::DoFiltersScriptActivate().
func doFiltersScriptActivate(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	if !ctx.sieveCapa(resolved) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	result, err := ctx.filters().ActivateScript(resolved, ctx.stringParam("name", ""))
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doFiltersScriptDelete is Actions\Filters::DoFiltersScriptDelete().
func doFiltersScriptDelete(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	if !ctx.sieveCapa(resolved) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	result, err := ctx.filters().DeleteScript(resolved, ctx.stringParam("name", ""))
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(result, nil), nil
}
