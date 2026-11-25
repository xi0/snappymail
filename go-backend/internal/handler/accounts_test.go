package handler

// Tests of the Step 24 account/identity actions. The action logic is exercised
// against a fake handler.Accounts (the production implementation lives in
// internal/service) and a real file-backed identity provider.

import (
	"net/http"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// fakeAccounts is a handler.Accounts with canned responses.
type fakeAccounts struct {
	main     *account.MainAccount
	current  *account.Account
	accounts *account.Accounts

	switched  bool
	switchErr error
	cleared   bool
	saved     bool

	loginResult *account.AdditionalAccount
	loginErr    error

	sealResult map[string]any
	sealErr    error

	unread    int
	unreadErr error
}

// Main returns the canned main account.
func (f *fakeAccounts) Main() (*account.MainAccount, error) { return f.main, nil }

// Current returns the canned selected account.
func (f *fakeAccounts) Current() (*account.Account, error) { return f.current, nil }

// GetAccounts returns the canned document.
func (f *fakeAccounts) GetAccounts(*account.MainAccount) (*account.Accounts, error) {
	if nil == f.accounts {
		f.accounts = account.NewAccounts()
	}
	return f.accounts, nil
}

// SetAccounts stores the document.
func (f *fakeAccounts) SetAccounts(_ *account.MainAccount, accounts *account.Accounts) error {
	f.accounts = accounts
	f.saved = true
	return nil
}

// NewAdditional returns the token entry of the canned document.
func (f *fakeAccounts) NewAdditional(data any, _ bool) (*account.AdditionalAccount, error) {
	return nil, nil
}

// Seal returns the canned token array.
func (f *fakeAccounts) Seal(*account.AdditionalAccount, *account.MainAccount) (map[string]any, error) {
	return f.sealResult, f.sealErr
}

// Login returns the canned additional account.
func (f *fakeAccounts) Login(string, string) (*account.AdditionalAccount, error) {
	return f.loginResult, f.loginErr
}

// Unread returns the canned counter.
func (f *fakeAccounts) Unread(*account.AdditionalAccount) (int, error) {
	return f.unread, f.unreadErr
}

// Switch returns the canned switch result.
func (f *fakeAccounts) Switch(string) (bool, error) {
	if nil != f.switchErr {
		return false, f.switchErr
	}
	return f.switched, nil
}

// ClearAdditional records the cookie clearing.
func (f *fakeAccounts) ClearAdditional() error {
	f.cleared = true
	return nil
}

// accountsMain builds a main account for the account tests.
func accountsMain() *account.MainAccount {
	main := account.NewMainAccount()
	main.SetCredentials(nil, testEmail, testEmail, "secret", "", "")
	return main
}

// accountsContext builds a dispatcher/context wired to the fake accounts, the
// identity provider and the given configuration.
func accountsContext(
	t *testing.T,
	session Session,
	accounts Accounts,
	identities *account.Identities,
	config userConfig,
) *Context {
	t.Helper()

	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Salt:          "test-salt",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Accounts:      func(Session) Accounts { return accounts },
		Identities:    identities,
		Config:        config,
		LocalSettings: newMemorySettings(),
		LocalDomains:  func() []string { return []string{"example.com"} },
	})

	return &Context{dispatcher: dispatcher, session: session, params: map[string]any{}}
}

// TestAccountsActionsNeedAnAccount checks the InvalidToken failure of a request
// without a session.
func TestAccountsActionsNeedAnAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	for _, action := range []ActionFunc{doAccountsAndIdentities, doIdentityUpdate, doIdentityDelete} {
		ctx := &Context{dispatcher: dispatcher, params: map[string]any{}}
		if _, err := action(ctx); appctx.InvalidToken != clientCodeOf(err) {
			t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
		}
	}
}

// TestAccountSetupAlreadyExists checks the AccountAlreadyExists guard.
func TestAccountSetupAlreadyExists(t *testing.T) {
	main := accountsMain()
	accounts := account.NewAccounts()
	accounts.Set("add@example.com", map[string]any{"email": "add@example.com"})

	additional := account.NewAdditionalAccount()
	additional.SetCredentials(nil, "add@example.com", "add@example.com", "secret", "", "")

	fake := &fakeAccounts{main: main, accounts: accounts, loginResult: additional,
		sealResult: map[string]any{"email": "add@example.com", "pass": "sealed"}}
	ctx := accountsContext(t, &folderSession{account: &main.Account}, fake, nil,
		userConfig{bools: map[string]bool{"webmail.allow_additional_accounts": true}})
	ctx.params = map[string]any{"email": "add@example.com", "password": "x", "new": 1}

	_, err := doAccountSetup(ctx)
	if appctx.AccountAlreadyExists != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.AccountAlreadyExists)
	}
}

// TestAccountSetupCapaOff checks the Capa.ADDITIONAL_ACCOUNTS gate.
func TestAccountSetupCapaOff(t *testing.T) {
	main := accountsMain()
	fake := &fakeAccounts{main: main}
	ctx := accountsContext(t, &folderSession{account: &main.Account}, fake, nil, userConfig{})
	ctx.params = map[string]any{"email": "add@example.com", "password": "x", "new": 1}

	response := mustResponse(doAccountSetup(ctx))
	if nil == response || false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
	if fake.saved {
		t.Error("nothing may be saved when the capability is off")
	}
}

// TestAccountSetupCreatesToken checks the login + seal + store path.
func TestAccountSetupCreatesToken(t *testing.T) {
	main := accountsMain()

	additional := account.NewAdditionalAccount()
	additional.SetCredentials(nil, "add@example.com", "add@example.com", "secret", "", "")

	fake := &fakeAccounts{
		main:        main,
		loginResult: additional,
		sealResult:  map[string]any{"email": "add@example.com", "login": "add@example.com", "pass": "sealed"},
	}

	ctx := accountsContext(t, &folderSession{account: &main.Account}, fake, nil,
		userConfig{bools: map[string]bool{"webmail.allow_additional_accounts": true}})
	ctx.params = map[string]any{
		"email":    "add@example.com",
		"password": "secret",
		"new":      1,
		"name":     "  Work  ",
	}

	response := mustResponse(doAccountSetup(ctx))
	if nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}
	if !fake.saved {
		t.Fatal("the accounts document was not saved")
	}

	token, ok := fake.accounts.Get("add@example.com")
	if !ok {
		t.Fatal("the additional account was not stored")
	}
	document, _ := token.(map[string]any)
	if "Work" != phpString(document["name"]) {
		t.Errorf("name = %v, want the trimmed Work", document["name"])
	}
}

// TestAccountDeleteClearsSelection checks the Reload flag and the cookie clear.
func TestAccountDeleteClearsSelection(t *testing.T) {
	main := accountsMain()

	additional := account.NewAdditionalAccount()
	additional.SetCredentials(nil, "add@example.com", "add@example.com", "secret", "", "")

	accounts := account.NewAccounts()
	accounts.Set("add@example.com", map[string]any{"email": "add@example.com"})

	fake := &fakeAccounts{main: main, accounts: accounts, current: &additional.Account}
	ctx := accountsContext(t, &folderSession{account: &additional.Account}, fake, nil,
		userConfig{bools: map[string]bool{"webmail.allow_additional_accounts": true}})
	ctx.params = map[string]any{"emailToDelete": "add@example.com"}

	response := mustResponse(doAccountDelete(ctx))
	if nil == response {
		t.Fatal("doAccountDelete failed")
	}
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
	if true != response["Reload"] {
		t.Errorf("Reload = %v, want true", response["Reload"])
	}
	if !fake.cleared {
		t.Error("the additional account cookie must be cleared")
	}
	if fake.accounts.Has("add@example.com") {
		t.Error("the account must be removed")
	}

	// Deleting an unknown address answers FalseResponse.
	ctx.params = map[string]any{"emailToDelete": "unknown@example.com"}
	if response := mustResponse(doAccountDelete(ctx)); false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
}

// TestAccountSwitch checks the getAccountData() payload of DoAccountSwitch.
func TestAccountSwitch(t *testing.T) {
	main := accountsMain()
	fake := &fakeAccounts{main: main, current: &main.Account, switched: true}

	ctx := accountsContext(t, &folderSession{account: &main.Account}, fake, nil, userConfig{})
	ctx.params = map[string]any{"Email": testEmail}

	response := mustResponse(doAccountSwitch(ctx))
	if nil == response {
		t.Fatal("doAccountSwitch failed")
	}
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %v", response["Result"])
	}
	if testEmail != result["Email"] {
		t.Errorf("Email = %v", result["Email"])
	}
	if testEmail != result["mainEmail"] {
		t.Errorf("mainEmail = %v", result["mainEmail"])
	}
	if "" == phpString(result["accountHash"]) {
		t.Error("accountHash must not be empty")
	}
	if 15 != phpIntValue(result["CheckMailInterval"]) {
		t.Errorf("CheckMailInterval = %v, want 15", result["CheckMailInterval"])
	}
	localDomains, _ := result["localDomains"].([]string)
	if 1 != len(localDomains) || "example.com" != localDomains[0] {
		t.Errorf("localDomains = %v", result["localDomains"])
	}

	// A failed switch answers FalseResponse.
	fake.switched = false
	if response := mustResponse(doAccountSwitch(ctx)); false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
}

// TestAccountsAndIdentities checks the combined payload: ordered accounts plus
// the account identities.
func TestAccountsAndIdentities(t *testing.T) {
	main := accountsMain()

	accounts := account.NewAccounts()
	accounts.Set("a@example.com", map[string]any{"email": "a@example.com", "name": "A"})
	accounts.Set("b@example.com", map[string]any{"email": "b@example.com", "name": "B"})

	fake := &fakeAccounts{main: main, current: &main.Account, accounts: accounts}

	resolved := &main.Account
	store := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))
	identities := account.NewIdentities(store, store, false)
	alias := account.NewIdentity("abc", "alias@example.com")
	alias.SetName("Alias")
	identities.UpdateIdentity(resolved, alias)

	ctx := accountsContext(t, &folderSession{account: resolved}, fake, identities,
		userConfig{bools: map[string]bool{"webmail.allow_additional_identities": true}})

	response := mustResponse(doAccountsAndIdentities(ctx))
	if nil == response {
		t.Fatal("doAccountsAndIdentities failed")
	}
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %v", response["Result"])
	}

	list, _ := result["Accounts"].([]any)
	if 2 != len(list) {
		t.Fatalf("Accounts = %v", result["Accounts"])
	}
	first, _ := list[0].(map[string]any)
	if "a@example.com" != first["email"] || "A" != first["name"] {
		t.Errorf("first account = %v", first)
	}

	identityList, _ := result["Identities"].([]any)
	if 2 != len(identityList) {
		t.Fatalf("Identities = %v", result["Identities"])
	}
}

// TestIdentityUpdateAndDelete checks the identity actions against the file
// provider.
func TestIdentityUpdateAndDelete(t *testing.T) {
	resolved := folderAccount()
	store := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))
	identities := account.NewIdentities(store, store, false)

	fake := &fakeAccounts{current: resolved}
	ctx := accountsContext(t, &folderSession{account: resolved}, fake, identities,
		userConfig{bools: map[string]bool{"webmail.allow_additional_identities": true}})

	ctx.params = map[string]any{"Id": "abc", "Email": "alias@example.com", "Name": "Alias"}
	if response := mustResponse(doIdentityUpdate(ctx)); true != response["Result"] {
		t.Fatalf("IdentityUpdate Result = %v", response["Result"])
	}
	if 2 != len(identities.GetIdentities(resolved, true)) {
		t.Error("the identity was not stored")
	}

	// A missing address is InvalidInputArgument.
	ctx.params = map[string]any{"Id": "abc"}
	if _, err := doIdentityUpdate(ctx); appctx.InvalidInputArgument != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidInputArgument)
	}

	// An empty id cannot be deleted.
	ctx.params = map[string]any{"idToDelete": ""}
	if _, err := doIdentityDelete(ctx); appctx.UnknownError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.UnknownError)
	}

	// A real id removes the identity.
	ctx.params = map[string]any{"idToDelete": "abc"}
	if response := mustResponse(doIdentityDelete(ctx)); true != response["Result"] {
		t.Errorf("IdentityDelete Result = %v", response["Result"])
	}
	if 1 != len(identities.GetIdentities(resolved, true)) {
		t.Error("the identity was not removed")
	}
}

// TestAccountsAndIdentitiesSortOrder checks the account reorder and the identity
// order persistence.
func TestAccountsAndIdentitiesSortOrder(t *testing.T) {
	resolved := folderAccount()
	main := accountsMain()

	accounts := account.NewAccounts()
	accounts.Set("a@example.com", map[string]any{"email": "a@example.com"})
	accounts.Set("b@example.com", map[string]any{"email": "b@example.com"})

	store := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))
	identities := account.NewIdentities(store, store, false)

	fake := &fakeAccounts{main: main, current: resolved, accounts: accounts}
	ctx := accountsContext(t, &folderSession{account: resolved}, fake, identities, userConfig{})
	ctx.params = map[string]any{
		"Accounts":   []any{"b@example.com", "a@example.com"},
		"Identities": []any{"", "abc"},
	}

	response := mustResponse(doAccountsAndIdentitiesSortOrder(ctx))
	if nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}

	order := fake.accounts.Order()
	if `["b@example.com","a@example.com"]` != spanStrings(order) {
		t.Errorf("account order = %v", order)
	}

	// Neither parameter: FalseResponse.
	ctx.params = map[string]any{}
	if response := mustResponse(doAccountsAndIdentitiesSortOrder(ctx)); false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
}

// spanStrings renders a list for deterministic error messages.
func spanStrings(values []string) string {
	if 0 == len(values) {
		return "[]"
	}
	result := "["
	for index, value := range values {
		if 0 < index {
			result += ","
		}
		result += `"` + value + `"`
	}
	return result + "]"
}
