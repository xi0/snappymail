// Package config implements the SnappyMail configuration contract:
// data/_data_/<name>/configs/application.ini as read and written by
// RainLoop\Config\Application and RainLoop\Config\AbstractConfig.
//
// The file is a compatibility contract, because the PHP backend may run against
// the same data folder at any time, so the observable behaviour is ported as-is
// instead of being "improved":
//
//   - every value keeps the PHP type of its default and Set() casts new values
//     to it (AbstractConfig::Set() switches on get_debug_type());
//   - the section and key order is the insertion order of defaultValues(),
//     which is also the order Save() writes, because Set() never reorders
//     $aData;
//   - the legacy key rewrites of Application::Set() (webmail.language_admin,
//     labs.imap_*, …) are applied on load, so an old file is migrated in
//     memory;
//   - Save() keeps the header comment and the "; comment" blocks, writes
//     On/Off for booleans and bare numbers for int/float, and ends without a
//     trailing newline.
//
// The environment dependent parts of the PHP class are injectable, so the later
// steps can plug in without touching this package: the OpenSSL cipher list
// (step 6, internal/cryptox), the language list (step 11, internal/l10n) and the
// load average sampler (the operating system).
package config

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// applicationHeader is the header RainLoop\Config\Application passes to
// AbstractConfig (the apostrophe is escaped in the PHP single quoted string).
const applicationHeader = "; SnappyMail configuration file\n" +
	"; Please don't add custom parameters here, those will be overwritten"

// DefaultCipher is the security.encrypt_cipher default and the cipher written
// when the configured one is not supported.
const DefaultCipher = "aes-256-cbc-hmac-sha1"

// saveDateFormat is PHP's gmdate('r') / DATE_RFC2822.
const saveDateFormat = "Mon, 02 Jan 2006 15:04:05 -0700"

// customSection is the only section AbstractConfig::Set() accepts unknown keys
// in.
const customSection = "custom"

// option is one configuration entry: the typed value plus the documentation
// comment PHP keeps next to it ($aData[section][key] = [$value, $comment]).
type option struct {
	// value is a bool, int, float64 or string, mirroring the PHP type of the
	// corresponding default. The type decides how Set() casts a new value and
	// how Save() renders the entry.
	value any
	// comment is written above the key as "; <line>" lines.
	comment string
}

// section is one [name] block of the configuration file.
type section struct {
	name   string
	keys   []string
	values map[string]*option
}

// newSection builds a section with the given keys in order.
func newSection(name string, entries ...defaultValue) *section {
	result := &section{
		name:   name,
		keys:   make([]string, 0, len(entries)),
		values: make(map[string]*option, len(entries)),
	}
	for _, entry := range entries {
		result.keys = append(result.keys, entry.key)
		result.values[entry.key] = entry.option
	}
	return result
}

// lookup returns the option of key.
func (s *section) lookup(key string) (*option, bool) {
	option, ok := s.values[key]
	return option, ok
}

// Application is RainLoop\Config\Application: configs/application.ini.
//
// The zero value is not usable, call NewApplication. All methods are safe for
// concurrent use, so a single instance can be shared by the request handlers.
type Application struct {
	mu sync.RWMutex

	// file is the full path of application.ini
	// (APP_PRIVATE_DATA . 'configs/application.ini').
	file string
	// version is APP_VERSION, written to [version] current by Save().
	version string
	// header is sFileHeader: the file comment Save() puts on top.
	header string
	// sections keeps the [section] blocks in defaultValues() order.
	sections []*section
	// index addresses the sections by name.
	index map[string]*section
	// replaceEnv is the lowercased "section.key" list of
	// labs.replace_env_in_configuration.
	replaceEnv []string

	// Env is the $_ENV/$_SERVER snapshot used for the $NAME replacement of
	// labs.replace_env_in_configuration. A nil or empty map disables it,
	// exactly like an empty $_ENV and $_SERVER in PHP.
	Env map[string]string
	// Logf receives the non fatal warnings PHP sends to SnappyMail\Log (an
	// unsupported security.encrypt_cipher). A nil func silences them.
	Logf func(category, message string)
	// CipherSupported reports SnappyMail\Crypt::cipherSupported(). A nil func
	// uses DefaultCipherSupported; step 6 (internal/cryptox) plugs in the real
	// list.
	CipherSupported func(cipher string) bool
	// ValidateLanguage ports SnappyMail\L10n::validLanguage(): the normalised
	// language code, or "" when it is not available. A nil func uses the
	// placeholder that only validates the code syntax; step 11
	// (internal/l10n) plugs in the language list.
	ValidateLanguage func(language string, admin bool) string
	// LoadAverage samples the 1 minute load average for the
	// security.max_sys_getloadavg guard; the second result is false when the
	// platform has no equivalent of sys_getloadavg(), which skips the guard
	// like PHP does. A nil func disables the guard.
	LoadAverage func() (float64, bool)
	// Now returns the current time, used for [version] saved. A nil func uses
	// time.Now.
	Now func() time.Time
}

// NewApplication builds the configuration with the defaults of
// RainLoop\Config\Application::defaultValues().
//
// file is the full path of application.ini and version is APP_VERSION. The
// defaults of two keys depend on the environment, see Defaults.
func NewApplication(file, version string, defaults Defaults) *Application {
	sections := defaultSections(defaults)
	index := make(map[string]*section, len(sections))
	for _, section := range sections {
		index[section.name] = section
	}

	return &Application{
		file:     file,
		version:  version,
		header:   applicationHeader,
		sections: sections,
		index:    index,
	}
}

// File returns the full path of the configuration file.
func (a *Application) File() string { return a.file }

// Version returns APP_VERSION, the value Save() writes to [version] current.
func (a *Application) Version() string { return a.version }

// Lookup returns the raw value of section/key, without any type conversion.
func (a *Application) Lookup(sectionName, key string) (any, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return a.lookup(sectionName, key)
}

// lookup is Lookup without the lock.
func (a *Application) lookup(sectionName, key string) (any, bool) {
	if section, ok := a.index[sectionName]; ok {
		if option, ok := section.lookup(key); ok {
			return option.value, true
		}
	}
	return nil, false
}

// GetString returns a string value, with the $NAME replacement of
// labs.replace_env_in_configuration applied like Application::Get() does for
// string results.
func (a *Application) GetString(sectionName, key, defaultValue string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	result := defaultValue
	if section, ok := a.index[sectionName]; ok {
		if option, ok := section.lookup(key); ok {
			result = phpString(option.value)
		}
	}

	return a.replaceEnvIn(sectionName, key, result)
}

// GetBool returns a bool value, or defaultValue when the key does not exist.
func (a *Application) GetBool(sectionName, key string, defaultValue bool) bool {
	if value, ok := a.Lookup(sectionName, key); ok {
		return phpBool(scalar(value))
	}
	return defaultValue
}

// GetInt returns an int value, or defaultValue when the key does not exist.
func (a *Application) GetInt(sectionName, key string, defaultValue int) int {
	if value, ok := a.Lookup(sectionName, key); ok {
		return phpInt(scalar(value))
	}
	return defaultValue
}

// GetFloat returns a float value, or defaultValue when the key does not exist.
func (a *Application) GetFloat(sectionName, key string, defaultValue float64) float64 {
	if value, ok := a.Lookup(sectionName, key); ok {
		return phpFloat(scalar(value))
	}
	return defaultValue
}

// Set is Application::Set(): the legacy key rewrites, the language
// normalisation and the AbstractConfig type cast, in that order.
func (a *Application) Set(sectionName, key string, value any) {
	sectionName, key = rewriteKeys(sectionName, key)

	// Application::Set(): every "language" key is validated, so an unknown
	// language falls back to "en" (the admin panel uses its own list).
	if "language" == key {
		language := a.validateLanguage(phpString(value), "admin_panel" == sectionName)
		if "" == language {
			language = "en"
		}
		value = language
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.set(sectionName, key, value)
}

// set is Set without the lock and without the rewrites.
func (a *Application) set(sectionName, key string, value any) {
	section, ok := a.index[sectionName]
	if ok {
		if existing, found := section.lookup(key); found {
			existing.value = castLike(existing.value, value)
			return
		}
	}

	// AbstractConfig::Set() accepts unknown keys in the "custom" section only,
	// and only when they are safe to write unquoted.
	if customSection == sectionName && isValidIniKey(key) {
		if !ok {
			section = newSection(customSection)
			a.sections = append(a.sections, section)
			a.index[customSection] = section
		}
		section.keys = append(section.keys, key)
		section.values[key] = &option{value: phpString(value)}
	}
}

// Dump returns the configuration as map[section]map[key]value, the shape
// json_encode($config) produces for PHP (the comments are left out).
func (a *Application) Dump() map[string]map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	dump := make(map[string]map[string]any, len(a.sections))
	for _, section := range a.sections {
		values := make(map[string]any, len(section.keys))
		for _, key := range section.keys {
			values[key] = section.values[key].value
		}
		dump[section.name] = values
	}

	return dump
}

// rewriteKeys ports the "workarounds for the changed application structure" at
// the top of Application::Set(). The rewrites are chained exactly like the PHP
// if-statements, including the odd combinations (labs.imap_force_https really
// ends up in [security]).
func rewriteKeys(sectionName, key string) (string, string) {
	if "webmail" == sectionName && "language_admin" == key {
		sectionName, key = "admin_panel", "language"
	}

	if "security" == sectionName && strings.HasPrefix(key, "admin_panel_") {
		sectionName, key = "admin_panel", strings.ReplaceAll(key, "admin_panel_", "")
	}

	if "labs" == sectionName {
		if strings.HasPrefix(key, "imap_") {
			sectionName, key = "imap", strings.ReplaceAll(key, "imap_", "")
		}

		if strings.HasPrefix(key, "use_app_debug_") {
			sectionName = "debug"
			key = strings.ReplaceAll(key, "use_app_debug_js", "javascript")
			key = strings.ReplaceAll(key, "use_app_debug_css", "css")
		}

		switch key {
		case "cache_system_data":
			sectionName, key = "cache", "system_data"
		case "force_https":
			sectionName = "security"
		case "check_new_messages":
			sectionName, key = "imap", "fetch_new_messages"
		case "login_fault_delay":
			sectionName, key = "login", "fault_delay"
		case "log_ajax_response_write_limit":
			sectionName, key = "logs", "json_response_write_limit"
		}
	}

	return sectionName, key
}

// envPattern is the '/\$([^\s]+)/' pattern of Application::Get().
var envPattern = regexp.MustCompile(`\$([^\s]+)`)

// replaceEnvIn ports the $NAME substitution of Application::Get(): it only
// applies to the keys listed in labs.replace_env_in_configuration and to string
// values that contain a "$".
func (a *Application) replaceEnvIn(sectionName, key, value string) string {
	if 0 == len(a.replaceEnv) || !strings.Contains(value, "$") {
		return value
	}

	name := strings.ToLower(sectionName + "." + key)
	listed := false
	for _, candidate := range a.replaceEnv {
		if candidate == name {
			listed = true
			break
		}
	}
	if !listed {
		return value
	}

	return envPattern.ReplaceAllStringFunc(value, func(match string) string {
		// PHP: !empty($_ENV[$name]) ? value : (!empty($_SERVER[$name]) ?
		// value : whole match), and empty() is true for "" and "0".
		if replaced := a.Env[match[1:]]; "" != replaced && "0" != replaced {
			return replaced
		}
		return match
	})
}

// validateLanguage calls the configured validator.
func (a *Application) validateLanguage(language string, admin bool) string {
	if nil != a.ValidateLanguage {
		return a.ValidateLanguage(language, admin)
	}
	return defaultValidateLanguage(language, admin)
}

// cipherSupported calls the configured cipher check.
func (a *Application) cipherSupported(cipher string) bool {
	if nil != a.CipherSupported {
		return a.CipherSupported(cipher)
	}
	return DefaultCipherSupported(cipher)
}

// logf forwards a warning to Logf when it is set.
func (a *Application) logf(category, message string) {
	if nil != a.Logf {
		a.Logf(category, message)
	}
}

// now returns the current time through the injected clock.
func (a *Application) now() time.Time {
	if nil != a.Now {
		return a.Now()
	}
	return time.Now()
}
