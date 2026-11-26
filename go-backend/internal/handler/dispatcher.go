package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// The meta actions ServiceJson() treats specially.
const (
	// logoutAction skips the connection-token check, because the client may not
	// have a valid token left when it logs out.
	logoutAction = "Logout"
	// adminActionPrefix is checked with stripos() === 0 in PHP.
	adminActionPrefix = "admin"
	// jsonContentType is the CONTENT_TYPE that forces a body decode.
	jsonContentType = "application/json"
	// maxMultipartMemory caps the multipart form PHP would have parsed into
	// $_POST before it hands the request to the JSON action.
	maxMultipartMemory = 32 << 20
)

// Logger is the \SnappyMail\Log surface the dispatcher writes to.
// *logx.Logger satisfies it; a nil logger drops the lines.
type Logger interface {
	Write(desc string, level logx.Level, name string) bool
}

// Session is the request scoped session surface of \RainLoop\Actions the
// dispatcher needs: the connection/CSRF token of the token checks and the
// account of the mismatch log line.
//
// *auth.Authenticator satisfies it.
type Session interface {
	// ConnectionToken is Utils::GetConnectionToken()/GetCsrfToken().
	ConnectionToken() string
	// GetAccountFromToken is Actions::getAccountFromToken().
	GetAccountFromToken(throw bool) (*account.Account, error)
}

// SessionFactory builds the session of one request, so the dispatcher stays
// independent of the cookie jar and the response writer. Steps 7–10 wire the
// real authenticator here; a nil factory disables the session.
type SessionFactory func(w http.ResponseWriter, r *http.Request) Session

// Admin is the admin session surface of Actions\Admin::IsAdminLoggined(); step
// 27 provides the real implementation.
type Admin interface {
	// IsLoggedIn is Actions\Admin::IsAdminLoggined(): true, false, or a
	// ClientError (102) when throw is set and the session is not logged in.
	IsLoggedIn(throw bool) (bool, error)
}

// Plugins is the plugin manager surface ServiceJson() uses: the json.before-*/
// json.after-*/filter.action-params hooks and the DoPlugin* additional JSON
// actions. Step 28 provides the real manager.
type Plugins interface {
	// HasAdditionalJSON is Plugins\Manager::HasAdditionalJson().
	HasAdditionalJSON(method string) bool
	// RunAdditionalJSON is Plugins\Manager::RunAdditionalJson().
	RunAdditionalJSON(method string) (any, error)
	// RunHook is Plugins\Manager::RunHook().
	RunHook(name string, args ...any)
}

// Cacher is the MailSo\Cache\CacheClient surface responseObject() uses for the
// read-receipt suppression. *storage.Cache satisfies it.
type Cacher interface {
	// GetDefault is CacheClient::Get($sKey, $mDefault).
	GetDefault(key, defaultValue string) string
}

// CacherSetter is the optional Set() extension DoSendReadReceiptMessage() needs
// when the $MDNSent flag cannot be stored. A Cacher that does not implement it
// simply skips the cache fallback.
type CacherSetter interface {
	Set(key, value string) error
}

// CacherFactory builds Actions::Cacher($oAccount): the cache client of one
// account (or of the "_default_" prefix when resolved is nil).
type CacherFactory func(resolved *account.Account) Cacher

// Sender is the smtpclient.Sender surface DoSendMessage() needs.
type Sender interface {
	// Send delivers data to recipients (plus the optional hidden Bcc copies)
	// through the SMTP server of the account.
	Send(target *account.Account, from string, recipients, hidden []string, data []byte, dsn, requireTLS bool) error
}

// SenderFactory builds the SMTP sender of a dispatcher. The plugin manager is
// handed in so the sender can run the smtp.* connect/login hooks.
type SenderFactory func(plugins Plugins) Sender

// FiltersFactory builds Actions\Filters::FiltersProvider(): the filter provider
// of one request. The plugin manager is handed in so the provider can run the
// sieve.* connect/login hooks.
type FiltersFactory func(plugins Plugins) Filters

// Options build a Dispatcher. Everything but Version is optional; a nil
// dependency keeps the corresponding behaviour unported (see the step notes).
type Options struct {
	// Version is APP_VERSION, used by DoVersion.
	Version string
	// Salt is APP_SALT, needed by the RawKey account-hash validation
	// (Actions::decodeRawKey()).
	Salt string
	// Logger receives the JSON/POST/SERVICE log lines.
	Logger Logger
	// Session builds the per-request session.
	Session SessionFactory
	// Admin is the admin session used by the admin-only action gate.
	Admin Admin
	// Plugins runs the hooks and additional JSON actions.
	Plugins Plugins
	// Cacher is Actions::Cacher(), used to suppress an already sent read
	// receipt of a viewed message.
	Cacher CacherFactory
	// Now is the clock of the envelope "epoch"; nil uses time.Now.
	Now func() time.Time
	// Registry pre-populated with handlers; nil creates an empty one and the
	// trivial actions are registered on top.
	Registry *Registry
	// Config is configs/application.ini (the labs.*/imap.* switches the folder
	// actions read).
	Config Config
	// Settings is SettingsProvider(): the "settings" document (step 21).
	Settings SettingsProvider
	// LocalSettings is SettingsProvider(true): the "settings_local" document
	// that keeps the CheckableFolder list and the system folder names.
	LocalSettings SettingsProvider
	// Storage is Actions::StorageProvider(): the NOBODY "Cache/Timers" entries
	// of DoAppDelayStart and the per-account CONFIG entries of
	// DoClearUserBackground (step 21).
	Storage StorageProvider
	// Language is Actions\Localization::ValidateLanguage() (DoSettingsUpdate,
	// step 21). A nil value keeps the client supplied code as-is.
	Language func(language string) string
	// Theme is Actions\Themes::ValidateTheme() (DoSettingsUpdate, step 21).
	Theme func(theme string) string
	// Suggestions is Providers\Suggestions() (DoSuggestions, step 21).
	Suggestions Suggestions
	// Mail builds the mail layer of a request (Actions::MailClient() plus
	// initMailClientConnection()).
	Mail MailFactory
	// Files is FilesProvider() (only DoFolderAppend needs it).
	Files FilesProvider
	// SMTP sends composed messages (DoSendMessage/DoSendReadReceiptMessage).
	// A nil factory makes sending report CantSendMessage.
	SMTP SenderFactory
	// Contacts is Actions\Contacts::AddressBookProvider() (step 22). A nil
	// provider reports every account as having no address book.
	Contacts AddressBook
	// Filters is Actions\Filters::FiltersProvider() (step 23). A nil factory
	// reports every account as having no filter driver.
	Filters FiltersFactory
	// Accounts is the additional account surface (step 24). A nil factory makes
	// the DoAccount* list actions report FalseResponse.
	Accounts AccountsFactory
	// PGP is the OpenPGP surface (step 25): the key escrow of
	// SnappyMail\PGP\Backup, the GnuPG engine and the key server lookups. A nil
	// factory makes the DoPgp*/DoGnupg* actions report no keys and no engine.
	PGP PGPFactory
	// SMIME is the S/MIME surface (step 26): the certificate creator of
	// SnappyMail\SMime\Certificate and the ".smime" store of the main account.
	// A nil factory makes the DoSMime* actions report UnknownError, like PHP
	// calling a method on null.
	SMIME SMIMEFactory
	// Identities is Actions\Accounts::IdentitiesProvider() (step 24). A nil
	// provider makes DoAccountsAndIdentities return an empty list.
	Identities *account.Identities
	// LocalDomains is array_column(DomainProvider()->GetList(), 'name'): the
	// "localDomains" value of getAccountData().
	LocalDomains func() []string
}

// Dispatcher is the port of \RainLoop\ServiceActions::ServiceJson().
type Dispatcher struct {
	version       string
	salt          string
	logger        Logger
	newSession    SessionFactory
	admin         Admin
	plugins       Plugins
	newCacher     CacherFactory
	now           func() time.Time
	registry      *Registry
	config        Config
	settings      SettingsProvider
	localSettings SettingsProvider
	storage       StorageProvider
	language      func(language string) string
	theme         func(theme string) string
	suggestions   Suggestions
	newMail       MailFactory
	files         FilesProvider
	newSender     SenderFactory
	contacts      AddressBook
	newFilters    FiltersFactory
	newAccounts   AccountsFactory
	newPGP        PGPFactory
	newSMIME      SMIMEFactory
	identities    *account.Identities
	localDomains  func() []string
}

// NewDispatcher builds the dispatcher and registers the trivial actions.
func NewDispatcher(options Options) *Dispatcher {
	dispatcher := &Dispatcher{
		version:       options.Version,
		salt:          options.Salt,
		logger:        options.Logger,
		newSession:    options.Session,
		admin:         options.Admin,
		plugins:       options.Plugins,
		newCacher:     options.Cacher,
		now:           options.Now,
		registry:      options.Registry,
		config:        options.Config,
		settings:      options.Settings,
		localSettings: options.LocalSettings,
		storage:       options.Storage,
		language:      options.Language,
		theme:         options.Theme,
		suggestions:   options.Suggestions,
		newMail:       options.Mail,
		files:         options.Files,
		newSender:     options.SMTP,
		contacts:      options.Contacts,
		newFilters:    options.Filters,
		newAccounts:   options.Accounts,
		newPGP:        options.PGP,
		newSMIME:      options.SMIME,
		identities:    options.Identities,
		localDomains:  options.LocalDomains,
	}

	if nil == dispatcher.now {
		dispatcher.now = time.Now
	}
	if nil == dispatcher.registry {
		dispatcher.registry = NewRegistry()
	}

	RegisterSystem(dispatcher)
	RegisterFolders(dispatcher)
	RegisterMessages(dispatcher)
	RegisterMessageActions(dispatcher)
	RegisterMessageSend(dispatcher)
	RegisterAttachments(dispatcher)
	RegisterUser(dispatcher)
	RegisterContacts(dispatcher)
	RegisterFilters(dispatcher)
	RegisterAccounts(dispatcher)
	RegisterPGP(dispatcher)
	RegisterSMIME(dispatcher)

	return dispatcher
}

// Register adds an action handler, the registry hook the later steps use.
func (d *Dispatcher) Register(action string, fn ActionFunc) {
	if nil == d {
		return
	}
	if nil == d.registry {
		d.registry = NewRegistry()
	}
	d.registry.Register(action, fn)
}

// Version is APP_VERSION.
func (d *Dispatcher) Version() string {
	if nil == d {
		return ""
	}
	return d.version
}

// Serve is the ?/Json endpoint: it always handles the request.
func (d *Dispatcher) Serve(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	if nil == route {
		route = &web.Route{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	session := d.session(w, r)
	response, action := d.dispatch(w, r, route, session)

	if nil == response {
		response = d.ExceptionResponse(clientError(appctx.UnknownError, ""))
	}

	if "" == action {
		response["Action"] = "Unknown"
	} else {
		response["Action"] = action
	}
	response["epoch"] = d.now().Unix()

	d.write(w, response)

	return true
}

// dispatch is the try/catch body of ServiceJson().
func (d *Dispatcher) dispatch(
	w http.ResponseWriter,
	r *http.Request,
	route *web.Route,
	session Session,
) (response map[string]any, action string) {
	// PHP catches \Throwable; a handler bug must not take the request down.
	defer func() {
		if recovered := recover(); nil != recovered {
			response = d.ExceptionResponse(asError(recovered))
		}
	}()

	post, err := readPost(r)
	if nil != err {
		return d.ExceptionResponse(err), action
	}

	action, err = resolveAction(post, r, route)
	if nil != err {
		return d.ExceptionResponse(err), action
	}

	if logoutAction != action {
		if err := d.checkToken(r, post, session); nil != err {
			return d.ExceptionResponse(err), action
		}
	}

	if err := d.checkAdmin(route, action); nil != err {
		return d.ExceptionResponse(err), action
	}

	method := "Do" + action
	d.logWrite("Action: "+method, logx.Info, "JSON")

	params, err := d.actionParams(post, r, route, method)
	if nil != err {
		return d.ExceptionResponse(err), action
	}

	ctx := &Context{
		dispatcher: d,
		session:    session,
		Request:    r,
		Response:   w,
		Route:      route,
		Action:     action,
		Method:     method,
		params:     params,
	}

	var result any

	if fn, ok := d.registry.Lookup(action); ok {
		d.runHook("json.before-" + action)
		result, err = fn(ctx)
	} else if nil != d.plugins && d.plugins.HasAdditionalJSON(method) {
		d.runHook("json.before-" + action)
		result, err = d.plugins.RunAdditionalJSON(method)
	} else {
		// No Do<Action> and no plugin action: PHP leaves $aResponse null and
		// falls through to the UnknownError below.
		return d.ExceptionResponse(clientError(appctx.UnknownError, "")), action
	}

	if nil != err {
		return d.ExceptionResponse(err), action
	}

	response, err = d.finalizeResponse(action, result)
	if nil != err {
		return d.ExceptionResponse(err), action
	}

	return response, action
}

// actionParams is the SetActionParams() part of ServiceJson(): the POST body,
// else the GET RawKey path segment.
func (d *Dispatcher) actionParams(
	post map[string]any,
	r *http.Request,
	route *web.Route,
	method string,
) (map[string]any, error) {
	if 0 < len(post) {
		params, err := d.setActionParams(post, method)
		if nil == err {
			d.logPost(post)
		}
		return params, err
	}

	if len(route.Paths) > 3 && httpx.IsGet(r) {
		rawKey := ""
		if "" != route.Paths[3] {
			rawKey = route.Paths[3]
		}
		return d.setActionParams(map[string]any{"RawKey": rawKey}, method)
	}

	return map[string]any{}, nil
}

// setActionParams runs the filter.action-params hook and normalizes the result,
// like Actions::SetActionParams().
func (d *Dispatcher) setActionParams(params map[string]any, method string) (map[string]any, error) {
	if nil != d.plugins {
		// PHP passes the method name and the parameters by reference.
		d.plugins.RunHook("filter.action-params", method, &params)
	}
	return normalizeParams(params)
}

// finalizeResponse is the "if (is_array($aResponse))" block plus the
// "if (!is_array($aResponse)) throw UnknownError" guard.
func (d *Dispatcher) finalizeResponse(action string, result any) (map[string]any, error) {
	response, ok := result.(map[string]any)
	if !ok {
		return nil, clientError(appctx.UnknownError, "")
	}

	if nil != d.plugins {
		// PHP: RunHook("json.after-{$sAction}", array(&$aResponse)).
		d.plugins.RunHook("json.after-"+action, &response)
	}

	return response, nil
}

// ExceptionResponse is Actions\Response::ExceptionResponse().
func (d *Dispatcher) ExceptionResponse(err error) map[string]any {
	code := appctx.UnknownError
	message := ""
	additional := ""
	exceptionCode := 0

	var clientErr *appctx.ClientError
	if errors.As(err, &clientErr) {
		code = clientErr.Code
		message = clientErr.Message
		additional = clientErr.AdditionalMessage
	} else if nil != err {
		message = err.Error()
	}

	d.logException(err)

	return d.defaultResponse(false, map[string]any{
		"code":              code,
		"message":           message,
		"messageAdditional": additional,
		"ExceptionCode":     exceptionCode,
	}, nil)
}

// checkToken is the token block of ServiceJson(): the X-SM-Token header when it
// is present, else the XToken body field on POST.
func (d *Dispatcher) checkToken(r *http.Request, post map[string]any, session Session) error {
	token := ""
	if nil != session {
		token = session.ConnectionToken()
	}

	if httpx.HasHeader(r, "X-SM-Token") {
		provided := httpx.GetHeader(r, "X-SM-Token")
		if provided != token {
			d.logTokenMismatch(provided, token, session, "Token")
			return clientError(appctx.InvalidToken, "HTTP Token mismatch")
		}
		return nil
	}

	if httpx.IsPost(r) {
		value := post["XToken"]
		provided, isString := value.(string)
		if phpEmpty(value) || !isString || provided != token {
			d.logTokenMismatch(scalarString(value), token, session, "XToken")
			return clientError(appctx.InvalidToken, "XToken mismatch")
		}
	}

	return nil
}

// logTokenMismatch is the logWrite() of the token mismatch branches.
func (d *Dispatcher) logTokenMismatch(provided, token string, session Session, category string) {
	email := "guest"

	if nil != session {
		if resolved, err := session.GetAccountFromToken(false); nil == err && nil != resolved {
			email = resolved.Email()
		}
	}

	d.logWrite(provided+" !== "+token+" for "+email, logx.Error, category)
}

// checkAdmin is the admin-only action gate: an "Admin*" action other than
// AdminLogin/AdminLogout needs an admin session, but only when the request
// addresses the admin area (PHP: "$this->oActions instanceof ActionsAdmin").
func (d *Dispatcher) checkAdmin(route *web.Route, action string) error {
	if nil == route || !route.Admin {
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(action), adminActionPrefix) {
		return nil
	}
	// in_array() is case-sensitive.
	if "AdminLogin" == action || "AdminLogout" == action {
		return nil
	}

	if nil == d.admin {
		// No admin session wired yet: the action is refused exactly like an
		// unauthenticated ActionsAdmin would refuse it.
		return clientError(appctx.AuthError, "")
	}

	_, err := d.admin.IsLoggedIn(true)
	return err
}

// resolveAction is the action name resolution of ServiceJson():
//
//	$sAction = $_POST['Action'] ?? '';
//	if (empty($sAction) && IsGet() && !empty($aPaths[2])) $sAction = $aPaths[2];
//	if (!is_string($sAction)) throw InvalidInputArgument('Invalid Action');
//	if (empty($sAction)) throw InvalidInputArgument('Action unknown');
func resolveAction(post map[string]any, r *http.Request, route *web.Route) (string, error) {
	var value any = ""

	// ?? '' treats a missing or null value as the empty string.
	if candidate, ok := post["Action"]; ok && nil != candidate {
		value = candidate
	}

	if phpEmpty(value) && httpx.IsGet(r) && nil != route &&
		len(route.Paths) > 2 && !phpEmpty(route.Paths[2]) {
		value = route.Paths[2]
	}

	action, isString := value.(string)
	if !isString {
		return "", clientError(appctx.InvalidInputArgument, "Invalid Action")
	}
	// empty("0") is true in PHP, so a "0" action is unknown as well.
	if phpEmpty(action) {
		return "", clientError(appctx.InvalidInputArgument, "Action unknown")
	}

	return action, nil
}

// readPost reproduces the request body handling of ServiceJson(): $_POST when
// the request is a form, else json_decode(php://input, true).
func readPost(r *http.Request) (map[string]any, error) {
	if nil == r {
		return map[string]any{}, nil
	}

	var body []byte
	if nil != r.Body {
		if read, err := io.ReadAll(r.Body); nil == err {
			body = read
		}
	}

	isJSON := strings.Contains(strings.ToLower(httpx.GetHeader(r, "Content-Type")), jsonContentType)

	post := map[string]any{}
	if !isJSON {
		post = formValues(r, body)
	}

	if !isJSON && 0 != len(post) {
		return post, nil
	}

	decoded, _ := decodeJSONBody(body)

	if !isArrayLike(decoded) {
		// An empty body is acceptable on GET (the action comes from the URL);
		// anything else is an invalid action payload.
		if !httpx.IsGet(r) || nil != decoded {
			return nil, clientError(appctx.InvalidInputArgument, "Invalid request body")
		}
		return map[string]any{}, nil
	}

	return objectParams(decoded), nil
}

// formValues is the $_POST part of the body handling: urlencoded and multipart
// bodies become string parameters (repeated keys keep the last value, like PHP).
func formValues(r *http.Request, body []byte) map[string]any {
	if nil == r {
		return map[string]any{}
	}

	if 0 < len(body) {
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	if strings.HasPrefix(strings.ToLower(httpx.GetHeader(r, "Content-Type")), "multipart/form-data") {
		_ = r.ParseMultipartForm(maxMultipartMemory)
	} else {
		_ = r.ParseForm()
	}

	result := make(map[string]any, len(r.PostForm))
	for key, values := range r.PostForm {
		if 0 < len(values) {
			result[key] = values[len(values)-1]
		}
	}
	return result
}

// decodeJSONBody is json_decode($body, true): invalid or empty input yields nil
// (PHP returns null and the caller reports the error), a JSON document yields
// its Go value.
func decodeJSONBody(body []byte) (any, error) {
	if 0 == len(bytes.TrimSpace(body)) {
		return nil, nil
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); nil != err {
		return nil, nil
	}
	return decoded, nil
}

// objectParams turns a decoded JSON document into action parameters.
//
// A JSON object is a PHP associative array and maps directly. A JSON list is
// also an array in PHP, but every string-keyed lookup on it ("Action", …) comes
// back empty, which an empty parameter set reproduces.
func objectParams(decoded any) map[string]any {
	if params, ok := decoded.(map[string]any); ok {
		return params
	}
	return map[string]any{}
}

// session builds the request session, or nil when the auth layer is not wired.
func (d *Dispatcher) session(w http.ResponseWriter, r *http.Request) Session {
	if nil == d || nil == d.newSession {
		return nil
	}
	return d.newSession(w, r)
}

// runHook runs a plugin hook when a manager is configured.
func (d *Dispatcher) runHook(name string, args ...any) {
	if nil != d && nil != d.plugins {
		d.plugins.RunHook(name, args...)
	}
}

// logWrite writes one SnappyMail\Log line of the given category.
func (d *Dispatcher) logWrite(message string, level logx.Level, category string) {
	if nil == d || nil == d.logger {
		return
	}
	d.logger.Write(message, level, category)
}

// logPost logs the (password masked) action parameters like ServiceJson().
func (d *Dispatcher) logPost(post map[string]any) {
	if nil == d || nil == d.logger {
		return
	}

	masked := make(map[string]any, len(post))
	for key, value := range post {
		if strings.Contains(strings.ToLower(key), "pass") {
			masked[key] = logx.Mask
		} else {
			masked[key] = value
		}
	}

	if encoded, err := encodeJSON(masked); nil == err {
		d.logger.Write(string(encoded), logx.Info, "POST")
	}
}

// logException is Actions::logException(): the previous error when there is
// one, else the exception itself, at LOG_ERR.
func (d *Dispatcher) logException(err error) {
	if nil == d || nil == d.logger || nil == err {
		return
	}

	logged := err
	if previous := errors.Unwrap(err); nil != previous {
		logged = previous
	}
	d.logger.Write(logged.Error(), logx.Error, "SERVICE")
}

// write renders and writes the JSON envelope.
func (d *Dispatcher) write(w http.ResponseWriter, response map[string]any) {
	encoded, err := encodeJSON(response)
	if nil != err {
		// Cannot happen for the value types a handler returns; emit a valid
		// envelope rather than an empty body.
		encoded = []byte(`{"Action":"Unknown","Result":false,"code":999,"message":"","messageAdditional":"","ExceptionCode":0,"epoch":0}`)
	}

	d.logWrite(string(encoded), logx.Info, "JSON")

	_, _ = w.Write(encoded)
}

// asError turns a recovered panic value into an error.
func asError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("%v", recovered)
}
