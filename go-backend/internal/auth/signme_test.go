package auth

// Tests of the "remember me" cookie of step 8: SetSignMeToken(),
// GetAccountFromSignMeToken(), GetSignMeToken() and ClearSignMeData().

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestSetSignMeTokenFormat(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	main := env.mainAccount()

	login.SetSignMeToken(main)

	raw := recorderCookieValue(recorder, AuthSignMeTokenKey)
	if "" == raw {
		t.Fatal("the smremember cookie was not written")
	}

	// The cookie is Crypt::EncryptUrlSafe([...], 'signme') and holds the
	// address, the UUID, the method and the base64 nonce.
	value, err := env.crypt.DecryptURLSafe(raw, signMeCryptKey)
	if nil != err {
		t.Fatalf("DecryptURLSafe: %v", err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("the cookie holds %T", value)
	}

	email, _ := document["e"].(string)
	uuid, _ := document["u"].(string)
	cipher, _ := document["c"].(string)
	nonce, _ := document["d"].(string)
	if testEmail != email {
		t.Errorf("cookie e = %q", email)
	}
	if !uuidPattern.MatchString(uuid) {
		t.Errorf("cookie u = %q, want a v4 UUID", uuid)
	}
	if cryptox.MethodSodium != cipher {
		t.Errorf("cookie c = %q", cipher)
	}
	if _, err := base64.StdEncoding.DecodeString(nonce); nil != err {
		t.Errorf("cookie d is not base64: %q", nonce)
	}

	// The payload lives in .sign_me/<uuid> and decrypts to the token array of
	// the account, sealed with the "signme" key.
	payload, ok := env.storage.Get(main.Email(), storage.TypeSignMe, uuid)
	if !ok || "" == payload {
		t.Fatal("the .sign_me entry was not written")
	}
	decodedNonce, _ := base64.StdEncoding.DecodeString(nonce)
	decrypted, err := env.crypt.Decrypt(cryptox.Blob{
		Method:  cipher,
		Nonce:   decodedNonce,
		Payload: []byte(payload),
	}, signMeCryptKey)
	if nil != err {
		t.Fatalf("Decrypt: %v", err)
	}
	token, ok := decrypted.(map[string]any)
	if !ok || testEmail != token["email"] || testPassword != token["pass"] {
		t.Errorf("the stored payload = %v", decrypted)
	}
}

func TestSignMeTokenSurvivesARestart(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	main := env.mainAccount()

	login.SetSignMeToken(main)

	// A "restart": a new request, a new authenticator and a new login flow, but
	// the same data folder.
	request := requestWith(map[string]string{
		AuthSignMeTokenKey: recorderCookieValue(recorder, AuthSignMeTokenKey),
	})

	restarted, restartRecorder := env.loginFor(request)
	resolved := restarted.GetAccountFromSignMeToken()
	if nil == resolved {
		t.Fatal("the sign-me cookie did not survive the restart")
	}
	if testEmail != resolved.Email() || testPassword != resolved.ImapPass() {
		t.Errorf("resolved = %q/%q", resolved.Email(), resolved.ImapPass())
	}
	if nil == resolved.Domain() || "imap.example.com" != resolved.Domain().IMAP.Host {
		t.Errorf("domain = %v", resolved.Domain())
	}

	// Reading the token refreshes the cookie (SetSignMeToken writes a new one).
	refreshed := recorderCookieValue(restartRecorder, AuthSignMeTokenKey)
	if "" == refreshed {
		t.Fatal("the sign-me cookie was not refreshed")
	}
	if refreshed == request.Cookies()[0].Value {
		t.Error("the sign-me cookie was not replaced")
	}
}

func TestSignMeTokenAuthenticatesTheRequest(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	login.SetSignMeToken(env.mainAccount())

	// Only the smremember cookie: getMainAccountFromToken() falls back to it,
	// writes the SESSION entry and the smaccount cookie (step 7 wiring).
	request := requestWith(map[string]string{
		AuthSignMeTokenKey: recorderCookieValue(recorder, AuthSignMeTokenKey),
	})

	authenticator, response := env.authenticator(request)
	authenticator.SignMe = env.loginWith(authenticator).SignMeResolver()

	resolved, err := authenticator.GetMainAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetMainAccountFromToken: %v", err)
	}
	if nil == resolved || testEmail != resolved.Email() {
		t.Fatalf("resolved = %v", resolved)
	}
	if !cookieLine(response, AuthSpecTokenKey+"=") {
		t.Error("the smaccount cookie was not written")
	}
	if value, ok := env.storage.Get(resolved, storage.TypeSession, authenticator.SessionToken(false)); !ok || "true" != value {
		t.Errorf("the SESSION entry = %q, %v", value, ok)
	}
}

func TestInvalidSignMeTokenIsDropped(t *testing.T) {
	env := newTestEnv(t)

	// Not a CryptUrlSafe blob at all.
	login, recorder := env.loginFor(requestWith(map[string]string{AuthSignMeTokenKey: "garbage"}))
	if nil != login.GetAccountFromSignMeToken() {
		t.Fatal("a garbage cookie must not authenticate")
	}
	if !cookieLine(recorder, AuthSignMeTokenKey+"=") {
		t.Error("the unusable smremember cookie was not cleared")
	}
	if !env.logger.has(AuthSignMeTokenKey + ": invalid") {
		t.Errorf("the invalid token was not logged: %v", env.logger.lines)
	}

	// A valid blob without a UUID is invalid as well (UUID::isValid).
	cookie, err := env.crypt.EncryptURLSafe(signMeCookie{
		Email:  testEmail,
		UUID:   "not-a-uuid",
		Cipher: cryptox.MethodSodium,
		Nonce:  base64.StdEncoding.EncodeToString(make([]byte, 24)),
	}, signMeCryptKey)
	if nil != err {
		t.Fatalf("EncryptURLSafe: %v", err)
	}

	broken, brokenRecorder := env.loginFor(requestWith(map[string]string{AuthSignMeTokenKey: cookie}))
	if nil != broken.GetAccountFromSignMeToken() {
		t.Fatal("a cookie without a valid UUID must not authenticate")
	}
	if !cookieLine(brokenRecorder, AuthSignMeTokenKey+"=") {
		t.Error("the UUID-less smremember cookie was not cleared")
	}
}

func TestSignMeTokenWithoutStorageEntryIsDropped(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	login.SetSignMeToken(env.mainAccount())

	// The PHP backend cleaned up the .sign_me entry (or it expired after 30
	// days) while the cookie is still around.
	token := login.GetSignMeToken()
	if nil == token {
		t.Fatal("the sign-me token was not written")
	}
	if !env.storage.Clear(token.email, storage.TypeSignMe, token.uuid) {
		t.Fatal("the .sign_me entry could not be removed")
	}

	request := requestWith(map[string]string{
		AuthSignMeTokenKey: recorderCookieValue(recorder, AuthSignMeTokenKey),
	})
	restarted, restartRecorder := env.loginFor(request)

	if nil != restarted.GetAccountFromSignMeToken() {
		t.Fatal("a cookie without a stored payload must not authenticate")
	}
	if !env.logger.has(AuthSignMeTokenKey + ": server token not found for " + testEmail) {
		t.Errorf("the missing payload was not logged: %v", env.logger.lines)
	}
	if !cookieLine(restartRecorder, AuthSignMeTokenKey+"=") {
		t.Error("the orphaned smremember cookie was not cleared")
	}
}

func TestClearSignMeData(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	main := env.mainAccount()

	login.SetSignMeToken(main)

	token := login.GetSignMeToken()
	if nil == token {
		t.Fatal("the sign-me token was not written")
	}

	cleared, clearedRecorder := env.loginFor(requestWith(map[string]string{
		AuthSignMeTokenKey: recorderCookieValue(recorder, AuthSignMeTokenKey),
	}))
	cleared.ClearSignMeData()

	if !cookieLine(clearedRecorder, AuthSignMeTokenKey+"=") {
		t.Error("the smremember cookie was not cleared")
	}
	if _, ok := env.storage.Get(main.Email(), storage.TypeSignMe, token.uuid); ok {
		t.Error("the .sign_me entry was not removed")
	}

	// Without a cookie there is nothing to remove, but PHP still clears the
	// (already absent) cookie.
	silent, silentRecorder := env.loginFor(nil)
	silent.ClearSignMeData()
	if !cookieLine(silentRecorder, AuthSignMeTokenKey+"=") {
		t.Error("ClearSignMeData must clear the smremember cookie")
	}
	if 1 != len(silentRecorder.Header().Values("Set-Cookie")) {
		t.Errorf("ClearSignMeData wrote %v", silentRecorder.Header().Values("Set-Cookie"))
	}
}

func TestSignMeRefreshReplacesTheOldEntry(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)
	main := env.mainAccount()

	login.SetSignMeToken(main)
	first := login.GetSignMeToken()
	if nil == first {
		t.Fatal("no sign-me token")
	}

	// SetSignMeToken() calls ClearSignMeData() first, so the previous UUID is
	// gone and a fresh one is used.
	login.SetSignMeToken(main)
	second := login.GetSignMeToken()
	if nil == second {
		t.Fatal("no sign-me token")
	}
	if first.uuid == second.uuid {
		t.Error("the UUID was not replaced")
	}
	if _, ok := env.storage.Get(main.Email(), storage.TypeSignMe, first.uuid); ok {
		t.Error("the previous .sign_me entry was not removed")
	}
	if _, ok := env.storage.Get(main.Email(), storage.TypeSignMe, second.uuid); !ok {
		t.Error("the new .sign_me entry was not written")
	}

	// The cookie is a 30 day cookie, like time() + 3600 * 24 * 30.
	value := recorderCookieValue(recorder, AuthSignMeTokenKey)
	if "" == value {
		t.Fatal("no smremember cookie")
	}
	for _, line := range recorder.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, AuthSignMeTokenKey+"=") && !strings.Contains(line, "Expires=") {
			t.Errorf("the sign-me cookie has no expiry: %q", line)
		}
	}
}
