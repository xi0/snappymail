package service

// The Actions\Localization / Actions\Themes compile surface the Lang, Css and
// Plugins endpoints and the index page need, exposed on the AppData service.
//
// The plugin parts are reached through optional interfaces: the plugin manager
// (step 28) implements them, and until it is wired they are no-ops exactly like
// plugins.enable = Off.

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/l10n"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/themes"
)

// pluginJSCompiler is Plugins\Manager::CompileJs().
type pluginJSCompiler interface {
	CompileJS(admin, minified bool) string
}

// ValidateLanguage is Actions\Localization::ValidateLanguage() with the
// $bDefault = ” and $bAllowEmptyResult = false defaults.
func (a *AppData) ValidateLanguage(language string, admin bool) string {
	return a.validateLanguage(language, "", admin)
}

// ValidateTheme is Actions\Themes::ValidateTheme().
func (a *AppData) ValidateTheme(theme string) string {
	return a.themes.Validate(theme, a.config().GetString("webmail", "theme", defaultTheme))
}

// CompileLanguage is Actions\Localization::compileLanguage().
func (a *AppData) CompileLanguage(language string, admin bool) (string, error) {
	var reader l10n.LangReader
	if plugin, ok := a.options.Plugins.(l10n.LangReader); ok && nil != a.options.Plugins {
		reader = plugin
	}

	return a.languages.CompileLanguage(language, admin, reader)
}

// CompileCSS is Actions\Themes::compileCss().
func (a *AppData) CompileCSS(theme string, admin, minified bool) (string, error) {
	var pluginCSS themes.PluginCSS
	if plugin, ok := a.options.Plugins.(themes.PluginCSS); ok && nil != a.options.Plugins {
		pluginCSS = plugin
	}

	return a.themes.CompileCSS(theme, admin, minified, pluginCSS)
}

// StaticI18N is Actions\Localization::StaticI18N() for a resolved language.
func (a *AppData) StaticI18N(language, key string) string {
	language = a.validateLanguage(language, "en", false)

	document := a.languages.Load(language, "static")
	if plugin, ok := a.options.Plugins.(l10n.LangReader); ok && nil != a.options.Plugins {
		plugin.ReadLang(language, document)
	}

	if value, ok := document[key].(string); ok {
		return value
	}
	return key
}

// PluginsHash is Plugins()->Hash().
func (a *AppData) PluginsHash() string {
	if nil == a.options.Plugins {
		return ""
	}
	return a.options.Plugins.Hash()
}

// PluginsHaveJS is Plugins()->HaveJs().
func (a *AppData) PluginsHaveJS(admin bool) bool {
	if nil == a.options.Plugins {
		return false
	}
	return a.options.Plugins.HaveJS(admin)
}

// PluginsManager is Actions::Plugins(). A nil value means no plugin manager is
// wired yet (step 28).
func (a *AppData) PluginsManager() Plugins {
	return a.options.Plugins
}

// CompilePluginsJS is Plugins()->CompileJs().
func (a *AppData) CompilePluginsJS(admin, minified bool) string {
	if compiler, ok := a.options.Plugins.(pluginJSCompiler); ok && nil != a.options.Plugins {
		return compiler.CompileJS(admin, minified)
	}
	return ""
}

// Cacher is Actions::Cacher(): nil when no file cache is wired.
func (a *AppData) Cacher() *storage.Cache {
	return a.options.Cache
}

// NewCacher is Actions::Cacher($oAccount): a cache client on the same folder
// whose driver is prefixed with the account, so the per-account entries (the
// read-receipt marks, the UID lists) live next to the system data without
// colliding with it. A nil account uses the "_default_" (unprefixed) folder,
// exactly like PHP's empty key.
func (a *AppData) NewCacher(resolved *account.Account) *storage.Cache {
	dir := ""
	if nil != a.options.Cache {
		dir = a.options.Cache.Dir()
	}
	if "" == dir {
		return nil
	}

	driver, err := storage.NewCacheFileDriver(dir)
	if nil != err {
		return nil
	}

	prefix := ""
	if nil != resolved {
		prefix = resolved.Email()
	}
	driver.SetPrefix(prefix)

	cache := storage.NewCache(driver)
	cache.SetCacheIndex(a.config().GetString("cache", "fast_cache_index", ""))
	return cache
}

// Config exposes the application configuration (the index page and the
// endpoints read the same values).
func (a *AppData) Config() Config {
	return a.config()
}

// Salt is APP_SALT, the value the account hashes and the RawKeys are built with.
func (a *AppData) Salt() string {
	return a.options.Salt
}
