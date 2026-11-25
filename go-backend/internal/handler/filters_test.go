package handler

// Tests of the Step 23 filter actions: the Capa.Sieve gate (the domain must have
// ManageSieve enabled), the delegation to Providers\Filters and the error
// pass-through of the provider.

import (
	"net/http"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// fakeFilters is a handler.Filters with canned responses.
type fakeFilters struct {
	loaded   any
	loadErr  error
	result   bool
	actionFn func(err error) error

	calls    []string
	names    []string
	bodies   []string
	accounts []*account.Account
}

// Load records the call and returns the canned document.
func (f *fakeFilters) Load(target *account.Account) (any, error) {
	f.calls = append(f.calls, "Load")
	f.accounts = append(f.accounts, target)
	return f.loaded, f.loadErr
}

// Save records the call and returns the canned result.
func (f *fakeFilters) Save(target *account.Account, scriptName, source string) (bool, error) {
	f.calls = append(f.calls, "Save")
	f.accounts = append(f.accounts, target)
	f.names = append(f.names, scriptName)
	f.bodies = append(f.bodies, source)
	if nil != f.actionFn {
		return false, f.actionFn(nil)
	}
	return f.result, nil
}

// ActivateScript records the call and returns the canned result.
func (f *fakeFilters) ActivateScript(target *account.Account, scriptName string) (bool, error) {
	f.calls = append(f.calls, "ActivateScript")
	f.accounts = append(f.accounts, target)
	f.names = append(f.names, scriptName)
	if nil != f.actionFn {
		return false, f.actionFn(nil)
	}
	return f.result, nil
}

// DeleteScript records the call and returns the canned result.
func (f *fakeFilters) DeleteScript(target *account.Account, scriptName string) (bool, error) {
	f.calls = append(f.calls, "DeleteScript")
	f.accounts = append(f.accounts, target)
	f.names = append(f.names, scriptName)
	if nil != f.actionFn {
		return false, f.actionFn(nil)
	}
	return f.result, nil
}

// sieveAccount builds an account whose domain has ManageSieve enabled or not.
func sieveAccount(enabled bool) *account.Account {
	config := domain.New("example.com", domain.Defaults{})
	config.Sieve.Enabled = enabled

	main := account.NewMainAccount()
	main.SetCredentials(config, testEmail, testEmail, "secret", "", "")
	return &main.Account
}

// filtersDispatcher builds a dispatcher wired to the given filter provider.
func filtersDispatcher(session Session, provider Filters) *Dispatcher {
	return NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Filters: func(Plugins) Filters { return provider },
	})
}

// TestFiltersNeedAnAccount checks the InvalidToken failure (101) of a request
// without a session.
func TestFiltersNeedAnAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	actions := []struct {
		name   string
		action ActionFunc
	}{
		{"Filters", doFilters},
		{"FiltersScriptSave", doFiltersScriptSave},
		{"FiltersScriptActivate", doFiltersScriptActivate},
		{"FiltersScriptDelete", doFiltersScriptDelete},
	}

	for _, test := range actions {
		ctx := &Context{dispatcher: dispatcher, params: map[string]any{}}
		if _, err := test.action(ctx); appctx.InvalidToken != clientCodeOf(err) {
			t.Errorf("%s code = %d, want %d", test.name, clientCodeOf(err), appctx.InvalidToken)
		}
	}
}

// TestFiltersDisabledDomain checks the Capa.Sieve gate: a domain without
// ManageSieve answers FalseResponse and never touches the provider.
func TestFiltersDisabledDomain(t *testing.T) {
	session := &folderSession{account: sieveAccount(false)}
	provider := &fakeFilters{loaded: map[string]any{}, result: true}
	dispatcher := filtersDispatcher(session, provider)

	actions := []struct {
		name   string
		action ActionFunc
	}{
		{"Filters", doFilters},
		{"FiltersScriptSave", doFiltersScriptSave},
		{"FiltersScriptActivate", doFiltersScriptActivate},
		{"FiltersScriptDelete", doFiltersScriptDelete},
	}

	for _, test := range actions {
		ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{
			"name": "rainloop.user",
			"body": "keep;",
		}}

		response, err := test.action(ctx)
		if nil != err {
			t.Errorf("%s: %v", test.name, err)
			continue
		}

		envelope, _ := response.(map[string]any)
		if false != envelope["Result"] {
			t.Errorf("%s Result = %v, want false", test.name, envelope["Result"])
		}
		if 0 != envelope["code"] {
			t.Errorf("%s code = %v, want 0", test.name, envelope["code"])
		}
	}

	if 0 != len(provider.calls) {
		t.Errorf("the provider was called: %v", provider.calls)
	}
}

// TestFiltersLoad checks the DefaultResponse of the provider document.
func TestFiltersLoad(t *testing.T) {
	resolved := sieveAccount(true)
	session := &folderSession{account: resolved}
	document := map[string]any{
		"Capa": []any{"fileinto"},
		"Scripts": map[string]any{
			"rainloop.user": map[string]any{
				"@Object": "Object/SieveScript",
				"name":    "rainloop.user",
				"active":  true,
				"body":    "keep;",
			},
		},
	}
	provider := &fakeFilters{loaded: document}
	dispatcher := filtersDispatcher(session, provider)

	ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{}}
	response := mustResponse(doFilters(ctx))
	if nil == response {
		t.Fatal("doFilters failed")
	}

	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	scripts, ok := result["Scripts"].(map[string]any)
	if !ok {
		t.Fatalf("Scripts = %#v", result["Scripts"])
	}
	// The nested "@Object" documents survive responseObject().
	script, ok := scripts["rainloop.user"].(map[string]any)
	if !ok || "Object/SieveScript" != script["@Object"] {
		t.Errorf("script = %#v", scripts["rainloop.user"])
	}

	if 1 != len(provider.calls) || "Load" != provider.calls[0] {
		t.Errorf("provider calls = %v", provider.calls)
	}
	if provider.accounts[0] != resolved {
		t.Error("the action must pass the resolved account")
	}
}

// TestFiltersScriptSave checks the name/body parameters and that "active" is
// ignored (the PHP activation call is commented out).
func TestFiltersScriptSave(t *testing.T) {
	resolved := sieveAccount(true)
	session := &folderSession{account: resolved}
	provider := &fakeFilters{result: true}
	dispatcher := filtersDispatcher(session, provider)

	ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{
		"name":   "rainloop.user",
		"body":   "require [\"fileinto\"];\r\nkeep;\r\n",
		"active": true,
	}}

	response := mustResponse(doFiltersScriptSave(ctx))
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
	if 1 != len(provider.calls) || "Save" != provider.calls[0] {
		t.Fatalf("provider calls = %v", provider.calls)
	}
	if "rainloop.user" != provider.names[0] {
		t.Errorf("name = %q", provider.names[0])
	}
	if "require [\"fileinto\"];\r\nkeep;\r\n" != provider.bodies[0] {
		t.Errorf("body = %q", provider.bodies[0])
	}
}

// TestFiltersScriptActivateAndDelete checks the "name" parameter of the two
// remaining actions.
func TestFiltersScriptActivateAndDelete(t *testing.T) {
	resolved := sieveAccount(true)
	session := &folderSession{account: resolved}
	provider := &fakeFilters{result: true}
	dispatcher := filtersDispatcher(session, provider)

	activate := &Context{dispatcher: dispatcher, session: session, params: map[string]any{"name": "mine"}}
	if response := mustResponse(doFiltersScriptActivate(activate)); true != response["Result"] {
		t.Errorf("activate Result = %v", response["Result"])
	}

	remove := &Context{dispatcher: dispatcher, session: session, params: map[string]any{"name": "mine"}}
	if response := mustResponse(doFiltersScriptDelete(remove)); true != response["Result"] {
		t.Errorf("delete Result = %v", response["Result"])
	}

	want := []string{"ActivateScript", "DeleteScript"}
	if len(want) != len(provider.calls) {
		t.Fatalf("provider calls = %v", provider.calls)
	}
	for index, call := range want {
		if call != provider.calls[index] {
			t.Errorf("call %d = %q, want %q", index, provider.calls[index], call)
		}
		if "mine" != provider.names[index] {
			t.Errorf("name %d = %q, want mine", index, provider.names[index])
		}
	}
}

// TestFiltersProviderError checks the pass-through of the notification code of
// the provider (ClientViewError 902 for a negative ManageSieve response).
func TestFiltersProviderError(t *testing.T) {
	resolved := sieveAccount(true)
	session := &folderSession{account: resolved}

	clientErr := appctx.NewClientError(appctx.ClientViewError, nil, `NO "no such script"`)
	provider := &fakeFilters{actionFn: func(error) error { return clientErr }}
	dispatcher := filtersDispatcher(session, provider)

	ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{"name": "x"}}
	_, err := doFiltersScriptSave(ctx)
	if appctx.ClientViewError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.ClientViewError)
	}

	loadErr := &fakeFilters{loadErr: clientErr}
	loadDispatcher := filtersDispatcher(session, loadErr)
	loadCtx := &Context{dispatcher: loadDispatcher, session: session, params: map[string]any{}}
	if _, err := doFilters(loadCtx); appctx.ClientViewError != clientCodeOf(err) {
		t.Errorf("load code = %d, want %d", clientCodeOf(err), appctx.ClientViewError)
	}
}

// TestFiltersWithoutProvider checks the "no driver" behaviour: Load answers an
// empty list and the mutations answer false, exactly like
// \RainLoop\Providers\Filters without a driver.
func TestFiltersWithoutProvider(t *testing.T) {
	resolved := sieveAccount(true)
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
	})

	load := &Context{dispatcher: dispatcher, session: session, params: map[string]any{}}
	response := mustResponse(doFilters(load))
	if nil == response {
		t.Fatal("doFilters failed")
	}
	list, ok := response["Result"].([]any)
	if !ok || 0 != len(list) {
		t.Errorf("Result = %#v", response["Result"])
	}

	save := &Context{dispatcher: dispatcher, session: session, params: map[string]any{"name": "x"}}
	saved := mustResponse(doFiltersScriptSave(save))
	if false != saved["Result"] {
		t.Errorf("Result = %v, want false", saved["Result"])
	}
}

// TestSieveCapa checks Actions::GetCapa(Capa::Sieve, $oAccount): only the domain
// flag matters, an account without a domain never has Sieve.
func TestSieveCapa(t *testing.T) {
	ctx := &Context{}

	if ctx.sieveCapa(nil) {
		t.Error("a nil account has no Sieve capability")
	}
	if ctx.sieveCapa(folderAccount()) {
		t.Error("an account without a domain has no Sieve capability")
	}
	if ctx.sieveCapa(sieveAccount(false)) {
		t.Error("a disabled domain has no Sieve capability")
	}
	if !ctx.sieveCapa(sieveAccount(true)) {
		t.Error("an enabled domain has the Sieve capability")
	}
}
