package account

import (
	"crypto/hmac"
	"errors"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// errInvalidTokenArray is the \RuntimeException Account::NewInstanceFromTokenArray()
// throws when email, login or pass is missing. It never reaches the client:
// the method catches it, logs it as an "ACCOUNT" debug line and only reports it
// when the caller asked to throw.
var errInvalidTokenArray = errors.New("Invalid TokenArray")

// ConvertArray is Account::convertArray(): the current associative token array
// passes through, the legacy indexed array
// ["account", email, login, pass, ...] is converted.
//
// Anything else yields nil, which is the PHP empty array return value.
func ConvertArray(value any) map[string]any {
	if items, ok := value.(map[string]any); ok {
		if email, exists := items["email"]; exists && nil != email {
			return items
		}
		return nil
	}

	items, ok := value.([]any)
	if !ok || 7 > len(items) {
		return nil
	}
	if "account" != phpString(items[0]) {
		return nil
	}

	return map[string]any{
		"email": nonEmptyString(items[1]),
		"login": nonEmptyString(items[2]),
		"pass":  nonEmptyString(items[3]),
	}
}

// nonEmptyString is PHP's "$value ?: ''" for the elements of a token array.
func nonEmptyString(value any) string {
	if phpEmpty(value) {
		return ""
	}
	return phpString(value)
}

// NewMainAccountFromToken is MainAccount::NewInstanceFromTokenArray().
//
// throw mirrors $bThrowExceptionOnFalse: when false a failure is only logged and
// reported as (nil, nil), exactly like the PHP method's catch block.
func NewMainAccountFromToken(ctx *Context, data any, throw bool) (*MainAccount, error) {
	result := NewMainAccount()
	if err := fillFromToken(ctx, &result.Account, ConvertArray(data), throw); nil != err {
		return nil, err
	}
	if "" == result.Email() {
		return nil, nil
	}
	return result, nil
}

// NewAdditionalAccountFromToken is
// AdditionalAccount::NewInstanceFromTokenArray().
//
// The password of an additional account is sealed with the crypt key of the main
// account and signed with an hmac, so the token array is only accepted for the
// account it was written for. A broken hmac clears the password (and therefore
// makes the token unusable) instead of trusting it.
func NewAdditionalAccountFromToken(ctx *Context, data any, throw bool) (*AdditionalAccount, error) {
	converted := ConvertAdditionalArray(data)
	if nil == converted || "" == phpString(converted["email"]) {
		return nil, nil
	}

	main, err := mainAccountOf(ctx)
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, errNoMainAccount()
	}

	cryptKey, err := main.CryptKey(ctx)
	if nil != err {
		return nil, err
	}

	// PHP: "$sPasswordHMAC = $aAccountHash['hmac'] ?? null".
	if signature, ok := converted["hmac"]; ok && truthy(signature) {
		sealedPass := phpString(converted["pass"])
		if hmac.Equal([]byte(phpString(signature)), []byte(HMAC(sealedPass, cryptKey))) {
			decrypted, err := ctx.Crypt.DecryptURLSafe(sealedPass, cryptKey)
			if nil != err {
				// PHP ignores the failure and keeps the undecryptable value.
				converted["pass"] = ""
			} else {
				converted["pass"] = phpString(decrypted)
			}

			// PHP bug (kept on purpose): the SMTP password branch tests the
			// undefined $aData, so an SMTP password is never decrypted here.
		} else {
			converted["pass"] = ""
		}
	}

	result := NewAdditionalAccount()
	if err := fillFromToken(ctx, &result.Account, converted, throw); nil != err {
		return nil, err
	}
	if "" == result.Email() {
		return nil, nil
	}

	result.SetParentEmail(domain.EmailToASCII(main.Email()))

	return result, nil
}

// fillFromToken is the body of Account::NewInstanceFromTokenArray(): validate
// the token array, resolve the domain and copy the credentials.
//
// A nil error with an untouched account means "the domain provider returned no
// domain"; a nil error with a filled account means success.
func fillFromToken(ctx *Context, target *Account, data map[string]any, throw bool) error {
	if nil == data {
		return reportTokenFailure(ctx, errInvalidTokenArray, throw)
	}

	email := phpString(data["email"])
	login := phpString(data["login"])
	pass := phpString(data["pass"])

	if phpEmpty(data["email"]) || phpEmpty(data["login"]) || phpEmpty(data["pass"]) {
		return reportTokenFailure(ctx, errInvalidTokenArray, throw)
	}

	provider := ctx.domainProvider()
	if nil == provider {
		return reportTokenFailure(ctx, errors.New("no domain provider"), throw)
	}

	domainConfig, err := provider.GetByEmailAddress(email)
	if nil != err {
		return reportTokenFailure(ctx, err, throw)
	}
	if nil == domainConfig {
		// PHP: the "if ($oDomain)" block is skipped, the account stays null.
		return nil
	}

	target.email = domain.EmailToASCII(email)
	target.imapUser = login
	target.imapPass = pass
	target.domain = domainConfig

	// The hook may reject the account by resetting it (login-oauth2 and the
	// ldap plugin use this to inject or refuse credentials).
	ctx.runHook("filter.account", target)
	if "" == target.Email() {
		return reportTokenFailure(ctx,
			appctx.NewClientError(appctx.AccountFilterError, nil, ""), throw)
	}

	if name, ok := data["name"]; ok && nil != name {
		target.name = phpString(name)
	}
	if smtp, ok := data["smtp"].(map[string]any); ok {
		if user, ok := smtp["user"]; ok && nil != user {
			target.SetSmtpUser(phpString(user))
		}
		if pass, ok := smtp["pass"]; ok && nil != pass {
			target.SetSmtpPass(phpString(pass))
		}
	}

	return nil
}

// reportTokenFailure logs a failed token conversion like
// \SnappyMail\Log::debug('ACCOUNT', ...) and returns the error only when the
// caller asked for it.
func reportTokenFailure(ctx *Context, err error, throw bool) error {
	ctx.logf("ACCOUNT", err.Error())
	if throw {
		return err
	}
	return nil
}

// errNoMainAccount is the InvalidToken "Account undefined" failure of
// Actions::getMainAccountFromToken(), which AdditionalAccount depends on.
func errNoMainAccount() error {
	return appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
}

// mainAccountOf resolves the main account of the request. The callback is
// wired by internal/auth; without it the additional account cannot be built.
func mainAccountOf(ctx *Context) (*MainAccount, error) {
	if nil == ctx || nil == ctx.MainAccount {
		return nil, errNoMainAccount()
	}
	return ctx.MainAccount()
}
