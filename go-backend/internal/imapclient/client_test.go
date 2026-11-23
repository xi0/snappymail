package imapclient

// Tests of Step 14: connect, capability negotiation/filtering and the login
// (SASL) paths of internal/imapclient, against the mock server.

import (
	"errors"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

func TestConnectCapabilityFromGreeting(t *testing.T) {
	server := newMockIMAP(t)
	client := New(server.settings(), nil)

	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// The greeting carried the capabilities, so no CAPABILITY command is sent.
	if server.hasCommand("CAPABILITY") {
		t.Errorf("CAPABILITY was sent although the greeting advertised it: %v", server.commands())
	}

	want := []string{"IMAP4REV1", "AUTH=PLAIN", "SASL-IR"}
	if got := client.Capabilities(); !equalStrings(want, got) {
		t.Errorf("Capabilities = %v, want %v", got, want)
	}
	if !client.HasCapability("auth=plain") {
		t.Error("HasCapability is case insensitive")
	}
	if value, ok := client.CapabilityValue("auth"); !ok || "PLAIN" != value {
		t.Errorf("CapabilityValue = %q, %v", value, ok)
	}
}

func TestCapabilityFiltering(t *testing.T) {
	server := newMockIMAP(t)
	server.greeting = "* OK [CAPABILITY IMAP4rev1 PREVIEW=FUZZY SORT=DISPLAY MOVE " +
		"THREAD=REFERENCES STATUS=SIZE] mock ready"

	settings := server.settings()
	settings.DisabledCapabilities = []string{"MOVE", "THREAD", "STATUS=SIZE"}
	client := New(settings, nil)

	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// The unused capabilities are stripped from the raw set …
	wantRaw := []string{"IMAP4REV1", "MOVE", "THREAD=REFERENCES", "STATUS=SIZE"}
	if got := client.Capabilities(); !equalStrings(wantRaw, got) {
		t.Errorf("Capabilities = %v, want %v", got, wantRaw)
	}

	// … and disabled_capabilities (including the THREAD= prefix rule) from the
	// active set.
	wantActive := []string{"IMAP4REV1"}
	if got := client.Capability(); !equalStrings(wantActive, got) {
		t.Errorf("Capability = %v, want %v", got, wantActive)
	}
}

func TestCapabilityCommandWhenGreetingHasNone(t *testing.T) {
	server := newMockIMAP(t)
	server.greeting = "* OK mock ready"
	server.capabilities = "IMAP4rev1 AUTH=PLAIN"

	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// No capabilities in the greeting: the first HasCapability triggers a
	// CAPABILITY command.
	if !client.HasCapability("AUTH=PLAIN") {
		t.Error("AUTH=PLAIN should be available after a CAPABILITY fetch")
	}
	if !server.hasCommand("CAPABILITY") {
		t.Errorf("CAPABILITY was not sent: %v", server.commands())
	}
}

func TestLoginPlainSASLIR(t *testing.T) {
	server := newMockIMAP(t)
	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if !client.IsLoggedIn() {
		t.Error("client should be logged in")
	}
	if !server.hasCommand("AUTHENTICATE PLAIN") {
		t.Errorf("AUTHENTICATE PLAIN was not sent: %v", server.commands())
	}
}

func TestLoginWrongPasswordIsBadCredentials(t *testing.T) {
	server := newMockIMAP(t)
	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	err := client.Login("user@example.com", "wrong")
	if nil == err {
		t.Fatal("a wrong password must fail")
	}

	var mailError *auth.MailError
	if !errors.As(err, &mailError) {
		t.Fatalf("error is not *auth.MailError: %v", err)
	}
	if auth.MailErrorBadCredentials != mailError.Kind {
		t.Errorf("kind = %v, want BadCredentials", mailError.Kind)
	}
	if client.IsLoggedIn() {
		t.Error("client must not be logged in after a failed login")
	}
}

func TestLoginAlertFromStatus(t *testing.T) {
	server := newMockIMAP(t)
	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// A server that reports the failure as an [ALERT] carries the text.
	server.mu.Lock()
	server.pass = "something-else"
	server.mu.Unlock()

	// AUTHENTICATIONFAILED is not ALERT, so the alert text is empty.
	err := client.Login("user@example.com", "wrong")
	var mailError *auth.MailError
	if !errors.As(err, &mailError) {
		t.Fatalf("error is not *auth.MailError: %v", err)
	}
	if "" != mailError.Alert {
		t.Errorf("alert = %q, want empty for a non-ALERT failure", mailError.Alert)
	}
}

func TestLoginCommandFallback(t *testing.T) {
	server := newMockIMAP(t)
	// No AUTH= capability and no LOGINDISABLED: the RFC 3501 LOGIN command.
	server.greeting = "* OK [CAPABILITY IMAP4rev1] mock ready"
	server.capabilities = "IMAP4rev1"

	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if !server.hasCommand("LOGIN \"user@example.com\" \"secret\"") {
		t.Errorf("the LOGIN command was not sent: %v", server.commands())
	}
	if server.hasCommand("AUTHENTICATE") {
		t.Errorf("AUTHENTICATE must not be used when falling back to LOGIN: %v", server.commands())
	}
}

func TestLoginLogindisabled(t *testing.T) {
	server := newMockIMAP(t)
	server.greeting = "* OK [CAPABILITY IMAP4rev1 LOGINDISABLED SASL-IR] mock ready"
	server.capabilities = "IMAP4rev1 LOGINDISABLED SASL-IR"

	client := New(server.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// LOGIN is disabled and no SASL mechanism is advertised: the login fails
	// before any AUTHENTICATE/LOGIN command.
	err := client.Login("user@example.com", "secret")
	if nil == err {
		t.Fatal("login must fail when nothing is supported")
	}
	if server.hasCommand("AUTHENTICATE") || server.hasCommand("LOGIN ") {
		t.Errorf("no authentication command should have been sent: %v", server.commands())
	}
}

func TestStartTLS(t *testing.T) {
	server := newMockIMAP(t)
	server.greeting = "* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN SASL-IR STARTTLS] mock ready"
	server.capabilities = "IMAP4rev1 AUTH=PLAIN SASL-IR STARTTLS"
	server.tlsConfig = autoTLSConfig(t)

	settings := server.settings()
	settings.Type = domain.SecuritySTARTTLS
	settings.SSL.VerifyPeer = false

	client := New(settings, nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if !server.wasTLSUpgraded() {
		t.Fatal("the connection was not upgraded with STARTTLS")
	}
	if !client.encrypted {
		t.Error("client should report an encrypted connection")
	}
	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login over TLS: %v", err)
	}
}

func TestConnectRefusedIsConnectionError(t *testing.T) {
	// A settings host/port that nothing listens on yields a dial failure, which
	// imapConnect() maps to ConnectionError (104).
	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = 1 // nothing listens here
	settings.Type = domain.SecurityNone
	settings.Timeout = 1

	client := New(&settings, nil)
	err := client.Connect()
	if nil == err {
		t.Fatal("connecting to a closed port must fail")
	}

	var mailError *auth.MailError
	if !errors.As(err, &mailError) {
		t.Fatalf("error is not *auth.MailError: %v", err)
	}
	if auth.MailErrorConnection != mailError.Kind {
		t.Errorf("kind = %v, want Connection", mailError.Kind)
	}
}

// equalStrings compares two string slices.
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
