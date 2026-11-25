package sieveclient

// Tests of the Step 23 ManageSieve transport against the in-process mock server:
// the capability/SASL negotiation, the script CRUD commands, the literal and
// quoted-string codec, STARTTLS and the negative/connection error classes.

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// settingsFor builds the Sieve settings of the mock for one connection.
func settingsFor(m *mockSieve, securityType int) *domain.SieveSettings {
	settings := domain.New("example.com", domain.Defaults{}).Sieve
	settings.Host = "127.0.0.1"
	settings.Port = m.port
	settings.Type = securityType
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false
	return &settings
}

// accountFor builds the account ConnectAndLogin() needs for settings.
func accountFor(settings domain.SieveSettings) *account.Account {
	config := domain.New("example.com", domain.Defaults{})
	config.Sieve = settings

	main := account.NewMainAccount()
	main.SetCredentials(config, "user@example.com", "user@example.com", "secret", "", "")
	return &main.Account
}

// TestClientGreetingAndCapabilities checks the capability parsing of the
// greeting (Modules(), HasCapability(), SupportsAuthType()).
func TestClientGreetingAndCapabilities(t *testing.T) {
	server := newMockSieve(t)

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if !client.HasCapability("SASL") || !client.HasCapability("version") {
		t.Errorf("capabilities = %v", client.capa)
	}
	if client.HasCapability("NOT-A-CAPABILITY") {
		t.Error("HasCapability must be exact")
	}
	if !client.SupportsAuthType("plain") {
		t.Errorf("SASL mechanisms = %v", client.auth)
	}
	if client.SupportsAuthType("CRAM-MD5") {
		t.Errorf("SASL mechanisms = %v", client.auth)
	}

	// The "SIEVE" capability is lower cased and split on spaces, in protocol
	// order (Load() sorts it, the client does not).
	modules := client.Modules()
	if strings.Join(modules, " ") != mockModules {
		t.Errorf("Modules() = %v", modules)
	}

	// A CAPABILITY command refreshes the set.
	fresh, err := client.Capability(true)
	if nil != err {
		t.Fatalf("Capability: %v", err)
	}
	if "Mock ManageSieve" != fresh["IMPLEMENTATION"] {
		t.Errorf("IMPLEMENTATION = %q", fresh["IMPLEMENTATION"])
	}
	if !server.hasCommand("CAPABILITY") {
		t.Error("the server did not receive CAPABILITY")
	}
}

// TestClientLoginPlainLiteral checks the default authLiteral exchange and the
// script commands of one authenticated session.
func TestClientLoginPlainLiteral(t *testing.T) {
	server := newMockSieve(t)

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if !client.IsLoggedIn() {
		t.Error("IsLoggedIn must be true")
	}

	// AUTHENTICATE "PLAIN" {len+} + the base64 payload as a literal.
	if !server.hasCommand(`AUTHENTICATE "PLAIN" {32+}`) {
		t.Errorf("commands = %v", server.commands())
	}
	if !server.hasCommand("AHVzZXJAZXhhbXBsZS5jb20Ac2VjcmV0") {
		t.Errorf("the PLAIN payload was not sent as a literal: %v", server.commands())
	}

	scripts, err := client.ListScripts()
	if nil != err {
		t.Fatalf("ListScripts: %v", err)
	}
	if 2 != len(scripts) || !scripts["rainloop.user"] || scripts["other"] {
		t.Errorf("ListScripts() = %v", scripts)
	}

	body, err := client.GetScript("rainloop.user")
	if nil != err {
		t.Fatalf("GetScript: %v", err)
	}
	// PHP's parseResponse() keeps the empty line that follows the literal, and
	// GetScript() joins the remaining lines, so the body ends up with a
	// trailing CRLF. Ported as-is.
	if !strings.HasPrefix(body, "require [\"fileinto\"];\r\nkeep;\r\n") {
		t.Errorf("GetScript() = %q", body)
	}
	if !server.hasCommand(`GETSCRIPT "rainloop.user"`) {
		t.Errorf("commands = %v", server.commands())
	}

	// PutScript normalises the line endings and sends the script as a literal.
	if err := client.PutScript("other", "if true { keep; }\nstop;\n"); nil != err {
		t.Fatalf("PutScript: %v", err)
	}
	stored, ok := server.script("other")
	if !ok {
		t.Fatal("the script was not stored")
	}
	// Byte counts are of the CRLF normalised script, plus the CRLF that
	// terminates the literal line.
	if !strings.HasPrefix(stored, "if true { keep; }\r\nstop;\r\n") {
		t.Errorf("stored = %q", stored)
	}
	if !server.hasCommand("PUTSCRIPT \"other\" {" + strconv.Itoa(len("if true { keep; }\r\nstop;\r\n")) + "+}") {
		t.Errorf("commands = %v", server.commands())
	}

	if err := client.SetActiveScript("other"); nil != err {
		t.Fatalf("SetActiveScript: %v", err)
	}
	if "other" != server.activeScript() {
		t.Errorf("active script = %q", server.activeScript())
	}

	// An empty name deactivates the script (RFC 5804 §2.7).
	if err := client.SetActiveScript(""); nil != err {
		t.Fatalf("SetActiveScript(\"\"): %v", err)
	}
	if "" != server.activeScript() {
		t.Errorf("active script = %q", server.activeScript())
	}

	if err := client.DeleteScript("other"); nil != err {
		t.Fatalf("DeleteScript: %v", err)
	}
	if _, ok := server.script("other"); ok {
		t.Error("the script was not deleted")
	}

	if err := client.CheckScript("keep;\n"); nil != err {
		t.Fatalf("CheckScript: %v", err)
	}
	if !server.hasCommand("CHECKSCRIPT {7+}") {
		t.Errorf("commands = %v", server.commands())
	}

	client.Logout()
	if !server.hasCommand("LOGOUT") {
		t.Errorf("commands = %v", server.commands())
	}
}

// TestClientLoginPlainQuoted checks the non-literal PLAIN form
// (SieveSettings.authLiteral = false).
func TestClientLoginPlainQuoted(t *testing.T) {
	server := newMockSieve(t)

	settings := settingsFor(server, domain.SecurityNone)
	settings.AuthLiteral = false

	client := New(settings, nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if !server.hasCommand(`AUTHENTICATE "PLAIN" "AHVzZXJAZXhhbXBsZS5jb20Ac2VjcmV0"`) {
		t.Errorf("commands = %v", server.commands())
	}
}

// TestClientLoginLoginMechanism checks the AUTHENTICATE "LOGIN" exchange, which
// is chosen when the domain only prefers LOGIN.
func TestClientLoginLoginMechanism(t *testing.T) {
	server := newMockSieve(t)

	settings := settingsFor(server, domain.SecurityNone)
	settings.SASL = []string{"LOGIN"}

	client := New(settings, nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}

	// base64("user@example.com") and base64("secret"), each as a literal.
	if !server.hasCommand(`AUTHENTICATE "LOGIN"`) {
		t.Errorf("commands = %v", server.commands())
	}
	if !server.hasCommand("{24+}") || !server.hasCommand("dXNlckBleGFtcGxlLmNvbQ==") {
		t.Errorf("the user name literal was not sent: %v", server.commands())
	}
	if !server.hasCommand("{8+}") || !server.hasCommand("c2VjcmV0") {
		t.Errorf("the password literal was not sent: %v", server.commands())
	}
}

// TestClientStartTLSFromLogin checks that Login() upgrades a plaintext
// connection when the server only advertises SASL after STARTTLS.
func TestClientStartTLSFromLogin(t *testing.T) {
	server := newMockSieve(t)
	server.tlsConfig = autoTLSConfig(t)
	server.hideSASL = true

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if client.HasCapability("SASL") {
		t.Error("SASL must not be advertised before STARTTLS")
	}

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	if !server.wasTLSUpgraded() {
		t.Error("the connection was not upgraded")
	}
	// The SASL mechanisms the post-STARTTLS capability list advertised are what
	// made the login possible; parseStartupResponse() keeps them (it only
	// rebuilds the capability map, exactly like PHP).
	if len(client.auth) != 2 || "PLAIN" != client.auth[0] || "LOGIN" != client.auth[1] {
		t.Errorf("SASL mechanisms = %v", client.auth)
	}
	// Documented quirk: the reply of the AUTHENTICATE command is parsed with
	// parseStartupResponse() too, which replaces the capability map with the
	// empty one of an "OK" line — so HasCapability() is false again after a
	// successful login (PHP does the same).
	if client.HasCapability("SASL") {
		t.Error("the capability map is replaced by the login response, like PHP")
	}
}

// TestClientStartTLSSettings checks the explicit STARTTLS type, which upgrades
// during Connect().
func TestClientStartTLSSettings(t *testing.T) {
	server := newMockSieve(t)
	server.tlsConfig = autoTLSConfig(t)

	client := New(settingsFor(server, domain.SecuritySTARTTLS), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if !server.wasTLSUpgraded() {
		t.Error("Connect() must run STARTTLS for the STARTTLS type")
	}
	if !client.encrypted {
		t.Error("the client does not consider the connection encrypted")
	}
}

// TestClientNegativeResponse checks the negative-response error class and its
// carried reply lines.
func TestClientNegativeResponse(t *testing.T) {
	server := newMockSieve(t)
	server.refuse = "LISTSCRIPTS"

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}

	_, err := client.ListScripts()
	if nil == err {
		t.Fatal("ListScripts must fail")
	}

	var failure *Error
	if !errors.As(err, &failure) || KindNegative != failure.Kind {
		t.Fatalf("error = %#v", err)
	}
	if 1 != len(failure.Responses) || !strings.HasPrefix(failure.Responses[0], "NO") {
		t.Errorf("Responses = %v", failure.Responses)
	}
}

// TestClientBadCredentials checks the login failure class: a rejected password
// is still a negative response, exactly like the PHP LoginBadCredentialsException.
func TestClientBadCredentials(t *testing.T) {
	server := newMockSieve(t)

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	err := client.Login("user@example.com", "wrong")
	if nil == err {
		t.Fatal("Login must fail")
	}

	var failure *Error
	if !errors.As(err, &failure) || KindNegative != failure.Kind {
		t.Fatalf("error = %#v", err)
	}
	if client.IsLoggedIn() {
		t.Error("IsLoggedIn must stay false")
	}
}

// TestClientConnectionFailure checks the connection error class of an
// unreachable server.
func TestClientConnectionFailure(t *testing.T) {
	settings := domain.New("example.com", domain.Defaults{}).Sieve
	settings.Host = "127.0.0.1"
	// Port 1 is reserved and cannot be served, so the dial fails.
	settings.Port = 1
	settings.Timeout = 2

	_, err := ConnectAndLogin(accountFor(settings), nil, nil)
	if nil == err {
		t.Fatal("ConnectAndLogin must fail")
	}

	var failure *Error
	if !errors.As(err, &failure) || KindConnection != failure.Kind {
		t.Fatalf("error = %#v", err)
	}
}

// TestClientRejectsEmptyScript checks the sendRequest() guard: PHP cannot save
// an empty script either, because the body is sent as a request line.
func TestClientRejectsEmptyScript(t *testing.T) {
	server := newMockSieve(t)

	client := New(settingsFor(server, domain.SecurityNone), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Disconnect()

	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}

	if err := client.PutScript("empty", ""); nil == err {
		t.Fatal("PutScript(\"\") must fail like PHP")
	}
}

// TestLiteralLength checks intval() of the "{N+}" marker.
func TestLiteralLength(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"31+", 31},
		{"0+", 0},
		{"12", 12},
		{"-1+", -1},
		{"", 0},
		{"abc", 0},
		{"5abc", 5},
	}
	for _, test := range tests {
		if got := literalLength(test.input); got != test.want {
			t.Errorf("literalLength(%q) = %d, want %d", test.input, got, test.want)
		}
	}
}

// TestQuotedStrings checks the parseLine() codec against the PHP regexp and
// stripcslashes().
func TestQuotedStrings(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{`"SASL" "PLAIN LOGIN"`, []string{"SASL", "PLAIN LOGIN"}},
		{`"a \"quoted\" name" ACTIVE`, []string{`a "quoted" name`}},
		{`"back\\slash"`, []string{`back\slash`}},
		{"OK \"done\"", []string{"done"}},
		{`no quotes here`, nil},
	}

	for _, test := range tests {
		got := quotedStrings(test.line)
		if len(got) != len(test.want) {
			t.Errorf("quotedStrings(%q) = %v, want %v", test.line, got, test.want)
			continue
		}
		for index := range got {
			if got[index] != test.want[index] {
				t.Errorf("quotedStrings(%q)[%d] = %q, want %q", test.line, index, got[index], test.want[index])
			}
		}
	}

	// parseLine() drops the status lines.
	if nil != parseLine(`OK "done"`) {
		t.Error("parseLine must ignore OK lines")
	}
	if nil != parseLine(`NO "failed"`) {
		t.Error("parseLine must ignore NO lines")
	}
}

// TestNormalizeCRLF checks preg_replace('/\r?\n/', "\r\n").
func TestNormalizeCRLF(t *testing.T) {
	tests := []struct{ input, want string }{
		{"a\nb", "a\r\nb"},
		{"a\r\nb", "a\r\nb"},
		{"a\rb", "a\rb"},
		{"a\r\r\nb", "a\r\r\nb"},
		{"", ""},
	}
	for _, test := range tests {
		if got := normalizeCRLF(test.input); got != test.want {
			t.Errorf("normalizeCRLF(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

// TestEscapeName checks addcslashes($name, '"\\').
func TestEscapeName(t *testing.T) {
	tests := []struct{ input, want string }{
		{"plain", "plain"},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{`a"b\c"d`, `a\"b\\c\"d`},
		{"with space", "with space"},
	}
	for _, test := range tests {
		if got := escapeName(test.input); got != test.want {
			t.Errorf("escapeName(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}
