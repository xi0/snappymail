package auth

// The "remember me" cookie (smremember) of Actions\UserAuth: SetSignMeToken(),
// GetAccountFromSignMeToken(), GetSignMeToken() and ClearSignMeData().
//
// The cookie is not self contained: it holds the method, the nonce and the
// account address plus a random UUID, while the encrypted account and the
// payload live in the .sign_me storage of the account. Both halves are sealed
// with Crypt's "signme" key, which is derived from APP_SALT alone, so a cookie
// and a storage entry written by the PHP backend are interchangeable with the Go
// backend.
//
// The cookie document keeps the PHP key order "e", "u", "c", "d" on purpose: it
// is a compatibility contract with GetSignMeToken().
//
// Deviation (documented in SPEC step notes): the legacy branch of
// GetSignMeToken() that derives "c"/"d" from array_key_last()/end() of the
// decrypted document is not ported. It depends on PHP's insertion ordered
// arrays, which a decoded JSON object does not preserve, and it cannot yield a
// usable method name for any layout the current writer produces; a cookie
// without "c"/"d" is treated as invalid instead.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// signMeCryptKey is the Crypt key of the sign-me blobs ('signme').
const signMeCryptKey = "signme"

// signMeCookie is the document of the smremember cookie
// (Crypt::EncryptUrlSafe([...], 'signme')). The field order is the PHP array
// order.
type signMeCookie struct {
	// Email is "e": the account address.
	Email string `json:"e"`
	// UUID is "u": the random name of the .sign_me storage entry.
	UUID string `json:"u"`
	// Cipher is "c": the Crypt method of the stored payload.
	Cipher string `json:"c"`
	// Nonce is "d": base64 of the payload nonce.
	Nonce string `json:"d"`
}

// signMeToken is the decoded smremember cookie of GetSignMeToken().
type signMeToken struct {
	email  string
	uuid   string
	cipher string
	nonce  string
}

// uuidPattern is SnappyMail\UUID::isValid() without the PECL extension.
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// GetSignMeToken is Actions\UserAuth::GetSignMeToken(): the decrypted
// smremember cookie, or nil when there is none or it is unusable.
//
// An unusable cookie is cleared, exactly like PHP.
func (l *Login) GetSignMeToken() *signMeToken {
	if nil == l || nil == l.Session {
		return nil
	}

	raw := l.Session.cookie(AuthSignMeTokenKey)
	if "" == raw {
		return nil
	}

	l.logNotice(AuthSignMeTokenKey, "decrypt")

	crypter := l.crypter()
	if nil == crypter {
		l.logNotice(AuthSignMeTokenKey, "invalid")
		l.Session.clearCookie(AuthSignMeTokenKey)
		return nil
	}

	value, err := crypter.DecryptURLSafe(raw, signMeCryptKey)
	if nil != err {
		l.logNotice(AuthSignMeTokenKey, "invalid")
		l.Session.clearCookie(AuthSignMeTokenKey)
		return nil
	}

	document, ok := value.(map[string]any)
	if !ok {
		l.logNotice(AuthSignMeTokenKey, "invalid")
		l.Session.clearCookie(AuthSignMeTokenKey)
		return nil
	}

	email := valueString(document["e"])
	uuid := valueString(document["u"])
	cipher := valueString(document["c"])
	nonce := valueString(document["d"])

	// isset($aResult['e'], $aResult['u']) && UUID::isValid($aResult['u'])
	if "" == email || "" == uuid || !uuidPattern.MatchString(uuid) || "" == cipher || "" == nonce {
		l.logNotice(AuthSignMeTokenKey, "invalid")
		l.Session.clearCookie(AuthSignMeTokenKey)
		return nil
	}

	return &signMeToken{email: email, uuid: uuid, cipher: cipher, nonce: nonce}
}

// SetSignMeToken is Actions\UserAuth::SetSignMeToken(): replace the current
// sign-me data and write a fresh 30 day cookie.
//
// The sealed account is split in two: the method and the nonce go into the
// cookie (so the cookie stays small enough for the browser), the payload goes
// into the .sign_me storage of the account.
func (l *Login) SetSignMeToken(main *account.MainAccount) {
	if nil == l || nil == main || nil == l.Session {
		return
	}

	l.ClearSignMeData()

	crypter := l.crypter()
	if nil == crypter {
		return
	}

	uuid, err := newUUID()
	if nil != err {
		return
	}

	blob, err := crypter.Encrypt(main, signMeCryptKey)
	if nil != err {
		return
	}

	cookie, err := crypter.EncryptURLSafe(signMeCookie{
		Email:  main.Email(),
		UUID:   uuid,
		Cipher: blob.Method,
		Nonce:  base64.StdEncoding.EncodeToString(blob.Nonce),
	}, signMeCryptKey)
	if nil != err {
		return
	}

	// time() + 3600 * 24 * 30
	l.Session.setCookie(AuthSignMeTokenKey, cookie, time.Now().Add(signMeLifetime))

	// StorageProvider()->Put($oAccount, SIGN_ME, $uuid, $data[2])
	_ = l.put(main, storage.TypeSignMe, uuid, string(blob.Payload))
}

// GetAccountFromSignMeToken is Actions\UserAuth::GetAccountFromSignMeToken():
// rebuild the account from the cookie and the .sign_me payload, validate it
// against the IMAP server and refresh the cookie lifetime.
//
// Any failure clears the sign-me data and yields nil, like PHP's catch block.
func (l *Login) GetAccountFromSignMeToken() *account.MainAccount {
	token := l.GetSignMeToken()
	if nil == token {
		return nil
	}

	main, err := l.rebuildSignMeAccount(token)
	if nil != err {
		l.logWarning(AuthSignMeTokenKey, err.Error())
		l.ClearSignMeData()
		return nil
	}

	return main
}

// rebuildSignMeAccount is the try block of GetAccountFromSignMeToken().
func (l *Login) rebuildSignMeAccount(token *signMeToken) (*account.MainAccount, error) {
	payload, ok := l.storageGet(token.email, storage.TypeSignMe, token.uuid)
	if !ok || "" == payload {
		return nil, errServerTokenMissing(token)
	}

	nonce, err := base64.StdEncoding.DecodeString(token.nonce)
	if nil != err {
		return nil, err
	}

	crypter := l.crypter()
	if nil == crypter {
		return nil, errNoCrypter()
	}

	document, err := crypter.Decrypt(cryptox.Blob{
		Method:  token.cipher,
		Nonce:   nonce,
		Payload: []byte(payload),
	}, signMeCryptKey)
	if nil != err {
		return nil, err
	}

	main, err := account.NewMainAccountFromToken(l.Context, document, true)
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, errTokenHasNoAccount()
	}

	// Test the credentials, then update the lifetime.
	if err := l.imapConnect(&main.Account, false); nil != err {
		return nil, err
	}

	l.SetSignMeToken(main)

	return main, nil
}

// ClearSignMeData is Actions\UserAuth::ClearSignMeData(): drop the .sign_me
// entry of the current cookie and delete the cookie itself.
func (l *Login) ClearSignMeData() {
	if nil == l || nil == l.Session {
		return
	}

	if token := l.GetSignMeToken(); nil != token {
		l.storageClear(token.email, storage.TypeSignMe, token.uuid)
	}

	l.Session.clearCookie(AuthSignMeTokenKey)
}

// SignMeResolver is the Authenticator.SignMe callback of step 7:
// GetAccountFromSignMeToken() adapted to the func() (*account.MainAccount,
// error) signature. A missing or unusable cookie is reported as (nil, nil).
func (l *Login) SignMeResolver() func() (*account.MainAccount, error) {
	return func() (*account.MainAccount, error) {
		return l.GetAccountFromSignMeToken(), nil
	}
}

// crypter is the SnappyMail\Crypt of the request.
func (l *Login) crypter() *cryptox.Crypter {
	if nil == l || nil == l.Context {
		return nil
	}
	return l.Context.Crypt
}

// newUUID is SnappyMail\UUID::generate() without the PECL extension: a random
// (version 4) UUID in lower case.
func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); nil != err {
		return "", err
	}

	// Version 4 and the RFC 4122 variant.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	encoded := hex.EncodeToString(raw[:])

	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}

// errServerTokenMissing is the RuntimeException of GetAccountFromSignMeToken()
// when the .sign_me entry is gone.
func errServerTokenMissing(token *signMeToken) error {
	return &signMeError{"server token not found for " + token.email + "/.sign_me/" + token.uuid}
}

// errTokenHasNoAccount is the "token has no account" RuntimeException.
func errTokenHasNoAccount() error { return &signMeError{"token has no account"} }

// errNoCrypter is reported when the request has no crypt engine configured.
func errNoCrypter() error { return &signMeError{"no crypt engine configured"} }

// signMeError is a plain message error, like the PHP \RuntimeException messages
// that end up in the smremember log line.
type signMeError struct{ message string }

// Error returns the message.
func (e *signMeError) Error() string { return e.message }
