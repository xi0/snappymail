package auth

// This file ports the credential path of SnappyMail:
// Actions\UserAuth::resolveLoginCredentials(), LoginProcess() and
// imapConnect(), Actions::loginErrorDelay() and
// Actions\User::DoLogin()/DoLogout().
//
// The HTTP surface is not part of this step: the ?/Json dispatcher of step 9
// calls DoLogin()/DoLogout() and the ?/Sso endpoint of step 13 calls DoSso(),
// both with the values of LoginParams, and wraps the result into the JSON
// envelope (and, for DoLogin, into AppData() of step 10). Everything the flow
// needs from later steps is injected, so this package stays testable:
//
//   - IMAPLogin is Account::ImapConnectAndLogin() (step 14). A nil callback
//     skips the credentials check, which is what the pre-IMAP steps do.
//   - Settings is Actions::SettingsProvider(), used to persist the language
//     chosen on the login form.
//   - ValidateLanguage is Actions::ValidateLanguage(); the language list
//     arrives in step 11 (internal/l10n).
//   - Cache is Actions::Cacher(), used by the single-use SSO hashes of
//     API::CreateUserSsoHash().
//
// The PHP quirks are kept on purpose:
//
//   - the credentials hooks receive the values by pointer (PHP's &$sEmail), so
//     a plugin can rewrite them, and an empty result falls back to the previous
//     value ("$sNewEmail ?: $sEmail");
//   - login.success fires after SetMainAuthAccount() but before SetAuthToken(),
//     which is the "Must be here due to bug #1241" ordering;
//   - the SESSION storage entry is written before the cookies, so a login that
//     cannot seal the account cookie still leaves a usable session;
//   - login.fault_delay is measured against the request start, not against the
//     moment the failure was detected.

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// loginCategory is the "LOGIN" category of the SnappyMail\Log lines.
const loginCategory = "LOGIN"

// maxDomainParts is the min(count($aDomainParts), 14) limit of
// determine_user_domain.
const maxDomainParts = 14

// signMeLifetime is the 30 day lifetime of the smremember cookie
// (time() + 3600 * 24 * 30).
const signMeLifetime = 30 * 24 * time.Hour

// The signMe values of \RainLoop\Enumerations\SignMeType.
const (
	// SignMeDefaultOff is SignMeType::DefaultOff.
	SignMeDefaultOff = "DefaultOff"
	// SignMeDefaultOn is SignMeType::DefaultOn.
	SignMeDefaultOn = "DefaultOn"
	// SignMeUnused is SignMeType::Unused.
	SignMeUnused = "Unused"
)

// Config is the application.ini surface of the login flow.
// *config.Application satisfies it; a nil Config yields the PHP defaults.
type Config interface {
	GetString(sectionName, key, defaultValue string) string
	GetBool(sectionName, key string, defaultValue bool) bool
	GetInt(sectionName, key string, defaultValue int) int
}

// SettingsStore is Actions::SettingsProvider(); *storage.SettingsStorage
// satisfies it. A nil store disables the language persisting of DoLogin() and
// DoSso().
type SettingsStore interface {
	Load(account storage.Account) *storage.Settings
	Save(account storage.Account, settings *storage.Settings) error
}

// MailErrorKind classifies a failed IMAP login, because imapConnect() maps the
// MailSo exception classes onto different notification codes.
type MailErrorKind int

// The MailSo\Net/Imap exception classes imapConnect() distinguishes.
const (
	// MailErrorOther is any other failure: AuthError (102).
	MailErrorOther MailErrorKind = iota
	// MailErrorConnection is MailSo\Net\Exceptions\ConnectionException:
	// ConnectionError (104).
	MailErrorConnection
	// MailErrorBadCredentials is
	// MailSo\Imap\Exceptions\LoginBadCredentialsException: AuthError (102), with
	// getAlertFromStatus() as the additional message when
	// imap.show_login_alert is on.
	MailErrorBadCredentials
)

// MailError is the failure an IMAPLogin callback reports. Step 14
// (internal/imapclient) fills Kind/Alert from the connection result.
type MailError struct {
	// Kind selects the notification code.
	Kind MailErrorKind
	// Alert is the IMAP alert text of a credentials failure.
	Alert string
	// Err is the underlying transport/protocol failure.
	Err error
}

// Error is the message of the wrapped error.
func (e *MailError) Error() string {
	if nil == e {
		return ""
	}
	if nil != e.Err {
		return e.Err.Error()
	}
	return "imap login failed"
}

// Unwrap exposes the wrapped error for errors.Is()/errors.As().
func (e *MailError) Unwrap() error {
	if nil == e {
		return nil
	}
	return e.Err
}

// Credentials is the array resolveLoginCredentials() returns.
type Credentials struct {
	// Email is the (possibly rewritten) address, punycode encoded.
	Email string
	// Domain is the domain config of the address, or nil when it has none.
	Domain *domain.Domain
	// ImapUser is the IMAP login name after ImapSettings::fixUsername().
	ImapUser string
	// SmtpUser is the SMTP login name after SmtpSettings::fixUsername().
	SmtpUser string
	// Password is the password after the login.credentials hooks.
	Password string
}

// LoginParams are the action parameters of DoLogin(): Email, Password,
// signMe and language.
type LoginParams struct {
	// Email is the "Email" parameter.
	Email string
	// Password is the "Password" parameter.
	Password string
	// SignMe is the truthiness of "signMe".
	SignMe bool
	// Language is the "language" parameter (empty when the client did not send
	// one, which is the normal case after the first login).
	Language string
}

// Login is the login flow of one request.
//
// A Login is scoped to one request and not safe for concurrent use.
type Login struct {
	// Context is the request environment (salt, storage, crypt, domains, hooks).
	Context *account.Context
	// Session is the authenticator of the request: it provides the session
	// token, the SESSION/SIGN_ME storage access and the account cookies.
	Session *Authenticator
	// Config is application.ini.
	Config Config
	// Logger writes the LOGIN and smremember lines.
	Logger Logger
	// Plugins runs the login.* hooks; nil falls back to Context.Plugins.
	Plugins account.Hooks
	// Host is Http()->GetHost(false, true): the request host without "www." or
	// port, used by login.determine_user_domain.
	Host string
	// HTTPHost and ServerName are $_SERVER['HTTP_HOST'] and
	// $_SERVER['SERVER_NAME'], used by the "HTTP_HOST"/"SERVER_NAME" values of
	// login.default_domain. HostName is gethostname() for the "gethostname"
	// value; a nil function uses os.Hostname.
	HTTPHost   string
	ServerName string
	HostName   func() (string, error)
	// IMAPLogin validates the credentials (Account::ImapConnectAndLogin,
	// step 14). A nil callback skips the check.
	IMAPLogin func(a *account.Account) error
	// Settings is Actions::SettingsProvider(); nil disables the language
	// persisting.
	Settings SettingsStore
	// ValidateLanguage is Actions::ValidateLanguage(); nil keeps the code as
	// sent (the language list arrives in step 11).
	ValidateLanguage func(language string) string
	// Cache is Actions::Cacher(), the store of the single-use SSO hashes. A
	// nil cache makes DoSso() a no-op.
	Cache *storage.Cache
	// ClientIP is the client address for the auth log line.
	ClientIP string
	// Start is $_SERVER['REQUEST_TIME_FLOAT'], the reference point of
	// login.fault_delay. A zero Start uses time.Now on the first delay.
	Start time.Time
	// Sleep pauses for login.fault_delay; nil uses time.Sleep.
	Sleep func(d time.Duration)
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

// DoLogin is Actions\User::DoLogin(): the login itself, the optional sign-me
// cookie and the language the user picked on the login form.
//
// The caller wraps the returned account into AppData(false) (step 10) and the
// JSON envelope (step 9). A failed login sleeps for login.fault_delay first,
// exactly like PHP.
func (l *Login) DoLogin(params LoginParams) (*account.MainAccount, error) {
	main, err := l.LoginProcess(params.Email, params.Password)
	if nil != err {
		l.LoginErrorDelay()
		return nil, err
	}

	// empty($this->GetActionParam('signMe', 0)) || $this->SetSignMeToken($oAccount)
	if params.SignMe {
		l.SetSignMeToken(main)
	}

	l.persistLanguage(main, params.Language)

	return main, nil
}

// DoLogout is Actions\User::DoLogout(): clear the account cookies (the main
// one always, see the $bMain comment in PHP) and the sign-me data.
func (l *Login) DoLogout() error {
	if nil != l.Session {
		l.Session.Logout(true)
	}
	l.ClearSignMeData()
	return nil
}

// LoginProcess is Actions\UserAuth::LoginProcess() for a main account: resolve
// the credentials, validate them against the IMAP server and start the session.
func (l *Login) LoginProcess(email, password string) (*account.MainAccount, error) {
	credentials, err := l.resolveCredentials(email, password)
	if nil != err {
		return nil, err
	}
	if "" == password {
		return nil, appctx.NewClientError(appctx.InvalidInputArgument, nil, "")
	}

	main := account.NewMainAccount()
	if err := l.prepareAccount(&main.Account, credentials); nil != err {
		return nil, err
	}

	if err := l.imapConnect(&main.Account, true); nil != err {
		return nil, err
	}

	if nil != l.Session {
		// Server side control/kickout of logged in sessions.
		if err := l.put(main, storage.TypeSession, l.Session.SessionToken(true), "true"); nil != err {
			return nil, err
		}

		// Must be here due to bug #1241.
		l.Session.SetMainAuthAccount(main)
		l.runHook("login.success", main)

		if err := l.Session.SetAuthToken(main); nil != err {
			return nil, err
		}
		if err := l.Session.SetAdditionalAuthToken(nil); nil != err {
			return nil, err
		}
	} else {
		l.runHook("login.success", main)
	}

	return main, nil
}

// LoginAdditionalProcess is LoginProcess() with $bMainAccount = false: no
// session is started, which is what DoAccountSetup of step 24 needs.
func (l *Login) LoginAdditionalProcess(email, password string) (*account.AdditionalAccount, error) {
	credentials, err := l.resolveCredentials(email, password)
	if nil != err {
		return nil, err
	}
	if "" == password {
		return nil, appctx.NewClientError(appctx.InvalidInputArgument, nil, "")
	}

	additional := account.NewAdditionalAccount()
	if err := l.prepareAccount(&additional.Account, credentials); nil != err {
		return nil, err
	}

	if err := l.imapConnect(&additional.Account, true); nil != err {
		return nil, err
	}

	return additional, nil
}

// resolveCredentials is the first half of LoginProcess(): the credentials
// resolution plus the "@" check PHP does before it touches the domain provider.
func (l *Login) resolveCredentials(email, password string) (*Credentials, error) {
	credentials, err := l.ResolveLoginCredentials(email, password)
	if nil != err {
		return nil, err
	}
	if !strings.Contains(credentials.Email, "@") {
		return nil, appctx.NewClientError(appctx.InvalidInputArgument, nil, "")
	}
	return credentials, nil
}

// prepareAccount is the try block of LoginProcess(): build the account from the
// credentials, let the plugins vet it and report a rejected account as
// AccountFilterError.
//
// The getByEmailAddress() check is a gate only: PHP discards its result and
// passes the domain that resolveLoginCredentials() found to setCredentials(),
// so this does the same. A failing lookup (DomainNotAllowed 109,
// AccountNotAllowed 110) is reported as-is; it happens outside PHP's try block,
// so it is not auth logged.
func (l *Login) prepareAccount(target *account.Account, credentials *Credentials) error {
	if provider := l.domainProvider(); nil != provider {
		if _, err := provider.GetByEmailAddress(credentials.Email); nil != err {
			return err
		}
	}

	target.SetCredentials(credentials.Domain, credentials.Email,
		credentials.ImapUser, credentials.Password, credentials.SmtpUser, "")

	// The hook may reject the account by resetting it to an empty address.
	l.runHook("filter.account", target)
	if "" == target.Email() {
		l.AuthLog(nil, credentials.Email)
		return appctx.NewClientError(appctx.AccountFilterError, nil, "")
	}

	return nil
}

// ResolveLoginCredentials is Actions\UserAuth::resolveLoginCredentials().
//
// It completes an address without "@" from login.determine_user_domain or
// login.default_domain, runs the three credentials hooks and applies the
// domain's fixUsername() rules to the IMAP and SMTP login names.
func (l *Login) ResolveLoginCredentials(email, password string) (*Credentials, error) {
	email = domain.EmailToASCII(trimValue(email))

	newEmail := email
	l.runHook("login.credentials.step-1", &newEmail)
	if "" != newEmail {
		email = newEmail
	}

	var config *domain.Domain
	provider := l.domainProvider()

	// When the email address is missing the domain, try to add it.
	if !strings.Contains(email, "@") {
		l.logf(logx.Info, "The email address '"+email+"' is incomplete")

		if l.configBool("login", "determine_user_domain", false) {
			userHost := domain.EmailToASCII(asciiDomain(l.Host))
			l.logf(logx.Info, "Determined user domain: "+userHost)

			// Determine without wildcard.
			parts := strings.Split(userHost, ".")
			limit := len(parts)
			if maxDomainParts < limit {
				limit = maxDomainParts
			}
			for ; 0 < limit; limit-- {
				candidate := strings.Join(parts, ".")
				if config = l.loadDomain(provider, candidate); nil != config {
					email += "@" + candidate
					l.logf(logx.Info, "Check '"+candidate+"': OK")
					break
				}
				l.logf(logx.Info, "Check '"+candidate+"': NO")
				parts = parts[1:]
			}

			// Else determine with wildcard.
			if nil == config {
				if config = l.loadDomain(provider, userHost, domain.WithWildcard()); nil != config {
					email += "@" + userHost
					l.logf(logx.Info, "Check '"+userHost+"' with wildcard: OK")
				} else {
					l.logf(logx.Info, "Check '"+userHost+"' with wildcard: NO")
				}
			}

			if nil == config {
				l.logf(logx.Info, "Domain '"+userHost+"' was not determined!")
			}
		}

		// Else try the default domain.
		if nil == config {
			defaultDomain := strings.TrimSpace(l.configString("login", "default_domain", ""))
			if "" != defaultDomain {
				switch defaultDomain {
				case "HTTP_HOST", "SERVER_NAME":
					value := l.ServerName
					if "HTTP_HOST" == defaultDomain {
						value = l.HTTPHost
					}
					defaultDomain = portSuffix.ReplaceAllString(value, "")
				case "gethostname":
					defaultDomain = l.hostName()
				}
				email += "@" + defaultDomain
				l.logf(logx.Info, "Default domain '"+defaultDomain+"' will be used.")
			} else {
				l.logf(logx.Info, "Default domain not configured.")
			}
		}
	}

	newEmail = email
	newPassword := password
	l.runHook("login.credentials.step-2", &newEmail, &newPassword)
	if "" != newEmail {
		email = newEmail
	}

	imapUser := email
	smtpUser := email

	if strings.Contains(email, "@") {
		if nil == config {
			config = l.loadDomain(provider, account.EmailDomain(email), domain.WithWildcard())
		}
		if nil != config {
			email = config.IMAP.FixUsername(email, false)
			imapUser = config.IMAP.FixUsername(imapUser, true)
			smtpUser = config.SMTP.FixUsername(smtpUser, true)
		}
	}

	newEmail = email
	newImapUser := imapUser
	newSmtpUser := smtpUser
	l.runHook("login.credentials", &newEmail, &newImapUser, &newPassword, &newSmtpUser)

	// PHP: '$sNewEmail ?: $sEmail' for every value.
	email = firstNonEmpty(newEmail, email)
	imapUser = firstNonEmpty(newImapUser, imapUser)
	smtpUser = firstNonEmpty(newSmtpUser, smtpUser)

	return &Credentials{
		Email:    email,
		Domain:   config,
		ImapUser: imapUser,
		SmtpUser: smtpUser,
		Password: newPassword,
	}, nil
}

// imapConnect is Actions\UserAuth::imapConnect(): run the IMAP login and map
// its failure onto the notification codes of the PHP catch blocks.
func (l *Login) imapConnect(target *account.Account, authLog bool) error {
	if nil == l.IMAPLogin || nil == target {
		return nil
	}

	err := l.IMAPLogin(target)
	if nil == err {
		return nil
	}

	// A ClientException is rethrown unchanged.
	var clientError *appctx.ClientError
	if errors.As(err, &clientError) {
		return err
	}

	var mailError *MailError
	if errors.As(err, &mailError) {
		switch mailError.Kind {
		case MailErrorConnection:
			return appctx.NewClientError(appctx.ConnectionError, err, "")

		case MailErrorBadCredentials:
			if authLog {
				l.AuthLog(target, "")
			}
			if l.configBool("imap", "show_login_alert", true) {
				return appctx.NewClientError(appctx.AuthError, err, mailError.Alert)
			}
			return appctx.NewClientError(appctx.AuthError, err, "")
		}
	}

	return appctx.NewClientError(appctx.AuthError, err, "")
}

// LoginErrorDelay is Actions::loginErrorDelay(): keep a failed login busy for
// login.fault_delay seconds, counted from the request start.
func (l *Login) LoginErrorDelay() {
	delay := l.configInt("login", "fault_delay", 0)
	if 0 >= delay {
		return
	}

	start := time.Time{}
	if nil != l {
		start = l.Start
	}
	if start.IsZero() {
		start = l.now()
	}

	// $seconds = $iDelay - (microtime(true) - $_SERVER['REQUEST_TIME_FLOAT'])
	remaining := time.Duration(delay)*time.Second - l.now().Sub(start)
	if 0 >= remaining {
		return
	}

	l.sleep(remaining)
}

// SignMeAuto is login.sign_me_auto, the SignMeType value the frontend uses to
// pre-select the "remember me" checkbox (AppData.signMe, step 10).
//
// Like PHP, DoLogin() does not act on it: the client sends "signMe", and the
// nextcloud integration calls SetSignMeToken() directly for DefaultOn.
func (l *Login) SignMeAuto() string {
	return l.configString("login", "sign_me_auto", SignMeDefaultOff)
}

// AuthLog is Actions::LoggerAuthHelper(): write the configured
// logs.auth_logging_format line for a failed authentication.
//
// Partial: the line is written through Logger at Warning level, so it reaches the
// configured logs.filename target. The separate auth log file
// (logs.auth_logging + logs.auth_logging_filename) and the
// logs.auth_syslog target need a second log driver and are added in step 33.
func (l *Login) AuthLog(target *account.Account, login string) {
	format := l.configString("logs", "auth_logging_format", "")
	if "" == format {
		return
	}

	email := login
	if "" == email && nil != target {
		email = target.Email()
	}

	host := account.EmailDomain(email)
	values := map[string]string{
		"{request:ip}":     l.clientIP(),
		"{imap:login}":     firstNonEmpty(login, email),
		"{imap:host}":      host,
		"{smtp:login}":     firstNonEmpty(login, email),
		"{smtp:host}":      host,
		"{user:email}":     email,
		"{user:login}":     account.EmailLocalPart(email),
		"{user:domain}":    host,
		"{request:domain}": l.Host,
	}
	if nil != target && "" != target.ImapUser() {
		if "" == login {
			values["{imap:login}"] = target.ImapUser()
			values["{smtp:login}"] = target.SmtpUser()
		}
		if config := target.Domain(); nil != config {
			values["{imap:host}"] = config.IMAP.Host
			values["{imap:port}"] = strconv.Itoa(config.IMAP.Port)
			values["{smtp:host}"] = config.SMTP.Host
			values["{smtp:port}"] = strconv.Itoa(config.SMTP.Port)
		}
	}

	line := compileLogLine(format, values, l.now())
	if nil != l && nil != l.Logger {
		l.Logger.Write(line, logx.Warning, "")
	}
}

// persistLanguage is the language block of DoLogin() and the
// AdditionalOptions block of ServiceSso(): store the validated language in the
// account settings when it changed.
func (l *Login) persistLanguage(main *account.MainAccount, language string) {
	if nil == l || nil == l.Settings || nil == main || "" == language {
		return
	}

	settings := l.Settings.Load(main)
	if nil == settings {
		return
	}

	language = l.validateLanguage(language)
	if "" == language {
		return
	}

	if valueString(settings.GetConf("language", "")) == language {
		return
	}

	settings.SetConf("language", language)
	_ = l.Settings.Save(main, settings)
}

// validateLanguage calls the injected validator, else keeps the code as sent.
func (l *Login) validateLanguage(language string) string {
	if nil != l && nil != l.ValidateLanguage {
		return l.ValidateLanguage(language)
	}
	return language
}

// domainProvider is the configured Providers\Domain wrapper.
func (l *Login) domainProvider() account.DomainProvider {
	if nil == l || nil == l.Context {
		return nil
	}
	return l.Context.Domains
}

// loadDomain is Providers\Domain::Load() with a nil-safe provider.
func (l *Login) loadDomain(provider account.DomainProvider, name string, options ...domain.LoadOption) *domain.Domain {
	if nil == provider || "" == name {
		return nil
	}
	return provider.Load(name, options...)
}

// put is StorageProvider()->Put(); the PHP callers ignore the result outside the
// session entry.
func (l *Login) put(target any, storageType storage.StorageType, key, value string) error {
	if nil == l || nil == l.Context || nil == l.Context.Storage {
		return nil
	}
	return l.Context.Storage.Put(target, storageType, key, value)
}

// storageGet is StorageProvider()->Get().
func (l *Login) storageGet(target any, storageType storage.StorageType, key string) (string, bool) {
	if nil == l || nil == l.Context || nil == l.Context.Storage {
		return "", false
	}
	return l.Context.Storage.Get(target, storageType, key)
}

// storageClear is StorageProvider()->Clear().
func (l *Login) storageClear(target any, storageType storage.StorageType, key string) bool {
	if nil == l || nil == l.Context || nil == l.Context.Storage {
		return false
	}
	return l.Context.Storage.Clear(target, storageType, key)
}

// runHook runs a plugin hook, preferring the explicitly configured manager.
func (l *Login) runHook(name string, args ...any) {
	if nil == l {
		return
	}
	if nil != l.Plugins {
		l.Plugins.RunHook(name, args...)
		return
	}
	if nil != l.Context && nil != l.Context.Plugins {
		l.Context.Plugins.RunHook(name, args...)
	}
}

// logf writes a SnappyMail\Log line of the LOGIN category.
func (l *Login) logf(level logx.Level, message string) {
	if nil != l && nil != l.Logger {
		l.Logger.Write(message, level, loginCategory)
	}
}

// logWarning writes a SnappyMail\Log warning line of the given category.
func (l *Login) logWarning(category, message string) {
	if nil != l && nil != l.Logger {
		l.Logger.Write(message, logx.Warning, category)
	}
}

// logNotice writes a SnappyMail\Log notice line of the given category.
func (l *Login) logNotice(category, message string) {
	if nil != l && nil != l.Logger {
		l.Logger.Write(message, logx.Notice, category)
	}
}

// clientIP is the client address of the auth log line.
func (l *Login) clientIP() string {
	if nil == l {
		return ""
	}
	return l.ClientIP
}

// hostName is gethostname().
func (l *Login) hostName() string {
	if nil != l && nil != l.HostName {
		name, err := l.HostName()
		if nil == err {
			return name
		}
		return ""
	}
	name, err := os.Hostname()
	if nil != err {
		return ""
	}
	return name
}

// now returns the current time through the injected clock.
func (l *Login) now() time.Time {
	if nil != l && nil != l.Now {
		return l.Now()
	}
	return time.Now()
}

// sleep pauses for login.fault_delay.
func (l *Login) sleep(d time.Duration) {
	if nil != l && nil != l.Sleep {
		l.Sleep(d)
		return
	}
	time.Sleep(d)
}

// configString reads a string value from application.ini.
func (l *Login) configString(sectionName, key, defaultValue string) string {
	if nil == l || nil == l.Config {
		return defaultValue
	}
	return l.Config.GetString(sectionName, key, defaultValue)
}

// configBool reads a bool value from application.ini.
func (l *Login) configBool(sectionName, key string, defaultValue bool) bool {
	if nil == l || nil == l.Config {
		return defaultValue
	}
	return l.Config.GetBool(sectionName, key, defaultValue)
}

// configInt reads an int value from application.ini.
func (l *Login) configInt(sectionName, key string, defaultValue int) int {
	if nil == l || nil == l.Config {
		return defaultValue
	}
	return l.Config.GetInt(sectionName, key, defaultValue)
}

// portSuffix is the '/:[0-9]+$/D' replacement of the default_domain
// HTTP_HOST/SERVER_NAME handling.
var portSuffix = regexp.MustCompile(`:[0-9]+$`)

// logToken matches a "{category:name}" placeholder of the log formats.
var logToken = regexp.MustCompile(`\{([^}]*)\}`)

// compileLogLine replaces the {category:name} placeholders of a log format and
// strips the rest, mirroring the $aClear rules of Actions::compileLogParams().
func compileLogLine(format string, values map[string]string, now time.Time) string {
	line := format

	if strings.Contains(line, "{date:") {
		line = dateToken.ReplaceAllStringFunc(line, func(match string) string {
			return now.UTC().Format(phpDateFormat(dateToken.FindStringSubmatch(match)[1]))
		})
	}

	for token, value := range values {
		line = strings.ReplaceAll(line, token, value)
	}

	return logToken.ReplaceAllStringFunc(line, func(match string) string {
		name, _, found := strings.Cut(match[1:], ":")
		if !found {
			return match
		}
		switch name {
		case "date":
			// A {date:...} pattern that did not match the replacer stays as is.
			return match
		case "imap", "smtp", "request", "user", "labs":
			return name
		}
		return match
	})
}

// dateToken matches the "{date:<format>}" placeholder.
var dateToken = regexp.MustCompile(`\{date:([^}]+)\}`)

// phpDateReplacements are the PHP date() tokens the default
// auth_logging_format uses.
var phpDateReplacements = []struct {
	php   string
	goFmt string
}{
	{"Y", "2006"},
	{"m", "01"},
	{"d", "02"},
	{"H", "15"},
	{"i", "04"},
	{"s", "05"},
}

// phpDateFormat converts the PHP date() tokens of a {date:...} pattern.
func phpDateFormat(pattern string) string {
	result := pattern
	for _, replacement := range phpDateReplacements {
		result = strings.ReplaceAll(result, replacement.php, replacement.goFmt)
	}
	return result
}

// asciiDomain converts a host name to punycode, like SnappyMail\IDN::toAscii().
func asciiDomain(host string) string {
	host = trimValue(host)
	if "" == host {
		return ""
	}
	// EmailToASCII only converts the part after the last "@", so prefixing an
	// empty local part converts the whole host.
	return strings.TrimPrefix(domain.EmailToASCII("@"+host), "@")
}

// trimValue is MailSo\Base\Utils::Trim(): trim, drop leading/trailing control
// characters, trim again.
func trimValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimFunc(value, func(character rune) bool {
		return 0x1F >= character && 0x00 <= character
	})
	return strings.TrimSpace(value)
}

// firstNonEmpty is PHP's "$value ?: $fallback" for strings.
func firstNonEmpty(value, fallback string) string {
	if "" != value {
		return value
	}
	return fallback
}

// valueString is PHP's (string) cast for decoded JSON values.
func valueString(value any) string {
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
	}
	return ""
}

// valueInt is PHP's (int) cast for decoded JSON values.
func valueInt(value any) int64 {
	switch typed := value.(type) {
	case string:
		// (int) "5" is 5, (int) "5abc" is 5, (int) "abc" is 0.
		digits := 0
		for digits < len(typed) && '0' <= typed[digits] && '9' >= typed[digits] {
			digits++
		}
		number, err := strconv.ParseInt(typed[:digits], 10, 64)
		if nil != err {
			return 0
		}
		return number
	case bool:
		if typed {
			return 1
		}
		return 0
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	}
	return 0
}
