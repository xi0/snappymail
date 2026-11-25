package sieveclient

// The filter provider of \RainLoop\Actions\Filters: the port of
// RainLoop\Providers\Filters (the error mapping of handleException()) and its
// only driver RainLoop\Providers\Filters\SieveStorage.
//
// The provider is what the DoFilters* actions call, so the notification codes
// the frontend switches on are produced here:
//
//   - a connection failure is ConnectionError (104);
//   - a negative response (including a failed login) is ClientViewError (902)
//     with the server's reply lines as the additional message, which the
//     filters page shows verbatim;
//   - anything else falls back to the action's own code (352 CantGetFilters,
//     351 CantSaveFilters, 353 CantActivateFiltersScript, 354
//     CantDeleteFiltersScript).
//
// The PHP quirks are kept: Load() always appends a phantom "rainloop.user" entry
// when the server does not have that script (it is the script the filter GUI
// writes into), DeleteScript silently succeeds for an unknown name, and Save()
// does not activate the script (DoFiltersScriptSave's ActivateScript call is
// commented out in PHP).

import (
	"errors"
	"sort"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// SieveFileName is SieveStorage::SIEVE_FILE_NAME: the script the filter GUI
// owns, so the only one whose body it parses.
const SieveFileName = "rainloop.user"

// scriptObject is the "@Object" tag of a script document
// (Object/SieveScript) that SieveScriptModel.reviveFromJson() validates.
const scriptObject = "Object/SieveScript"

// Provider is \RainLoop\Providers\Filters with the SieveStorage driver.
type Provider struct {
	hooks  account.Hooks
	logger *logx.Logger
}

// NewProvider builds the filter provider of one installation. hooks may be nil
// (no plugin manager wired yet), like the other connect helpers.
func NewProvider(hooks account.Hooks, logger *logx.Logger) *Provider {
	return &Provider{hooks: hooks, logger: logger}
}

// Load is Providers\Filters::Load() → SieveStorage::Load(): the Sieve extensions
// of the server plus every script with its body.
func (p *Provider) Load(target *account.Account) (any, error) {
	result, err := p.load(target)
	if nil != err {
		return nil, filterError(err, appctx.CantGetFilters)
	}
	return result, nil
}

// Save is Providers\Filters::Save() → SieveStorage::Save().
func (p *Provider) Save(target *account.Account, scriptName, source string) (bool, error) {
	client, err := p.connect(target)
	if nil != err {
		return false, filterError(err, appctx.CantSaveFilters)
	}
	if nil == client {
		return false, nil
	}
	defer client.Disconnect()

	if err := client.PutScript(scriptName, source); nil != err {
		return false, filterError(err, appctx.CantSaveFilters)
	}

	return true, nil
}

// ActivateScript is Providers\Filters::ActivateScript() → SieveStorage::Activate().
// An empty name disables the active script.
func (p *Provider) ActivateScript(target *account.Account, scriptName string) (bool, error) {
	client, err := p.connect(target)
	if nil != err {
		return false, filterError(err, appctx.CantActivateFiltersScript)
	}
	if nil == client {
		return false, nil
	}
	defer client.Disconnect()

	if err := client.SetActiveScript(strings.TrimSpace(scriptName)); nil != err {
		return false, filterError(err, appctx.CantActivateFiltersScript)
	}

	return true, nil
}

// DeleteScript is Providers\Filters::DeleteScript() → SieveStorage::Delete().
//
// An unknown script name is not an error: PHP only sends DELETESCRIPT when the
// script is in ListScripts().
func (p *Provider) DeleteScript(target *account.Account, scriptName string) (bool, error) {
	client, err := p.connect(target)
	if nil != err {
		return false, filterError(err, appctx.CantDeleteFiltersScript)
	}
	if nil == client {
		return false, nil
	}
	defer client.Disconnect()

	scripts, err := client.ListScripts()
	if nil != err {
		return false, filterError(err, appctx.CantDeleteFiltersScript)
	}

	if _, exists := scripts[scriptName]; exists {
		if err := client.DeleteScript(strings.TrimSpace(scriptName)); nil != err {
			return false, filterError(err, appctx.CantDeleteFiltersScript)
		}
	}

	return true, nil
}

// load is SieveStorage::Load() without the error mapping.
func (p *Provider) load(target *account.Account) (any, error) {
	client, err := p.connect(target)
	if nil != err {
		return nil, err
	}
	if nil == client {
		// No driver: Providers\Filters::Load() returns array() → JSON [].
		return []any{}, nil
	}

	modules := client.Modules()
	sort.Strings(modules)

	list, err := client.ListScripts()
	if nil != err {
		client.Disconnect()
		return nil, err
	}

	// ksort() of the script names, which is also the JSON object order PHP
	// writes (Go sorts the keys of a map the same way).
	names := make([]string, 0, len(list))
	for name := range list {
		names = append(names, name)
	}
	sort.Strings(names)

	scripts := make(map[string]any, len(names)+1)
	for _, name := range names {
		body, bodyErr := client.GetScript(name)
		if nil != bodyErr {
			client.Disconnect()
			return nil, bodyErr
		}
		scripts[name] = scriptDocument(name, list[name], body)
	}

	client.Disconnect()

	if _, exists := list[SieveFileName]; !exists {
		scripts[SieveFileName] = scriptDocument(SieveFileName, false, "")
	}

	capa := make([]any, 0, len(modules))
	for _, module := range modules {
		capa = append(capa, module)
	}

	var scriptsValue any = scripts
	if 0 == len(scripts) {
		// json_encode() writes an empty PHP array as [], not {}.
		scriptsValue = []any{}
	}

	return map[string]any{
		"Capa":    capa,
		"Scripts": scriptsValue,
	}, nil
}

// scriptDocument is one SieveScriptModel document of the Load() result.
func scriptDocument(name string, active bool, body string) map[string]any {
	return map[string]any{
		"@Object": scriptObject,
		"name":    name,
		"active":  active,
		"body":    body,
	}
}

// connect is SieveStorage::getConnection(): a connected and logged in client, or
// nil when the account cannot be used (a nil connection is "no driver").
func (p *Provider) connect(target *account.Account) (*Client, error) {
	if nil == target || nil == target.Domain() {
		return nil, nil
	}
	return ConnectAndLogin(target, p.hooks, p.logger)
}

// filterError is Providers\Filters::handleException(): map the MailSo exception
// classes onto the notification codes of the frontend.
func filterError(err error, defaultCode int) error {
	var failure *Error
	if errors.As(err, &failure) {
		switch failure.Kind {
		case KindConnection:
			return appctx.NewClientError(appctx.ConnectionError, err, "")
		case KindNegative:
			return appctx.NewClientError(appctx.ClientViewError, err,
				strings.Join(failure.Responses, "\r\n"))
		}
	}

	return appctx.NewClientError(defaultCode, err, "")
}
