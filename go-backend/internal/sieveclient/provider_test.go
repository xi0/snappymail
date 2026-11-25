package sieveclient

// Tests of the Step 23 filter provider: the Load() document (sorted
// capabilities, script documents, the phantom "rainloop.user" entry), the
// mutation commands, and the Providers\Filters::handleException() notification
// code mapping.

import (
	"errors"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// providerCode is the notification code of a provider failure.
func providerCode(t *testing.T, err error) *appctx.ClientError {
	t.Helper()

	var clientErr *appctx.ClientError
	if !errors.As(err, &clientErr) || nil == clientErr {
		t.Fatalf("not a client error: %#v", err)
	}
	return clientErr
}

// TestProviderLoad checks the document SieveStorage::Load() produces.
func TestProviderLoad(t *testing.T) {
	server := newMockSieve(t)

	result, err := NewProvider(nil, nil).Load(server.account(true))
	if nil != err {
		t.Fatalf("Load: %v", err)
	}

	document, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("Load() = %#v", result)
	}

	// Capa is sort()ed: "body fileinto imap4flags regex reject vacation".
	capa, ok := document["Capa"].([]any)
	if !ok {
		t.Fatalf("Capa = %#v", document["Capa"])
	}
	want := []string{"body", "fileinto", "imap4flags", "regex", "reject", "vacation"}
	if len(capa) != len(want) {
		t.Fatalf("Capa = %v", capa)
	}
	for index, value := range want {
		if capa[index] != value {
			t.Errorf("Capa[%d] = %v, want %q", index, capa[index], value)
		}
	}

	scripts, ok := document["Scripts"].(map[string]any)
	if !ok {
		t.Fatalf("Scripts = %#v", document["Scripts"])
	}
	if 2 != len(scripts) {
		t.Fatalf("Scripts = %#v", scripts)
	}

	rainloop, ok := scripts["rainloop.user"].(map[string]any)
	if !ok {
		t.Fatalf("rainloop.user = %#v", scripts["rainloop.user"])
	}
	if "Object/SieveScript" != rainloop["@Object"] {
		t.Errorf("@Object = %v", rainloop["@Object"])
	}
	if "rainloop.user" != rainloop["name"] {
		t.Errorf("name = %v", rainloop["name"])
	}
	if true != rainloop["active"] {
		t.Errorf("active = %v", rainloop["active"])
	}
	// The trailing CRLF is the parseResponse()/GetScript() quirk of PHP: the
	// empty line after the literal is part of the body.
	if body, _ := rainloop["body"].(string); !strings.HasPrefix(body, "require [\"fileinto\"];\r\nkeep;") {
		t.Errorf("body = %q", body)
	}

	other, ok := scripts["other"].(map[string]any)
	if !ok {
		t.Fatalf("other = %#v", scripts["other"])
	}
	if false != other["active"] {
		t.Errorf("other active = %v", other["active"])
	}
}

// TestProviderLoadAddsTheRainloopScript checks the phantom "rainloop.user" entry
// SieveStorage::Load() appends when the server does not have that script.
func TestProviderLoadAddsTheRainloopScript(t *testing.T) {
	server := newMockSieve(t)
	server.scripts = map[string]string{"custom": "keep;"}
	server.order = []string{"custom"}
	server.active = "custom"

	result, err := NewProvider(nil, nil).Load(server.account(true))
	if nil != err {
		t.Fatalf("Load: %v", err)
	}

	scripts, ok := result.(map[string]any)["Scripts"].(map[string]any)
	if !ok {
		t.Fatalf("Scripts = %#v", result)
	}
	if 2 != len(scripts) {
		t.Fatalf("Scripts = %#v", scripts)
	}

	fallback, ok := scripts[SieveFileName].(map[string]any)
	if !ok {
		t.Fatalf("the %q fallback is missing: %#v", SieveFileName, scripts)
	}
	if false != fallback["active"] || "" != fallback["body"] {
		t.Errorf("fallback = %#v", fallback)
	}
}

// TestProviderLoadWithoutAccount checks the "no driver" answer (an empty PHP
// array, i.e. JSON []).
func TestProviderLoadWithoutAccount(t *testing.T) {
	result, err := NewProvider(nil, nil).Load(nil)
	if nil != err {
		t.Fatalf("Load: %v", err)
	}

	list, ok := result.([]any)
	if !ok || 0 != len(list) {
		t.Errorf("Load(nil) = %#v", result)
	}
}

// TestProviderSaveActivateDelete checks the three mutation commands.
func TestProviderSaveActivateDelete(t *testing.T) {
	server := newMockSieve(t)
	provider := NewProvider(nil, nil)
	target := server.account(true)

	saved, err := provider.Save(target, "second", "keep;\n")
	if nil != err || !saved {
		t.Fatalf("Save: %v %v", saved, err)
	}
	if body, ok := server.script("second"); !ok || !strings.HasPrefix(body, "keep;\r\n") {
		t.Errorf("stored script = %q", body)
	}

	activated, err := provider.ActivateScript(target, " second ")
	if nil != err || !activated {
		t.Fatalf("ActivateScript: %v %v", activated, err)
	}
	if "second" != server.activeScript() {
		t.Errorf("active script = %q", server.activeScript())
	}

	deleted, err := provider.DeleteScript(target, "second")
	if nil != err || !deleted {
		t.Fatalf("DeleteScript: %v %v", deleted, err)
	}
	if _, ok := server.script("second"); ok {
		t.Error("the script was not deleted")
	}

	// An unknown script name is not an error: PHP only sends DELETESCRIPT when
	// the script is listed.
	ignored, err := provider.DeleteScript(target, "does-not-exist")
	if nil != err || !ignored {
		t.Fatalf("DeleteScript(unknown): %v %v", ignored, err)
	}
	if server.hasCommand(`DELETESCRIPT "does-not-exist"`) {
		t.Error("DELETESCRIPT must not be sent for an unknown script")
	}
}

// TestProviderNegativeResponseCode checks the ClientViewError (902) mapping with
// the server reply lines as the additional message.
func TestProviderNegativeResponseCode(t *testing.T) {
	server := newMockSieve(t)
	server.refuse = "LISTSCRIPTS"

	_, err := NewProvider(nil, nil).Load(server.account(true))
	if nil == err {
		t.Fatal("Load must fail")
	}

	clientErr := providerCode(t, err)
	if appctx.ClientViewError != clientErr.Code {
		t.Errorf("code = %d, want %d", clientErr.Code, appctx.ClientViewError)
	}
	if !strings.Contains(clientErr.AdditionalMessage, "refused by the test") {
		t.Errorf("additional = %q", clientErr.AdditionalMessage)
	}
}

// TestProviderConnectionErrorCode checks the ConnectionError (104) mapping.
func TestProviderConnectionErrorCode(t *testing.T) {
	settings := newMockSieve(t).account(true).Domain().Sieve
	// Port 1 is reserved and cannot be served, so the dial fails.
	settings.Port = 1
	settings.Timeout = 2

	_, err := NewProvider(nil, nil).Load(accountFor(settings))
	if nil == err {
		t.Fatal("Load must fail")
	}

	if code := providerCode(t, err).Code; appctx.ConnectionError != code {
		t.Errorf("code = %d, want %d", code, appctx.ConnectionError)
	}
}

// TestProviderSaveErrorCode checks the CantSaveFilters (351) fallback for a
// failure that is neither a connection nor a negative response.
func TestProviderSaveErrorCode(t *testing.T) {
	// An empty script body is rejected by sendRequest() before it is sent, so
	// the failure carries no NegativeResponse class.
	_, err := NewProvider(nil, nil).Save(newMockSieve(t).account(true), "empty", "")
	if nil == err {
		t.Fatal("Save must fail")
	}

	if code := providerCode(t, err).Code; appctx.CantSaveFilters != code {
		t.Errorf("code = %d, want %d", code, appctx.CantSaveFilters)
	}
}
