package auth

import (
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestSessionTokenGeneration(t *testing.T) {
	env := newTestEnv(t)
	authenticator, recorder := env.authenticator(nil)

	// Without a cookie and generate=false there is no token (PHP's null).
	if token := authenticator.SessionToken(false); "" != token {
		t.Errorf("SessionToken(false) = %q, want empty", token)
	}

	token := authenticator.SessionToken(true)
	if "" == token {
		t.Fatal("SessionToken(true) must generate a token")
	}

	raw := authenticator.Cookies.Get(SessionTokenName)
	if "" == raw {
		t.Fatal("the smsession cookie was not set")
	}
	if !cookieLine(recorder, SessionTokenName+"=") {
		t.Error("the smsession cookie was not written to the response")
	}

	// PHP: sha1('Session' . APP_SALT . $token . 'Token' . APP_SALT).
	want := storage.SHA1("Session" + testSalt + raw + "Token" + testSalt)
	if want != token {
		t.Errorf("SessionToken = %q, want %q", token, want)
	}

	// The token of the same request is stable, and a second request carrying the
	// cookie derives the same token.
	if again := authenticator.SessionToken(true); token != again {
		t.Errorf("SessionToken changed: %q != %q", again, token)
	}

	next, _ := env.authenticator(requestFromCookies(recorder))
	if again := next.SessionToken(false); token != again {
		t.Errorf("SessionToken of the next request = %q, want %q", again, token)
	}
}

func TestConnectionTokenForGuests(t *testing.T) {
	env := newTestEnv(t)
	authenticator, recorder := env.authenticator(nil)

	token := authenticator.ConnectionToken()
	if "" == token {
		t.Fatal("ConnectionToken must return a value")
	}
	if "0-" != token[:2] {
		t.Errorf("ConnectionToken = %q, want the guest prefix 0-", token)
	}

	raw := authenticator.Cookies.Get(ConnectionTokenName)
	want := "0-" + storage.SHA1("Connection"+testSalt+raw+"Token"+testSalt)
	if want != token {
		t.Errorf("ConnectionToken = %q, want %q", token, want)
	}

	// The smtoken cookie is a 30 day cookie.
	lines := recorder.Header().Values("Set-Cookie")
	if 0 == len(lines) {
		t.Fatal("the smtoken cookie was not written")
	}
	for _, line := range lines {
		if !strings.Contains(line, ConnectionTokenName+"=") {
			continue
		}
		if !strings.Contains(line, "Expires=") {
			t.Errorf("the smtoken cookie must expire: %q", line)
		}
	}

	// UpdateConnectionToken refreshes an existing cookie.
	refreshed, refreshRecorder := env.authenticator(requestWith(map[string]string{ConnectionTokenName: "fixed"}))
	refreshed.UpdateConnectionToken()
	if !cookieLine(refreshRecorder, ConnectionTokenName+"=fixed") {
		t.Errorf("UpdateConnectionToken did not refresh the cookie: %v",
			refreshRecorder.Header().Values("Set-Cookie"))
	}

	// Without a cookie it writes nothing.
	silent, silentRecorder := env.authenticator(nil)
	silent.UpdateConnectionToken()
	if 0 != len(silentRecorder.Header().Values("Set-Cookie")) {
		t.Errorf("UpdateConnectionToken without a cookie wrote %v",
			silentRecorder.Header().Values("Set-Cookie"))
	}
}

func TestCsrfTokenIsTheConnectionToken(t *testing.T) {
	env := newTestEnv(t)
	authenticator, _ := env.authenticator(requestWith(map[string]string{ConnectionTokenName: "fixed"}))

	if got, want := authenticator.CSRFToken(), authenticator.ConnectionToken(); want != got {
		t.Errorf("CSRFToken = %q, want %q", got, want)
	}
}

func TestEncodeDecodeKeyValuesQ(t *testing.T) {
	env := newTestEnv(t)
	authenticator, _ := env.authenticator(nil)

	values := map[string]any{"Folder": "INBOX", "Uid": int64(42)}
	encoded, err := authenticator.EncodeKeyValuesQ(values, "")
	if nil != err {
		t.Fatalf("EncodeKeyValuesQ: %v", err)
	}
	if "" == encoded {
		t.Fatal("EncodeKeyValuesQ returned nothing")
	}

	decoded := authenticator.DecodeKeyValuesQ(encoded, "")
	if "INBOX" != decoded["Folder"] || int64(42) != decoded["Uid"] {
		t.Errorf("DecodeKeyValuesQ = %v", decoded)
	}

	// The blob is bound to the session token: another session cannot read it.
	other, _ := env.authenticator(nil)
	if decoded := other.DecodeKeyValuesQ(encoded, ""); 0 != len(decoded) {
		t.Errorf("a foreign session decoded the blob: %v", decoded)
	}

	// The custom key is part of the derivation as well.
	sameSession, _ := env.authenticator(requestWith(map[string]string{
		SessionTokenName: authenticator.Cookies.Get(SessionTokenName),
	}))
	if decoded := sameSession.DecodeKeyValuesQ(encoded, "custom"); 0 != len(decoded) {
		t.Errorf("a foreign custom key decoded the blob: %v", decoded)
	}
	if decoded := sameSession.DecodeKeyValuesQ(encoded, ""); "INBOX" != decoded["Folder"] {
		t.Errorf("the same session could not decode the blob: %v", decoded)
	}
}
