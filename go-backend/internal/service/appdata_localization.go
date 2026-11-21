package service

// The localization and theme parts of AppData:
// Actions\Localization::{GetLanguage,ValidateLanguage,detectClientLanguage} and
// Actions\Themes::{GetTheme,GetThemes,ValidateTheme}.

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/l10n"
)

// defaultTheme is the ValidateTheme() fallback and the webmail.theme default.
const defaultTheme = "Gazellemail"

// theme is Actions\Themes::GetTheme(): the configured theme, the per-user
// "settings_local" Theme (only when the Themes capability is on) and the
// validation against the install tree.
func (a *AppData) theme(admin bool, resolved *account.Account) string {
	config := a.config()
	fallback := config.GetString("webmail", "theme", defaultTheme)
	name := fallback

	if !admin && nil != resolved && phpBool(a.capa(false, resolved)["Themes"]) {
		if settings := a.localSettings(resolved); nil != settings {
			name = phpString(settings.GetConf("Theme", name))
		}
	}

	return a.themes.Validate(name, fallback)
}

// language is Actions\Localization::GetLanguage().
//
// AppData() calls it without the $bAdmin argument, so the webmail language (and
// the webmail settings) are used in the admin area too.
func (a *AppData) language(admin bool, resolved *account.Account, acceptLanguageHeader string) string {
	config := a.config()

	var language string
	if admin {
		language = config.GetString("admin_panel", "language", "en")
	} else {
		language = config.GetString("webmail", "language", "en")
		if nil != resolved {
			if config.GetBool("login", "determine_user_language", true) {
				language = a.validateLanguage(a.detectClientLanguage(admin, acceptLanguageHeader), language, false)
			}
			if config.GetBool("webmail", "allow_languages_on_settings", true) {
				if settings := a.mainSettings(resolved); nil != settings {
					language = phpString(settings.GetConf("language", language))
				}
			}
		} else if config.GetBool("login", "allow_languages_on_login", true) &&
			config.GetBool("login", "determine_user_language", true) {
			language = a.validateLanguage(a.detectClientLanguage(admin, acceptLanguageHeader), language, false)
		}
	}

	language = a.validateLanguage(language, "", admin)
	if "" == language {
		language = "en"
	}

	// PHP: $sHookLanguage = $sLanguage = ValidateLanguage(...) ?: 'en';
	//      RunHook('filter.language', [&$sHookLanguage, $bAdmin]);
	hooked := language
	if nil != a.options.Plugins {
		a.options.Plugins.RunHook("filter.language", &hooked, admin)
	}

	return a.validateLanguage(hooked, language, admin)
}

// validateLanguage is Actions\Localization::ValidateLanguage() with the
// $bAllowEmptyResult default of false.
func (a *AppData) validateLanguage(language, fallback string, admin bool) string {
	return a.validateLanguageResult(language, fallback, admin, false)
}

// validateLanguageEmpty is ValidateLanguage() with $bAllowEmptyResult = true:
// an unknown language yields "" instead of the configured one.
func (a *AppData) validateLanguageEmpty(language, fallback string, admin bool) string {
	return a.validateLanguageResult(language, fallback, admin, true)
}

// validateLanguageResult is Actions\Localization::ValidateLanguage().
func (a *AppData) validateLanguageResult(language, fallback string, admin, allowEmpty bool) string {
	result := a.languages.ValidLanguage(language, admin)
	if "" == result {
		result = a.languages.ValidLanguage(fallback, admin)
	}

	if "" != result || allowEmpty {
		return result
	}

	sectionName := "webmail"
	if admin {
		sectionName = "admin_panel"
	}

	configured := a.config().GetString(sectionName, "language", "en")
	if "" != a.languages.ValidLanguage(configured, admin) {
		return configured
	}

	return "en"
}

// detectClientLanguage is Actions\Localization::detectClientLanguage(): the
// first Accept-Language entry that resolves to an available language, else "".
func (a *AppData) detectClientLanguage(admin bool, acceptLanguageHeader string) string {
	for _, code := range l10n.AcceptLanguage(acceptLanguageHeader) {
		if language := a.validateLanguageEmpty(code, "", admin); "" != language {
			return language
		}
	}

	return ""
}
