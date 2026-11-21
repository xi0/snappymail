package service

// The three small endpoints of step 13a, which need no new infrastructure:
//
//	GET  ?/Mailto&to=mailto:…   ServiceActions::ServiceMailto()
//	GET  ?/Sso&hash=…           ServiceActions::ServiceSso()
//	POST ?/CspReport            ServiceActions::ServiceCspReport()
//
// ServiceMailto() puts the "mailto:" URL into the single-use smmailtoauth cookie
// (Actions::AUTH_MAILTO_TOKEN_KEY), which AppData() reports once as "mailToEmail"
// and then drops (appdata_user.go: mailTo). ServiceSso() logs in with the
// single-use hash of \RainLoop\API::CreateUserSsoHash() through the existing
// auth.DoSso() (internal/auth/sso.go) and ServiceCspReport() records a
// Content-Security-Policy violation report through the logx logger
// (SnappyMail\HTTP\CSP::logReport(): a 204, the raw body logged unless it is
// empty or reports the bare 'moz-extension' source).
//
// PHP swallows every failure of the first two and always redirects to "./", so
// the handlers report the request as handled in every case.
//
// The URL shapes look odd but are PHP's: Service::RunResult() splits the query
// string on "&" and "/" only, so a "?" inside it stays part of the path vector.
// The frontend therefore registers the protocol handler as "?mailto&to=%s"
// (app.js registerMailto()) and the bundled login-external-sso plugin documents
// "?Sso&hash="; a literal "?/Sso?hash=…" would address the index page in PHP as
// well. The parameters are read through queryParam()/requestParam(), so both the
// "?/Mailto&to=…" and the "?mailto&to=…" spelling reach the endpoints.

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// cspLogName is the log category of the CSP reports: SnappyMail\Log::error()
// with the "CSP" prefix, which Logger()->Write() receives as the name.
const cspLogName = "CSP"

// mailtoPattern is the '/^mailto:/i' test of ServiceMailto().
var mailtoPattern = regexp.MustCompile(`(?i)^mailto:`)

// queryParam is $_GET[$name] as a string.
//
// A repeated or array-shaped parameter would be an array in PHP (and a fatal
// trim() TypeError for ServiceMailto()), so only the first value is used.
func queryParam(r *http.Request, name string) string {
	if nil == r || nil == r.URL {
		return ""
	}
	return r.URL.Query().Get(name)
}

// requestParam is $_REQUEST[$name]: the query string first, then the POST body,
// which wins with PHP's default request_order = "GP".
func requestParam(r *http.Request, name string) string {
	value := queryParam(r, name)

	if nil != r && httpx.IsPost(r) {
		if err := r.ParseForm(); nil == err {
			if values, ok := r.PostForm[name]; ok && 0 < len(values) {
				value = values[0]
			}
		}
	}

	return value
}

// requestBody is php://input; a nil body reads as empty.
func requestBody(r *http.Request) io.Reader {
	if nil == r || nil == r.Body {
		return strings.NewReader("")
	}
	return r.Body
}

// microtime is PHP's microtime(true): the current time as a float.
func microtime() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Mailto is ServiceActions::ServiceMailto(): hand a "mailto:" URL over to the
// webmail client through the single-use smmailtoauth cookie.
func (a *AppData) Mailto(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	httpx.NoStore(w)

	// PHP: $sTo = \trim($_GET['to'] ?? '');
	if to := strings.TrimSpace(queryParam(r, "to")); mailtoPattern.MatchString(to) {
		a.setMailtoCookie(w, r, to)
	}

	// \MailSo\Base\Http::Location('./');
	httpx.Location(w, "./", http.StatusFound)

	return true
}

// setMailtoCookie is the Cookies::set() call of ServiceMailto(): the timestamp
// and the mailto URL sealed with Utils::EncodeKeyValuesQ() into a session
// cookie (no expiry, HttpOnly, like the PHP default arguments).
//
// A request without a cookie stack (no session factory, no crypter) simply drops
// the cookie, which is what an unwritable cookie jar amounts to.
func (a *AppData) setMailtoCookie(w http.ResponseWriter, r *http.Request, to string) {
	session := a.session(w, r)
	if nil == session || nil == session.Jar || nil == session.Authenticator {
		return
	}

	encoded, err := session.Authenticator.EncodeKeyValuesQ(map[string]any{
		"Time":   microtime(),
		"MailTo": "MailTo",
		"To":     to,
	}, "")
	if nil != err {
		return
	}

	session.Jar.Set(w, auth.AuthMailtoTokenKey, encoded, time.Time{}, true)
}

// Sso is ServiceActions::ServiceSso(): log in with the
// \RainLoop\API::CreateUserSsoHash() generated hash and redirect to "./".
//
// auth.DoSso() already reads the /Sso/Data/<hash>/Login/ cache entry, drops it
// (single use), decrypts it, logs in and applies AdditionalOptions.language, so
// this is the redirect plus the session wiring around it.
func (a *AppData) Sso(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	httpx.NoStore(w)

	// PHP: $sSsoHash = $_REQUEST['hash'] ?? ''; if (!empty($sSsoHash)) …
	if hash := requestParam(r, "hash"); !phpEmpty(hash) {
		if login := a.newLogin(w, r); nil != login {
			// PHP catches every Throwable of LoginProcess() and still redirects.
			_ = login.DoSso(hash)
		}
	}

	// \MailSo\Base\Http::Location('./');
	httpx.Location(w, "./", http.StatusFound)

	return true
}

// newLogin builds the login flow (step 8) of the request on top of the request
// session, so ServiceSso() reuses exactly the cookie/account stack of the
// AppData and Json endpoints.
//
// The result is nil without a session: there is no account to log in to and no
// cookie jar to write the login cookies to.
func (a *AppData) newLogin(w http.ResponseWriter, r *http.Request) *auth.Login {
	session := a.session(w, r)
	if nil == session || nil == session.Authenticator {
		return nil
	}

	authenticator := session.Authenticator

	// Actions::SettingsProvider(): DoSso() persists AdditionalOptions.language.
	// The provider only has to be a SettingsStore; a Load-only provider leaves
	// the language unpersisted, like a missing settings provider in PHP.
	settings, _ := a.options.Settings.(auth.SettingsStore)

	// A typed-nil *logx.Logger would be a non-nil auth.Logger and panic on the
	// first log line, so it is only set when usable.
	var logger auth.Logger
	if nil != a.options.Logger {
		logger = a.options.Logger
	}

	host := ""
	if nil != r {
		host = r.Host
	}

	return &auth.Login{
		Context:          authenticator.Context,
		Session:          authenticator,
		Config:           a.config(),
		Logger:           logger,
		Host:             httpx.Host(r, false, true),
		HTTPHost:         host,
		Settings:         settings,
		ValidateLanguage: func(language string) string { return a.validateLanguage(language, "", false) },
		Cache:            a.options.Cache,
		ClientIP:         httpx.ClientIP(r,
			a.config().GetBool("labs", "http_client_ip_check_proxy", false)),
	}
}

// CspReport is ServiceActions::ServiceCspReport() /
// SnappyMail\HTTP\CSP::logReport(): reply 204 and record the violation report.
func (a *AppData) CspReport(w http.ResponseWriter, r *http.Request, _ *web.Route) bool {
	body, _ := io.ReadAll(requestBody(r))

	var report any
	_ = json.Unmarshal(body, &report)

	w.WriteHeader(http.StatusNoContent)

	// PHP: if ($json && $report && 'moz-extension' !== $report['csp-report']
	//           ['source-file']) { \SnappyMail\Log::error('CSP', $json); }
	// A report without "csp-report"/"source-file" is logged, like the undefined
	// array key comparison of PHP evaluates to true.
	if !phpEmpty(string(body)) && !phpEmpty(report) && !cspReportFromExtension(report) {
		if nil != a.options.Logger {
			a.options.Logger.Write(string(body), logx.Error, cspLogName)
		}
	}

	return true
}

// cspReportFromExtension is the 'moz-extension' === source-file test of
// logReport(): a report from a browser extension has no useful source, so it is
// not logged.
//
// PHP compares the whole source-file against the bare string 'moz-extension',
// which is what Firefox sends for an extension injected script; a full
// "moz-extension://<uuid>/content.js" URL compares unequal and is logged. The
// comparison is kept exactly as-is instead of being turned into a prefix match.
func cspReportFromExtension(report any) bool {
	document, ok := report.(map[string]any)
	if !ok {
		return false
	}

	details, ok := document["csp-report"].(map[string]any)
	if !ok {
		return false
	}

	source, _ := details["source-file"].(string)

	return "moz-extension" == source
}
