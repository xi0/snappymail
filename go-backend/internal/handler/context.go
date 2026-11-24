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
// key needs (Actions::GetMainEmail()). *auth.Authenticator implements it.
type mainAccountSession interface {
	GetMainAccountFromToken(throw bool) (*account.Account, error)
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
