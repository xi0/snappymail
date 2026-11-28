package plugin

// Port of RainLoop\Plugins\Manager: the plugin registry of one installation.
//
// The manager mirrors the observable surface of the PHP class:
//
//   - it is constructed from configs/application.ini ("plugins.enable" and
//     "plugins.enabled_list") but the plugins themselves come from the
//     build-time registry, not from disk;
//   - Hash() is md5(APP_VERSION | each plugin's name@version |
//     each CSS/JS asset and its filemtime), which feeds the Plugins/<etag>/
//     URL and the Lang/Css/index cache keys;
//   - CompileJS/CompileCSS/CompileTemplate/ReadLang/InitAppData collect the
//     assets, view templates, translations and AppData section;
//   - RunHook/RunAdditionalPart/RunAdditionalJSON dispatch to the callbacks the
//     plugins registered in Init().
//
// Everything is built once at start-up (the plugins' Init() calls run there)
// and is read-only afterwards, so a single Manager serves all requests.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/l10n"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/pages"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// Crypter is the SnappyMail\Crypt surface the encrypted plugin properties need.
// *cryptox.Crypter satisfies it.
type Crypter interface {
	EncryptToJSON(data any, key string) (string, error)
	DecryptFromJSON(data, key string) (any, error)
}

// SettingsProvider is Providers\Settings (the per-user "settings" document).
// *storage.SettingsStorage satisfies it.
type SettingsProvider interface {
	Load(account storage.Account) *storage.Settings
	Save(account storage.Account, settings *storage.Settings) error
}

// Logger is the logx surface of WriteLog(). A nil logger drops the lines.
type Logger interface {
	Write(desc string, level logx.Level, name string) bool
}

// Options is the installation-level state of the manager. The zero value is a
// disabled manager without plugins, which is what plugins.enable = Off does.
type Options struct {
	// Version is APP_VERSION (the first Hash() element).
	Version string
	// Salt is APP_SALT, the key of the encrypted plugin properties.
	Salt string
	// PluginsPath is APP_PLUGINS_PATH (data/_data_/<name>/plugins/).
	PluginsPath string
	// ConfigPath is APP_PRIVATE_DATA . 'configs/' (plugin-<name>.json).
	ConfigPath string
	// Enabled is plugins.enable.
	Enabled bool
	// EnabledList is plugins.enabled_list (comma separated plugin ids).
	EnabledList string
	// Registry is the build-time plugin list (internal/plugins).
	Registry []Factory
	// Logger receives the "Hook: …" and plugin error lines (category PLUGIN).
	Logger Logger
	// Crypt seals the encrypted properties of a plugin configuration.
	Crypt Crypter
	// Settings is ProviderSettings(): the per-user "Plugins" settings.
	Settings SettingsProvider
	// SaveEnabledList persists a normalised plugins.enabled_list (PHP rewrites
	// the configuration when a listed plugin is not installed). A nil callback
	// only logs the difference.
	SaveEnabledList func(list string) error
}

// loaded is one plugin of the manager: the instance, its folder and its
// configuration document.
type loaded struct {
	plugin Plugin
	path   string
	config *Config
}

// Manager is Plugins\Manager.
type Manager struct {
	options   Options
	enabled   bool
	plugins   []*loaded
	byName    map[string]*loaded
	css       [2][]string
	js        [2][]string
	templates [2][]string
	hooks     map[string][]Hook
	parts     map[string][]Part
	json      map[string]JSONAction
}

// New builds the manager and calls Init() of every enabled plugin.
func New(options Options) *Manager {
	manager := &Manager{
		options: options,
		enabled: options.Enabled,
		byName:  map[string]*loaded{},
		hooks:   map[string][]Hook{},
		parts:   map[string][]Part{},
		json:    map[string]JSONAction{},
	}

	if manager.enabled {
		manager.load(options.EnabledList)
	}

	return manager
}

// load is the constructor half of Plugins\Manager::__construct(): only the
// plugins of "plugins.enabled_list" are initialised, and the list is rewritten
// when some of them are not registered in this build.
func (m *Manager) load(enabledList string) {
	list := strings.TrimSpace(enabledList)
	if "" == list {
		return
	}

	// The registry is the Go equivalent of the plugin folders on disk: a listed
	// id that no factory provides is dropped, like a missing folder in PHP.
	available := map[string]Plugin{}
	for _, factory := range m.options.Registry {
		if nil == factory {
			continue
		}
		instance := factory()
		if nil == instance {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(instance.Name()))
		if "" == name {
			continue
		}
		if _, exists := available[name]; !exists {
			available[name] = instance
		}
	}

	names := make([]string, 0, len(available))
	seen := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		name := strings.ToLower(strings.TrimSpace(item))
		if "" == name || seen[name] {
			continue
		}
		seen[name] = true

		instance, ok := available[name]
		if !ok {
			continue
		}
		if _, exists := m.byName[name]; exists {
			continue
		}
		if m.start(instance) {
			names = append(names, instance.Name())
		}
	}

	// PHP: $aList = implode(',', array_keys($this->aPlugins)); rewrite when it
	// differs from the configured value.
	normalised := strings.Join(names, ",")
	if normalised != list {
		m.logf("%s => %s", list, normalised)
		if nil != m.options.SaveEnabledList {
			if err := m.options.SaveEnabledList(normalised); nil != err {
				m.WriteLog("Cannot save plugins.enabled_list: "+err.Error(), logx.Warning)
			}
		}
	}
}

// start creates the configuration of one plugin and runs its Init().
func (m *Manager) start(instance Plugin) bool {
	name := instance.Name()
	entry := &loaded{
		plugin: instance,
		path:   "",
	}
	if "" != strings.TrimSpace(m.options.PluginsPath) {
		entry.path = filepath.Join(m.options.PluginsPath, name)
	}

	entry.config = newConfig(name, m.options.ConfigPath, instance.ConfigMap(), m.options.Crypt, m.options.Salt)
	if "" != m.options.ConfigPath {
		entry.config.Load()
	}

	m.plugins = append(m.plugins, entry)
	m.byName[strings.ToLower(name)] = entry

	instance.Init(&environment{manager: m, entry: entry})

	return true
}

// IsEnabled is Plugins\Manager::IsEnabled().
func (m *Manager) IsEnabled() bool { return nil != m && m.enabled }

// Count is Plugins\Manager::count().
func (m *Manager) Count() int {
	if nil == m || !m.enabled {
		return 0
	}
	return len(m.plugins)
}

// Names returns the plugin ids in registration order.
func (m *Manager) Names() []string {
	if nil == m || !m.enabled {
		return nil
	}
	names := make([]string, 0, len(m.plugins))
	for _, entry := range m.plugins {
		names = append(names, entry.plugin.Name())
	}
	return names
}

// Plugin returns the plugin with the given id, or nil.
func (m *Manager) Plugin(name string) Plugin {
	if entry, ok := m.byName[strings.ToLower(name)]; ok {
		return entry.plugin
	}
	return nil
}

// Config returns the configuration document of a plugin (Config\Plugin), or
// nil when the plugin is not loaded.
func (m *Manager) Config(name string) *Config {
	if entry, ok := m.byName[strings.ToLower(name)]; ok {
		return entry.config
	}
	return nil
}

// SetEnabledList is Plugins\Manager's enabled_list rewrite point: it is used by
// the admin panel of a later step. A manager built with Enabled = false stays
// disabled.
func (m *Manager) SetEnabledList(list string) {
	if nil == m || !m.enabled {
		return
	}
	m.plugins = nil
	m.byName = map[string]*loaded{}
	m.load(list)
}

// Hash is Plugins\Manager::Hash(): the md5 of the version, every plugin's
// name@version and every registered CSS/JS asset with its modification time.
//
// The asset mtimes matter: without them an edited plugin stylesheet keeps being
// served from the cached Css/Lang/Plugins responses.
func (m *Manager) Hash() string {
	if nil == m {
		return md5Hex("")
	}

	parts := []string{m.options.Version}

	if m.enabled {
		for _, entry := range m.plugins {
			parts = append(parts, entry.plugin.Name()+"@"+entry.plugin.Version())
		}

		// PHP iterates array($this->aCss, $this->aJs) and then both scopes.
		for _, scopes := range [][2][]string{m.css, m.js} {
			for _, files := range scopes {
				for _, file := range files {
					parts = append(parts, file+"@"+itoa64(fileMTime(file)))
				}
			}
		}
	}

	return md5Hex(strings.Join(parts, "|"))
}

// HaveJS is Plugins\Manager::HaveJs().
func (m *Manager) HaveJS(admin bool) bool {
	return nil != m && m.enabled && 0 < len(m.js[scope(admin)])
}

// CompileJS is Plugins\Manager::CompileJs(): the concatenated scripts of the
// scope, preferring the ".min.js" variant when minified is set.
func (m *Manager) CompileJS(admin, minified bool) string {
	if nil == m || !m.enabled {
		return ""
	}

	files := make([]string, 0, len(m.js[scope(admin)]))
	for _, file := range m.js[scope(admin)] {
		if minified {
			if candidate := strings.Replace(file, ".js", ".min.js", 1); isReadable(candidate) {
				file = candidate
			}
		}
		if !isReadable(file) {
			continue
		}
		content, err := os.ReadFile(file)
		if nil != err {
			continue
		}
		files = append(files, string(content))
	}

	return strings.Join(files, "\n")
}

// CompileCSS is Plugins\Manager::CompileCss(): the concatenated stylesheets of
// the scope; less is set when a fragment is a .less file (the LESS compiler is
// not ported, see SPEC.md).
func (m *Manager) CompileCSS(admin bool, less *bool, minified bool) string {
	if nil == m || !m.enabled {
		return ""
	}

	files := make([]string, 0, len(m.css[scope(admin)]))
	for _, file := range m.css[scope(admin)] {
		if minified {
			if candidate := strings.Replace(file, ".css", ".min.css", 1); isReadable(candidate) {
				file = candidate
			}
		}
		if !isReadable(file) {
			continue
		}
		if nil != less && strings.HasSuffix(file, ".less") {
			*less = true
		}
		content, err := os.ReadFile(file)
		if nil != err {
			continue
		}
		files = append(files, string(content))
	}

	return strings.Join(files, "\n")
}

// CompileTemplate is Plugins\Manager::CompileTemplate(): the plugin view
// templates appended to the index page's template list. The template name is
// the file name without its last five characters (".html"), like PHP's
// substr(basename($sFile), 0, -5).
func (m *Manager) CompileTemplate(templates *pages.Templates, admin bool) {
	if nil == m || !m.enabled || nil == templates {
		return
	}

	for _, file := range m.templates[scope(admin)] {
		if !isReadable(file) {
			continue
		}
		name := filepath.Base(file)
		if 5 < len(name) {
			name = name[:len(name)-5]
		}
		templates.Set(name, file)
	}
}

// ReadLang is Plugins\Manager::ReadLang(): merge the plugin translations into
// doc. English is always the base, then the requested language; a JSON file
// wins over the legacy INI file of the same language.
//
// PHP reassigns its own $sLang variable when the dash-to-underscore fallback is
// needed, which affects the plugins read afterwards; that quirk is reproduced.
func (m *Manager) ReadLang(language string, doc map[string]any) {
	if nil == m || !m.enabled || nil == doc {
		return
	}

	for _, entry := range m.plugins {
		if !entry.plugin.UseLangs() || "" == entry.path {
			continue
		}

		dir := filepath.Join(entry.path, "langs")

		if document := readLangDocument(filepath.Join(dir, "en.json"), filepath.Join(dir, "en.ini")); nil != document {
			doc = l10n.MergeRecursive(doc, document)
		}

		if "en" == language {
			continue
		}

		document := readLangDocument(filepath.Join(dir, language+".json"), "")
		if nil == document {
			if !isFile(filepath.Join(dir, language+".ini")) {
				language = strings.ReplaceAll(language, "-", "_")
			}
			document = readLangDocument("", filepath.Join(dir, language+".ini"))
		}
		if nil != document {
			doc = l10n.MergeRecursive(doc, document)
		}
	}
}

// readLangDocument reads a plugin language file: the JSON file first, then the
// legacy INI file. A missing or broken file yields nil.
func readLangDocument(jsonFile, iniFile string) map[string]any {
	if "" != jsonFile && isFile(jsonFile) {
		if content, err := os.ReadFile(jsonFile); nil == err {
			var document map[string]any
			if err := json.Unmarshal(content, &document); nil == err && 0 < len(document) {
				return document
			}
		}
		return nil
	}

	if "" != iniFile && isFile(iniFile) {
		if content, err := os.ReadFile(iniFile); nil == err {
			// parse_ini_file($file, true): the top level keys are sections.
			sections := map[string]any{}
			for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
				line = strings.TrimSpace(line)
				if "" == line || ';' == line[0] || '#' == line[0] || '[' != line[0] {
					continue
				}
				if end := strings.IndexByte(line, ']'); 0 < end {
					name := line[1:end]
					sections[name] = parseLegacyINI(string(content), name)
				}
			}
			if 0 < len(sections) {
				return sections
			}
		}
	}

	return nil
}

// InitAppData is Plugins\Manager::InitAppData(): expose the AllowedInJs
// configuration of every plugin through AppData.Plugins[<name>] and run the
// "filter.app-data" hook.
func (m *Manager) InitAppData(admin bool, appData map[string]any, _ *account.Account) {
	if nil == m || !m.enabled || nil == appData {
		return
	}

	if value, ok := appData["Plugins"]; ok {
		switch value.(type) {
		case []any, map[string]any:
		default:
			return
		}
	} else {
		return
	}

	// PHP keys the AppData document by the plugin name; the empty array PHP
	// starts with becomes a JSON object as soon as a plugin adds settings.
	plugins, _ := appData["Plugins"].(map[string]any)
	if nil == plugins {
		plugins = map[string]any{}
	}

	auth := !phpEmpty(appData["Auth"])

	for _, entry := range m.plugins {
		config := map[string]any{}
		for _, property := range entry.plugin.ConfigMap() {
			if nil == property || !property.AllowedInJs() {
				continue
			}
			config[property.Name()] = entry.config.Get(property.Name(), property.DefaultValue())
		}

		entry.plugin.FilterAppDataPluginSection(admin, auth, config)

		if 0 < len(config) {
			plugins[entry.plugin.Name()] = config
		}
	}

	if 0 < len(plugins) {
		appData["Plugins"] = plugins
	}

	m.RunHook("filter.app-data", admin, &appData)
}

// RunHook is Plugins\Manager::RunHook(): call every callback of the hook in
// registration order. A panicking callback is re-thrown to the caller, exactly
// like PHP re-throws the exception.
func (m *Manager) RunHook(name string, args ...any) {
	if nil == m || !m.enabled {
		return
	}

	callbacks, ok := m.hooks[strings.ToLower(name)]
	if !ok || 0 == len(callbacks) {
		return
	}

	m.WriteLog("Hook: "+strings.ToLower(name), logx.Info)
	for _, callback := range callbacks {
		callback(args...)
	}
}

// RunAdditionalPart is Plugins\Manager::RunAdditionalPart(): run the plugin
// endpoints registered for a "?/Name" path. PHP spreads the path vector into
// the callback arguments and ORs the results.
func (m *Manager) RunAdditionalPart(action string, parts []string) bool {
	if nil == m || !m.enabled {
		return false
	}

	handled := false
	for _, callback := range m.parts[strings.ToLower(action)] {
		if callback(parts...) {
			handled = true
		}
	}
	return handled
}

// AddHook is Plugins\Manager::AddHook().
func (m *Manager) AddHook(name string, callback Hook) {
	if nil == m || !m.enabled || nil == callback {
		return
	}
	key := strings.ToLower(name)
	m.hooks[key] = append(m.hooks[key], callback)
}

// AddJS is Plugins\Manager::AddJs().
func (m *Manager) AddJS(file string, admin bool) {
	if nil == m || !m.enabled || "" == file {
		return
	}
	m.js[scope(admin)] = append(m.js[scope(admin)], file)
}

// AddCSS is Plugins\Manager::AddCss().
func (m *Manager) AddCSS(file string, admin bool) {
	if nil == m || !m.enabled || "" == file {
		return
	}
	m.css[scope(admin)] = append(m.css[scope(admin)], file)
}

// AddTemplate is Plugins\Manager::AddTemplate().
func (m *Manager) AddTemplate(file string, admin bool) {
	if nil == m || !m.enabled || "" == file {
		return
	}
	m.templates[scope(admin)] = append(m.templates[scope(admin)], file)
}

// AddPartHook is Plugins\Manager::AddAdditionalPartAction().
func (m *Manager) AddPartHook(action string, callback Part) {
	if nil == m || !m.enabled || nil == callback || "" == action {
		return
	}
	key := strings.ToLower(action)
	m.parts[key] = append(m.parts[key], callback)
}

// AddJSONAction is Plugins\Manager::AddAdditionalJsonAction(): the action is
// addressed as "Plugin<action>" by rl.pluginRemoteRequest() and dispatched
// through the "DoPlugin<action>" method name.
func (m *Manager) AddJSONAction(action string, callback JSONAction) {
	if nil == m || !m.enabled || nil == callback || "" == action {
		return
	}
	key := "DoPlugin" + action
	if _, exists := m.json[key]; exists {
		return
	}
	m.json[key] = callback
}

// HasAdditionalJSON is Plugins\Manager::HasAdditionalJson().
func (m *Manager) HasAdditionalJSON(method string) bool {
	if nil == m || !m.enabled {
		return false
	}
	_, ok := m.json[method]
	return ok
}

// RunAdditionalJSON is Plugins\Manager::RunAdditionalJson(): the plugin action
// is called with the request context so it can read its parameters and build
// the response envelope.
func (m *Manager) RunAdditionalJSON(ctx ActionContext, method string) (any, error) {
	if nil == m || !m.enabled {
		return false, nil
	}
	callback, ok := m.json[method]
	if !ok {
		return false, nil
	}
	return callback(ctx)
}

// GetUserPluginSettings is Plugins\Manager::GetUserPluginSettings(): the
// "Plugins"/<name> section of the account's settings document.
func (m *Manager) GetUserPluginSettings(target *account.Account, name string) map[string]any {
	if nil == m || nil == target || nil == m.options.Settings {
		return map[string]any{}
	}

	settings := m.options.Settings.Load(target)
	if nil == settings {
		return map[string]any{}
	}

	all, _ := settings.ToMap()["Plugins"].(map[string]any)
	if nil == all {
		return map[string]any{}
	}

	value, _ := all[name].(map[string]any)
	if nil == value {
		return map[string]any{}
	}
	return value
}

// SaveUserPluginSettings is Plugins\Manager::SaveUserPluginSettings(): the
// settings are merged into the existing "Plugins"/<name> section.
func (m *Manager) SaveUserPluginSettings(target *account.Account, name string, values map[string]any) error {
	if nil == m || nil == target || nil == m.options.Settings {
		return nil
	}

	settings := m.options.Settings.Load(target)
	if nil == settings {
		return nil
	}

	all, _ := settings.ToMap()["Plugins"].(map[string]any)
	if nil == all {
		all = map[string]any{}
	}

	current, _ := all[name].(map[string]any)
	if nil == current {
		current = map[string]any{}
	}
	for key, value := range values {
		current[key] = value
	}
	all[name] = current

	// The nested values are re-encoded by the settings writer; the top level
	// key order (the file layout PHP reads) is preserved by SetConf().
	settings.SetConf("Plugins", all)

	return m.options.Settings.Save(target, settings)
}

// WriteLog is Plugins\Manager::WriteLog(): a log line in the PLUGIN category.
func (m *Manager) WriteLog(message string, level logx.Level) {
	if nil == m || nil == m.options.Logger {
		return
	}
	m.options.Logger.Write(message, level, "PLUGIN")
}

// logf is the plugin loader's debug line.
func (m *Manager) logf(format string, args ...any) {
	if nil != m {
		m.WriteLog(fmt.Sprintf(format, args...), logx.Debug)
	}
}

// scope maps the admin flag onto the [2] array index PHP uses
// ($bAdminScope ? 1 : 0).
func scope(admin bool) int {
	if admin {
		return 1
	}
	return 0
}

// environment is the per-plugin registration surface handed to Plugin.Init().
type environment struct {
	manager *Manager
	entry   *loaded
}

// AddHook is AbstractPlugin::addHook().
func (e *environment) AddHook(name string, fn Hook) { e.manager.AddHook(name, fn) }

// AddJS is AbstractPlugin::addJs().
func (e *environment) AddJS(file string, admin bool) { e.manager.AddJS(e.file(file), admin) }

// AddCSS is AbstractPlugin::addCss().
func (e *environment) AddCSS(file string, admin bool) { e.manager.AddCSS(e.file(file), admin) }

// AddTemplate is AbstractPlugin::addTemplate().
func (e *environment) AddTemplate(file string, admin bool) {
	e.manager.AddTemplate(e.file(file), admin)
}

// AddPartHook is AbstractPlugin::addPartHook().
func (e *environment) AddPartHook(action string, fn Part) { e.manager.AddPartHook(action, fn) }

// AddJSONAction is AbstractPlugin::addJsonHook().
func (e *environment) AddJSONAction(action string, fn JSONAction) {
	e.manager.AddJSONAction(action, fn)
}

// Path is AbstractPlugin::Path().
func (e *environment) Path() string { return e.entry.path }

// Config is AbstractPlugin::Config().
func (e *environment) Config() *Config { return e.entry.config }

// Account is nil: Init() runs once at start-up, not per request.
func (e *environment) Account() *account.Account { return nil }

// file is AbstractPlugin's $this->sPath.'/'.$sFile concatenation.
func (e *environment) file(file string) string {
	if "" == e.entry.path {
		return ""
	}
	return filepath.Join(e.entry.path, filepath.FromSlash(file))
}
