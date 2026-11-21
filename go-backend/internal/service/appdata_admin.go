package service

// The admin state of AppData: ActionsAdmin::AdminAppData().

import (
	"os"
	"path/filepath"
)

// adminAppData is ActionsAdmin::AdminAppData(): the "Admin" block, the admin
// session state and — for a logged in administrator — the settings the admin
// UI edits.
func (a *AppData) adminAppData(result map[string]any, session *Session) {
	config := a.config()

	key := config.GetString("admin_panel", "key", "")
	if "" == key {
		key = "admin"
	}

	admin := map[string]any{
		"host":    "" != config.GetString("admin_panel", "host", ""),
		"path":    key,
		"allowed": config.GetBool("security", "allow_admin_panel", true),
	}
	result["Admin"] = admin

	// PHP: $aResult['Auth'] = $oActions->IsAdminLoggined(false);
	result["Auth"] = a.adminLoggedIn(session)

	if !phpBool(result["Auth"]) {
		// The password bootstrap of the not-logged-in branch (write
		// admin_password.txt, generate and store the hash) belongs to the admin
		// core port (step 27); the response shape is identical without it.
		return
	}

	result["adminLogin"] = config.GetString("security", "admin_login", "")
	result["adminTOTP"] = config.GetString("security", "admin_totp", "")
	result["pluginsEnable"] = config.GetBool("plugins", "enable", false)

	result["loginDefaultDomain"] = config.GetString("login", "default_domain", "")
	result["determineUserLanguage"] = config.GetBool("login", "determine_user_language", true)
	result["determineUserDomain"] = config.GetBool("login", "determine_user_domain", false)

	result["supportedPdoDrivers"] = a.pdoDrivers()

	result["contactsEnable"] = config.GetBool("contacts", "enable", false)
	result["contactsSync"] = config.GetBool("contacts", "allow_sync", false)
	// PHP assigns contactsPdoType twice (validPdoType() and then the raw
	// configuration value), so the raw value wins.
	result["contactsPdoType"] = config.GetString("contacts", "type", "")
	result["contactsPdoDsn"] = config.GetString("contacts", "pdo_dsn", "")
	result["contactsPdoUser"] = config.GetString("contacts", "pdo_user", "")
	result["contactsPdoPassword"] = dummy
	result["contactsMySQLSSLCA"] = config.GetString("contacts", "mysql_ssl_ca", "")
	result["contactsMySQLSSLVerify"] = config.GetBool("contacts", "mysql_ssl_verify", true)
	result["contactsMySQLSSLCiphers"] = config.GetString("contacts", "mysql_ssl_ciphers", "")
	result["contactsSQLiteGlobal"] = config.GetBool("contacts", "sqlite_global", false)
	result["contactsSuggestionsLimit"] = config.GetInt("contacts", "suggestions_limit", 20)

	result["faviconUrl"] = config.GetString("webmail", "favicon_url", "")

	result["weakPassword"] = a.weakAdminPassword()

	admin["language"] = a.validateLanguage(
		config.GetString("admin_panel", "language", "en"), "", true)
	admin["languages"] = a.languages.Languages(true)
	admin["clientLanguage"] = a.validateLanguageEmpty(
		a.detectClientLanguage(true, acceptLanguage(session)), "", true)

	// PHP: PGP\GnuPG::getInstance('')->getEngineInfo()['version'], null when
	// the binary is unavailable.
	if "" != a.options.GnuPGVersion {
		result["gnupg"] = a.options.GnuPGVersion
	} else {
		result["gnupg"] = nil
	}
}

// adminLoggedIn is Actions\Admin::IsAdminLoggined(false).
func (a *AppData) adminLoggedIn(session *Session) bool {
	if nil != a.options.Admin {
		// throw=false: an invalid session is "not logged in", not an error.
		if loggedIn, err := a.options.Admin.IsLoggedIn(false); nil == err {
			return loggedIn
		}
		return false
	}
	if nil != session {
		return session.AdminLoggedIn
	}
	return false
}

// pdoDrivers is Pdo\Base::getAvailableDrivers(): always an array.
func (a *AppData) pdoDrivers() []string {
	if nil == a.options.PdoDrivers {
		return []string{}
	}
	return a.options.PdoDrivers
}

// weakAdminPassword is is_file(APP_PRIVATE_DATA . 'admin_password.txt'): the
// installer stores the generated password there until it is changed.
func (a *AppData) weakAdminPassword() bool {
	if "" == a.options.PrivateData {
		return false
	}

	info, err := os.Stat(filepath.Join(a.options.PrivateData, "admin_password.txt"))
	return nil == err && info.Mode().IsRegular()
}
