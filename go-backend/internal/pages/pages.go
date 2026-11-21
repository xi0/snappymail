// Package pages renders the application shell: app/templates/Index.html (with
// its compiled view templates, boot CSS/JS, language, theme and CSP nonce) and
// app/templates/Error.html.
//
// It is the port of the index half of RainLoop\Service::RunResult() plus
// ServiceActions::compileTemplates(). The per-request webmail state
// (GetLanguage/GetTheme/compileCss/compileLanguage/StaticI18N) is injected
// through the Actions interface, so the package has no dependency on
// internal/service or internal/config.
package pages

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// Config is the application.ini surface of the index page.
//
// *config.Application satisfies it.
type Config interface {
	GetString(section, key, defaultValue string) string
	GetBool(section, key string, defaultValue bool) bool
}

// Cache is the MailSo\Cache\CacheClient subset the TMPL: entry uses.
//
// *storage.Cache satisfies it.
type Cache interface {
	Get(key string) (string, bool)
	Set(key, value string) error
}

// Plugins is the Plugins\Manager surface the index cache key needs.
//
// *plugin.Manager satisfies it (step 28).
type Plugins interface {
	Hash() string
}

// TemplatePlugins is the optional CompileTemplate part of Plugins\Manager.
type TemplatePlugins interface {
	CompileTemplate(templates *Templates, admin bool)
}

// Actions is the per-request webmail state the index page reads. It mirrors the
// Actions\Localization / Actions\Themes methods with the admin flag and the
// session already bound.
type Actions interface {
	// Language is Actions\Localization::GetLanguage().
	Language() string
	// Theme is Actions\Themes::GetTheme().
	Theme() string
	// CompileCSS is Actions\Themes::compileCss().
	CompileCSS(theme string) (string, error)
	// CompileLanguage is Actions\Localization::compileLanguage().
	CompileLanguage(language string) (string, error)
	// StaticI18N is Actions\Localization::StaticI18N().
	StaticI18N(key string) string
	// LoggedIn is Actions::getAccountFromToken(false) != null.
	LoggedIn() bool
}

// ActionsFactory builds the Actions of one request; admin is route.Admin.
type ActionsFactory func(w http.ResponseWriter, r *http.Request, admin bool) Actions

// Options is the installation-level state of the renderer. The zero value is
// usable: it reports the defaults of an untouched application.ini.
type Options struct {
	// Config is configs/application.ini.
	Config Config
	// Version is APP_VERSION.
	Version string
	// Paths is the install tree URL helpers.
	Paths httpx.WebPaths
	// DataFolder is APP_DATA_FOLDER_PATH (the writable check of the index
	// page). Empty skips the check.
	DataFolder string
	// Cache is Actions::Cacher() for the TMPL: entry. A nil cache renders
	// every request without caching.
	Cache Cache
	// Plugins is Plugins() (step 28).
	Plugins Plugins
	// Actions builds the per-request webmail state. A nil factory renders the
	// guest defaults.
	Actions ActionsFactory
	// SetCSP re-emits the Content-Security-Policy with the script nonce. A nil
	// func leaves the CSP set by the router without the nonce.
	SetCSP func(w http.ResponseWriter, nonce string)
}

// Renderer renders the Index/Error templates for one installation.
type Renderer struct {
	options Options
}

// New builds the renderer.
func New(options Options) *Renderer {
	return &Renderer{options: options}
}

// Index is Service::RunResult()'s index branch. It satisfies web.IndexFunc.
func (r *Renderer) Index(w http.ResponseWriter, request *http.Request, route *web.Route) {
	admin := nil != route && route.Admin

	var actions Actions
	if nil != r.options.Actions {
		actions = r.options.Actions(w, request, admin)
	}

	// labs.custom_login_link: a guest is redirected to the configured login.
	if !admin {
		if link := strings.TrimSpace(r.config().GetString("labs", "custom_login_link", "")); "" != link {
			if nil == actions || !actions.LoggedIn() {
				httpx.Location(w, link, http.StatusFound)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// The data folder must exist and be writable; PHP echoes the error
	// template without changing the status code.
	if "" != r.options.DataFolder && !writableDirectory(r.options.DataFolder) {
		_, _ = w.Write([]byte(r.ErrorTemplate(actions,
			"Permission denied!",
			"SnappyMail can not access the data folder \""+r.options.DataFolder+"\"",
			true,
		)))
		return
	}

	result, err := r.render(w, actions, admin, request)
	if nil != err {
		_, _ = w.Write([]byte(r.ErrorTemplate(actions, "An error occurred", err.Error(), true)))
		return
	}

	_, _ = w.Write([]byte(result))
}

// render builds the Index.html document (cached by the TMPL: key).
func (r *Renderer) render(w http.ResponseWriter, actions Actions, admin bool, request *http.Request) (string, error) {
	config := r.config()

	language := "en"
	theme := ""
	if nil != actions {
		language = actions.Language()
		theme = actions.Theme()
	}

	// debug.enable forces the unminified assets (debug.javascript / debug.css
	// select them per asset type). The ".min" suffix is part of the asset file
	// names and of the TMPL: cache key.
	appDebug := config.GetBool("debug", "enable", false)
	appJsMin := ""
	if !appDebug && !config.GetBool("debug", "javascript", false) {
		appJsMin = ".min"
	}
	appCssMin := ""
	if !appDebug && !config.GetBool("debug", "css", false) {
		appCssMin = ".min"
	}

	faviconURL := config.GetString("webmail", "favicon_url", "")
	faviconPng := faviconURL
	appleTouch := ""
	if "" == faviconPng {
		faviconPng = r.options.Paths.StaticPath("favicon.png")
		appleTouch = faviconPng
	}

	// The map the TMPL: cache key is built from (the first six parameters of
	// Service::RunResult()).
	static := map[string]string{
		"{{BaseAppThemeName}}":           theme,
		"{{BaseAppManifestLink}}":        r.options.Paths.StaticPath("manifest.json"),
		"{{LoadingDescriptionEsc}}":      escapeHTML(config.GetString("webmail", "loading_description", "SnappyMail")),
		"{{BaseAppAdmin}}":               boolDigit(admin),
		"{{BaseAppFaviconPngLinkTag}}":   faviconLinkTag("shortcut icon", faviconPng),
		"{{BaseAppFaviconTouchLinkTag}}": faviconLinkTag("apple-touch-icon", appleTouch),
	}

	cacheKey := r.cacheKey(language, appJsMin, appCssMin, static, admin)

	result := ""
	if config.GetBool("cache", "system_data", true) && nil != r.options.Cache {
		result, _ = r.options.Cache.Get(cacheKey)
	}

	if "" != result {
		result += "<!--cached-->"
	} else {
		params := make(map[string]string, len(static)+12)
		for key, value := range static {
			params[key] = value
		}

		bootCss, _ := r.readStatic("css/boot" + appCssMin + ".css")
		bootScript, _ := r.readStatic("js" + minFolder(appJsMin) + "/boot" + appJsMin + ".js")

		themeCss := ""
		languageJs := ""
		templates := ""
		if nil != actions {
			if css, cssErr := actions.CompileCSS(theme); nil == cssErr {
				themeCss = minifyCSS(css)
			}
			if js, jsErr := actions.CompileLanguage(language); nil == jsErr {
				languageJs = js
			}
			templates = ClearHTMLOutput(r.compileTemplates(admin))
		}

		area := "app"
		if admin {
			area = "admin"
		}

		params["{{BaseAppBootCss}}"] = bootCss
		params["{{BaseAppBootScript}}"] = bootScript
		params["{{BaseAppMainCssLink}}"] = r.options.Paths.StaticPath("css/" + area + appCssMin + ".css")
		params["{{BaseAppThemeCss}}"] = themeCss
		params["{{BaseLanguage}}"] = languageJs
		params["{{BaseTemplates}}"] = templates
		params["{{NO_SCRIPT_DESC}}"] = nl2br(staticI18N(actions, "NO_SCRIPT_TITLE") + "\n" + staticI18N(actions, "NO_SCRIPT_DESC"))
		params["{{NO_COOKIE_TITLE}}"] = staticI18N(actions, "NO_COOKIE_TITLE")
		params["{{NO_COOKIE_DESC}}"] = staticI18N(actions, "NO_COOKIE_DESC")
		params["{{BAD_BROWSER_TITLE}}"] = staticI18N(actions, "BAD_BROWSER_TITLE")
		params["{{BAD_BROWSER_DESC}}"] = nl2br(staticI18N(actions, "BAD_BROWSER_DESC"))

		template, err := r.readVersionFile("app/templates/Index.html")
		if nil != err {
			// A missing template must not take the whole page down; the PHP
			// file_get_contents() would emit a warning and an empty page. The
			// Go backend keeps a minimal shell so the frontend can report the
			// broken installation.
			template = fallbackIndexTemplate
		}

		result = replacePlaceholders(ClearHTMLOutput(template), params)

		if "" != cacheKey && nil != r.options.Cache {
			_ = r.options.Cache.Set(cacheKey, result)
		}
	}

	// security.cookie_samesite / secure (boot.js contains samesite=strict).
	sameSite := strings.ToLower(config.GetString("security", "cookie_samesite", "Strict"))
	secure := ""
	if httpx.IsSecure(request) || "none" == sameSite {
		secure = ";secure"
	}
	result = strings.ReplaceAll(result, "samesite=strict", "samesite="+sameSite+secure)

	// CSP nonce: every nonce="" placeholder gets a fresh random UUID and the
	// header is re-emitted with a nonce- source.
	nonce := newUUID()
	result = strings.ReplaceAll(result, `nonce=""`, `nonce="`+nonce+`"`)
	if nil != r.options.SetCSP {
		r.options.SetCSP(w, nonce)
	}

	return result, nil
}

// cacheKey is the 'TMPL:' . sha1(jsonEncode([...])) key of the index page.
func (r *Renderer) cacheKey(language, appJsMin, appCssMin string, static map[string]string, admin bool) string {
	pluginHash := ""
	if nil != r.options.Plugins {
		pluginHash = r.options.Plugins.Hash()
	}

	payload := []any{
		language,
		r.config().GetString("cache", "index", ""),
		pluginHash,
		appJsMin,
		appCssMin,
		static,
		r.options.Version,
		r.staticResourcesVersion(admin, appCssMin, appJsMin),
	}

	encoded, err := json.Marshal(payload)
	if nil != err {
		return ""
	}

	return "TMPL:" + sha1Hex(string(encoded))
}

// staticResourcesVersion is staticResourcesVersion(): a stamp of the templates
// and the selected boot assets, so editing them invalidates the cached page.
func (r *Renderer) staticResourcesVersion(admin bool, appCssMin, appJsMin string) string {
	root := r.versionRoot()

	files, _ := filepath.Glob(filepath.Join(root, "app", "templates", "*.html"))
	views, _ := filepath.Glob(filepath.Join(root, "app", "templates", "Views", "*", "*.html"))
	files = append(files, views...)

	area := "app"
	if admin {
		area = "admin"
	}
	files = append(files,
		filepath.Join(root, "static", "css", area+appCssMin+".css"),
		filepath.Join(root, "static", "css", "boot"+appCssMin+".css"),
		filepath.Join(root, "static", "js", minFolder(appJsMin), "boot"+appJsMin+".js"),
	)

	var parts []string
	for _, file := range files {
		info, err := os.Stat(file)
		if nil != err || info.IsDir() {
			continue
		}
		parts = append(parts, file+":"+strconv.FormatInt(info.ModTime().Unix(), 10))
	}

	return sha1Hex(strings.Join(parts, "|"))
}

// staticI18N calls Actions.StaticI18N(), tolerating a nil factory.
func staticI18N(actions Actions, key string) string {
	if nil == actions {
		return key
	}
	return actions.StaticI18N(key)
}

// readStatic reads a static asset below APP_VERSION_ROOT_PATH/static.
func (r *Renderer) readStatic(relative string) (string, error) {
	return r.readVersionFile("static/" + relative)
}

// readVersionFile reads a file below APP_VERSION_ROOT_PATH.
func (r *Renderer) readVersionFile(relative string) (string, error) {
	content, err := os.ReadFile(filepath.Join(r.versionRoot(), filepath.FromSlash(relative)))
	if nil != err {
		return "", err
	}
	return string(content), nil
}

// versionRoot is APP_VERSION_ROOT_PATH without a trailing separator.
func (r *Renderer) versionRoot() string {
	return strings.TrimRight(r.options.Paths.VersionRoot, `/\`)
}

// config returns the configuration, or an empty one when it is not wired.
func (r *Renderer) config() Config {
	if nil == r.options.Config {
		return emptyConfig{}
	}
	return r.options.Config
}

// emptyConfig reports the defaults of an untouched application.ini.
type emptyConfig struct{}

// GetString returns defaultValue.
func (emptyConfig) GetString(_, _, defaultValue string) string { return defaultValue }

// GetBool returns defaultValue.
func (emptyConfig) GetBool(_, _ string, defaultValue bool) bool { return defaultValue }

// minFolder is the "/min" segment of the non-debug boot script path.
func minFolder(appJsMin string) string {
	if "" != appJsMin {
		return "/min"
	}
	return ""
}

// boolDigit renders a bool like the PHP 1/0 of the template parameter.
func boolDigit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// faviconLinkTag builds the <link> tag for a favicon URL ("" when absent).
func faviconLinkTag(rel, href string) string {
	if "" == href {
		return ""
	}
	return `<link type="image/png" rel="` + rel + `" href="` + href + `">`
}

// replacePlaceholders is PHP's strtr($template, $parameters).
func replacePlaceholders(template string, params map[string]string) string {
	pairs := make([]string, 0, len(params)*2)
	for key, value := range params {
		pairs = append(pairs, key, value)
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

// minifyCSSPattern is the '/\s*([:;{},]+)\s*/s' of the inline theme style.
var minifyCSSPattern = regexp.MustCompile(`\s*([:;{},]+)\s*`)

// minifyCSS collapses the whitespace around the CSS separators.
func minifyCSS(css string) string {
	return minifyCSSPattern.ReplaceAllString(css, "$1")
}

// sha1Hex is PHP's sha1().
func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// writableDirectory reports whether path is a writable directory (PHP's
// is_dir() && is_writable()).
func writableDirectory(path string) bool {
	info, err := os.Stat(path)
	if nil != err || !info.IsDir() {
		return false
	}
	return 0 != info.Mode().Perm()&0o222
}

// fallbackIndexTemplate is served when app/templates/Index.html is missing.
const fallbackIndexTemplate = `<!DOCTYPE html>
<html class="rl-started-trigger">
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width">
	<title>SnappyMail</title>
	<script nonce="">
	var SnappyMailBoot = 1;
	</script>
	<style id="app-boot-css">{{BaseAppBootCss}}</style>
	<link type="text/css" rel="stylesheet" href="{{BaseAppMainCssLink}}">
	<style id="app-theme-style" data-name="{{BaseAppThemeName}}">{{BaseAppThemeCss}}</style>
</head>
<body id="rl-app" data-admin='{{BaseAppAdmin}}' spellcheck="false">
	<div id="rl-loading">
		<div id="rl-loading-desc">{{LoadingDescriptionEsc}}</div>
		<noscript><div class="error-desc">{{NO_SCRIPT_DESC}}</div></noscript>
	</div>
	<div id="rl-content" hidden="">
		<div id="rl-left"></div>
		<div id="rl-right"></div>
	</div>
	<div id="rl-popups"></div>
	{{BaseTemplates}}
	<script nonce="" type="text/javascript">{{BaseAppBootScript}}{{BaseLanguage}}</script>
</body>
</html>
`
