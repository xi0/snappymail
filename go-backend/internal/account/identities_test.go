package account

// Tests of the Step 24 identity provider (Providers\Identities + FileIdentities).

import (
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// identityAccount builds an account for the identity provider tests.
func identityAccount() *Account {
	acct := NewAccount()
	acct.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")
	return acct
}

// TestIdentitiesPrimaryFallback checks the synthesised primary identity and the
// popup_identity "exists" flag.
func TestIdentitiesPrimaryFallback(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()

	// popup_identity on: the generated primary identity does not exist yet.
	provider := NewIdentities(store, store, true)
	list := provider.GetIdentities(acct, true)
	if 1 != len(list) {
		t.Fatalf("identities = %d, want the primary one", len(list))
	}
	if !list[0].IsAccountIdentities() || "user@example.com" != list[0].Email() {
		t.Errorf("primary = %+v", list[0])
	}
	if list[0].Exists() {
		t.Error("popup_identity on: the generated identity must not exist")
	}

	// popup_identity off: it exists.
	off := NewIdentities(store, store, false)
	if !off.GetIdentities(acct, true)[0].Exists() {
		t.Error("popup_identity off: the generated identity must exist")
	}
}

// TestIdentitiesRoundTrip checks that an added identity survives a new provider
// instance (the "restart" of the done-when).
func TestIdentitiesRoundTrip(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()

	alias := NewIdentity("abc", "alias@example.com")
	alias.SetName("Alias")

	NewIdentities(store, store, false).UpdateIdentity(acct, alias)

	// A fresh provider reads the document back from the storage.
	provider := NewIdentities(store, store, false)
	list := provider.GetIdentities(acct, true)

	if 2 != len(list) {
		t.Fatalf("identities = %d, want alias + primary", len(list))
	}

	found := false
	for _, identity := range list {
		if "abc" == identity.Id() {
			found = true
			if "Alias" != identity.Name() || "alias@example.com" != identity.Email() {
				t.Errorf("alias = %+v", identity)
			}
		}
	}
	if !found {
		t.Error("the alias identity did not survive the reload")
	}

	// allowMultiple false returns only the primary identity.
	only := provider.GetIdentities(acct, false)
	if 1 != len(only) || !only[0].IsAccountIdentities() {
		t.Errorf("Identities off must return only the primary identity, got %+v", only)
	}
}

// TestIdentitiesUpdateReplaces checks that an update with the same id replaces
// the document instead of appending a duplicate.
func TestIdentitiesUpdateReplaces(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()
	provider := NewIdentities(store, store, false)

	alias := NewIdentity("abc", "alias@example.com")
	alias.SetName("First")
	provider.UpdateIdentity(acct, alias)

	alias.SetName("Second")
	provider.UpdateIdentity(acct, alias)

	provider = NewIdentities(store, store, false)
	list := provider.GetIdentities(acct, true)
	if 2 != len(list) {
		t.Fatalf("identities = %d, want a replaced alias + primary", len(list))
	}
	for _, identity := range list {
		if "abc" == identity.Id() && "Second" != identity.Name() {
			t.Errorf("alias name = %q, want Second", identity.Name())
		}
	}
}

// TestIdentitiesDelete checks that a normal identity is removed and the primary
// one is never removed.
func TestIdentitiesDelete(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()
	provider := NewIdentities(store, store, false)

	alias := NewIdentity("abc", "alias@example.com")
	provider.UpdateIdentity(acct, alias)

	// Deleting the primary identity (empty id) is a no-op.
	provider.DeleteIdentity(acct, "")
	if 2 != len(provider.GetIdentities(acct, true)) {
		t.Error("the primary identity must never be removed")
	}

	provider.DeleteIdentity(acct, "abc")
	if 1 != len(provider.GetIdentities(acct, true)) {
		t.Error("the alias identity was not removed")
	}
}

// TestIdentitiesPrimaryEmail checks that a stored primary identity mirrors the
// account address (FileIdentities::GetIdentities()).
func TestIdentitiesPrimaryEmail(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()
	provider := NewIdentities(store, store, false)

	primary := NewIdentity("", "other@example.com")
	provider.UpdateIdentity(acct, primary)

	for _, identity := range provider.GetIdentities(acct, true) {
		if identity.IsAccountIdentities() && "user@example.com" != identity.Email() {
			t.Errorf("primary email = %q, want the account address", identity.Email())
		}
	}
}

// TestIdentitiesOrder checks the stored order and the legacy migration.
func TestIdentitiesOrder(t *testing.T) {
	store := testStorage(t)
	acct := identityAccount()
	provider := NewIdentities(store, store, false)

	provider.UpdateIdentity(acct, NewIdentity("a", "a@example.com"))
	provider.UpdateIdentity(acct, NewIdentity("b", "b@example.com"))

	if !provider.SaveOrder(acct, []string{"b", "a"}) {
		t.Fatal("SaveOrder failed")
	}

	sorted := provider.GetSortedIdentities(acct, true)
	if 3 != len(sorted) {
		t.Fatalf("identities = %d, want b, a, primary", len(sorted))
	}
	if "b" != sorted[0].Id() || "a" != sorted[1].Id() {
		t.Errorf("order = %s, %s", sorted[0].Id(), sorted[1].Id())
	}

	// The legacy key is migrated onto the local provider and removed.
	if err := store.Put(acct, storage.TypeConfig, legacyIdentitiesOrderKey,
		`{"Identities":["a","b"]}`); nil != err {
		t.Fatalf("Put: %v", err)
	}
	_ = store.Clear(acct, storage.TypeConfig, identitiesOrderKey)

	sorted = provider.GetSortedIdentities(acct, true)
	if "a" != sorted[0].Id() || "b" != sorted[1].Id() {
		t.Errorf("legacy order = %s, %s", sorted[0].Id(), sorted[1].Id())
	}
	if _, ok := store.Get(acct, storage.TypeConfig, identitiesOrderKey); !ok {
		t.Error("the legacy order was not migrated")
	}
	if _, ok := store.Get(acct, storage.TypeConfig, legacyIdentitiesOrderKey); ok {
		t.Error("the legacy order key was not cleared")
	}
}
