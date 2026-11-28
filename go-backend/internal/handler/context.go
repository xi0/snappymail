package handler

import (
	"io"
	"net/http"
	"os"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Config is the application.ini surface the Do* handlers read, the same trio of
// typed getters config\Application offers.
type Config interface {
	GetString(sectionName, key, defaultValue string) string
	GetBool(sectionName, key string, defaultValue bool) bool
	GetInt(sectionName, key string, defaultValue int) int
}

// SettingsProvider is Providers\Settings::Load()/Save().
// *storage.SettingsStorage satisfies it.
type SettingsProvider interface {
	Load(account storage.Account) *storage.Settings
	Save(account storage.Account, settings *storage.Settings) error
}

// FilesProvider is the part of Providers\Files the handlers use: the
// DoFolderAppend upload of step 15 and the attachment blobs of step 20 (the
// fetched message parts and the produced zip archives). *storage.Files
// satisfies it.
type FilesProvider interface {
	MoveUploadedFile(account storage.Account, key, source string) error
	PutFile(account storage.Account, key string, source io.Reader) error
	FileExists(account storage.Account, key string) bool
	FileSize(account storage.Account, key string) (int64, bool)
	Open(account storage.Account, key string) (*os.File, error)
	Clear(account storage.Account, key string) bool
}

// StorageProvider is the part of Providers\Storage Actions\StorageProvider()
// exposes to the handlers: the NOBODY "Cache/Timers" bookkeeping and the
// per-account CONFIG entries of DoAppDelayStart/DoClearUserBackground.
// *storage.Provider satisfies it.
type StorageProvider interface {
	Get(account any, storageType storage.StorageType, key string) (string, bool)
	Put(account any, storageType storage.StorageType, key, value string) error
	Clear(account any, storageType storage.StorageType, key string) bool
	GC()
}

// Mail is the per-request mail layer: Actions::MailClient() together with
// initMailClientConnection().
type Mail interface {
	// Connect returns the resolved account and a logged-in mail client. The
	// error is an *appctx.ClientError (InvalidToken 101, ConnectionError 104 or
	// AuthError 102) like the PHP exceptions imapConnect() maps.
	Connect() (*account.Account, *mail.Client, error)
}

// MailFactory builds the mail layer of one request, so a request connects to
// IMAP at most once no matter how many Do* actions it runs.
type MailFactory func(w http.ResponseWriter, r *http.Request) Mail

// Context is the per-request action context a handler is called with, the Go
// counterpart of the $this of \RainLoop\Actions: the request, the parsed action
// parameters, the resolved account and the response helpers.
//
// Later steps attach more of the Actions surface (Cacher, Plugins, …) here.
type Context struct {
	dispatcher *Dispatcher
	session    Session

	// Request is the HTTP request of the action.
	Request *http.Request
	// Response is the writer the non-JSON endpoints use; the JSON envelope is
	// written by the dispatcher after the handler returns.
	Response http.ResponseWriter
	// Route is the parsed query-string path vector.
	Route *web.Route
	// Action is the action name without the "Do" prefix ("Version").
	Action string
	// Method is the PHP method name ("DoVersion"), which is also the plugin
	// additional-JSON lookup key.
	Method string

	params map[string]any

	// mailClient is the connection of this action (Actions::$oMailClient) and
	// mailAccount the account initMailClientConnection() returned.
	mailClient  *mail.Client
	mailAccount *account.Account
	mailDone    bool

	// checkableFolders caches Actions\Response::$aCheckableFolder.
	checkableFolders []string
	checkableDone    bool
}

// Dispatcher returns the dispatcher running the action.
func (c *Context) Dispatcher() *Dispatcher { return c.dispatcher }

// Version is APP_VERSION.
func (c *Context) Version() string {
	if nil == c || nil == c.dispatcher {
		return ""
	}
	return c.dispatcher.version
}

// Config is Actions::Config(): the application.ini of the request.
func (c *Context) Config() Config {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.config {
		return emptyConfig{}
	}
	return c.dispatcher.config
}

// Account is Actions::getAccountFromToken(): the selected additional account,
// else the main account, else nil. A nil session (the dispatcher is not wired to
// the auth layer yet) yields (nil, nil).
func (c *Context) Account(throw bool) (*account.Account, error) {
	if nil == c || nil == c.session {
		return nil, nil
	}
	return c.session.GetAccountFromToken(throw)
}

// requireAccount is getAccountFromToken() with the InvalidToken failure of the
// PHP method.
func (c *Context) requireAccount() (*account.Account, error) {
	resolved, err := c.Account(true)
	if nil != err {
		return nil, err
	}
	if nil == resolved {
		return nil, clientError(appctx.InvalidToken, "Account undefined")
	}
	return resolved, nil
}

// MailClient is Actions::initMailClientConnection() followed by
// Actions::MailClient(): the account and its logged-in client, connected once
// per request.
func (c *Context) MailClient() (*account.Account, *mail.Client, error) {
	if nil == c {
		return nil, nil, clientError(appctx.UnknownError, "")
	}
	if c.mailDone {
		return c.mailAccount, c.mailClient, nil
	}
	c.mailDone = true

	if nil == c.dispatcher || nil == c.dispatcher.newMail {
		return nil, nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	layer := c.dispatcher.newMail(c.Response, c.Request)
	if nil == layer {
		return nil, nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	resolved, client, err := layer.Connect()
	if nil != err {
		return nil, nil, err
	}

	c.mailAccount, c.mailClient = resolved, client
	return resolved, client, nil
}

// LocalSettings is Actions::SettingsProvider(true)->Load($account).
func (c *Context) LocalSettings(account *account.Account) *storage.Settings {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.localSettings || nil == account {
		return storage.NewSettings()
	}
	settings := c.dispatcher.localSettings.Load(account)
	if nil == settings {
		return storage.NewSettings()
	}
	return settings
}

// SaveLocalSettings is Actions::SettingsProvider(true)->Save($account, $settings).
func (c *Context) SaveLocalSettings(account *account.Account, settings *storage.Settings) error {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.localSettings {
		return nil
	}
	return c.dispatcher.localSettings.Save(account, settings)
}

// MainSettings is Actions::SettingsProvider()->Load($account) (the "settings"
// document). A nil provider or account yields an empty document.
func (c *Context) MainSettings(account *account.Account) *storage.Settings {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.settings || nil == account {
		return storage.NewSettings()
	}
	settings := c.dispatcher.settings.Load(account)
	if nil == settings {
		return storage.NewSettings()
	}
	return settings
}

// SaveMainSettings is Actions::SettingsProvider()->Save($account, $settings).
func (c *Context) SaveMainSettings(account *account.Account, settings *storage.Settings) error {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.settings {
		return nil
	}
	return c.dispatcher.settings.Save(account, settings)
}

// Storage is Actions::StorageProvider(): the "storage" provider of the request.
// A nil provider makes the storage dependent actions report a no-op success,
// like the PHP provider does when the driver is missing.
func (c *Context) Storage() StorageProvider {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	return c.dispatcher.storage
}

// accountHash is $account->Hash() of the current account, the value the RawKeys
// carry ("accountHash"), or "" without an account.
func (c *Context) accountHash() string {
	resolved, err := c.Account(false)
	if nil != err || nil == resolved || nil == c.dispatcher {
		return ""
	}
	return resolved.Hash(c.dispatcher.salt)
}

// Cacher is Actions::Cacher($oAccount): nil when no cache factory is wired.
func (c *Context) Cacher() Cacher {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newCacher {
		return nil
	}

	// PHP keys the cache by the *main* account of the session.
	resolved, err := c.Account(false)
	if nil != err {
		resolved = nil
	}
	if main := c.mainAccount(); nil != main {
		resolved = main
	}

	return c.dispatcher.newCacher(resolved)
}

// mainAccountSession is the optional Session extension the per-account cache
// key needs (Actions::GetMainEmail()).
//
// The method signature mirrors getMainAccountFromToken() with the base account
// type; MainAccount() below uses sessionMainAccount for the concrete
// *account.MainAccount the crypt key handling needs.
type mainAccountSession interface {
	GetMainAccountFromToken(throw bool) (*account.Account, error)
}

// sessionMainAccount is getMainAccountFromToken() with the concrete
// *account.MainAccount result (MainAccount::CryptKey() needs it).
// *auth.Authenticator implements it.
type sessionMainAccount interface {
	GetMainAccountFromToken(throw bool) (*account.MainAccount, error)
}

// accountContextSession is the optional Session extension that exposes the
// account Context of the request (APP_SALT, storage and crypt provider).
// *auth.Authenticator implements it.
type accountContextSession interface {
	AccountContext() *account.Context
}

// MainAccount is Actions::getMainAccountFromToken(): the mailbox the user
// logged in with, which owns the crypt key that seals the more sensitive
// values (additional account passwords, plugin data).
//
// Unlike Account() it always returns the main account, even when an additional
// account is selected. A nil session or one that does not implement the
// extension yields (nil, nil).
func (c *Context) MainAccount(throw bool) (*account.MainAccount, error) {
	if nil == c || nil == c.session {
		return nil, nil
	}
	session, ok := c.session.(sessionMainAccount)
	if !ok || nil == session {
		return nil, nil
	}
	return session.GetMainAccountFromToken(throw)
}

// AccountContext is the account Context of the request: the storage provider,
// the crypt engine and APP_SALT the account models (and the ported plugins)
// need. It is nil when the session does not offer one, which is what an
// unauthenticated request without a cookie/crypt stack amounts to.
func (c *Context) AccountContext() *account.Context {
	if nil == c || nil == c.session {
		return nil
	}
	session, ok := c.session.(accountContextSession)
	if !ok || nil == session {
		return nil
	}
	return session.AccountContext()
}

// Language is Actions\Localization::GetLanguage() as far as a plugin needs it:
// the language code of the account settings, else webmail.language.
//
// Partial: the Accept-Language detection of detectClientLanguage() and the
// login.determine_user_language/login.allow_languages_on_login switches are not
// ported (they belong to the unported page rendering path), and the
// filter.language hook is not run here — the callers that build AppData run it
// themselves.
func (c *Context) Language() string {
	if nil == c {
		return "en"
	}

	language := c.Config().GetString("webmail", "language", "en")

	resolved, err := c.Account(false)
	if nil != err || nil == resolved {
		return language
	}

	if c.Config().GetBool("webmail", "allow_languages_on_settings", true) {
		if settings := c.MainSettings(resolved); nil != settings {
			language = phpString(settings.GetConf("language", language))
		}
	}

	return language
}

// mainAccount is getMainAccountFromToken() when the session offers it.
func (c *Context) mainAccount() *account.Account {
	session, ok := c.session.(mainAccountSession)
	if !ok || nil == session {
		return nil
	}
	main, err := session.GetMainAccountFromToken(false)
	if nil != err {
		return nil
	}
	return main
}

// isCheckableFolder is in_array($sFolderFullName, $aCheckableFolder) of
// Actions\Response::responseObject(): the list is loaded once per request.
func (c *Context) isCheckableFolder(fullName string) bool {
	if nil == c {
		return false
	}
	for _, folder := range c.checkableFolderList() {
		if folder == fullName {
			return true
		}
	}
	return false
}

// checkableFolderList is the cached json_decode(settings_local.CheckableFolder).
func (c *Context) checkableFolderList() []string {
	if c.checkableDone {
		return c.checkableFolders
	}
	c.checkableDone = true
	c.checkableFolders = []string{}

	// PHP: getAccountFromToken() without a session returns null, and the
	// json_decode of the default "[]" then yields an empty list.
	resolved, err := c.Account(false)
	if nil != err || nil == resolved {
		return c.checkableFolders
	}

	settings := c.LocalSettings(resolved)
	c.checkableFolders = decodeCheckableFolders(settings.GetConf("CheckableFolder", "[]"))
	return c.checkableFolders
}

// emptyConfig reports the defaults of an untouched application.ini.
type emptyConfig struct{}

// GetString returns defaultValue.
func (emptyConfig) GetString(_, _, defaultValue string) string { return defaultValue }

// GetBool returns defaultValue.
func (emptyConfig) GetBool(_, _ string, defaultValue bool) bool { return defaultValue }

// GetInt returns defaultValue.
func (emptyConfig) GetInt(_, _ string, defaultValue int) int { return defaultValue }
