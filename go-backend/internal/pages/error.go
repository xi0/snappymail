package pages

// app/templates/Error.html rendering (ServiceActions::ErrorTemplates()).

import (
	"os"
	"path/filepath"
	"strings"
)

// backLinkKey is the static localization key of the back link text.
const backLinkKey = "BACK_LINK"

// errorFallbackLink is the back link text used when no language is wired.
const errorFallbackLink = "Reload"

// ErrorTemplate is ServiceActions::ErrorTemplates(). The title and description
// are escaped (PHP interpolates them raw, but the description can carry request
// data).
//
// actions supplies the localized "BACK_LINK" text and may be nil.
func (r *Renderer) ErrorTemplate(actions Actions, title, description string, backLink bool) string {
	visibility := "display:none"
	if backLink {
		visibility = "display:inline-block"
	}

	backText := errorFallbackLink
	if text := staticI18N(actions, backLinkKey); "" != text && backLinkKey != text {
		backText = text
	}

	replacer := strings.NewReplacer(
		"{{ErrorTitle}}", escapeHTML(title),
		"{{ErrorHeader}}", escapeHTML(title),
		"{{ErrorDesc}}", escapeHTML(description),
		"{{BackLinkVisibilityStyle}}", visibility,
		"{{BackLink}}", escapeHTML(backText),
		"{{BackHref}}", "./",
	)

	template, err := os.ReadFile(filepath.Join(r.versionRoot(), "app", "templates", "Error.html"))
	if nil != err {
		return "<!DOCTYPE html>\n<html lang=\"en\" dir=\"ltr\"><head><meta charset=\"utf-8\">" +
			"<title>" + escapeHTML(title) + "</title></head><body class=\"content\">" +
			"<div class=\"error-header\">" + escapeHTML(title) + "</div>" +
			"<div class=\"error-desc\">" + escapeHTML(description) + "</div>" +
			"</body></html>\n"
	}

	return replacer.Replace(string(template))
}
