// AppData is the single biggest contract the frontend has with the backend:
// \RainLoop\Actions::AppData() (guest, user) and
// \RainLoop\ActionsAdmin::AdminAppData() (admin panel) build the document that
// boots app.js. It is served by ?/AppData/0/<rand>/ and ?/AdminAppData/0/<rand>/
// (ServiceActions::ServiceAppData/ServiceAdminAppData) and is also the result of
// DoLogin/DoAdminLogin.
//
// The document is a compatibility contract, so the port keeps every key, every
// default and every cast of the PHP method, including the odd ones:
//
//   - "Plugins" is an empty *array* (`[]`), not an object, until a plugin adds
//     settings to it (step 28);
//   - "language" is the *webmail* language even in the admin area, because
//     AppData() calls GetLanguage() without the $bAdmin argument, while
//     "clientLanguage" does respect the area;
//   - MessagesPerPage is clamped to >= 10 (and to 10..100 on update);
//   - "signMe" maps login.sign_me_auto onto 0/1/2 and yields null for a value
//     outside the table, like the undefined array key of PHP does;
//   - ViewImages is a string ("ask"), messageReadAuto an int (true -> 1).
//
// The document is assembled from injected state (Config, SettingsProvider,
// DomainLister, Plugins, Admin, Session), so the ports of DoLogin (step 8) and
// AdminLogin (step 27) can reuse the same builder for the same request.
package service

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/l10n"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/themes"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// dummy is Actions::APP_DUMMY: the placeholder returned instead of a stored
// secret (the mail password of the admin panel and of contacts sync).
const dummy = "********"

// The PHP ini defaults of a Go process, which has no php.ini: AppData reports
// them through "phpUploadSizes" and derives "attachmentLimit" from
// upload_max_filesize.
const (
	defaultUploadMaxFilesize = "2M"
	defaultPostMaxSize       = "8M"
)

// Config is the application.ini surface of the AppData document.
//
// *config.Application satisfies it.
type Config interface {
	// GetString is Config\Application::Get() for a string value.
	GetString(sectionName, key, defaultValue string) string
	// GetBool is Config\Application::Get() for a boolean value.
	GetBool(sectionName, key string, defaultValue bool) bool
	// GetInt is Config\Application::Get() for an integer value.
	GetInt(sectionName, key string, defaultValue int) int
}

// SettingsProvider is Providers\Settings::Load(): the per-account document of
// the "settings" (or "settings_local") CONFIG entry.
//
// *storage.SettingsStorage satisfies it.
type SettingsProvider interface {
	Load(account storage.Account) *storage.Settings
}

// DomainLister is Providers\Domain::GetList(), used for the "localDomains"
// list (the domains this instance serves).
//
// *domain.Provider satisfies it.
type DomainLister interface {
	GetList(includeAliases bool) []domain.ListEntry
}

// AddressBook is the part of Providers\AddressBook AppData needs (step 22).
//
// A nil AddressBook reports every account as having no address book, which is
// what contacts.enable = false does in PHP.
type AddressBook interface {
	// IsActive is Providers\AddressBook::IsActive().
	IsActive(account *account.Account) bool
	// SyncData is Actions\Contacts::getContactsSyncData(); nil means "no
	// stored sync data".
	SyncData(account *account.Account) map[string]any
}

// Plugins is the Plugins\Manager surface AppData uses (step 28).
//
// A nil Plugins keeps "Plugins" empty and "PluginsLink" unset, exactly like a
// build without plugins.
type Plugins interface {
	// HaveJS is Plugins\Manager::HaveJs().
	HaveJS(admin bool) bool
	// Hash is Plugins\Manager::Hash(): the plugin version plus the asset
	// modification times.
	Hash() string
	// RunHook is Plugins\Manager::RunHook() (filter.language).
	RunHook(name string, args ...any)
	// InitAppData is Plugins\Manager::InitAppData().
	InitAppData(admin bool, appData map[string]any, account *account.Account)
}

// AdminSession is Actions\Admin::IsAdminLoggined() (step 27).
type AdminSession interface {
	IsLoggedIn(throw bool) (bool, error)
}

// Options is the installation-level state of the AppData builder. The zero
// value is usable: it reports the PHP defaults of an untouched
// application.ini with no account and no plugins.
type Options struct {
	// Config is configs/application.ini.
	Config Config
	// Version is APP_VERSION.
	Version string
	// Salt is APP_SALT (account hashes and the cookie tokens).
	Salt string
	// Paths is the install tree URL helpers (webPath, webVersionPath,
	// StaticLibsJs).
	Paths httpx.WebPaths
	// IndexRoot is APP_INDEX_ROOT_PATH (the "@custom" themes).
	IndexRoot string
	// PrivateData is APP_PRIVATE_DATA (admin_password.txt).
	PrivateData string
	// Dev is SNAPPYMAIL_DEV: expose labs.dev_email/dev_password to the login
	// form.
	Dev bool
	// Settings is SettingsProvider() (the "settings" file).
	Settings SettingsProvider
	// LocalSettings is SettingsProvider(true) (the "settings_local" file).
	LocalSettings SettingsProvider
	// Cache is Actions::Cacher() (the file cache of system data and the index
	// page). A nil cache disables caching.
	Cache *storage.Cache
	// Domains is DomainProvider().
	Domains DomainLister
	// AddressBook is AddressBookProvider() (step 22).
	AddressBook AddressBook
	// Files is FilesProvider(): the uploaded attachment blobs (step 12).
	Files *storage.Files
	// Storage is StorageProvider(): account CONFIG entries such as the user
	// background (step 12).
	Storage *storage.Provider
	// MessageStreamer is MailClient()->MessageMimeStream() for the Raw message
	// endpoints (step 14). A nil value disables the message branches.
	MessageStreamer MessageStreamer
	// Plugins is Plugins() (step 28).
	Plugins Plugins
	// Admin is the admin session of the admin area (step 27).
	Admin AdminSession
	// GnuPGVersion is PGP\GnuPG::getEngineInfo()['version'] (step 25); "" means
	// the binary is not available and drops the GnuPG capability.
	GnuPGVersion string
	// PdoDrivers is Pdo\Base::getAvailableDrivers(); nil reports a build
	// without contact database drivers.
	PdoDrivers []string
	// AttachmentThumbnails tells whether image thumbnails can be produced.
	// Go resizes them itself (golang.org/x/image), so main sets this to true;
	// the PHP capability needs gd/gmagick/imagick.
	AttachmentThumbnails bool
	// UploadMaxFilesize and PostMaxSize are the php.ini values the frontend
	// reports (Go has no php.ini).
	UploadMaxFilesize string
	PostMaxSize       string
	// NewSession builds the request session from the cookies (main wires
	// cryptox.Jar + auth.Authenticator here). A nil factory serves the guest
	// state only.
	NewSession func(w http.ResponseWriter, r *http.Request) *Session
	// Logger is the SnappyMail\Log sink of the endpoints that only log:
	// ?/CspReport (SnappyMail\HTTP\CSP::logReport()). A nil logger drops the
	// reports.
	Logger *logx.Logger
}

// Session is the request-scoped auth state of AppData: the cookies, the
// resolved account and the admin state.
//
// main builds it per request from cryptox.Jar + auth.Authenticator; the
// later DoLogin/DoAdminLogin ports pass the same value to Build.
type Session struct {
	// Request is the HTTP request (Accept-Language).
	Request *http.Request
	// Response receives the cookie clearing of the single-use mailto cookie.
	Response http.ResponseWriter
	// Authenticator resolves the account and the connection/CSRF token.
	Authenticator *auth.Authenticator
	// Jar is the cookie jar (smremember, smmailtoauth).
	Jar *cryptox.Jar
	// Account is the account the caller already resolved (DoLogin passes the
	// account it just logged in). A nil value means "resolve from the cookies".
	Account *account.Account
	// MainEmail is the main account address of Account; empty means Account is
	// the main account itself.
	MainEmail string
	// AdminLoggedIn is Actions\Admin::IsAdminLoggined(false) when no
	// AdminSession is configured on the Options.
	AdminLoggedIn bool
}

// AppData builds the ?/AppData and ?/AdminAppData documents.
type AppData struct {
	options   Options
	themes    themes.Set
	languages *l10n.Catalog
}

// NewAppData builds the AppData service for an installation.
func NewAppData(options Options) *AppData {
	versionRoot := options.Paths.VersionRoot

	return &AppData{
		options: options,
		themes: themes.Set{
			VersionRoot: versionRoot,
			IndexRoot:   options.IndexRoot,
			Paths:       options.Paths,
		},
		languages: l10n.NewCatalog(versionRoot),
	}
}

// Serve is ServiceActions::ServiceAppData(): ?/AppData/0/<rand>/.
func (a *AppData) Serve(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	return a.serve(w, r, false)
}

// ServeAdmin is ServiceActions::ServiceAdminAppData(): ?/AdminAppData/0/<rand>/.
func (a *AppData) ServeAdmin(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	return a.serve(w, r, true)
}

// serve is ServiceActions::localAppData(): a no-store JSON document, or a 500
// with the error message when the document cannot be built.
func (a *AppData) serve(w http.ResponseWriter, r *http.Request, admin bool) bool {
	httpx.NoStore(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	result, err := a.build(admin, a.session(w, r))
	if nil != err {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, err.Error())
		return true
	}

	encoded, err := encodeJSON(result)
	if nil != err {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, err.Error())
		return true
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)

	return true
}

// NewSession builds the request session with the configured factory, so the
// ?/Json dispatcher (step 9) and the login actions (steps 8 and 27) share
// exactly the same cookie/account stack as the AppData endpoints. The result is
// nil when no factory is configured.
func (a *AppData) NewSession(w http.ResponseWriter, r *http.Request) *Session {
	return a.session(w, r)
}

// session builds the request session from the injected factory.
func (a *AppData) session(w http.ResponseWriter, r *http.Request) *Session {
	factory := a.options.NewSession
	if nil == factory && nil == a.options.Admin {
		return nil
	}

	session := (*Session)(nil)
	if nil != factory {
		session = factory(w, r)
	}
	if nil == session {
		// An admin-only installation (no cookie wiring yet): the admin flag of
		// the Options still decides the Auth state.
		session = &Session{Request: r, Response: w}
	}

	return session
}

// build is localAppData() with the panic guard of the PHP try/catch: a broken
// hook must not take the request down.
func (a *AppData) build(admin bool, session *Session) (result map[string]any, err error) {
	defer func() {
		if recovered := recover(); nil != recovered {
			result = nil
			err = panicError(recovered)
		}
	}()

	return a.Build(admin, session), nil
}

// Build is Actions::AppData(): the document of the guest, user or admin state.
func (a *AppData) Build(admin bool, session *Session) map[string]any {
	config := a.config()

	system := map[string]any{
		"version":        a.options.Version,
		"token":          csrfToken(session),
		"languages":      a.languages.Languages(false),
		"webPath":        a.options.Paths.Path(),
		"webVersionPath": a.options.Paths.VersionPath(),
	}

	result := map[string]any{
		"Auth":                  false,
		"title":                 config.GetString("webmail", "title", "SnappyMail Webmail"),
		"loadingDescription":    config.GetString("webmail", "loading_description", "SnappyMail"),
		"Plugins":               []any{},
		"System":                system,
		"allowLanguagesOnLogin": config.GetBool("login", "allow_languages_on_login", true),
	}

	var resolved *account.Account
	mainEmail := ""

	if admin {
		a.adminAppData(result, session)
	} else {
		resolved, mainEmail = accounts(session)
		if nil != resolved {
			a.userAppData(result, system, resolved, mainEmail, session)
		} else {
			a.guestAppData(result)
		}
	}

	if phpBool(result["Auth"]) {
		a.applyAuth(result, system, admin, resolved)
	}

	// AppData() calls GetLanguage() without the $bAdmin argument, so the
	// webmail language is reported in the admin area as well.
	result["Theme"] = a.theme(admin, resolved)
	result["language"] = a.language(false, resolved, acceptLanguage(session))
	result["clientLanguage"] = a.validateLanguageEmpty(
		a.detectClientLanguage(admin, acceptLanguage(session)), "", admin)
	result["PluginsLink"] = a.pluginsLink(admin)
	result["StaticLibsJs"] = a.staticLibsJS()

	if nil != a.options.Plugins {
		a.options.Plugins.InitAppData(admin, result, resolved)
	}

	return result
}

// applyAuth is the "if ($aResult['Auth'])" block shared by the user and the
// logged in admin state.
func (a *AppData) applyAuth(result, system map[string]any, admin bool, resolved *account.Account) {
	config := a.config()

	result["proxyExternalImages"] = config.GetBool("labs", "use_local_proxy_for_external_images", false)
	result["autoVerifySignatures"] = config.GetBool("security", "auto_verify_signatures", false)
	result["allowLanguagesOnSettings"] = config.GetBool("webmail", "allow_languages_on_settings", true)
	result["minRefreshInterval"] = config.GetInt("webmail", "min_refresh_interval", 5)
	result["Capa"] = a.capa(admin, resolved)
	result["attachmentLimit"] = a.attachmentLimit()
	result["phpUploadSizes"] = map[string]any{
		"upload_max_filesize": a.uploadMaxFilesize(),
		"post_max_size":       a.postMaxSize(),
	}
	system["themes"] = a.themes.List()
}

// capa is Actions::Capa(): the frontend feature switches.
//
// The PHP method caches the array in a static and only recomputes Sieve; the
// result is identical because every other entry depends on the configuration
// alone (and on the admin flag for AttachmentThumbnails, which is why the
// cached-first-call quirk cannot be observed through AppData: the user branch
// always asks first with false).
func (a *AppData) capa(admin bool, resolved *account.Account) map[string]any {
	config := a.config()

	sieve := admin
	if nil != resolved && nil != resolved.Domain() && resolved.Domain().Sieve.Enabled {
		sieve = true
	}

	return map[string]any{
		"AdditionalAccounts": config.GetBool("webmail", "allow_additional_accounts", false),
		"AttachmentThumbnails": config.GetBool("interface", "show_attachment_thumbnail", true) &&
			(admin || a.options.AttachmentThumbnails),
		"AttachmentsActions": config.GetBool("capa", "attachments_actions", false),
		"Contacts":           config.GetBool("contacts", "enable", false),
		"DangerousActions":   config.GetBool("capa", "dangerous_actions", true),
		"GnuPG":              config.GetBool("security", "gnupg", true) && "" != a.options.GnuPGVersion,
		"Identities":         config.GetBool("webmail", "allow_additional_identities", false),
		"OpenPGP":            config.GetBool("security", "openpgp", true),
		"Sieve":              sieve,
		"Themes":             config.GetBool("webmail", "allow_themes", false),
		"UserBackground":     config.GetBool("webmail", "allow_user_background", false),
		"Kolab":              false,
	}
}

// attachmentLimit is the "attachmentLimit" value:
// min(upload_max_filesize, webmail.attachment_size_limit * 1024 * 1024).
func (a *AppData) attachmentLimit() int {
	upload := iniSizeBytes(a.uploadMaxFilesize())
	configured := a.config().GetInt("webmail", "attachment_size_limit", 10) * 1024 * 1024

	return phpMin(upload, configured)
}

// uploadMaxFilesize is ini_get('upload_max_filesize').
func (a *AppData) uploadMaxFilesize() string {
	value := strings.TrimSpace(a.options.UploadMaxFilesize)
	if "" == value {
		return defaultUploadMaxFilesize
	}
	return value
}

// postMaxSize is ini_get('post_max_size').
func (a *AppData) postMaxSize() string {
	value := strings.TrimSpace(a.options.PostMaxSize)
	if "" == value {
		return defaultPostMaxSize
	}
	return value
}

// iniSizeBytes is PHP's intval() plus the K/M/G suffix arithmetic of AppData().
func iniSizeBytes(value string) int {
	value = strings.TrimSpace(value)
	if "" == value {
		value = defaultUploadMaxFilesize
	}

	// PHP: intval('2M') reads the leading number and stops at the suffix.
	size := leadingInt(value)
	switch strings.ToUpper(value[len(value)-1:]) {
	case "G":
		size *= 1024
		fallthrough
	case "M":
		size *= 1024
		fallthrough
	case "K":
		size *= 1024
	}
	return size
}

// leadingInt is PHP's intval() for a string: the leading decimal number, or 0.
func leadingInt(value string) int {
	value = strings.TrimSpace(value)

	end := 0
	if 0 < len(value) && ('+' == value[0] || '-' == value[0]) {
		end = 1
	}
	start := end
	for end < len(value) && '0' <= value[end] && '9' >= value[end] {
		end++
	}
	if end == start {
		return 0
	}

	number, err := strconv.Atoi(value[:end])
	if nil != err {
		return 0
	}
	return number
}

// newMailSounds is the glob(APP_VERSION_ROOT_PATH.'static/sounds/*.mp3') loop.
func (a *AppData) newMailSounds() []string {
	matches, err := filepath.Glob(filepath.Join(
		strings.TrimRight(a.options.Paths.VersionRoot, `/\`), "static", "sounds", "*.mp3"))
	if nil != err {
		return []string{}
	}

	sounds := make([]string, 0, len(matches))
	for _, file := range matches {
		sounds = append(sounds, strings.TrimSuffix(filepath.Base(file), ".mp3"))
	}

	return sounds
}

// pluginsLink is the PluginsLink value: the ?/Plugins URL including the plugin
// asset hash, or "" when no plugin adds JavaScript.
func (a *AppData) pluginsLink(admin bool) string {
	if nil == a.options.Plugins || !a.options.Plugins.HaveJS(admin) {
		return ""
	}

	area := "User"
	if admin {
		area = "Admin"
	}

	return "Plugins/0/" + area + "/" + a.etag(a.options.Plugins.Hash()) + "/"
}

// staticLibsJS is the StaticLibsJs value: the vendor bundle of the current
// debug setting.
func (a *AppData) staticLibsJS() string {
	debug := a.config().GetBool("debug", "javascript", false) ||
		a.config().GetBool("debug", "enable", false)

	if debug {
		return a.options.Paths.StaticPath("js/libs.js")
	}
	return a.options.Paths.StaticPath("js/min/libs.min.js")
}

// etag is Actions::etag(): md5(key + cache.index + APP_VERSION).
func (a *AppData) etag(key string) string {
	return md5Hex(key + a.config().GetString("cache", "index", "") + a.options.Version)
}

// localDomains is array_column(DomainProvider()->GetList(), 'name').
func (a *AppData) localDomains() []string {
	if nil == a.options.Domains {
		return []string{}
	}

	entries := a.options.Domains.GetList(true)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}

	return names
}

// config returns the configuration, or an empty one when it is not wired.
func (a *AppData) config() Config {
	if nil == a.options.Config {
		return emptyConfig{}
	}
	return a.options.Config
}

// emptyConfig reports the defaults of an untouched application.ini.
type emptyConfig struct{}

// GetString returns defaultValue.
func (emptyConfig) GetString(_, _, defaultValue string) string { return defaultValue }

// GetBool returns defaultValue.
func (emptyConfig) GetBool(_, _ string, defaultValue bool) bool { return defaultValue }

// GetInt returns defaultValue.
func (emptyConfig) GetInt(_, _ string, defaultValue int) int { return defaultValue }

// csrfToken is Utils::GetCsrfToken() of the request.
func csrfToken(session *Session) string {
	if nil == session || nil == session.Authenticator {
		return ""
	}
	return session.Authenticator.CSRFToken()
}

// accounts resolves the selected account and the main account address, like
// getAccountFromToken(false) + getMainAccountFromToken()->Email().
func accounts(session *Session) (*account.Account, string) {
	if nil == session {
		return nil, ""
	}

	if nil != session.Account {
		mainEmail := session.MainEmail
		if "" == mainEmail {
			mainEmail = session.Account.Email()
		}
		return session.Account, mainEmail
	}

	if nil == session.Authenticator {
		return nil, ""
	}

	resolved, err := session.Authenticator.GetAccountFromToken(false)
	if nil != err || nil == resolved {
		return nil, ""
	}

	mainEmail := resolved.Email()
	if main, _ := session.Authenticator.GetMainAccountFromToken(false); nil != main {
		mainEmail = main.Email()
	}

	return resolved, mainEmail
}

// acceptLanguage is $_SERVER['HTTP_ACCEPT_LANGUAGE'] with the 'en' default of
// detectClientLanguage() for a request without the header.
func acceptLanguage(session *Session) string {
	if nil == session || nil == session.Request {
		return "en"
	}
	if !httpx.HasHeader(session.Request, "Accept-Language") {
		return "en"
	}
	return httpx.GetHeader(session.Request, "Accept-Language")
}

// hasSignMe is isset($_COOKIE[AUTH_SIGN_ME_TOKEN_KEY]).
func hasSignMe(session *Session) bool {
	return nil != session && nil != session.Jar && session.Jar.Has(auth.AuthSignMeTokenKey)
}

// panicError turns a recovered panic value into an error for the 500 body.
func panicError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("%v", recovered)
}
