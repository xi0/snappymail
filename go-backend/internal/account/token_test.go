package account

import (
	"errors"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

func TestConvertArray(t *testing.T) {
	// The current associative layout passes through unchanged.
	assoc := map[string]any{"email": "a@example.com", "login": "a", "pass": "p"}
	if got := ConvertArray(assoc); "a@example.com" != phpString(got["email"]) {
		t.Errorf("ConvertArray(assoc) = %v", got)
	}

	// An associative array without "email" is not converted either (isset()).
	if nil != ConvertArray(map[string]any{"login": "a"}) {
		t.Error("an array without email must not convert")
	}

	// The legacy indexed array needs the "account" marker and 7 elements.
	legacy := []any{"account", "a@example.com", "a", "p", 1, 2, 3}
	got := ConvertArray(legacy)
	if "a@example.com" != phpString(got["email"]) ||
		"a" != phpString(got["login"]) || "p" != phpString(got["pass"]) {
		t.Errorf("ConvertArray(legacy) = %v", got)
	}

	if nil != ConvertArray([]any{"other", "a@example.com", "a", "p", 1, 2, 3}) {
		t.Error("the legacy marker must be the string \"account\"")
	}
	if nil != ConvertArray([]any{"account", "a@example.com", "a", "p", 1, 2}) {
		t.Error("a legacy array needs at least 7 elements")
	}

	// A missing element becomes "" instead of "null".
	got = ConvertArray([]any{"account", "a@example.com", "", "p", 1, 2, 3})
	if "" != phpString(got["login"]) {
		t.Errorf("login = %v, want empty", got["login"])
	}
}

func TestConvertAdditionalArrayTakesTheLastElementAsHmac(t *testing.T) {
	legacy := []any{"account", "b@example.com", "b", "p", 1, 2, 3, "the-hmac"}
	got := ConvertAdditionalArray(legacy)
	if "the-hmac" != phpString(got["hmac"]) {
		t.Errorf("hmac = %v, want the-hmac", got["hmac"])
	}

	// Exactly 7 elements have no hmac.
	got = ConvertAdditionalArray([]any{"account", "b@example.com", "b", "p", 1, 2, 3})
	if _, ok := got["hmac"]; ok {
		t.Error("7 elements carry no hmac")
	}

	// More than 9 elements are not a valid legacy additional account array, but
	// the parent conversion still applies (PHP only skips the hmac then).
	got = ConvertAdditionalArray([]any{"account", "b@example.com", "b", "p", 1, 2, 3, 4, 5, 6})
	if nil == got {
		t.Fatal("the parent conversion must still apply")
	}
	if _, ok := got["hmac"]; ok {
		t.Error("more than 9 elements carry no hmac")
	}

	// The associative layout keeps an explicit hmac.
	if got := ConvertAdditionalArray(map[string]any{"email": "b@example.com", "hmac": "x"}); "x" != phpString(got["hmac"]) {
		t.Errorf("hmac = %v", got["hmac"])
	}
}

func TestNewMainAccountFromToken(t *testing.T) {
	context, hooks, _ := newTestContext(t, testDomain("example.com"))

	token := map[string]any{
		"email": "user@example.com",
		"login": "login",
		"pass":  "secret",
		"name":  "User",
		"smtp":  map[string]any{"user": "smtp-user", "pass": "smtp-secret"},
	}

	main, err := NewMainAccountFromToken(context, token, true)
	if nil != err {
		t.Fatalf("NewMainAccountFromToken: %v", err)
	}
	if nil == main {
		t.Fatal("NewMainAccountFromToken returned no account")
	}

	if "user@example.com" != main.Email() || "login" != main.ImapUser() || "secret" != main.ImapPass() {
		t.Errorf("credentials = %q/%q/%q", main.Email(), main.ImapUser(), main.ImapPass())
	}
	if "User" != main.Name() {
		t.Errorf("name = %q", main.Name())
	}
	if "smtp-user" != main.SmtpUser() || "smtp-secret" != main.SmtpPass() || !main.HasSmtpPass() {
		t.Errorf("smtp = %q/%q", main.SmtpUser(), main.SmtpPass())
	}
	if nil == main.Domain() || "imap.example.com" != main.Domain().IMAP.Host {
		t.Errorf("domain = %v", main.Domain())
	}
	if !hooks.called("filter.account") {
		t.Error("the filter.account hook did not run")
	}
	if got, ok := hooks.firstArg("filter.account").(*Account); !ok || got != &main.Account {
		t.Errorf("filter.account received %T, want the account", hooks.firstArg("filter.account"))
	}
}

func TestNewMainAccountFromTokenFailures(t *testing.T) {
	context, _, logs := newTestContext(t, testDomain("example.com"))

	// A token array without credentials is a RuntimeException in PHP: logged,
	// and only reported when the caller asked for it.
	token := map[string]any{"email": "user@example.com"}

	main, err := NewMainAccountFromToken(context, token, false)
	if nil != err || nil != main {
		t.Errorf("NewMainAccountFromToken(throw=false) = %v, %v", main, err)
	}
	if !logs.has("Invalid TokenArray") {
		t.Errorf("the failure was not logged: %v", logs.lines)
	}

	if _, err := NewMainAccountFromToken(context, token, true); nil == err {
		t.Error("NewMainAccountFromToken(throw=true) must report the failure")
	} else if "Invalid TokenArray" != err.Error() {
		t.Errorf("error = %q", err.Error())
	}

	// An unknown domain is a DomainNotAllowed client error.
	unknown := map[string]any{"email": "user@other.example", "login": "user", "pass": "p"}
	if _, err := NewMainAccountFromToken(context, unknown, true); nil == err {
		t.Error("an unknown domain must fail")
	} else if code := clientCode(t, err); appctx.DomainNotAllowed != code {
		t.Errorf("code = %d, want DomainNotAllowed", code)
	}
}

func TestNewMainAccountFromTokenWithoutDomainConfig(t *testing.T) {
	context, _, _ := newTestContext(t)

	// The provider has no config for the address: PHP's "if ($oDomain)" block is
	// skipped and the account stays null.
	context.Domains = &emptyDomains{}

	main, err := NewMainAccountFromToken(context, map[string]any{
		"email": "user@example.com", "login": "user", "pass": "p",
	}, true)
	if nil != err || nil != main {
		t.Errorf("NewMainAccountFromToken = %v, %v; want nil, nil", main, err)
	}
}

func TestFilterAccountHookCanRejectTheAccount(t *testing.T) {
	context, hooks, _ := newTestContext(t, testDomain("example.com"))
	hooks.on = func(name string, args []any) {
		if "filter.account" != name || 0 == len(args) {
			return
		}
		if target, ok := args[0].(*Account); ok {
			// Resetting the credentials is how a plugin refuses an account.
			target.SetCredentials(nil, "", "", "", "", "")
		}
	}

	_, err := NewMainAccountFromToken(context, map[string]any{
		"email": "user@example.com", "login": "user", "pass": "p",
	}, true)
	if nil == err {
		t.Fatal("a rejected account must fail")
	}
	if code := clientCode(t, err); appctx.AccountFilterError != code {
		t.Errorf("code = %d, want AccountFilterError", code)
	}
}

func TestAdditionalAccountTokenRoundTrip(t *testing.T) {
	context, _, _ := newTestContext(t, testDomain("example.com"), testDomain("second.example"))
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	additional := NewAdditionalAccount()
	additional.SetCredentials(testDomain("second.example"), "add@second.example", "add@second.example", "add-secret", "smtp-add", "smtp-secret")

	token, err := additional.AsTokenArray(context, main)
	if nil != err {
		t.Fatalf("AsTokenArray: %v", err)
	}

	// The password is sealed, not stored in the clear, and signed.
	if "add-secret" == phpString(token["pass"]) {
		t.Error("the password must be encrypted")
	}
	if "" == phpString(token["hmac"]) {
		t.Error("the token array must be signed")
	}
	smtp, ok := token["smtp"].(map[string]any)
	if !ok || "smtp-secret" == phpString(smtp["pass"]) {
		t.Errorf("the SMTP password must be encrypted: %v", token["smtp"])
	}

	// Reading it back needs the same main account (same crypt key).
	context.MainAccount = func() (*MainAccount, error) { return main, nil }

	restored, err := NewAdditionalAccountFromToken(context, token, true)
	if nil != err {
		t.Fatalf("NewAdditionalAccountFromToken: %v", err)
	}
	if nil == restored {
		t.Fatal("NewAdditionalAccountFromToken returned no account")
	}
	if "add@second.example" != restored.Email() || "add-secret" != restored.ImapPass() {
		t.Errorf("restored = %q/%q", restored.Email(), restored.ImapPass())
	}
	if "user@example.com" != restored.ParentEmail() {
		t.Errorf("ParentEmail = %q", restored.ParentEmail())
	}

	// The hash of an additional account includes the parent address.
	want := storage.SHA1(additional.Account.Hash(testSalt) + "user@example.com")
	if got := restored.Hash(testSalt); want != got {
		t.Errorf("Hash = %q, want %q", got, want)
	}

	// PHP keeps the SMTP password encrypted on the read path ($aData bug), so
	// the restored account has no usable SMTP password.
	if "smtp-secret" == restored.SmtpPass() {
		t.Error("the SMTP password must stay sealed, like the PHP reader does")
	}
}

func TestAdditionalAccountTokenNeedsAValidHmac(t *testing.T) {
	context, _, _ := newTestContext(t, testDomain("example.com"), testDomain("second.example"))
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")
	context.MainAccount = func() (*MainAccount, error) { return main, nil }

	additional := NewAdditionalAccount()
	additional.SetCredentials(testDomain("second.example"), "add@second.example", "add@second.example", "add-secret", "", "")

	token, err := additional.AsTokenArray(context, main)
	if nil != err {
		t.Fatalf("AsTokenArray: %v", err)
	}

	// A forged signature clears the password, which makes the token unusable
	// (the parent reader requires a non-empty password).
	token["hmac"] = "0000000000000000000000000000000000000000"
	restored, err := NewAdditionalAccountFromToken(context, token, false)
	if nil != err || nil != restored {
		t.Errorf("a forged hmac must not yield an account: %v, %v", restored, err)
	}
}

func TestAdditionalAccountFromLegacyArray(t *testing.T) {
	context, _, _ := newTestContext(t, testDomain("example.com"), testDomain("second.example"))
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")
	context.MainAccount = func() (*MainAccount, error) { return main, nil }

	// Legacy indexed arrays without an hmac carry the password in the clear.
	legacy := []any{"account", "add@second.example", "add@second.example", "add-secret", 1, 2, 3}

	restored, err := NewAdditionalAccountFromToken(context, legacy, true)
	if nil != err {
		t.Fatalf("NewAdditionalAccountFromToken: %v", err)
	}
	if nil == restored || "add-secret" != restored.ImapPass() {
		t.Fatalf("restored = %v", restored)
	}

	// A legacy array with an hmac (8th element) seals the password the way
	// asTokenArray() does.
	cryptKey := mustCryptKey(t, main, context)
	sealed, err := context.Crypt.EncryptURLSafe("add-secret", cryptKey)
	if nil != err {
		t.Fatalf("EncryptURLSafe: %v", err)
	}

	legacy = []any{"account", "add@second.example", "add@second.example", sealed, 1, 2, 3, HMAC(sealed, cryptKey)}

	restored, err = NewAdditionalAccountFromToken(context, legacy, true)
	if nil != err {
		t.Fatalf("NewAdditionalAccountFromToken: %v", err)
	}
	if nil == restored || "add-secret" != restored.ImapPass() {
		t.Fatalf("restored with hmac = %v", restored)
	}
}

func mustCryptKey(t *testing.T, main *MainAccount, context *Context) string {
	t.Helper()
	key, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}
	return key
}

// emptyDomains resolves nothing, without an error.
type emptyDomains struct{}

func (emptyDomains) Load(string, ...domain.LoadOption) *domain.Domain { return nil }
func (emptyDomains) GetByEmailAddress(string) (*domain.Domain, error)  { return nil, nil }

// clientCode returns the notification code of a client error.
func clientCode(t *testing.T, err error) int {
	t.Helper()

	var clientError *appctx.ClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("error is not a ClientError: %v", err)
	}
	return clientError.Code
}
