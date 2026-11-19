// Package account ports the account models of SnappyMail:
// RainLoop\Model\Account, RainLoop\Model\MainAccount and
// RainLoop\Model\AdditionalAccount, plus the token (de)serialization that turns
// the smaccount/smadditional cookies back into an account.
//
// The serialized account is a compatibility contract, because the PHP and Go
// backends may serve the same browser session during a migration: the cookie
// written by one backend must decrypt and authenticate in the other. That is why
//   - the JSON shape and key order of jsonSerialize() are reproduced exactly
//     ("email", "login", "pass", "name" and the "smtp" block);
//   - legacy indexed token arrays (["account", email, login, pass, ...]) are
//     still accepted, including the AdditionalAccount "hmac" element;
//   - Hash() stays sha1(email . APP_SALT . imapUser) and the additional account
//     hash is sha1(parentHash . parentEmail);
//   - the additional account password is sealed with the main account's crypt
//     key and signed with hash_hmac('sha1') exactly like asTokenArray().
//
// There are no globals: APP_SALT, the crypt engine, the storage provider, the
// domain provider, the plugin hooks and the logger are handed in through
// Context, so the package is testable and can be wired per request.
package account

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// Storage is the part of \RainLoop\Providers\Storage the account models use:
// the crypt key of a main account lives in the ROOT storage of the account.
//
// *storage.Provider satisfies it.
type Storage interface {
	Get(account any, storageType storage.StorageType, key string) (string, bool)
	Put(account any, storageType storage.StorageType, key, value string) error
	Clear(account any, storageType storage.StorageType, key string) bool
}

// DomainProvider is \RainLoop\Providers\Domain. NewInstanceFromTokenArray() uses
// GetByEmailAddress() and step 8 (login) uses Load().
type DomainProvider interface {
	// Load is Providers\Domain::Load().
	Load(name string, options ...domain.LoadOption) *domain.Domain
	// GetByEmailAddress is Providers\Domain::getByEmailAddress(): the domain
	// config of an address, or a ClientError (109/110).
	GetByEmailAddress(email string) (*domain.Domain, error)
}

// Hooks runs plugin hooks, the \RainLoop\Plugins\Manager surface the models
// call. Step 28 provides the implementation; a nil Hooks runs nothing.
type Hooks interface {
	RunHook(name string, args ...any)
}

// Context is the environment of one request: everything the models need that
// \RainLoop\Actions would otherwise reach through \RainLoop\Api globals.
//
// The zero value is usable for pure token (de)serialization; the storage
// dependent parts (CryptKey) report a CryptKeyError instead of panicking.
type Context struct {
	// Salt is APP_SALT, the md5(salt + privateDataName + salt) value of
	// include.php. Hash() and the crypt key derivation use it.
	Salt string
	// Storage is the storage provider of the account (Providers\Storage).
	Storage Storage
	// Domains resolves domain configurations (Providers\Domain).
	Domains DomainProvider
	// Crypt is SnappyMail\Crypt with APP_SALT/APP_VERSION configured.
	Crypt *cryptox.Crypter
	// InsecureCryptKey is security.insecure_cryptkey: seal the crypt key with
	// the e-mail address instead of the IMAP password.
	InsecureCryptKey bool
	// Plugins runs the filter.account/filter.domain hooks.
	Plugins Hooks
	// MainAccount is Actions::getMainAccountFromToken(). An additional account
	// needs it for its parent address and for the main crypt key; internal/auth
	// wires the callback, a nil callback makes it unavailable.
	MainAccount func() (*MainAccount, error)
	// Log writes a debug line, like \SnappyMail\Log::debug($category, $message).
	Log func(category, message string)
}

// logf writes a debug line when a logger is configured.
func (c *Context) logf(category, message string) {
	if nil != c && nil != c.Log {
		c.Log(category, message)
	}
}

// runHook runs a plugin hook when a plugin manager is configured.
func (c *Context) runHook(name string, args ...any) {
	if nil != c && nil != c.Plugins {
		c.Plugins.RunHook(name, args...)
	}
}

// DomainProvider returns the configured provider, or nil.
func (c *Context) domainProvider() DomainProvider {
	if nil == c {
		return nil
	}
	return c.Domains
}

// Account is RainLoop\Model\Account: the credentials and the domain config of
// one mailbox.
//
// The fields are private on purpose: the PHP class has typed private properties
// and setCredentials()/set*() accessors, and the account is passed to plugin
// hooks by pointer, so a hook mutates the same object the caller sees.
type Account struct {
	name     string
	email    string
	imapUser string
	imapPass string
	smtpUser string
	smtpPass string
	// smtpPassSet mirrors "oSmtpPass !== null": jsonSerialize() writes an empty
	// password when it was set explicitly, but omits the key when it was not.
	smtpPassSet bool
	// parentEmail is ParentEmail(): only an additional account has one. It is
	// kept on the base type so Hash() stays correct for every value of the
	// account, like PHP's virtual method dispatch does.
	parentEmail string
	domain      *domain.Domain
}

// NewAccount builds an empty account (PHP: "new Account").
func NewAccount() *Account { return &Account{} }

// Email is Account::Email().
func (a *Account) Email() string { return a.email }

// Name is Account::Name().
func (a *Account) Name() string { return a.name }

// ImapUser is Account::ImapUser().
func (a *Account) ImapUser() string { return a.imapUser }

// ImapPass is Account::ImapPass().
func (a *Account) ImapPass() string { return a.imapPass }

// SmtpUser is Account::SmtpUser(): the configured user, else the e-mail address
// passed through the SMTP fixUsername() of the domain.
func (a *Account) SmtpUser() string {
	if "" != a.smtpUser {
		return a.smtpUser
	}
	if nil != a.domain {
		return a.domain.SMTP.FixUsername(a.email, true)
	}
	return ""
}

// SmtpPass is the SMTP password of the account (PHP: oSmtpPass->getValue()).
func (a *Account) SmtpPass() string { return a.smtpPass }

// HasSmtpPass reports whether setSmtpPass()/setCredentials() set a password.
func (a *Account) HasSmtpPass() bool { return a.smtpPassSet }

// Domain is Account::Domain().
func (a *Account) Domain() *domain.Domain { return a.domain }

// Hash is Account::Hash(): sha1(implode(APP_SALT, [email, imapUser])).
//
// An additional account folds its parent address into the hash
// (AdditionalAccount::Hash()), so the same address in two main accounts gets two
// different storage folders. That part is applied here instead of in an
// overridden method because every caller holds the base type: PHP dispatches
// virtually, Go would silently use the main account formula.
//
// The value identifies the account in RawKeys, so it is part of the contract
// with the PHP backend.
func (a *Account) Hash(salt string) string {
	hash := storage.SHA1(a.email + salt + a.imapUser)
	if "" != a.parentEmail {
		return storage.SHA1(hash + a.parentEmail)
	}
	return hash
}

// SetName is the missing setter of the PHP model, which assigns sName directly
// when reading a token array.
func (a *Account) SetName(name string) { a.name = name }

// SetImapUser is Account::setImapUser().
func (a *Account) SetImapUser(user string) { a.imapUser = user }

// SetImapPass is Account::setImapPass().
func (a *Account) SetImapPass(pass string) { a.imapPass = pass }

// SetSmtpUser is Account::setSmtpUser().
func (a *Account) SetSmtpUser(user string) { a.smtpUser = user }

// SetSmtpPass is Account::setSmtpPass().
func (a *Account) SetSmtpPass(pass string) {
	a.smtpPass = pass
	a.smtpPassSet = true
}

// SetCredentials is Account::setCredentials().
//
// An empty smtpUser means "no separate SMTP login" and an empty smtpPass means
// "no separate SMTP password", like the PHP defaults of the method.
func (a *Account) SetCredentials(
	domainConfig *domain.Domain,
	email, imapUser, imapPass, smtpUser, smtpPass string,
) {
	a.email = email
	a.domain = domainConfig
	a.imapUser = imapUser
	a.imapPass = imapPass
	a.smtpUser = smtpUser
	if "" != smtpPass {
		a.SetSmtpPass(smtpPass)
	}
}

// MarshalJSON is Account::jsonSerialize(): the JSON document of the smaccount
// cookie and of the "accounts" setting.
//
// The key order and the conditional "smtp" block follow the PHP array exactly,
// so a cookie written by Go has the same layout PHP would write.
func (a *Account) MarshalJSON() ([]byte, error) {
	var builder bytes.Buffer

	builder.WriteString(`{"email":`)
	builder.WriteString(jsonString(a.email))
	builder.WriteString(`,"login":`)
	builder.WriteString(jsonString(a.imapUser))
	builder.WriteString(`,"pass":`)
	builder.WriteString(jsonString(a.imapPass))
	builder.WriteString(`,"name":`)
	builder.WriteString(jsonString(a.name))
	builder.WriteString(`,"smtp":{`)

	wrote := false
	if "" != a.smtpUser {
		builder.WriteString(`"user":`)
		builder.WriteString(jsonString(a.smtpUser))
		wrote = true
	}
	if a.smtpPassSet {
		if wrote {
			builder.WriteByte(',')
		}
		builder.WriteString(`"pass":`)
		builder.WriteString(jsonString(a.smtpPass))
	}

	builder.WriteString("}}")

	return builder.Bytes(), nil
}

// JSONObject is the account as the token array of jsonSerialize(), with the same
// keys. Callers that have to modify the document (the additional account token
// adds "hmac") use it instead of MarshalJSON().
func (a *Account) JSONObject() map[string]any {
	smtp := map[string]any{}
	if "" != a.smtpUser {
		smtp["user"] = a.smtpUser
	}
	if a.smtpPassSet {
		smtp["pass"] = a.smtpPass
	}

	return map[string]any{
		"email": a.email,
		"login": a.imapUser,
		"pass":  a.imapPass,
		"name":  a.name,
		"smtp":  smtp,
	}
}

// jsonString encodes one JSON string the way the PHP backend stores it: no HTML
// escaping (<, >, &) and no "\/" escaping of slashes.
func jsonString(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); nil != err {
		return `""`
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// phpString ports PHP's (string) cast for the types a JSON token array holds.
func phpString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	}
	return ""
}

// phpEmpty ports PHP's empty() for the values a decoded token array can hold:
// the string "0" counts as empty, just like the number 0.
func phpEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return "" == typed || "0" == typed
	case bool:
		return !typed
	case int:
		return 0 == typed
	case int64:
		return 0 == typed
	case float64:
		return 0 == typed
	case []any:
		return 0 == len(typed)
	case map[string]any:
		return 0 == len(typed)
	}
	return false
}

// truthy is !phpEmpty().
func truthy(value any) bool { return !phpEmpty(value) }
