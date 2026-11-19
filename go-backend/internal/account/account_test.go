package account

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// newTestAccountForTest builds the standard test account with a fresh context.
func newTestAccountForTest(t *testing.T) *Account {
	t.Helper()

	context, _, _ := newTestContext(t)
	return newTestAccount(context)
}

func TestHashMatchesPHPFormula(t *testing.T) {
	account := newTestAccountForTest(t)

	// PHP: sha1(implode(APP_SALT, [$sEmail, $sImapUser])).
	want := storage.SHA1("user@example.com" + testSalt + "user@example.com")
	if got := account.Hash(testSalt); want != got {
		t.Errorf("Hash = %q, want %q", got, want)
	}

	// The IMAP login is part of the hash, not the e-mail address.
	account.SetImapUser("other-login")
	other := storage.SHA1("user@example.com" + testSalt + "other-login")
	if got := account.Hash(testSalt); other != got {
		t.Errorf("Hash with another login = %q, want %q", got, other)
	}
}

func TestMarshalJSONMatchesPHPSerialization(t *testing.T) {
	account := newTestAccountForTest(t)

	got, err := json.Marshal(account)
	if nil != err {
		t.Fatalf("Marshal: %v", err)
	}

	// PHP: json_encode($oAccount) of an account without name and SMTP login.
	want := `{"email":"user@example.com","login":"user@example.com","pass":"secret","name":"","smtp":{}}`
	if want != string(got) {
		t.Errorf("MarshalJSON = %s, want %s", got, want)
	}

	account.SetName("User Name")
	account.SetSmtpUser("smtp-user@smtp.example.com")
	account.SetSmtpPass("smtp-secret")

	got, err = json.Marshal(account)
	if nil != err {
		t.Fatalf("Marshal: %v", err)
	}

	want = `{"email":"user@example.com","login":"user@example.com","pass":"secret","name":"User Name",` +
		`"smtp":{"user":"smtp-user@smtp.example.com","pass":"smtp-secret"}}`
	if want != string(got) {
		t.Errorf("MarshalJSON = %s, want %s", got, want)
	}

	// The JSON must decode into the token array shape the token reader expects,
	// so a cookie survives a round trip.
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); nil != err {
		t.Fatalf("Unmarshal: %v", err)
	}
	if "User Name" != decoded["name"] {
		t.Errorf("name = %v", decoded["name"])
	}
	smtp, ok := decoded["smtp"].(map[string]any)
	if !ok || "smtp-secret" != smtp["pass"] {
		t.Errorf("smtp = %v", decoded["smtp"])
	}
}

func TestMarshalJSONKeepsUnicodeAndSlashes(t *testing.T) {
	account := newTestAccountForTest(t)
	account.SetName(`Böse/<>"&`)

	got := marshalNoHTMLEscape(t, account)

	// No HTML escaping and no "\/" escaping, like PHP's json_encode() without
	// the JSON_HEX_* flags (cryptox encodes the cookie payload the same way).
	for _, forbidden := range []string{`\u003c`, `\u003e`, `\u0026`, `\/`} {
		if strings.Contains(got, forbidden) {
			t.Errorf("MarshalJSON escaped %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"name":"Böse/<>\"&"`) {
		t.Errorf("MarshalJSON = %s, want the raw name", got)
	}
}

// marshalNoHTMLEscape encodes a value the way cryptox does (SetEscapeHTML(false)),
// which is what the cookie payload sees.
func marshalNoHTMLEscape(t *testing.T, value any) string {
	t.Helper()

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); nil != err {
		t.Fatalf("Encode: %v", err)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func TestSmtpUserFallsBackToTheDomainSettings(t *testing.T) {
	config := testDomain("example.com")
	config.SMTP.ShortLogin = true
	config.SMTP.LowerLogin = true

	account := NewAccount()
	account.SetCredentials(config, "User@Example.com", "User@Example.com", "secret", "", "")

	if got, want := account.SmtpUser(), "user"; want != got {
		t.Errorf("SmtpUser = %q, want %q", got, want)
	}

	account.SetSmtpUser("explicit")
	if got, want := account.SmtpUser(), "explicit"; want != got {
		t.Errorf("SmtpUser = %q, want %q", got, want)
	}

	// Without a domain config there is nothing to fall back to.
	bare := NewAccount()
	bare.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")
	if got := bare.SmtpUser(); "" != got {
		t.Errorf("SmtpUser without a domain = %q, want empty", got)
	}
}

func TestMainAccountCryptKeyIsStableAndSealed(t *testing.T) {
	context, _, _ := newTestContext(t)
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	key, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// PHP stores sha1(pass . APP_SALT) as hex and uses the raw digest as key.
	digest, err := hex.DecodeString(storage.SHA1("secret" + testSalt))
	if nil != err {
		t.Fatalf("decode hex: %v", err)
	}
	if string(digest) != key {
		t.Errorf("CryptKey = %x, want %x", key, digest)
	}

	// The sealed value landed in storage[ROOT]/.cryptkey.
	sealed, ok := context.Storage.Get(main, storage.TypeRoot, ".cryptkey")
	if !ok || "" == sealed {
		t.Fatal("the crypt key was not stored in storage[ROOT]")
	}

	// It is the PHP format: a JSON array of [method, nonce, payload].
	var parts []string
	if err := json.Unmarshal([]byte(sealed), &parts); nil != err || 3 != len(parts) {
		t.Fatalf("the stored crypt key is not a Crypt blob: %q", sealed)
	}

	// A second account with the same credentials resolves the same key from the
	// same storage entry.
	again := NewMainAccount()
	again.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")
	second, err := again.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}
	if key != second {
		t.Errorf("CryptKey is not stable: %x != %x", key, second)
	}
}

func TestMainAccountCryptKeyIsCachedPerInstance(t *testing.T) {
	context, _, _ := newTestContext(t)
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	first, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// Removing the storage entry does not matter: the PHP model caches the key.
	if !context.Storage.Clear(main, storage.TypeRoot, ".cryptkey") {
		t.Fatal("the crypt key entry was not removed")
	}

	second, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}
	if first != second {
		t.Errorf("CryptKey = %x, want the cached %x", second, first)
	}
}

func TestMainAccountCryptKeyUsesEmailWhenInsecure(t *testing.T) {
	context, _, _ := newTestContext(t)
	context.InsecureCryptKey = true

	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	key, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// The *passphrase* is the address; the sealed value is still the password
	// digest, so the key itself is unchanged.
	digest, _ := hex.DecodeString(storage.SHA1("secret" + testSalt))
	if string(digest) != key {
		t.Errorf("CryptKey = %x, want %x", key, digest)
	}

	// Another password still resolves: the address is the passphrase now.
	other := NewMainAccount()
	other.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "changed", "", "")
	if _, err := other.CryptKey(context); nil != err {
		t.Errorf("CryptKey with insecure_cryptkey must not need the old password: %v", err)
	}
}

func TestMainAccountCryptKeyReportsCryptKeyError(t *testing.T) {
	context, _, _ := newTestContext(t)
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	if _, err := main.CryptKey(context); nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// A corrupt stored blob is a CryptKeyError (111).
	if err := context.Storage.Put(main, storage.TypeRoot, ".cryptkey", "not-json"); nil != err {
		t.Fatalf("Put: %v", err)
	}

	broken := NewMainAccount()
	broken.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "secret", "", "")

	code := cryptKeyErrorCode(t, broken, context)
	if appctx.CryptKeyError != code {
		t.Errorf("code = %d, want CryptKeyError 111", code)
	}

	// Without a storage/crypt environment it is a CryptKeyError as well instead
	// of a panic.
	if code := cryptKeyErrorCode(t, broken, &Context{}); appctx.CryptKeyError != code {
		t.Errorf("code without a context = %d, want CryptKeyError 111", code)
	}
}

func TestResealCryptKey(t *testing.T) {
	context, _, _ := newTestContext(t)
	main := NewMainAccount()
	main.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "old-password", "", "")

	key, err := main.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey: %v", err)
	}

	// The user changed the mailbox password: the key is re-sealed with it.
	main.SetImapPass("new-password")
	if resealed, err := main.ResealCryptKey(context, "old-password"); nil != err || !resealed {
		t.Fatalf("ResealCryptKey = %v, %v", resealed, err)
	}

	// A fresh account with the new password opens the re-sealed key.
	fresh := NewMainAccount()
	fresh.SetCredentials(testDomain("example.com"), "user@example.com", "user@example.com", "new-password", "", "")

	again, err := fresh.CryptKey(context)
	if nil != err {
		t.Fatalf("CryptKey after reseal: %v", err)
	}
	if key != again {
		t.Errorf("CryptKey after reseal = %x, want %x", again, key)
	}

	// The old password no longer opens it.
	if _, err := main.ResealCryptKey(context, "wrong-password"); nil == err {
		t.Error("ResealCryptKey with a wrong password must fail")
	}

	// Without a stored key reseal has nothing to do.
	empty := NewMainAccount()
	empty.SetCredentials(testDomain("other.example"), "x@other.example", "x@other.example", "secret", "", "")
	if resealed, err := empty.ResealCryptKey(context, "secret"); nil != err || resealed {
		t.Errorf("ResealCryptKey without a key = %v, %v", resealed, err)
	}
}

// cryptKeyErrorCode returns the notification code of a failing CryptKey call,
// or 0 when it succeeded.
func cryptKeyErrorCode(t *testing.T, main *MainAccount, context *Context) int {
	t.Helper()

	_, err := main.CryptKey(context)
	if nil == err {
		return 0
	}

	var clientError *appctx.ClientError
	if !errors.As(err, &clientError) {
		t.Fatalf("CryptKey error is not a ClientError: %v", err)
	}
	return clientError.Code
}
