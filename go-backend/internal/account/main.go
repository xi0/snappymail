package account

import (
	"encoding/hex"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// cryptKeyName is the ROOT storage key of the sealed crypt key
// (MainAccount::CryptKey()).
const cryptKeyName = ".cryptkey"

// MainAccount is RainLoop\Model\MainAccount: the mailbox the user logged in
// with. It owns the crypt key that seals the more sensitive values of the
// account (additional account passwords, PGP key material, ...).
type MainAccount struct {
	Account

	// cryptKey is the decoded key of a resolved CryptKey() call.
	cryptKey string
	// cryptKeyCached mirrors "if (!$this->sCryptKey)".
	cryptKeyCached bool
}

// NewMainAccount builds an empty main account.
func NewMainAccount() *MainAccount { return &MainAccount{} }

// CryptKey is MainAccount::CryptKey(): the account's symmetric key, sealed in
// the ROOT storage with the IMAP password.
//
// Sealing the key with the login password lets an operator change the mailbox
// password and re-seal the key with the old password afterwards
// (ResealCryptKey). The sealed value is the hex sha1 of the password plus
// APP_SALT, so it is derived from PHP-identical inputs.
func (a *MainAccount) CryptKey(ctx *Context) (string, error) {
	if a.cryptKeyCached {
		return a.cryptKey, nil
	}

	if nil == ctx || nil == ctx.Storage || nil == ctx.Crypt {
		return "", appctx.NewClientError(appctx.CryptKeyError, nil, "crypt key storage unavailable")
	}

	sealed, _ := ctx.Storage.Get(a, storage.TypeRoot, cryptKeyName)
	passphrase := a.cryptKeyPassphrase(ctx)

	if "" == sealed {
		var err error
		sealed, err = ctx.Crypt.EncryptToJSON(storage.SHA1(a.ImapPass()+ctx.Salt), passphrase)
		if nil != err {
			return "", appctx.NewClientError(appctx.CryptKeyError, err, "")
		}
		// PHP ignores a failing Put() here and fails on the decrypt below.
		_ = ctx.Storage.Put(a, storage.TypeRoot, cryptKeyName, sealed)
	}

	plain, err := ctx.Crypt.DecryptFromJSON(sealed, passphrase)
	if nil != err {
		return "", appctx.NewClientError(appctx.CryptKeyError, err, "")
	}

	key, ok := plain.(string)
	if !ok || "" == key {
		return "", appctx.NewClientError(appctx.CryptKeyError, nil, "")
	}

	decoded, err := hex.DecodeString(key)
	if nil != err {
		// PHP's hex2bin() emits a warning and returns false.
		return "", appctx.NewClientError(appctx.CryptKeyError, err, "")
	}

	a.cryptKey = string(decoded)
	a.cryptKeyCached = true

	return a.cryptKey, nil
}

// cryptKeyPassphrase is the passphrase of the sealed crypt key:
// security.insecure_cryptkey seals it with the e-mail address instead of the
// IMAP password.
func (a *MainAccount) cryptKeyPassphrase(ctx *Context) string {
	if nil != ctx {
		if ctx.InsecureCryptKey {
			return a.Email()
		}
	}
	return a.ImapPass()
}

// ResealCryptKey is MainAccount::resealCryptKey(): open the stored key with the
// old password and seal it again with the current one.
func (a *MainAccount) ResealCryptKey(ctx *Context, oldPass string) (bool, error) {
	if nil == ctx || nil == ctx.Storage || nil == ctx.Crypt {
		return false, nil
	}

	sealed, ok := ctx.Storage.Get(a, storage.TypeRoot, cryptKeyName)
	if !ok || "" == sealed {
		return false, nil
	}

	plain, err := ctx.Crypt.DecryptFromJSON(sealed, oldPass)
	if nil != err || !truthy(plain) {
		if nil != err {
			return false, appctx.NewClientError(appctx.CryptKeyError, err, "")
		}
		return false, appctx.NewClientError(appctx.CryptKeyError, nil, "")
	}

	resealed, err := ctx.Crypt.EncryptToJSON(plain, a.cryptKeyPassphrase(ctx))
	if nil != err || "" == resealed {
		return false, nil
	}

	a.cryptKey = ""
	a.cryptKeyCached = false

	if err := ctx.Storage.Put(a, storage.TypeRoot, cryptKeyName, resealed); nil != err {
		return false, nil
	}

	return true, nil
}
