package config

import "strings"

// defaultUploadMaxFilesize is PHP's built-in ini_get('upload_max_filesize'),
// used when the environment does not provide one.
const defaultUploadMaxFilesize = "2M"

// Defaults carries the parts of
// RainLoop\Config\Application::defaultValues() that are computed from the
// environment instead of being constant.
type Defaults struct {
	// UploadMaxFilesize is ini_get('upload_max_filesize') in php.ini notation
	// ("2M", "64M", "1G"). It only feeds the webmail.attachment_size_limit
	// default; an empty value uses PHP's built-in "2M".
	UploadMaxFilesize string
	// AddressBookExists is is_file(APP_PRIVATE_DATA . '/AddressBook.sqlite'),
	// the default of contacts.sqlite_global.
	AddressBookExists bool
}

// attachmentSizeLimit ports the ini_get('upload_max_filesize') arithmetic of
// defaultValues(): min(<upload_max_filesize in MB>, 25).
func (d Defaults) attachmentSizeLimit() float64 {
	value := strings.TrimSpace(d.UploadMaxFilesize)
	if "" == value {
		value = defaultUploadMaxFilesize
	}

	size := float64(phpInt(value))
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

	size = size / 1024 / 1024
	if 25 < size {
		return 25
	}
	return size
}

// sqliteGlobalDefault is \is_file(APP_PRIVATE_DATA . '/AddressBook.sqlite'), the
// default of contacts.sqlite_global.
func (d Defaults) sqliteGlobalDefault() any { return d.AddressBookExists }

// defaultValue is one entry of
// RainLoop\Config\Application::defaultValues(): the key, the default value and
// the documentation comment Save() writes above it.
type defaultValue struct {
	key    string
	option *option
}

// def builds one defaultValue. The optional comments are joined with a newline,
// the way PHP concatenates its multi line comment strings.
func def(key string, value any, comment ...string) defaultValue {
	return defaultValue{
		key:    key,
		option: &option{value: value, comment: strings.Join(comment, "\n")},
	}
}

// defaultSections ports Application::defaultValues(): every section, key, type
// and comment, in file order. The order matters, because Save() writes the
// sections and keys in exactly this order.
func defaultSections(d Defaults) []*section {
	return []*section{
		newSection("webmail",
			def("title", "SnappyMail Webmail", "Text displayed as page title"),
			def("loading_description", "SnappyMail", "Text displayed on startup"),
			def("favicon_url", ""),
			def("app_path", ""),
			def("theme", "Gazellemail", "Theme used by default"),
			def("allow_themes", true, "Allow theme selection on settings screen"),
			def("allow_user_background", false),
			def("language", "en", "Language used by default"),
			def("allow_languages_on_settings", true, "Allow language selection on settings screen"),
			def("allow_additional_accounts", true),
			def("allow_additional_identities", true),
			def("popup_identity", true, "When identity is not set yet, open identity popup after login"),
			def("messages_per_page", 20, "Number of messages displayed on page by default"),
			def("message_read_delay", 5, "Mark message read after N seconds"),
			def("min_refresh_interval", 5, "Minimal check for new messages interval in minutes"),
			def("attachment_size_limit", d.attachmentSizeLimit(),
				`File size limit (MB) for file upload on compose screen
0 for unlimited.`),
			def("compress_output", false,
				`brotli or gzip compress the output.
Warning: only enable when server does not do this, else double compression errors occur`),
		),
		newSection("interface",
			def("show_attachment_thumbnail", true),
		),
		newSection("contacts",
			def("enable", false, "Enable contacts"),
			def("allow_sync", false),
			def("sync_interval", 20),
			def("type", "sqlite"),
			def("pdo_dsn", "host=127.0.0.1;port=3306;dbname=snappymail"),
			def("pdo_user", "root"),
			def("pdo_password", ""),
			def("mysql_ssl_ca", "", "PEM format certificate"),
			def("mysql_ssl_verify", true),
			def("mysql_ssl_ciphers", "", "HIGH"),
			def("sqlite_global", d.sqliteGlobalDefault()),
			def("suggestions_limit", 20),
		),
		newSection("security",
			def("custom_server_signature", "SnappyMail"),
			def("x_xss_protection_header", "1; mode=block"),
			def("gnupg", true),
			def("openpgp", true),
			def("auto_verify_signatures", false),
			def("allow_admin_panel", true, "Access settings"),
			def("admin_login", "admin", "Login and password for web admin panel"),
			def("admin_password", ""),
			def("admin_totp", ""),
			def("insecure_cryptkey", false,
				"Use email address instead of login password for encrypting sensitive data (like account passwords)"),
			def("force_https", false),
			def("hide_x_mailer_header", true),
			def("max_sys_getloadavg", 0.0, "https://en.m.wikipedia.org/wiki/Load_(computing)"),
			def("content_security_policy", "",
				`For example to allow all images use "img-src https:". More info at https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Security-Policy#directives`),
			def("csp_report", false, "Report CSP errors to PHP and/or SnappyMail Log"),
			def("encrypt_cipher", DefaultCipher,
				"A valid cipher method from https://php.net/openssl_get_cipher_methods"),
			def("cookie_samesite", "Strict", "Strict, Lax or None"),
			def("secfetch_allow", "",
				`Additional allowed Sec-Fetch combinations separated by ";".
For example:
* Allow iframe on same domain in any mode: dest=iframe,site=same-origin
* Allow navigate to iframe on same domain: mode=navigate,dest=iframe,site=same-origin
* Allow navigate to iframe on (sub)domain: mode=navigate,dest=iframe,site=same-site
* Allow navigate to iframe from any domain: mode=navigate,dest=iframe,site=cross-site

Default is "site=same-origin;site=none"`),
		),
		newSection("admin_panel",
			def("host", ""),
			def("key", "admin"),
			def("allow_update", false),
			def("language", "en", "Admin Panel interface language"),
		),
		newSection("ssl",
			def("verify_certificate", true, "Require verification of SSL certificate used."),
			def("allow_self_signed", false, "Allow self-signed certificates. Requires verify_certificate."),
			def("security_level", 1,
				"https://www.openssl.org/docs/man1.1.1/man3/SSL_CTX_set_security_level.html"),
			def("cafile", "",
				"Location of Certificate Authority file on local filesystem (/etc/ssl/certs/ca-certificates.crt)"),
			def("capath", "",
				"capath must be a correctly hashed certificate directory. (/etc/ssl/certs/)"),
			def("local_cert", "",
				"Location of client certificate file (pem format with private key) on local filesystem"),
			def("disable_compression", true, "This can help mitigate the CRIME attack vector."),
		),
		newSection("capa",
			def("dangerous_actions", true, "Allow clear folder and delete messages without moving to trash"),
			def("attachments_actions", true, "Allow download attachments as Zip (and optionally others)"),
		),
		newSection("login",
			def("default_domain", "",
				`If someone logs in without "@domain.tld", this value will be used
When this value is HTTP_HOST, the $_SERVER["HTTP_HOST"] value is used.
When this value is SERVER_NAME, the $_SERVER["SERVER_NAME"] value is used.
When this value is gethostname, the gethostname() value is used.
`),
			def("allow_languages_on_login", true,
				"Allow language selection on webmail login screen"),
			def("determine_user_language", true,
				"Detect language from browser header `Accept-Language`"),
			def("determine_user_domain", false,
				"Like default_domain but then HTTP_HOST/SERVER_NAME without www."),
			def("sign_me_auto", "DefaultOff",
				`This option allows webmail to remember the logged in user
once they closed the browser window.

Values:
  "DefaultOff" - can be used, disabled by default;
  "DefaultOn"  - can be used, enabled by default;
  "Unused"     - cannot be used`),
			def("fault_delay", 5, "When login fails, wait N seconds before responding"),
		),
		newSection("plugins",
			def("enable", false, "Enable plugin support"),
			def("enabled_list", "", "Comma-separated list of enabled plugins"),
		),
		newSection("defaults",
			def("view_editor_type", "Html", "Editor mode used by default (Plain, Html)"),
			def("view_layout", 1, "layout: 0 - no preview, 1 - side preview, 2 - bottom preview"),
			def("view_use_checkboxes", true),
			def("view_show_next_message", true, "Show next message when (re)move current message"),
			def("autologout", 30),
			def("view_html", true),
			def("show_images", false),
			def("view_images", "ask",
				`View external images:
  "ask" - always ask
  "match" - whitelist or ask
  "always" - show always`),
			def("contacts_autosave", true),
			def("mail_list_grouped", false),
			def("mail_use_threads", false),
			def("allow_draft_autosave", true),
			def("mail_reply_same_folder", false),
			def("msg_default_action", 1, "1 - reply, 2 - reply all"),
			def("collapse_blockquotes", true),
			def("allow_spellcheck", false),
		),
		newSection("logs",
			def("enable", false, "Enable logging"),
			def("path", "", "Path where log files will be stored"),
			def("level", 4,
				`Log messages of set RFC 5424 section 6.2.1 Severity level and higher (0 = highest, 7 = lowest).
0 = Emergency
1 = Alert
2 = Critical
3 = Error
4 = Warning
5 = Notice
6 = Informational
7 = Debug`),
			def("hide_passwords", true,
				`Required for development purposes only.
Disabling this option is not recommended.`),
			def("time_zone", "UTC"),
			def("filename", "log-{date:Y-m-d}.txt",
				`Log filename.
For security reasons, some characters are removed from filename.
Allows for pattern-based folder creation (see examples below).

Patterns:
  {date:Y-m-d}  - Replaced by pattern-based date
                  Detailed info: http://www.php.net/manual/en/function.date.php
  {user:email}  - Replaced by user's email address
                  If user is not logged in, value is set to "unknown"
  {user:login}  - Replaced by user's login (the user part of an email)
                  If user is not logged in, value is set to "unknown"
  {user:domain} - Replaced by user's domain name (the domain part of an email)
                  If user is not logged in, value is set to "unknown"
  {user:uid}    - Replaced by user's UID regardless of account currently used

  {user:ip}
  {request:ip}  - Replaced by user's IP address

Others:
  {imap:login} {imap:host} {imap:port}
  {smtp:login} {smtp:host} {smtp:port}

Examples:
  filename = "log-{date:Y-m-d}.txt"
  filename = "{date:Y-m-d}/{user:domain}/{user:email}_{user:uid}.log"
  filename = "{user:email}-{date:Y-m-d}.txt"
  filename = "syslog"
  filename = "stderr"`),
			def("auth_logging", false, "Enable auth logging in a separate file (for fail2ban)"),
			def("auth_logging_filename", "fail2ban/auth-{date:Y-m-d}.txt"),
			def("auth_logging_format",
				"[{date:Y-m-d H:i:s}] Auth failed: ip={request:ip} user={imap:login} host={imap:host} port={imap:port}"),
			def("auth_syslog", false, "Enable auth logging to syslog for fail2ban"),
			def("json_response_write_limit", 300),
		),
		newSection("debug",
			def("enable", false, "Special option required for development purposes"),
			// use_app_debug_js
			def("javascript", false),
			// use_app_debug_css
			def("css", false),
		),
		newSection("cache",
			def("enable", true,
				`The section controls caching of the entire application.

Enables caching in the system`),
			def("path", "", "Path where cache files will be stored"),
			def("index", "v1", "Additional caching key. If changed, cache is purged"),
			def("fast_cache_index", "v1", "Additional caching key. If changed, fast cache is purged"),
			def("http", true, "Browser-level cache. If enabled, caching is maintainted without using files"),
			def("http_expires", 3600, "Browser-level cache time (seconds, Expires header)"),
			def("server_uids", true, "Caching message UIDs when searching and sorting (threading)"),
			def("system_data", true),
		),
		newSection("imap",
			def("use_force_selection", false),
			def("use_expunge_all_on_delete", false),
			def("message_list_fast_simple_search", true),
			def("message_list_permanent_filter", ""),
			def("message_all_headers", false),
			def("show_login_alert", true),
			def("fetch_new_messages", true),
		),
		newSection("labs",
			def("allow_message_append", false,
				"Allow drag & drop .eml files from system into messages list"),
			def("smtp_show_server_errors", false),
			def("mail_func_clear_headers", true, "PHP mail() remove To and Subject headers"),
			def("mail_func_additional_parameters", false, "PHP mail() set -f emailaddress"),
			def("folders_spec_limit", 50),
			def("curl_proxy", ""),
			def("curl_proxy_auth", ""),
			def("custom_login_link", ""),
			def("custom_logout_link", ""),
			def("http_client_ip_check_proxy", false,
				`Trust HTTP_CLIENT_IP / HTTP_X_FORWARDED_FOR for the client IP.
Only enable this when all direct requests come from a trusted reverse proxy,
otherwise clients can spoof their IP address in logs.`),
			def("use_local_proxy_for_external_images", true),
			def("image_exif_auto_rotate", false),
			def("cookie_default_path", ""),
			def("cookie_default_secure", false),
			def("replace_env_in_configuration", ""),
			def("boundary_prefix", ""),
			def("dev_email", ""),
			def("dev_password", ""),
		),
		newSection("version",
			def("current", ""),
			def("saved", ""),
		),
	}
}
