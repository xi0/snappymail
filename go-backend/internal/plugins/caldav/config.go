package caldav

// The plugin's own per-account configuration: the CONFIG entry "calendar_sync"
// of the account storage.
//
// PHP writes it with json_encode() of an associative array, so the document is a
// plain JSON object; Go reproduces the key order and the escaping of
// json_encode() to keep the stored file byte identical. The "Password" member is
// a SnappyMail\Crypt blob sealed with the *main* account's crypt key
// (CryptKey()), which is what getDecryptedPassword() opens again, and
// "PasswordHMAC" is hash_hmac('sha1') of that blob with the same key.

import (
	"encoding/json"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/plugin"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// Defaults of configMapping() (the values the plugin falls back to when the
// configs/plugin-caldav.json document does not exist).
const (
	// defaultCalDAVServer is the caldav_server default.
	defaultCalDAVServer = "https://my.mailbux.com/dav/cal"
	// defaultJMAPServer is the jmap_server default.
	defaultJMAPServer = "https://my.mailbux.com/jmap"
)

// The CONFIG keys of the plugin.
const (
	// calendarSyncKey is the plugin's own configuration blob
	// (AutoConfigureCalDAV()).
	calendarSyncKey = "calendar_sync"
	// additionalAccountsKey is the additional account list of
	// \RainLoop\Actions\Accounts, read for the password conversion.
	additionalAccountsKey = "additionalaccounts"
	// calendarSyncMode is the "Mode" member AutoConfigureCalDAV() stores (1 =
	// enabled).
	calendarSyncMode = 1
)

// calendarSync is the "calendar_sync" document.
//
// The field order is the order of the PHP array literal, which is what makes the
// encoded document identical to the PHP one.
type calendarSync struct {
	// Mode is the plugin's enabled flag (always 1 when written).
	Mode int
	// User is the (punycode) address the calendar home belongs to.
	User string
	// Password is the sealed IMAP password of the account.
	Password string
	// PasswordHMAC is hash_hmac('sha1', Password, cryptKey).
	PasswordHMAC string
	// Url is the CalDAV home URL (<caldav_server>/<email>).
	Url string
}

// encode is json_encode($aCalendarData).
func (c calendarSync) encode() string {
	var builder strings.Builder

	builder.WriteString(`{"Mode":`)
	builder.WriteString(phpString(c.Mode))
	builder.WriteString(`,"User":`)
	builder.WriteString(jsonQuote(c.User))
	builder.WriteString(`,"Password":`)
	builder.WriteString(jsonQuote(c.Password))
	builder.WriteString(`,"PasswordHMAC":`)
	builder.WriteString(jsonQuote(c.PasswordHMAC))
	builder.WriteString(`,"Url":`)
	builder.WriteString(jsonQuote(c.Url))
	builder.WriteString(`}`)

	return builder.String()
}

// decodeCalendarSync is json_decode($mData, true) with the is_array() check of
// readCalendarConfig().
func decodeCalendarSync(document string) (map[string]any, bool) {
	if "" == strings.TrimSpace(document) {
		return nil, false
	}

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(document), &decoded); nil != err {
		return nil, false
	}
	return decoded, true
}

// calendarConfig is the array readCalendarConfig() returns: the CalDAV home of
// the account (User), the sealed password and the trimmed URL.
type calendarConfig struct {
	// User is the CalDAV user name (the account address).
	User string
	// Password is the sealed password (open it with decryptedPassword()).
	Password string
	// URL is the calendar home without a trailing slash.
	URL string
}

// autoConfigure is CaldavPlugin::AutoConfigureCalDAV(): store the CalDAV
// credentials of the current account in the plugin's own "calendar_sync" blob.
//
// PHP wraps the body in a try/catch that swallows every failure, so a missing
// storage provider, an unusable crypt key or an unreadable account simply leaves
// the blob untouched.
func (r *request) autoConfigure(env plugin.Env) {
	if nil == r || nil == r.current || "" == r.current.Email() {
		return
	}

	// PHP: if (!$this->Config()->Get('plugin', 'auto_sync', true)) return;
	if !envBool(env, "auto_sync", true) {
		return
	}

	if nil == r.env || nil == r.env.Storage || nil == r.env.Crypt {
		return
	}

	email := r.current.Email()

	// Build the CalDAV home URL from this plugin's own setting.
	server := strings.TrimSpace(envString(env, "caldav_server", defaultCalDAVServer))
	if "" == server {
		return
	}
	home := strings.TrimRight(server, "/") + "/" + email

	password, passwordHMAC, ok := r.calendarCredentials(email)
	if !ok {
		return
	}

	document := calendarSync{
		Mode:         calendarSyncMode,
		User:         email,
		Password:     password,
		PasswordHMAC: passwordHMAC,
		Url:          home,
	}

	// The PHP callers ignore the result of the storage write.
	_ = r.env.Storage.Put(r.storageAccount(), storage.TypeConfig, calendarSyncKey, document.encode())
}

// storageAccount is the account the plugin's CONFIG entries are stored for.
//
// PHP passes $oAccount to Providers\Storage, which resolves an additional account
// to its *parent* folder for the non-local provider (the default of
// Actions::StorageProvider(), which the plugin uses). The main account of the
// request therefore addresses exactly the same folder, for a main and for an
// additional account.
func (r *request) storageAccount() any {
	if nil == r {
		return nil
	}
	if nil != r.main {
		return r.main
	}
	if nil != r.current {
		return r.current
	}
	return nil
}

// calendarCredentials is the credential block of AutoConfigureCalDAV(): the
// sealed password plus its HMAC.
//
// An additional account stores its password as a Crypt::EncryptUrlSafe() blob of
// the main account, so it is opened with the main crypt key and re-sealed with
// Crypt::EncryptToJSON(); the primary account seals its IMAP password directly.
func (r *request) calendarCredentials(email string) (string, string, bool) {
	if nil == r.env || nil == r.env.Crypt || nil == r.main {
		return "", "", false
	}

	cryptKey, err := r.main.CryptKey(r.env)
	if nil != err || "" == cryptKey {
		return "", "", false
	}

	if entry, exists := r.additionalAccounts()[email]; exists {
		document, isDocument := entry.(map[string]any)
		if !isDocument {
			// A legacy indexed token array: PHP reads $aEntry['pass'] which is
			// an undefined index there, i.e. "not an added account".
			return r.primaryCredentials()
		}
		if password, present := document["pass"]; present && nil != password {
			plain, err := r.env.Crypt.DecryptURLSafe(phpString(password), cryptKey)
			if nil != err {
				return "", "", false
			}
			if "" == phpString(plain) {
				return "", "", false
			}

			sealed, err := r.env.Crypt.EncryptToJSON(phpString(plain), cryptKey)
			if nil != err || "" == sealed {
				return "", "", false
			}
			return sealed, account.HMAC(sealed, cryptKey), true
		}
	}

	return r.primaryCredentials()
}

// primaryCredentials is the "primary account" branch of AutoConfigureCalDAV():
// seal the IMAP password with the account's crypt key (the hook is called with
// the main account, so this is the main crypt key).
func (r *request) primaryCredentials() (string, string, bool) {
	if nil == r.env || nil == r.env.Crypt || nil == r.main {
		return "", "", false
	}

	cryptKey, err := r.main.CryptKey(r.env)
	if nil != err || "" == cryptKey {
		return "", "", false
	}

	raw := ""
	if nil != r.current {
		raw = r.current.ImapPass()
	}

	sealed, err := r.env.Crypt.EncryptToJSON(raw, cryptKey)
	if nil != err || "" == sealed {
		return "", "", false
	}
	return sealed, account.HMAC(sealed, cryptKey), true
}

// additionalAccounts is getAdditionalAccounts(): the "additionalaccounts"
// document of the account, keyed by address.
func (r *request) additionalAccounts() map[string]any {
	if nil == r || nil == r.env || nil == r.env.Storage || nil == r.current {
		return map[string]any{}
	}

	document, ok := r.env.Storage.Get(r.storageAccount(), storage.TypeConfig, additionalAccountsKey)
	if !ok || "" == document {
		return map[string]any{}
	}

	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(document), &decoded); nil != err {
		return map[string]any{}
	}
	return decoded
}

// calendarConfigFor is getCalendarConfig(): the stored configuration, rebuilt on
// demand when the blob does not exist yet.
//
// The blob is normally written by autoConfigure() on login, but a session can be
// restored without firing "login.success" (remember me) or the plugin can be
// enabled while the user is already logged in.
func calendarConfigFor(env plugin.Env, r *request) *calendarConfig {
	config := readCalendarConfig(r)
	if nil != config {
		return config
	}

	r.autoConfigure(env)

	return readCalendarConfig(r)
}

// readCalendarConfig is readCalendarConfig(): the "calendar_sync" document of
// the account, or nil when it is missing or unusable.
func readCalendarConfig(r *request) *calendarConfig {
	if nil == r || nil == r.env || nil == r.env.Storage || nil == r.current {
		return nil
	}

	document, ok := r.env.Storage.Get(r.storageAccount(), storage.TypeConfig, calendarSyncKey)
	if !ok || "" == document {
		return nil
	}

	decoded, ok := decodeCalendarSync(document)
	if !ok {
		return nil
	}

	user, hasUser := decoded["User"]
	password, hasPassword := decoded["Password"]
	calendarURL, hasURL := decoded["Url"]
	if !hasUser || !hasPassword || !hasURL {
		return nil
	}

	return &calendarConfig{
		User:     phpString(user),
		Password: phpString(password),
		URL:      strings.TrimRight(phpString(calendarURL), "/"),
	}
}

// decryptedPassword is getDecryptedPassword(): the stored CalDAV password,
// opened with the MAIN account's crypt key.
//
// ok reports whether the environment offers a crypt key at all (PHP returns null
// then, which the callers report as ERROR_NO_ENCRYPTION_KEY); err is a failed
// decryption, which PHP lets escape as an exception.
func decryptedPassword(r *request, config *calendarConfig) (password string, ok bool, err error) {
	if nil == r || nil == config || nil == r.env || nil == r.env.Crypt || nil == r.main {
		return "", false, nil
	}

	cryptKey, keyErr := r.main.CryptKey(r.env)
	if nil != keyErr || "" == cryptKey {
		return "", false, nil
	}

	plain, err := r.env.Crypt.DecryptFromJSON(config.Password, cryptKey)
	if nil != err {
		return "", true, err
	}

	return phpString(plain), true, nil
}
