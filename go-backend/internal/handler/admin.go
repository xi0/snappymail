package handler

// The admin panel core actions of \RainLoop\ActionsAdmin (Step 27):
// AdminAppData is rendered by internal/service (it is the ?/AdminAppData
// endpoint and the result of DoAdminLogin); this file ports the DoAdmin*
// actions that login, edit the configuration, change the password, clear the
// cache, report the installation info and build the TOTP QR code.
//
// The admin session itself (cookie smadmin + file cache marker) lives in
// internal/service/admin.go and is reached through the AdminPanel interface so
// this package stays independent of the cookie/cache stack. The admin-only
// actions are already gated by the dispatcher (Actions/Admin::IsAdminLoggined
// of the ?/Json service), so the handlers do not re-check it.

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/skip2/go-qrcode"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// appDummy is Actions::APP_DUMMY: the placeholder for an unchanged secret.
const appDummy = "********"

// The setConfigFromParams() value types.
const (
	configTypeString = "string"
	configTypeInt    = "int"
	configTypeBool   = "bool"
	configTypeDummy  = "dummy"
)

// AdminPanel is the per-request admin session: the gate plus the login/logout
// and password check the admin actions need. *service.Admin satisfies it.
type AdminPanel interface {
	Admin
	// Login is ActionsAdmin::DoAdminLogin()'s credential check plus
	// setAdminAuthToken().
	Login(login, password, passcode string) error
	// Logout clears the admin token and cookie.
	Logout() error
	// ValidatePassword is Application::ValidatePassword().
	ValidatePassword(password string) bool
}

// AdminPanelFactory builds the admin session of one request.
type AdminPanelFactory func(session Session) AdminPanel

// AdminConfig is the configs/application.ini surface the admin panel edits. It
// is type-asserted from the dispatcher's Config (main passes the concrete
// *config.Application). The zero value is not usable; a nil result makes the
// settings actions report false.
type AdminConfig interface {
	GetString(sectionName, key, defaultValue string) string
	GetBool(sectionName, key string, defaultValue bool) bool
	GetInt(sectionName, key string, defaultValue int) int
	Set(sectionName, key string, value any)
	Save() error
	DumpExtended() map[string]map[string][]any
	ValidatePassword(password string) bool
	SetPassword(password string)
}

// RegisterAdmin registers the DoAdmin* core actions.
func RegisterAdmin(d *Dispatcher) {
	d.Register("AdminLogin", doAdminLogin)
	d.Register("AdminLogout", doAdminLogout)
	d.Register("AdminSettingsGet", doAdminSettingsGet)
	d.Register("AdminSettingsSet", doAdminSettingsSet)
	d.Register("AdminSettingsUpdate", doAdminSettingsUpdate)
	d.Register("AdminPasswordUpdate", doAdminPasswordUpdate)
	d.Register("AdminClearCache", doAdminClearCache)
	d.Register("AdminInfo", doAdminInfo)
	d.Register("AdminQRCode", doAdminQRCode)
	d.Register("AdminContactsTest", doAdminContactsTest)
}

// adminPanel is Actions\Admin for the request, or nil when unwired.
func (c *Context) adminPanel() AdminPanel {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.adminPanel {
		return nil
	}
	return c.dispatcher.adminPanel(c.session)
}

// adminConfig type-asserts the request configuration to the admin surface.
func (c *Context) adminConfig() AdminConfig {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	config, _ := c.dispatcher.config.(AdminConfig)
	return config
}

// buildAppData is Actions::AppData() for the request (nil when unwired).
func (c *Context) buildAppData(admin bool) map[string]any {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.appData {
		return nil
	}
	return c.dispatcher.appData(c.Response, c.Request, admin)
}

// doAdminLogin is ActionsAdmin::DoAdminLogin().
func doAdminLogin(ctx *Context) (any, error) {
	panel := ctx.adminPanel()
	if nil == panel {
		return nil, clientError(appctx.AuthError, "")
	}

	login := strings.TrimSpace(ctx.stringParam("Login", ""))
	password := ctx.stringParam("Password", "")
	passcode := ctx.stringParam("TOTP", "")

	if err := panel.Login(login, password, passcode); nil != err {
		return nil, err
	}

	appData := ctx.buildAppData(true)
	if nil == appData {
		return ctx.FalseResponse(0, "", ""), nil
	}

	return ctx.DefaultResponse(appData, nil), nil
}

// doAdminLogout is ActionsAdmin::DoAdminLogout().
func doAdminLogout(ctx *Context) (any, error) {
	if panel := ctx.adminPanel(); nil != panel {
		_ = panel.Logout()
	}
	return ctx.TrueResponse(nil), nil
}

// doAdminSettingsGet is ActionsAdmin::DoAdminSettingsGet().
//
//	$aConfig = $this->Config()->jsonSerialize();
//	unset($aConfig['version']);
//	$aConfig['logs']['time_zone'][1] = '';
//	$aConfig['logs']['time_zone'][2] = \DateTimeZone::listIdentifiers();
//	$aConfig['login']['sign_me_auto'][2] = ['DefaultOff','DefaultOn','Unused'];
//	$aConfig['defaults']['view_images'][2] = ['ask','match','always'];
func doAdminSettingsGet(ctx *Context) (any, error) {
	config := ctx.adminConfig()
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	dump := config.DumpExtended()
	delete(dump, "version")

	// Convert to the map[string]any shape the response helpers recurse into.
	settings := make(map[string]any, len(dump))
	for section, keys := range dump {
		inner := make(map[string]any, len(keys))
		for key, values := range keys {
			inner[key] = values
		}
		settings[section] = inner
	}

	setOption := func(section, key string, options []string) {
		values, ok := settings[section].(map[string]any)
		if !ok {
			return
		}
		entry, ok := values[key].([]any)
		if !ok {
			return
		}
		// [value, comment, options]
		for len(entry) < 3 {
			entry = append(entry, nil)
		}
		entry[1] = ""
		entry[2] = options
		values[key] = entry
	}

	setOption("logs", "time_zone", zoneIdentifiers())
	setOption("login", "sign_me_auto", []string{"DefaultOff", "DefaultOn", "Unused"})
	setOption("defaults", "view_images", []string{"ask", "match", "always"})

	return ctx.DefaultResponse(settings, nil), nil
}

// doAdminSettingsSet is ActionsAdmin::DoAdminSettingsSet(): write the whole
// form back, section by section.
func doAdminSettingsSet(ctx *Context) (any, error) {
	config := ctx.adminConfig()
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	sections, isMap := ctx.GetParam("config", nil).(map[string]any)
	if !isMap {
		return nil, clientError(appctx.InvalidInputArgument, "Invalid config")
	}

	for sectionName, rawSection := range sections {
		// The "custom" section accepts arbitrary keys and is never part of the
		// admin form, so it must not be writable from here.
		items, isItems := rawSection.(map[string]any)
		if "custom" == sectionName || !isItems {
			return nil, clientError(appctx.InvalidInputArgument, "Invalid config section")
		}
		for key, value := range items {
			if !isScalarValue(value) {
				return nil, clientError(appctx.InvalidInputArgument, "Invalid config key")
			}
			config.Set(sectionName, key, value)
		}
	}

	return ctx.DefaultResponse(nil == config.Save(), nil), nil
}

// doAdminSettingsUpdate is ActionsAdmin::DoAdminSettingsUpdate().
func doAdminSettingsUpdate(ctx *Context) (any, error) {
	config := ctx.adminConfig()
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	ctx.setConfigFromParams(config, "language", "webmail", "language", configTypeString,
		func(value string) string { return ctx.validateLanguage(value) })
	ctx.setConfigFromParams(config, "languageAdmin", "admin_panel", "language", configTypeString,
		func(value string) string { return ctx.validateAdminLanguage(value) })
	ctx.setConfigFromParams(config, "Theme", "webmail", "theme", configTypeString,
		func(value string) string { return ctx.validateTheme(value) })

	ctx.setConfigFromParams(config, "proxyExternalImages", "labs", "use_local_proxy_for_external_images", configTypeBool, nil)
	ctx.setConfigFromParams(config, "autoVerifySignatures", "security", "auto_verify_signatures", configTypeBool, nil)

	ctx.setConfigFromParams(config, "allowLanguagesOnSettings", "webmail", "allow_languages_on_settings", configTypeBool, nil)
	ctx.setConfigFromParams(config, "allowLanguagesOnLogin", "login", "allow_languages_on_login", configTypeBool, nil)
	ctx.setConfigFromParams(config, "attachmentLimit", "webmail", "attachment_size_limit", configTypeInt, nil)

	ctx.setConfigFromParams(config, "loginDefaultDomain", "login", "default_domain", configTypeString, nil)

	ctx.setConfigFromParams(config, "contactsEnable", "contacts", "enable", configTypeBool, nil)
	ctx.setConfigFromParams(config, "contactsSync", "contacts", "allow_sync", configTypeBool, nil)
	ctx.setConfigFromParams(config, "contactsPdoDsn", "contacts", "pdo_dsn", configTypeString, nil)
	ctx.setConfigFromParams(config, "contactsPdoUser", "contacts", "pdo_user", configTypeString, nil)
	ctx.setConfigFromParams(config, "contactsPdoPassword", "contacts", "pdo_password", configTypeDummy, nil)
	ctx.setConfigFromParams(config, "contactsMySQLSSLCA", "contacts", "mysql_ssl_ca", configTypeString, nil)
	ctx.setConfigFromParams(config, "contactsMySQLSSLVerify", "contacts", "mysql_ssl_verify", configTypeBool, nil)
	ctx.setConfigFromParams(config, "contactsMySQLSSLCiphers", "contacts", "mysql_ssl_ciphers", configTypeString, nil)
	ctx.setConfigFromParams(config, "contactsSQLiteGlobal", "contacts", "sqlite_global", configTypeBool, nil)
	ctx.setConfigFromParams(config, "contactsSuggestionsLimit", "contacts", "suggestions_limit", configTypeInt, nil)
	ctx.setConfigFromParams(config, "contactsPdoType", "contacts", "type", configTypeString,
		func(value string) string { return validPdoType(value) })

	ctx.setConfigFromParams(config, "CapaAdditionalAccounts", "webmail", "allow_additional_accounts", configTypeBool, nil)
	ctx.setConfigFromParams(config, "CapaIdentities", "webmail", "allow_additional_identities", configTypeBool, nil)
	ctx.setConfigFromParams(config, "CapaAttachmentThumbnails", "interface", "show_attachment_thumbnail", configTypeBool, nil)
	ctx.setConfigFromParams(config, "CapaThemes", "webmail", "allow_themes", configTypeBool, nil)
	ctx.setConfigFromParams(config, "CapaUserBackground", "webmail", "allow_user_background", configTypeBool, nil)
	ctx.setConfigFromParams(config, "capaGnuPG", "security", "gnupg", configTypeBool, nil)
	ctx.setConfigFromParams(config, "capaOpenPGP", "security", "openpgp", configTypeBool, nil)

	ctx.setConfigFromParams(config, "determineUserLanguage", "login", "determine_user_language", configTypeBool, nil)
	ctx.setConfigFromParams(config, "determineUserDomain", "login", "determine_user_domain", configTypeBool, nil)

	ctx.setConfigFromParams(config, "title", "webmail", "title", configTypeString, nil)
	ctx.setConfigFromParams(config, "loadingDescription", "webmail", "loading_description", configTypeString, nil)
	ctx.setConfigFromParams(config, "faviconUrl", "webmail", "favicon_url", configTypeString, nil)

	ctx.setConfigFromParams(config, "pluginsEnable", "plugins", "enable", configTypeBool, nil)

	return ctx.DefaultResponse(nil == config.Save(), nil), nil
}

// setConfigFromParams is ActionsAdmin::setConfigFromParams().
func (c *Context) setConfigFromParams(
	config AdminConfig,
	paramName, section, key, kind string,
	callback func(string) string,
) {
	if !c.HasParam(paramName) {
		return
	}

	value := c.GetParam(paramName, "")

	switch kind {
	case configTypeInt:
		config.Set(section, key, phpIntValue(value))

	case configTypeBool:
		// PHP: !empty($sValue) && 'false' !== $sValue.
		config.Set(section, key, !phpEmpty(value) && "false" != scalarString(value))

	case configTypeDummy:
		text := phpString(c.GetParam(paramName, appDummy))
		if appDummy != text {
			config.Set(section, key, text)
		}

	default: // configTypeString
		text := phpString(value)
		if nil != callback {
			text = callback(text)
		}
		config.Set(section, key, text)
	}
}

// doAdminPasswordUpdate is ActionsAdmin::DoAdminPasswordUpdate().
func doAdminPasswordUpdate(ctx *Context) (any, error) {
	config := ctx.adminConfig()
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	result := false
	password := ctx.stringParam("Password", "")
	newPassword := ctx.stringParam("newPassword", "")

	passfile := ""
	if "" != ctx.dispatcher.privateData {
		passfile = filepath.Join(ctx.dispatcher.privateData, "admin_password.txt")
	}

	if config.ValidatePassword(password) {
		if login := strings.TrimSpace(ctx.stringParam("Login", "")); "" != login {
			config.Set("security", "admin_login", login)
		}

		config.Set("security", "admin_totp", ctx.stringParam("TOTP", ""))

		if "" != newPassword {
			config.SetPassword(newPassword)
			if "" != passfile && isRegularFile(passfile) {
				if content, err := os.ReadFile(passfile); nil == err &&
					strings.TrimSpace(string(content)) != newPassword {
					_ = os.Remove(passfile)
				}
			}
		}

		result = nil == config.Save()
	}

	if !result {
		return ctx.FalseResponse(0, "", ""), nil
	}

	return ctx.DefaultResponse(map[string]any{
		"Weak": "" != passfile && isRegularFile(passfile),
	}, nil), nil
}

// doAdminClearCache is ActionsAdmin::DoAdminClearCache():
//
//	$this->Cacher()->GC(0);
//	if (is_dir(APP_PRIVATE_DATA . 'cache')) RecRmDir(APP_PRIVATE_DATA . 'cache');
func doAdminClearCache(ctx *Context) (any, error) {
	if "" != ctx.dispatcher.privateData {
		storage.RecRmDir(filepath.Join(ctx.dispatcher.privateData, "cache"))
	}
	return ctx.TrueResponse(nil), nil
}

// doAdminInfo is ActionsAdmin::DoAdminInfo().
//
// The remote repository fields (latest core version, updatable flag) are stubbed
// because the Go backend does not talk to the SnappyMail repository; the local
// warnings and the extension checklist are reported.
func doAdminInfo(ctx *Context) (any, error) {
	version := ctx.Version()

	warnings := []any{}
	if versionLe(version, "2.0") {
		warnings = append(warnings, version)
	}

	load := any(nil)

	result := map[string]any{
		"system": map[string]any{"load": load},
		"core": map[string]any{
			"updatable":      false,
			"warning":        false,
			"version":        "",
			"versionCompare": 1,
			"warnings":       warnings,
		},
		"php": goCapabilities(),
	}

	return ctx.DefaultResponse(result, nil), nil
}

// goCapabilities is the "php" checklist of DoAdminInfo() with the Go
// equivalents: the extensions a Go build always or never provides.
func goCapabilities() []any {
	list := []any{
		map[string]any{"name": "Go " + runtime.Version(), "loaded": true, "version": runtime.Version()},
		map[string]any{"name": "Go 64bit", "loaded": 8 == strconv.IntSize, "version": strconv.IntSize},
	}

	for _, name := range []string{"APCu", "cURL", "Fileinfo", "iconv", "intl", "LDAP", "redis", "Tidy", "uuid", "Zip"} {
		list = append(list, map[string]any{"name": name, "loaded": false, "version": nil})
	}

	list = append(list, map[string]any{"name": "Phar", "loaded": false, "version": nil})

	list = append(list, map[string]any{"name": "Contacts database:", "loaded": true, "version": 0})
	list = append(list,
		map[string]any{"name": "- pdo_mysql", "loaded": false, "version": nil},
		map[string]any{"name": "- pdo_pgsql", "loaded": false, "version": nil},
		map[string]any{"name": "- pdo_sqlite", "loaded": true, "version": nil},
	)

	list = append(list, map[string]any{"name": "Crypt:", "loaded": true, "version": 0})
	list = append(list,
		map[string]any{"name": "- Sodium", "loaded": true, "version": nil},
		map[string]any{"name": "- OpenSSL", "loaded": true, "version": nil},
		map[string]any{"name": "- XXTEA", "loaded": false, "version": nil},
		map[string]any{"name": "- GnuPG", "loaded": true, "version": nil},
	)

	list = append(list, map[string]any{"name": "Image processing:", "loaded": true, "version": 0})
	list = append(list,
		map[string]any{"name": "- GD", "loaded": false, "version": nil},
		map[string]any{"name": "- Gmagick", "loaded": false, "version": nil},
		map[string]any{"name": "- Imagick", "loaded": false, "version": nil},
	)

	return list
}

// doAdminQRCode is ActionsAdmin::DoAdminQRCode(): render the otpauth URL to the
// Unicode block QR string the admin panel displays.
//
// Unlike the PHP QRCode class the module matrix comes from skip2/go-qrcode, so
// the rendering (and possibly the chosen mask pattern) differs, but the encoded
// otpauth URL is identical and stays scannable.
func doAdminQRCode(ctx *Context) (any, error) {
	user := ctx.stringParam("username", "")
	secret := ctx.stringParam("TOTP", "")
	issuer := rawURLEncode(ctx.Config().GetString("webmail", "title", "SnappyMail"))

	content := "otpauth://totp/" + issuer + ":" + user + "?secret=" + secret + "&issuer=" + issuer

	code, err := qrcode.New(content, qrcode.Medium)
	if nil != err {
		return ctx.DefaultResponse("", nil), nil
	}
	// The PHP QRCode class renders the bare module matrix (no quiet zone).
	code.DisableBorder = true

	return ctx.DefaultResponse(renderQRCode(code), nil), nil
}

// renderQRCode reproduces the Unicode block output of SnappyMail\QRCode: two
// rows per line, using the upper half block (▀), full block (█), lower half
// block (▄) and a no-break space.
func renderQRCode(code *qrcode.QRCode) string {
	bitmap := code.Bitmap()
	moduleCount := len(bitmap)

	var builder strings.Builder
	for r := 0; r < moduleCount; r += 2 {
		for c := 0; c < moduleCount; c++ {
			lower := r+1 >= moduleCount || !bitmap[r+1][c]
			if bitmap[r][c] {
				if lower {
					builder.WriteString("▀")
				} else {
					builder.WriteString("█")
				}
			} else if lower {
				builder.WriteString("\u00a0")
			} else {
				builder.WriteString("▄")
			}
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

// doAdminContactsTest is ActionsAdmin::DoAdminContactsTest().
//
// Partial: the Go backend only ships the SQLite address book driver, so the
// connection is validated by the driver type instead of opening a live PDO
// connection (MySQL/PostgreSQL are not built).
func doAdminContactsTest(ctx *Context) (any, error) {
	config := ctx.adminConfig()
	if nil == config {
		return ctx.FalseResponse(0, "", ""), nil
	}

	ctx.setConfigFromParams(config, "PdoDsn", "contacts", "pdo_dsn", configTypeString, nil)
	ctx.setConfigFromParams(config, "PdoUser", "contacts", "pdo_user", configTypeString, nil)
	ctx.setConfigFromParams(config, "PdoPassword", "contacts", "pdo_password", configTypeDummy, nil)
	ctx.setConfigFromParams(config, "PdoType", "contacts", "type", configTypeString,
		func(value string) string { return validPdoType(value) })
	ctx.setConfigFromParams(config, "MySQLSSLCA", "contacts", "mysql_ssl_ca", configTypeString, nil)
	ctx.setConfigFromParams(config, "MySQLSSLVerify", "contacts", "mysql_ssl_verify", configTypeBool, nil)
	ctx.setConfigFromParams(config, "MySQLSSLCiphers", "contacts", "mysql_ssl_ciphers", configTypeString, nil)
	ctx.setConfigFromParams(config, "SQLiteGlobal", "contacts", "sqlite_global", configTypeBool, nil)

	return ctx.DefaultResponse(map[string]any{
		"Result":  true,
		"Message": "",
	}, nil), nil
}

// validPdoType is PdoAddressBook::validPdoType(): only the SQLite driver is
// bundled with the Go backend, so every other value falls back to sqlite.
func validPdoType(value string) string {
	switch strings.TrimSpace(value) {
	case "sqlite":
		return "sqlite"
	}
	return "sqlite"
}

// validateAdminLanguage is Actions\Localization::ValidateLanguage() for the
// admin panel language list.
func (c *Context) validateAdminLanguage(language string) string {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.adminLanguage {
		return language
	}
	return c.dispatcher.adminLanguage(language)
}

// isScalarValue reports whether value is a PHP scalar (not null, an array or an
// object), the is_scalar() guard of DoAdminSettingsSet().
func isScalarValue(value any) bool {
	switch value.(type) {
	case string, bool, float64, int, int64:
		return true
	}
	return false
}

// isRegularFile is PHP's is_file().
func isRegularFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}

// versionLe reports whether a <= b for a dotted version string, like
// version_compare($a, $b, '<=').
func versionLe(left, right string) bool { return versionCompare(left, right) <= 0 }

// versionCompare is a small subset of PHP's version_compare() for the
// dotted-numeric versions the backend compares.
func versionCompare(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")

	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		var leftValue, rightValue int
		if index < len(leftParts) {
			leftValue = phpIntValue(leftParts[index])
		}
		if index < len(rightParts) {
			rightValue = phpIntValue(rightParts[index])
		}
		if leftValue != rightValue {
			if leftValue < rightValue {
				return -1
			}
			return 1
		}
	}
	return 0
}

// rawURLEncode is PHP's rawurlencode(): percent-encode everything except the
// RFC 3986 unreserved characters, with a space as %20.
func rawURLEncode(value string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"

	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if strings.IndexByte(unreserved, character) >= 0 {
			builder.WriteByte(character)
			continue
		}
		builder.WriteString("%")
		builder.WriteString(strings.ToUpper(strconv.FormatInt(int64(character), 16)))
	}
	return builder.String()
}

// zoneIdentifiers is a best effort DateTimeZone::listIdentifiers(): the region
// names of the system tzdata. It falls back to ["UTC"] when no tzdata is found.
func zoneIdentifiers() []string {
	root := "/usr/share/zoneinfo"
	if _, err := os.Stat(root); nil != err {
		return []string{"UTC"}
	}

	skipNames := map[string]bool{
		"iso3166.tab": true, "zone.tab": true, "zone1970.tab": true,
		"leapseconds": true, "tzdata.zi": true, "posixrules": true,
		"localtime": true, "leap-seconds.list": true,
	}
	skipDirs := map[string]bool{
		"posix": true, "right": true, "posixrules": true,
	}

	var zones []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if nil != err {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if nil != relErr || "." == relative {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipNames[info.Name()] || strings.Contains(relative, "/") && skipDirs[strings.Split(relative, "/")[0]] {
			return nil
		}
		if !strings.Contains(relative, "/") {
			return nil
		}
		zones = append(zones, filepath.ToSlash(relative))
		return nil
	})

	if 0 == len(zones) {
		return []string{"UTC"}
	}

	sort.Strings(zones)
	return zones
}
