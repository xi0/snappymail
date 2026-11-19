package account

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
)

// AdditionalAccount is RainLoop\Model\AdditionalAccount: a second mailbox the
// user added to the session. Its credentials are sealed with the crypt key of
// the main account, and its storage lives below the main account.
//
// The parent address lives in the embedded Account (see Account.Hash), so the
// hash stays correct for every static type. Only this type exposes it as
// ParentEmail(), which is what tells the storage providers (and
// \RainLoop\Providers\Storage) that the account is an additional one.
type AdditionalAccount struct {
	Account
}

// NewAdditionalAccount builds an empty additional account.
func NewAdditionalAccount() *AdditionalAccount { return &AdditionalAccount{} }

// ParentEmail is AdditionalAccount::ParentEmail(): the (punycode) address of the
// main account.
func (a *AdditionalAccount) ParentEmail() string { return a.parentEmail }

// SetParentEmail records the main account address of the additional account.
func (a *AdditionalAccount) SetParentEmail(email string) { a.parentEmail = email }

// AsTokenArray is AdditionalAccount::asTokenArray(): the document stored in the
// "accounts" setting and read back by NewAdditionalAccountFromToken().
//
// The password (and the SMTP password) are sealed with the main account's crypt
// key and the sealed password is signed with hash_hmac('sha1'), so the token
// array cannot be replayed against another main account.
func (a *AdditionalAccount) AsTokenArray(ctx *Context, main *MainAccount) (map[string]any, error) {
	if nil == main {
		return nil, errNoMainAccount()
	}

	cryptKey, err := main.CryptKey(ctx)
	if nil != err {
		return nil, err
	}

	data := a.JSONObject()

	sealedPass, err := ctx.Crypt.EncryptURLSafe(a.ImapPass(), cryptKey)
	if nil != err {
		return nil, err
	}
	data["pass"] = sealedPass

	if smtp, ok := data["smtp"].(map[string]any); ok {
		if pass, ok := smtp["pass"].(string); ok && "" != pass {
			sealedSmtpPass, err := ctx.Crypt.EncryptURLSafe(pass, cryptKey)
			if nil != err {
				return nil, err
			}
			smtp["pass"] = sealedSmtpPass
			data["smtp"] = smtp
		}
	}

	data["hmac"] = HMAC(sealedPass, cryptKey)

	return data, nil
}

// ConvertAdditionalArray is AdditionalAccount::convertArray(): the parent
// conversion plus the "hmac" element of the legacy indexed array, which was
// appended last (8 or 9 elements in total).
func ConvertAdditionalArray(value any) map[string]any {
	result := ConvertArray(value)
	if nil == result {
		return nil
	}

	if items, ok := value.([]any); ok {
		if count := len(items); 7 < count && 9 >= count {
			result["hmac"] = items[count-1]
		}
	}

	return result
}

// HMAC is hash_hmac('sha1', value, key) with raw=false.
func HMAC(value, key string) string {
	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
