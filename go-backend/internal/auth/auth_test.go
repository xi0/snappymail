package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestAuthenticatedRequestRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	request, loginRecorder, _ := env.login(main)

	// The login wrote the smaccount cookie (encrypted account) and the
	// smsession cookie (raw session token).
	if !cookieLine(loginRecorder, AuthSpecTokenKey+"=") {
		t.Fatal("the smaccount cookie was not written")
	}
	if !cookieLine(loginRecorder, SessionTokenName+"=") {
		t.Fatal("the smsession cookie was not written")
	}

	// A later request authenticates from those cookies and the SESSION entry.
	authenticator, _ := env.authenticator(request)

	resolved, err := authenticator.GetMainAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetMainAccountFromToken: %v", err)
	}
	if nil == resolved {
		t.Fatal("no account was resolved")
	}
	if testEmail != resolved.Email() || testPassword != resolved.ImapPass() {
		t.Errorf("resolved = %q/%q", resolved.Email(), resolved.ImapPass())
	}
	if nil == resolved.Domain() || "imap.example.com" != resolved.Domain().IMAP.Host {
		t.Errorf("domain = %v", resolved.Domain())
	}
	if got, want := resolved.Hash(testSalt), main.Hash(testSalt); want != got {
		t.Errorf("Hash = %q, want %q", got, want)
	}

	// A logged in account gets the "1-" connection token derived from its hash.
	want := "1-" + storage.SHA1(testSalt+main.Hash(testSalt))
	if got := authenticator.ConnectionToken(); want != got {
		t.Errorf("ConnectionToken = %q, want %q", got, want)
	}

	// The account is also the one GetAccountFromToken reports.
	current, err := authenticator.GetAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetAccountFromToken: %v", err)
	}
	if nil == current || testEmail != current.Email() {
		t.Errorf("GetAccountFromToken = %v", current)
	}
	if nil == authenticator.MainAccount() {
		t.Error("MainAccount() must report the resolved account")
	}
}

func TestStaleSessionIsRejected(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	request, _, session := env.login(main)

	// The PHP backend kicked the session: the SESSION entry is gone.
	if !env.storage.Clear(main, storage.TypeSession, session) {
		t.Fatal("the SESSION entry could not be removed")
	}

	authenticator, recorder := env.authenticator(request)

	if _, err := authenticator.GetMainAccountFromToken(true); nil == err {
		t.Fatal("a stale session must fail")
	} else if code := clientCode(t, err); appctx.InvalidToken != code {
		t.Errorf("code = %d, want InvalidToken 101", code)
	}

	// PHP's get_debug_type() of the missing SESSION value is "null".
	if !env.logger.has("TOKENS: SESSION_TOKEN value invalid: null") {
		t.Errorf("the missing SESSION entry was not logged: %v", env.logger.lines)
	}

	// PHP clears the smsession cookie and both account cookies.
	for _, name := range []string{SessionTokenName, AuthSpecTokenKey, AuthAdditionalTokenKey} {
		if !cookieLine(recorder, name+"=") {
			t.Errorf("%s was not cleared: %v", name, recorder.Header().Values("Set-Cookie"))
		}
	}
}

func TestMissingSessionCookieIsReported(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	request, _, _ := env.login(main)
	// Remove the smsession cookie but keep the account cookie.
	pruned := requestWith(map[string]string{AuthSpecTokenKey: cookieValue(request, AuthSpecTokenKey)})

	authenticator, _ := env.authenticator(pruned)

	if _, err := authenticator.GetMainAccountFromToken(true); nil == err {
		t.Fatal("a missing session cookie must fail")
	} else if code := clientCode(t, err); appctx.InvalidToken != code {
		t.Errorf("code = %d, want InvalidToken 101", code)
	}

	if !env.logger.has("TOKENS: SESSION_TOKEN not set") {
		t.Errorf("the missing session cookie was not logged: %v", env.logger.lines)
	}
}

func TestEmptySessionCookieIsReportedAsInvalid(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	request, _, _ := env.login(main)
	pruned := requestWith(map[string]string{
		AuthSpecTokenKey: cookieValue(request, AuthSpecTokenKey),
		// An empty value is present but unusable, like a truncated cookie.
		SessionTokenName: "",
	})

	authenticator, _ := env.authenticator(pruned)

	if _, err := authenticator.GetMainAccountFromToken(false); nil != err {
		t.Fatalf("GetMainAccountFromToken(false): %v", err)
	}

	if !env.logger.has("TOKENS: SESSION_TOKEN invalid") {
		t.Errorf("the unusable session cookie was not logged: %v", env.logger.lines)
	}
}

func TestInvalidAccountCookieIsIgnoredWhenNotThrowing(t *testing.T) {
	env := newTestEnv(t)

	// A cookie that decrypts but does not hold a token array.
	sealed, err := env.crypt.EncryptToJSON(map[string]any{"a": 1}, "")
	if nil != err {
		t.Fatalf("EncryptToJSON: %v", err)
	}
	cookie := cryptox.URLSafeBase64Encode(sealed)

	request := requestWith(map[string]string{
		AuthSpecTokenKey: cookie,
		// The account is only read when a session token exists.
		SessionTokenName: "a-session",
	})

	authenticator, _ := env.authenticator(request)
	if resolved, err := authenticator.GetMainAccountFromToken(false); nil != err || nil != resolved {
		t.Fatalf("GetMainAccountFromToken(false) = %v, %v", resolved, err)
	}
	if !env.logger.has("TOKENS: AUTH_SPEC_TOKEN_KEY invalid") {
		t.Errorf("the unusable account cookie was not logged: %v", env.logger.lines)
	}

	// The PHP quirk: the failed attempt is cached, so a later call that asks to
	// throw still reports "no account" instead of the stored error.
	second, _ := env.authenticator(request)
	if _, err := second.GetMainAccountFromToken(false); nil != err {
		t.Fatalf("first call: %v", err)
	}
	if resolved, err := second.GetMainAccountFromToken(true); nil != err || nil != resolved {
		t.Errorf("cached failure = %v, %v; want nil, nil", resolved, err)
	}
}

func TestNoCookiesYieldsInvalidToken(t *testing.T) {
	env := newTestEnv(t)
	authenticator, _ := env.authenticator(nil)

	_, err := authenticator.GetMainAccountFromToken(true)
	if nil == err {
		t.Fatal("a request without cookies must fail")
	}
	if code := clientCode(t, err); appctx.InvalidToken != code {
		t.Errorf("code = %d, want InvalidToken 101", code)
	}

	var clientError *appctx.ClientError
	if errors.As(err, &clientError) && "Account undefined" != clientError.AdditionalMessage {
		t.Errorf("additional message = %q, want \"Account undefined\"", clientError.AdditionalMessage)
	}

	// Without throwing the caller gets nil and no error.
	quiet, _ := env.authenticator(nil)
	if resolved, err := quiet.GetMainAccountFromToken(false); nil != err || nil != resolved {
		t.Errorf("GetMainAccountFromToken(false) = %v, %v", resolved, err)
	}
	if resolved, err := quiet.GetAccountFromToken(false); nil != err || nil != resolved {
		t.Errorf("GetAccountFromToken(false) = %v, %v", resolved, err)
	}
}

func TestSignMeCallbackIsUsedWithoutAnAccountCookie(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	authenticator, recorder := env.authenticator(requestWith(map[string]string{AuthSignMeTokenKey: "anything"}))
	authenticator.SignMe = func() (*account.MainAccount, error) { return main, nil }

	resolved, err := authenticator.GetMainAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetMainAccountFromToken: %v", err)
	}
	if nil == resolved || testEmail != resolved.Email() {
		t.Fatalf("resolved = %v", resolved)
	}

	// It writes the SESSION entry and the account cookie, like PHP does.
	session := authenticator.SessionToken(false)
	if value, ok := env.storage.Get(main, storage.TypeSession, session); !ok || "true" != value {
		t.Errorf("the session entry = %q, %v", value, ok)
	}
	if !cookieLine(recorder, AuthSpecTokenKey+"=") {
		t.Error("the smaccount cookie was not written")
	}
}

func TestAdditionalAccountSelection(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()
	additional := env.additionalAccount()

	request, loginRecorder, _ := env.login(main)

	// Add the additional account to the session, like DoAccountSwitch does.
	switcher, switchRecorder := env.authenticator(request)
	switcher.SetMainAuthAccount(main)

	token, err := additional.AsTokenArray(switcher.Context, main)
	if nil != err {
		t.Fatalf("AsTokenArray: %v", err)
	}
	restored, err := account.NewAdditionalAccountFromToken(switcher.Context, token, true)
	if nil != err || nil == restored {
		t.Fatalf("NewAdditionalAccountFromToken = %v, %v", restored, err)
	}
	if err := switcher.SetAdditionalAuthToken(restored); nil != err {
		t.Fatalf("SetAdditionalAuthToken: %v", err)
	}

	// The next request carries both the login cookies and the selection.
	cookies := map[string]string{
		AuthSpecTokenKey:       recorderCookieValue(loginRecorder, AuthSpecTokenKey),
		SessionTokenName:       recorderCookieValue(loginRecorder, SessionTokenName),
		AuthAdditionalTokenKey: recorderCookieValue(switchRecorder, AuthAdditionalTokenKey),
	}

	authenticator, _ := env.authenticator(requestWith(cookies))

	current, err := authenticator.GetAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetAccountFromToken: %v", err)
	}
	if nil == current || "add@second.example" != current.Email() {
		t.Fatalf("GetAccountFromToken = %v, want the additional account", current)
	}

	// The main account stays available.
	if nil == authenticator.MainAccount() || testEmail != authenticator.MainAccount().Email() {
		t.Errorf("MainAccount = %v", authenticator.MainAccount())
	}

	// The connection token of an additional account uses the "2-" prefix.
	want := "2-" + storage.SHA1(testSalt+authenticator.AdditionalAccount().Hash(testSalt))
	if got := authenticator.ConnectionToken(); want != got {
		t.Errorf("ConnectionToken = %q, want %q", got, want)
	}

	// An additional cookie that cannot be resolved is dropped, and the main
	// account is used again.
	broken := requestWith(map[string]string{
		AuthSpecTokenKey:       cookies[AuthSpecTokenKey],
		SessionTokenName:       cookies[SessionTokenName],
		AuthAdditionalTokenKey: "not-a-cookie",
	})
	fallback, fallbackRecorder := env.authenticator(broken)

	current, err = fallback.GetAccountFromToken(true)
	if nil != err {
		t.Fatalf("GetAccountFromToken: %v", err)
	}
	if nil == current || testEmail != current.Email() {
		t.Errorf("GetAccountFromToken = %v, want the main account", current)
	}
	if !cookieLine(fallbackRecorder, AuthAdditionalTokenKey+"=") {
		t.Error("the unusable smadditional cookie was not cleared")
	}
}

func TestLogout(t *testing.T) {
	env := newTestEnv(t)

	authenticator, recorder := env.authenticator(nil)
	authenticator.Logout(true)

	for _, name := range []string{AuthAdditionalTokenKey, AuthSpecTokenKey} {
		if !cookieLine(recorder, name+"=") {
			t.Errorf("%s was not cleared", name)
		}
	}
	// The session cookie is deliberately kept, like PHP's commented out clear.
	if cookieLine(recorder, SessionTokenName+"=") {
		t.Error("Logout must not clear the smsession cookie")
	}

	// Logout(false) keeps the main account cookie.
	partial, partialRecorder := env.authenticator(nil)
	partial.Logout(false)

	if !cookieLine(partialRecorder, AuthAdditionalTokenKey+"=") {
		t.Error("the additional cookie was not cleared")
	}
	if cookieLine(partialRecorder, AuthSpecTokenKey+"=") {
		t.Error("Logout(false) must not clear the main account cookie")
	}
}

func TestSwitchAccount(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()
	additional := env.additionalAccount()

	request, _, _ := env.login(main)

	token, err := additional.AsTokenArray(&account.Context{
		Salt:    testSalt,
		Storage: env.storage,
		Crypt:   env.crypt,
	}, main)
	if nil != err {
		t.Fatalf("AsTokenArray: %v", err)
	}

	verified := 0
	options := SwitchOptions{
		AdditionalAccountsEnabled: true,
		Accounts: func(*account.MainAccount) (map[string]any, error) {
			return map[string]any{"add@second.example": token}, nil
		},
		Verify: func(resolved *account.AdditionalAccount) error {
			verified++
			if "add@second.example" != resolved.Email() {
				t.Errorf("verified %q", resolved.Email())
			}
			return nil
		},
	}

	authenticator, recorder := env.authenticator(request)

	switched, err := authenticator.SwitchAccount("add@second.example", options)
	if nil != err || !switched {
		t.Fatalf("SwitchAccount = %v, %v", switched, err)
	}
	if 1 != verified {
		t.Errorf("Verify ran %d times, want 1", verified)
	}
	if !cookieLine(recorder, AuthAdditionalTokenKey+"=") {
		t.Error("the smadditional cookie was not written")
	}

	// Switching to the main account clears the selection.
	cleared, clearedRecorder := env.authenticator(request)
	if switched, err := cleared.SwitchAccount(testEmail, options); nil != err || !switched {
		t.Fatalf("SwitchAccount(main) = %v, %v", switched, err)
	}
	if !cookieLine(clearedRecorder, AuthAdditionalTokenKey+"=") {
		t.Error("switching back must clear the additional cookie")
	}
}

func TestSwitchAccountFailures(t *testing.T) {
	env := newTestEnv(t)
	main := env.mainAccount()

	request, _, _ := env.login(main)

	options := SwitchOptions{
		AdditionalAccountsEnabled: true,
		Accounts: func(*account.MainAccount) (map[string]any, error) {
			return map[string]any{}, nil
		},
	}

	// An unknown address is AccountDoesNotExist (802).
	authenticator, _ := env.authenticator(request)
	if _, err := authenticator.SwitchAccount("add@second.example", options); nil == err {
		t.Fatal("an unknown account must fail")
	} else if code := clientCode(t, err); appctx.AccountDoesNotExist != code {
		t.Errorf("code = %d, want AccountDoesNotExist 802", code)
	}

	// A token array that cannot be read is AccountSwitchFailed (803).
	broken := options
	broken.Accounts = func(*account.MainAccount) (map[string]any, error) {
		return map[string]any{"add@second.example": map[string]any{"email": "add@second.example"}}, nil
	}
	authenticator, _ = env.authenticator(request)
	if _, err := authenticator.SwitchAccount("add@second.example", broken); nil == err {
		t.Fatal("a broken token array must fail")
	} else if code := clientCode(t, err); appctx.AccountSwitchFailed != code {
		t.Errorf("code = %d, want AccountSwitchFailed 803", code)
	}

	// A failing login test is reported as is (step 8 throws ConnectionError or
	// AuthError, not AccountSwitchFailed).
	additional := env.additionalAccount()
	token, err := additional.AsTokenArray(&account.Context{
		Salt:    testSalt,
		Storage: env.storage,
		Crypt:   env.crypt,
	}, main)
	if nil != err {
		t.Fatalf("AsTokenArray: %v", err)
	}

	failing := SwitchOptions{
		AdditionalAccountsEnabled: true,
		Accounts: func(*account.MainAccount) (map[string]any, error) {
			return map[string]any{"add@second.example": token}, nil
		},
		Verify: func(*account.AdditionalAccount) error {
			return appctx.NewClientError(appctx.AuthError, nil, "")
		},
	}
	authenticator, _ = env.authenticator(request)
	if _, err := authenticator.SwitchAccount("add@second.example", failing); nil == err {
		t.Fatal("a failing login test must fail")
	} else if code := clientCode(t, err); appctx.AuthError != code {
		t.Errorf("code = %d, want AuthError 102", code)
	}

	// Without the capability nothing happens, and an empty address is a no-op.
	disabled := SwitchOptions{Accounts: options.Accounts}
	authenticator, _ = env.authenticator(request)
	if switched, err := authenticator.SwitchAccount("add@second.example", disabled); nil != err || switched {
		t.Errorf("SwitchAccount without the capability = %v, %v", switched, err)
	}
	authenticator, _ = env.authenticator(request)
	if switched, err := authenticator.SwitchAccount("", options); nil != err || switched {
		t.Errorf("SwitchAccount(\"\") = %v, %v", switched, err)
	}

	// NoCache runs first.
	cached := 0
	noCache := options
	noCache.NoCache = func() { cached++ }
	authenticator, _ = env.authenticator(request)
	if _, err := authenticator.SwitchAccount(testEmail, noCache); nil != err {
		t.Fatalf("SwitchAccount: %v", err)
	}
	if 1 != cached {
		t.Errorf("NoCache ran %d times, want 1", cached)
	}
}

// cookieValue returns the value of a cookie of the request, or "".
func cookieValue(request *http.Request, name string) string {
	if nil == request {
		return ""
	}
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// recorderCookieValue returns the value of a cookie the response set, or "".
func recorderCookieValue(recorder *httptest.ResponseRecorder, name string) string {
	if nil == recorder {
		return ""
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
