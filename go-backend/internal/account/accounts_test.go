package account

// Tests of the Step 24 "additionalaccounts" document (Actions::GetAccounts()/
// SetAccounts()).

import (
	"strings"
	"testing"
)

// additionalMain builds a main account for the accounts document tests.
func additionalMain() *MainAccount {
	main := NewMainAccount()
	main.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")
	return main
}

// TestAdditionalAccountsOrder checks that the key order survives a save/load
// round trip and a reorder.
func TestAdditionalAccountsOrder(t *testing.T) {
	store := testStorage(t)
	main := additionalMain()

	accounts := NewAccounts()
	accounts.Set("a@example.com", map[string]any{"email": "a@example.com"})
	accounts.Set("b@example.com", map[string]any{"email": "b@example.com"})

	if err := SaveAccounts(store, main, accounts); nil != err {
		t.Fatalf("SaveAccounts: %v", err)
	}

	loaded := LoadAccounts(store, main)
	if `["a@example.com","b@example.com"]` != span(loaded.Order()) {
		t.Fatalf("order = %v", loaded.Order())
	}

	// Reorder: the requested ones first, the rest appended.
	loaded.Reorder([]string{"b@example.com", "a@example.com"})
	if `["b@example.com","a@example.com"]` != span(loaded.Order()) {
		t.Errorf("reorder = %v", loaded.Order())
	}

	// A requested account that does not exist is dropped, and a missing one is
	// kept at the end.
	loaded.Reorder([]string{"b@example.com", "unknown@example.com"})
	if `["b@example.com","a@example.com"]` != span(loaded.Order()) {
		t.Errorf("reorder with unknown = %v", loaded.Order())
	}

	if err := SaveAccounts(store, main, loaded); nil != err {
		t.Fatalf("SaveAccounts: %v", err)
	}
	if `["b@example.com","a@example.com"]` != span(LoadAccounts(store, main).Order()) {
		t.Errorf("stored order = %v", LoadAccounts(store, main).Order())
	}
}

// TestAdditionalAccountsDelete checks the removal and the empty clear.
func TestAdditionalAccountsDelete(t *testing.T) {
	store := testStorage(t)
	main := additionalMain()

	accounts := NewAccounts()
	accounts.Set("a@example.com", map[string]any{"email": "a@example.com"})
	accounts.Set("b@example.com", map[string]any{"email": "b@example.com"})
	accounts.Delete("a@example.com")

	if 1 != accounts.Len() || !accounts.Has("b@example.com") || accounts.Has("a@example.com") {
		t.Errorf("after delete: %v", accounts.Order())
	}

	// Saving an empty document clears the key.
	empty := NewAccounts()
	if err := SaveAccounts(store, main, empty); nil != err {
		t.Fatalf("SaveAccounts: %v", err)
	}
	if 0 != LoadAccounts(store, main).Len() {
		t.Error("an empty document must clear the key")
	}
}

// TestAdditionalAccountsToMap checks the map view used by the account switch.
func TestAdditionalAccountsToMap(t *testing.T) {
	accounts := NewAccounts()
	accounts.Set("a@example.com", map[string]any{"email": "a@example.com"})

	view := accounts.ToMap()
	if 1 != len(view) {
		t.Fatalf("ToMap = %v", view)
	}
	if _, ok := view["a@example.com"]; !ok {
		t.Error("ToMap is missing the entry")
	}
}

// span renders a string list for deterministic error messages.
func span(values []string) string {
	if nil == values || 0 == len(values) {
		return "[]"
	}
	return `["` + strings.Join(values, `","`) + `"]`
}
