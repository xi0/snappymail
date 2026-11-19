package auth

// Tests of the credential path of step 8: resolveLoginCredentials(),
// LoginProcess(), imapConnect(), DoLogin()/DoLogout() and loginErrorDelay().

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestResolveLoginCredentials(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// The address is trimmed, the domain punycoded and lowercased by
	// fixUsername(), and IMAP/SMTP share the same login.
	credentials, err := login.ResolveLoginCredentials(" \tUser@Example.COM ", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@example.com" != credentials.Email {
		t.Errorf("email = %q", credentials.Email)
	}
	if "user@example.com" != credentials.ImapUser || "user@example.com" != credentials.SmtpUser {
		t.Errorf("imap/smtp user = %q/%q", credentials.ImapUser, credentials.SmtpUser)
	}
	if "secret" != credentials.Password {
		t.Errorf("password = %q", credentials.Password)
	}
	if nil == credentials.Domain || "imap.example.com" != credentials.Domain.IMAP.Host {
		t.Errorf("domain = %v", credentials.Domain)
	}

	// A punycode domain is converted, an unknown domain keeps the address as is.
	punycode, err := login.ResolveLoginCredentials("user@bücher.example", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@xn--bcher-kva.example" != punycode.Email {
		t.Errorf("punycode email = %q", punycode.Email)
	}
	if nil != punycode.Domain {
		t.Errorf("domain = %v, want nil", punycode.Domain)
	}
}

func TestResolveLoginCredentialsDetermineUserDomain(t *testing.T) {
	env := newTestEnv(t)
	env.config.bools["login.determine_user_domain"] = true

	login, _ := env.loginFor(nil)
	login.Host = "webmail.mail.example.com"

	// The host labels are walked down until a domain config matches without a
	// wildcard.
	credentials, err := login.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@example.com" != credentials.Email {
		t.Errorf("email = %q, want user@example.com", credentials.Email)
	}

	for _, line := range []string{
		"LOGIN: The email address 'user' is incomplete",
		"LOGIN: Determined user domain: webmail.mail.example.com",
		"LOGIN: Check 'webmail.mail.example.com': NO",
		"LOGIN: Check 'mail.example.com': NO",
		"LOGIN: Check 'example.com': OK",
	} {
		if !env.logger.has(line) {
			t.Errorf("missing log line %q in %v", line, env.logger.lines)
		}
	}
}

func TestResolveLoginCredentialsDetermineUserDomainWithWildcard(t *testing.T) {
	env := newTestEnv(t)
	env.config.bools["login.determine_user_domain"] = true

	// Only a wildcard config for the request host, so the label walk fails and
	// the wildcard lookup completes the address.
	wildcard := testDomain("webmail.example.com")
	env.domains = &fakeDomains{
		configs:  map[string]*domain.Domain{"other.example": testDomain("other.example")},
		wildcard: wildcard,
		wildName: "webmail.example.com",
	}

	login, _ := env.loginFor(nil)
	login.Host = "webmail.example.com"
	login.Context.Domains = env.domains

	credentials, err := login.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@webmail.example.com" != credentials.Email {
		t.Errorf("email = %q", credentials.Email)
	}
	if nil == credentials.Domain || "imap.webmail.example.com" != credentials.Domain.IMAP.Host {
		t.Errorf("domain = %v", credentials.Domain)
	}
	if !env.logger.has("LOGIN: Check 'webmail.example.com' with wildcard: OK") {
		t.Errorf("the wildcard fallback was not logged: %v", env.logger.lines)
	}
}

func TestResolveLoginCredentialsDefaultDomain(t *testing.T) {
	env := newTestEnv(t)
	env.config.strings["login.default_domain"] = "example.com"

	login, _ := env.loginFor(nil)
	credentials, err := login.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@example.com" != credentials.Email {
		t.Errorf("email = %q", credentials.Email)
	}
	if !env.logger.has("LOGIN: Default domain 'example.com' will be used.") {
		t.Errorf("the default domain was not logged: %v", env.logger.lines)
	}

	// The HTTP_HOST/SERVER_NAME aliases lose their port.
	env.config.strings["login.default_domain"] = "HTTP_HOST"
	hostLogin, _ := env.loginFor(nil)
	fromHost, err := hostLogin.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@webmail.example.com" != fromHost.Email {
		t.Errorf("HTTP_HOST email = %q", fromHost.Email)
	}

	// "gethostname" uses the system name; empty values fall back to the raw
	// configuration value.
	env.config.strings["login.default_domain"] = "gethostname"
	nameLogin, _ := env.loginFor(nil)
	nameLogin.HostName = func() (string, error) { return "localhost", nil }
	fromName, err := nameLogin.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user@localhost" != fromName.Email {
		t.Errorf("gethostname email = %q", fromName.Email)
	}

	// Without a default domain the address stays incomplete.
	env.config.strings["login.default_domain"] = ""
	bare, _ := env.loginFor(nil)
	incomplete, err := bare.ResolveLoginCredentials("user", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "user" != incomplete.Email {
		t.Errorf("email = %q, want the incomplete address", incomplete.Email)
	}
	if !env.logger.has("LOGIN: Default domain not configured.") {
		t.Errorf("the missing default domain was not logged: %v", env.logger.lines)
	}
}

func TestResolveLoginCredentialsHooks(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// A plugin may rewrite the address, the password and the login names, like
	// the by-reference arguments of the PHP hooks.
	env.plugins().rewriter = func(name string, args ...any) {
		switch name {
		case "login.credentials.step-1":
			*args[0].(*string) = "rewritten@example.com"
		case "login.credentials.step-2":
			*args[1].(*string) = "rewritten-password"
		case "login.credentials":
			*args[1].(*string) = "imap-login"
			*args[3].(*string) = "smtp-login"
		}
	}

	credentials, err := login.ResolveLoginCredentials("user@example.com", "secret")
	if nil != err {
		t.Fatalf("ResolveLoginCredentials: %v", err)
	}
	if "rewritten@example.com" != credentials.Email {
		t.Errorf("email = %q", credentials.Email)
	}
	if "rewritten-password" != credentials.Password {
		t.Errorf("password = %q", credentials.Password)
	}
	if "imap-login" != credentials.ImapUser || "smtp-login" != credentials.SmtpUser {
		t.Errorf("imap/smtp user = %q/%q", credentials.ImapUser, credentials.SmtpUser)
	}

	for _, hook := range []string{
		"login.credentials.step-1", "login.credentials.step-2", "login.credentials",
	} {
		if !env.plugins().has(hook) {
			t.Errorf("hook %q did not run: %v", hook, env.plugins().runs)
		}
	}
}

// hookFunc adapts a function to account.Hooks.
type hookFunc func(name string, args ...any)

func (f hookFunc) RunHook(name string, args ...any) { f(name, args...) }

func TestLoginProcessInvalidInput(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// An address without "@" and an empty password are InvalidInputArgument.
	env.config.strings["login.default_domain"] = ""
	if _, err := login.LoginProcess("user", "secret"); nil == err {
		t.Fatal("an address without @ must fail")
	} else if code := clientCode(t, err); appctx.InvalidInputArgument != code {
		t.Errorf("code = %d, want InvalidInputArgument 903", code)
	}

	if _, err := login.LoginProcess("user@example.com", ""); nil == err {
		t.Fatal("an empty password must fail")
	} else if code := clientCode(t, err); appctx.InvalidInputArgument != code {
		t.Errorf("code = %d, want InvalidInputArgument 903", code)
	}

	// A domain without configuration is DomainNotAllowed (109).
	if _, err := login.LoginProcess("user@unknown.tld", "secret"); nil == err {
		t.Fatal("an unknown domain must fail")
	} else if code := clientCode(t, err); appctx.DomainNotAllowed != code {
		t.Errorf("code = %d, want DomainNotAllowed 109", code)
	}
}

func TestLoginProcessStartsTheSession(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)

	validated := 0
	login.IMAPLogin = func(a *account.Account) error {
		validated++
		if "user@example.com" != a.ImapUser() || "secret" != a.ImapPass() {
			t.Errorf("validated %q/%q", a.ImapUser(), a.ImapPass())
		}
		return nil
	}

	main, err := login.LoginProcess("user@example.com", "secret")
	if nil != err {
		t.Fatalf("LoginProcess: %v", err)
	}
	if nil == main || testEmail != main.Email() {
		t.Fatalf("main = %v", main)
	}
	if 1 != validated {
		t.Errorf("IMAPLogin ran %d times, want 1", validated)
	}

	// The SESSION entry and the account cookies are written, and the login
	// hooks fired.
	session := login.Session.SessionToken(false)
	if value, ok := env.storage.Get(main, storage.TypeSession, session); !ok || "true" != value {
		t.Errorf("SESSION entry = %q, %v", value, ok)
	}
	if !cookieLine(recorder, AuthSpecTokenKey+"=") {
		t.Error("the smaccount cookie was not written")
	}
	if !cookieLine(recorder, AuthAdditionalTokenKey+"=") {
		t.Error("the additional account cookie was not cleared")
	}
	for _, hook := range []string{"filter.account", "login.success"} {
		if !env.plugins().has(hook) {
			t.Errorf("hook %q did not run: %v", hook, env.plugins().runs)
		}
	}

	// The next request authenticates with the cookies of the login response.
	next, _ := env.authenticator(requestFromCookies(recorder))
	resolved, err := next.GetMainAccountFromToken(true)
	if nil != err || nil == resolved || testEmail != resolved.Email() {
		t.Fatalf("the session did not authenticate: %v, %v", resolved, err)
	}
}

func TestLoginProcessAccountFilterError(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// A plugin rejects the account by resetting it, like the ldap/oauth2
	// plugins do with PHP's "$if (!$oAccount)".
	login.Plugins = hookFunc(func(name string, args ...any) {
		if "filter.account" != name {
			return
		}
		if target, ok := args[0].(*account.Account); ok {
			target.SetCredentials(nil, "", "", "", "", "")
		}
	})

	_, err := login.LoginProcess("user@example.com", "secret")
	if nil == err {
		t.Fatal("a rejected account must fail")
	}
	if code := clientCode(t, err); appctx.AccountFilterError != code {
		t.Errorf("code = %d, want AccountFilterError 804", code)
	}
}

func TestImapConnectErrorMapping(t *testing.T) {
	env := newTestEnv(t)

	cases := []struct {
		name       string
		injected   error
		code       int
		additional string
	}{
		{
			name:     "connection",
			injected: &MailError{Kind: MailErrorConnection, Err: errors.New("dial tcp: refused")},
			code:     appctx.ConnectionError,
		},
		{
			name:       "bad credentials",
			injected:   &MailError{Kind: MailErrorBadCredentials, Alert: "Invalid credentials", Err: errors.New("NO")},
			code:       appctx.AuthError,
			additional: "Invalid credentials",
		},
		{
			name:     "other",
			injected: errors.New("unexpected"),
			code:     appctx.AuthError,
		},
		{
			name:     "client error passthrough",
			injected: appctx.NewClientError(appctx.CryptKeyError, nil, ""),
			code:     appctx.CryptKeyError,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			login, _ := env.loginFor(nil)
			login.IMAPLogin = func(*account.Account) error { return test.injected }

			_, err := login.LoginProcess("user@example.com", "secret")
			if nil == err {
				t.Fatal("the login must fail")
			}
			if code := clientCode(t, err); test.code != code {
				t.Errorf("code = %d, want %d", code, test.code)
			}
			if "" != test.additional {
				var clientError *appctx.ClientError
				if errors.As(err, &clientError) && test.additional != clientError.AdditionalMessage {
					t.Errorf("additional message = %q, want %q",
						clientError.AdditionalMessage, test.additional)
				}
			}
		})
	}

	// imap.show_login_alert = false drops the alert text (PHP still reports the
	// message of the wrapped exception as the additional message).
	env.config.bools["imap.show_login_alert"] = false
	login, _ := env.loginFor(nil)
	login.IMAPLogin = func(*account.Account) error {
		return &MailError{Kind: MailErrorBadCredentials, Alert: "Invalid credentials"}
	}
	_, err := login.LoginProcess("user@example.com", "secret")
	var clientError *appctx.ClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("error is not a ClientError: %v", err)
	}
	if "Invalid credentials" == clientError.AdditionalMessage {
		t.Errorf("the alert was not suppressed: %q", clientError.AdditionalMessage)
	}
	if appctx.AuthError != clientError.Code {
		t.Errorf("code = %d, want AuthError 102", clientError.Code)
	}
}

func TestImapConnectWithoutCallback(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// The pre-IMAP steps have no client yet, so the check is skipped.
	if _, err := login.LoginProcess("user@example.com", "secret"); nil != err {
		t.Fatalf("LoginProcess: %v", err)
	}
}

func TestLoginErrorDelay(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// Nothing configured: no pause at all.
	login.LoginErrorDelay()
	if 0 != len(env.slept) {
		t.Errorf("slept %v without a configured delay", env.slept)
	}

	env.config.ints["login.fault_delay"] = 2

	// PHP measures the delay from the request start, not from the failure.
	env.slept = nil
	login.Start = env.now.Add(-1500 * time.Millisecond)
	login.LoginErrorDelay()
	if 1 != len(env.slept) {
		t.Fatalf("slept %v, want one pause", env.slept)
	}
	if 500*time.Millisecond != env.slept[0] {
		t.Errorf("slept %v, want 500ms", env.slept[0])
	}

	// A request that already took longer than the delay does not sleep again.
	env.slept = nil
	login.Start = env.now.Add(-3 * time.Second)
	login.LoginErrorDelay()
	if 0 != len(env.slept) {
		t.Errorf("slept %v after a slow request", env.slept)
	}
}

func TestDoLogin(t *testing.T) {
	env := newTestEnv(t)
	env.config.ints["login.fault_delay"] = 1

	login, recorder := env.loginFor(nil)
	login.ValidateLanguage = func(language string) string {
		if "da" == language {
			return "da"
		}
		return ""
	}

	// The language of the login form is stored in the account settings.
	main, err := login.DoLogin(LoginParams{
		Email:    "user@example.com",
		Password: "secret",
		SignMe:   true,
		Language: "da",
	})
	if nil != err {
		t.Fatalf("DoLogin: %v", err)
	}

	settings := env.settings.documents[main.Email()]
	if nil == settings || "da" != settings.GetConf("language", "") {
		t.Errorf("settings language = %v", settings)
	}
	if 1 != env.settings.saves {
		t.Errorf("settings saves = %d, want 1", env.settings.saves)
	}

	// signMe wrote the smremember cookie.
	if !cookieLine(recorder, AuthSignMeTokenKey+"=") {
		t.Error("the smremember cookie was not written")
	}

	// The same language again does not save.
	if _, err := login.DoLogin(LoginParams{
		Email: "user@example.com", Password: "secret", Language: "da",
	}); nil != err {
		t.Fatalf("DoLogin: %v", err)
	}
	if 1 != env.settings.saves {
		t.Errorf("settings saves = %d, want 1 (unchanged language)", env.settings.saves)
	}

	// An invalid language is ignored, an empty one is normal.
	if _, err := login.DoLogin(LoginParams{
		Email: "user@example.com", Password: "secret", Language: "xx",
	}); nil != err {
		t.Fatalf("DoLogin: %v", err)
	}
	if "da" != settings.GetConf("language", "") {
		t.Errorf("the invalid language was stored: %v", settings.GetConf("language", ""))
	}
}

func TestDoLoginFailureDelay(t *testing.T) {
	env := newTestEnv(t)
	env.config.ints["login.fault_delay"] = 2
	env.config.bools["login.determine_user_domain"] = true
	env.domains = &fakeDomains{configs: map[string]*domain.Domain{}}

	login, _ := env.loginFor(nil)
	login.Context.Domains = env.domains
	login.Start = env.now

	// A failed login sleeps for fault_delay before the error is reported.
	if _, err := login.DoLogin(LoginParams{Email: "user@example.com", Password: "bad"}); nil == err {
		t.Fatal("an unknown domain must fail")
	}
	if 1 != len(env.slept) || 2*time.Second != env.slept[0] {
		t.Errorf("slept %v, want one 2s pause", env.slept)
	}
}

func TestDoLogout(t *testing.T) {
	env := newTestEnv(t)

	login, recorder := env.loginFor(nil)
	login.IMAPLogin = nil

	main, err := login.LoginProcess("user@example.com", "secret")
	if nil != err {
		t.Fatalf("LoginProcess: %v", err)
	}
	login.SetSignMeToken(main)

	uuid := ""
	if token := login.GetSignMeToken(); nil != token {
		uuid = token.uuid
	}
	if "" == uuid {
		t.Fatal("the sign-me token was not written")
	}
	if _, ok := env.storage.Get(main.Email(), storage.TypeSignMe, uuid); !ok {
		t.Fatal("the .sign_me entry was not written")
	}

	// DoLogout clears the account cookies and the sign-me data.
	next, logoutRecorder := env.loginFor(requestFromCookies(recorder))
	if err := next.DoLogout(); nil != err {
		t.Fatalf("DoLogout: %v", err)
	}
	for _, name := range []string{AuthSpecTokenKey, AuthAdditionalTokenKey, AuthSignMeTokenKey} {
		if !cookieLine(logoutRecorder, name+"=") {
			t.Errorf("%s was not cleared", name)
		}
	}
	if _, ok := env.storage.Get(main.Email(), storage.TypeSignMe, uuid); ok {
		t.Error("the .sign_me entry was not removed")
	}
}

func TestAuthLog(t *testing.T) {
	env := newTestEnv(t)
	env.config.strings["logs.auth_logging_format"] =
		"[{date:Y-m-d H:i:s}] Auth failed: ip={request:ip} user={imap:login} host={imap:host} port={imap:port}"

	login, _ := env.loginFor(nil)
	login.ClientIP = "192.0.2.10"

	login.AuthLog(nil, "user@example.com")

	want := "[2023-11-14 22:13:20] Auth failed: ip=192.0.2.10 " +
		"user=user@example.com host=example.com port=imap"
	if !env.logger.has(want) {
		t.Errorf("auth log line missing.\nwant: %s\ngot:  %v", want, env.logger.lines)
	}

	// Without a configured format nothing is written.
	env.logger.lines = nil
	env.config.strings["logs.auth_logging_format"] = ""
	login.AuthLog(nil, "user@example.com")
	if 0 != len(env.logger.lines) {
		t.Errorf("an empty format wrote %v", env.logger.lines)
	}
}

func TestSignMeAuto(t *testing.T) {
	env := newTestEnv(t)
	login, _ := env.loginFor(nil)

	// DefaultOff is the application.ini default.
	if got := login.SignMeAuto(); SignMeDefaultOff != got {
		t.Errorf("SignMeAuto = %q", got)
	}

	env.config.strings["login.sign_me_auto"] = SignMeDefaultOn
	if got := login.SignMeAuto(); SignMeDefaultOn != got {
		t.Errorf("SignMeAuto = %q", got)
	}
}

func TestLoginAdditionalProcess(t *testing.T) {
	env := newTestEnv(t)
	login, recorder := env.loginFor(nil)

	additional, err := login.LoginAdditionalProcess("add@second.example", "add-secret")
	if nil != err {
		t.Fatalf("LoginAdditionalProcess: %v", err)
	}
	if nil == additional || "add@second.example" != additional.Email() {
		t.Fatalf("additional = %v", additional)
	}

	// No session is started for an additional account.
	if 0 != len(recorder.Header().Values("Set-Cookie")) {
		t.Errorf("an additional login wrote cookies: %v", recorder.Header().Values("Set-Cookie"))
	}
	if 0 != len(env.settings.documents) {
		t.Errorf("an additional login touched the settings: %v", env.settings.documents)
	}
}

func TestTrimValueIsMailSoTrim(t *testing.T) {
	// MailSo\Base\Utils::Trim() also drops control characters around the value.
	if got := trimValue("\x01\t user@example.com \x02"); "user@example.com" != got {
		t.Errorf("trimValue = %q", got)
	}
	if got := trimValue("   "); "" != got {
		t.Errorf("trimValue = %q", got)
	}
	if !strings.Contains(compileLogLine("a{date:Y}b", nil, time.Unix(0, 0).UTC()), "a1970b") {
		t.Errorf("compileLogLine did not expand the date")
	}
}
