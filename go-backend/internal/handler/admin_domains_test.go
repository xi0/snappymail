package handler

// Tests of the Step 27 admin domain actions against a real file-backed
// domain.Provider: a domain is created, loaded, listed, disabled, aliased and
// deleted exactly like the PHP backend writes them.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// domainAdminDispatcher builds a dispatcher wired to a provider in dir.
func domainAdminDispatcher(provider *domain.Provider, config AdminConfig) *Dispatcher {
	return NewDispatcher(Options{
		Version:    "0.0.0",
		AdminPanel: func(Session) AdminPanel { return &adminPanelFake{loggedIn: true} },
		Config:     config,
		Domains:    provider,
	})
}

// domainSaveParams builds the Save action parameters of one domain.
func domainSaveParams(name, imapHost string) map[string]any {
	return map[string]any{
		"name":   name,
		"create": 1,
		"IMAP":   map[string]any{"host": imapHost, "port": 993, "type": 1},
		"SMTP":   map[string]any{"host": "smtp." + name, "port": 587, "type": 2},
		"Sieve":  map[string]any{"host": "sieve." + name, "port": 4190, "enabled": true},
	}
}

// TestAdminDomainSaveAndLoad checks the create/edit round-trip and the file.
func TestAdminDomainSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	provider := domain.NewProvider(dir, nil, domain.Defaults{})
	dispatcher := domainAdminDispatcher(provider, newAdminConfigFake())

	ctx := adminContext(dispatcher, domainSaveParams("example.com", "imap.example.com"))
	if response := mustResponse(doAdminDomainSave(ctx)); nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}

	// The canonical <punycode>.json file was written.
	if _, err := os.Stat(filepath.Join(dir, "example.com.json")); nil != err {
		t.Fatalf("example.com.json not written: %v", err)
	}

	loaded := provider.Load("example.com")
	if nil == loaded {
		t.Fatal("the saved domain does not load")
	}
	if "imap.example.com" != loaded.IMAP.Host {
		t.Errorf("IMAP host = %q", loaded.IMAP.Host)
	}

	// DoAdminDomainLoad returns the JSON document plus the name.
	ctx = adminContext(dispatcher, map[string]any{"name": "example.com"})
	response := mustResponse(doAdminDomainLoad(ctx))
	result, _ := response["Result"].(map[string]any)
	if "example.com" != result["name"] {
		t.Errorf("name = %v", result["name"])
	}
	imap, _ := result["IMAP"].(map[string]any)
	if "imap.example.com" != imap["host"] {
		t.Errorf("IMAP.host = %v", imap["host"])
	}

	// A second "create" of the same domain is refused.
	ctx = adminContext(dispatcher, domainSaveParams("example.com", "imap.example.com"))
	if _, err := doAdminDomainSave(ctx); appctx.DomainAlreadyExists != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.DomainAlreadyExists)
	}
}

// TestAdminDomainListDisableDelete checks listing, disabling and deletion.
func TestAdminDomainListDisableDelete(t *testing.T) {
	dir := t.TempDir()
	provider := domain.NewProvider(dir, nil, domain.Defaults{})
	dispatcher := domainAdminDispatcher(provider, newAdminConfigFake())

	if err := provider.Save(provider.NewFromArray("a.example.com", map[string]any{
		"IMAP": map[string]any{"host": "imap.a.example.com"},
	})); nil != err {
		t.Fatalf("save: %v", err)
	}

	response := mustResponse(doAdminDomainList(adminContext(dispatcher, map[string]any{})))
	list, _ := response["Result"].([]any)
	if 1 != len(list) {
		t.Fatalf("list = %v", response["Result"])
	}
	entry, _ := list[0].(map[string]any)
	if "a.example.com" != entry["name"] || false != entry["disabled"] {
		t.Errorf("entry = %v", entry)
	}

	// Disable: the domain no longer loads, the list flags it.
	if response := mustResponse(doAdminDomainDisable(adminContext(dispatcher, map[string]any{
		"name": "a.example.com", "disabled": "1",
	}))); nil == response {
		t.Fatal("doAdminDomainDisable failed")
	}
	if nil != provider.Load("a.example.com") {
		t.Error("a disabled domain must not load")
	}
	list, _ = mustResponse(doAdminDomainList(adminContext(dispatcher, map[string]any{})))["Result"].([]any)
	entry, _ = list[0].(map[string]any)
	if true != entry["disabled"] {
		t.Errorf("entry = %v, want disabled", entry)
	}

	// Delete removes the file.
	if response := mustResponse(doAdminDomainDelete(adminContext(dispatcher, map[string]any{
		"name": "a.example.com",
	}))); true != response["Result"] {
		t.Errorf("delete Result = %v", response["Result"])
	}
	if _, err := os.Stat(filepath.Join(dir, "a.example.com.json")); nil == err {
		t.Error("a.example.com.json must be removed")
	}
}

// TestAdminDomainAliasSave checks the alias create and its existence guard.
func TestAdminDomainAliasSave(t *testing.T) {
	dir := t.TempDir()
	provider := domain.NewProvider(dir, nil, domain.Defaults{})
	dispatcher := domainAdminDispatcher(provider, newAdminConfigFake())

	if err := provider.Save(provider.NewFromArray("real.example.com", map[string]any{
		"IMAP": map[string]any{"host": "imap.real.example.com"},
	})); nil != err {
		t.Fatalf("save: %v", err)
	}

	// An alias of a plain (non-existing) name is stored.
	if response := mustResponse(doAdminDomainAliasSave(adminContext(dispatcher, map[string]any{
		"name": "alias.example.com", "alias": "real.example.com",
	}))); true != response["Result"] {
		t.Errorf("alias Result = %v", response["Result"])
	}
	if _, err := os.Stat(filepath.Join(dir, "alias.example.com.alias")); nil != err {
		t.Errorf("the alias file was not written: %v", err)
	}

	// Aliasing to an existing name is refused.
	if _, err := doAdminDomainAliasSave(adminContext(dispatcher, map[string]any{
		"name": "real.example.com", "alias": "other.example.com",
	})); appctx.DomainAlreadyExists != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.DomainAlreadyExists)
	}
}

// TestAdminDomainMatch checks the login preview.
func TestAdminDomainMatch(t *testing.T) {
	dir := t.TempDir()
	provider := domain.NewProvider(dir, nil, domain.Defaults{})
	dispatcher := domainAdminDispatcher(provider, newAdminConfigFake())

	if err := provider.Save(provider.NewFromArray("example.com", map[string]any{
		"IMAP":      map[string]any{"host": "imap.example.com"},
		"whiteList": "user@example.com",
	})); nil != err {
		t.Fatalf("save: %v", err)
	}

	response := mustResponse(doAdminDomainMatch(adminContext(dispatcher, map[string]any{
		"username": "user@example.com",
	})))
	result, _ := response["Result"].(map[string]any)
	if "user@example.com" != result["email"] {
		t.Errorf("email = %v", result["email"])
	}
	if "example.com" != result["name"] {
		t.Errorf("name = %v", result["name"])
	}
	if true != result["whitelist"] {
		t.Errorf("whitelist = %v, want true for the listed address", result["whitelist"])
	}
}

// TestAdminDomainAutoconfig checks the domain validation of the autoconfig
// action (the remote discovery is stubbed).
func TestAdminDomainAutoconfig(t *testing.T) {
	dir := t.TempDir()
	provider := domain.NewProvider(dir, nil, domain.Defaults{})
	dispatcher := domainAdminDispatcher(provider, newAdminConfigFake())

	response := mustResponse(doAdminDomainAutoconfig(adminContext(dispatcher, map[string]any{
		"domain": "example.com",
	})))
	result, _ := response["Result"].(map[string]any)
	if "test@example.com" != result["email"] {
		t.Errorf("email = %v", result["email"])
	}
	if _, ok := result["config"]; !ok {
		t.Error("config key is missing")
	}

	for _, bad := range []string{"", "127.0.0.1", "not a domain", "-bad.example.com"} {
		ctx := adminContext(dispatcher, map[string]any{"domain": bad})
		if _, err := doAdminDomainAutoconfig(ctx); appctx.InvalidInputArgument != clientCodeOf(err) {
			t.Errorf("domain %q: code = %d, want %d", bad, clientCodeOf(err), appctx.InvalidInputArgument)
		}
	}
}
