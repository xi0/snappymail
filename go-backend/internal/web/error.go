package web

import (
	"html"
	"io"
	"net/http"
	"os"
	"strings"
)

// backLinkText is the en translation of StaticI18N('BACK_LINK'). Step 11
// replaces it with the l10n lookup of the request language.
const backLinkText = "Reload"

// writeError answers with app/templates/Error.html like
// ServiceActions::ErrorTemplates(), which is what both gates of
// Service::RunResult() use.
func (rt *Router) writeError(w http.ResponseWriter, r *http.Request, status int, title, description string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	if nil != r && http.MethodHead == r.Method {
		return
	}

	_, _ = io.WriteString(w, rt.errorTemplate(title, description, true))
}

// errorTemplate ports ServiceActions::ErrorTemplates().
//
// Deviation from PHP: the title and description are HTML escaped. PHP
// interpolates them raw, but the Sec-Fetch gate passes request headers into
// the description, which would be a reflected XSS vector.
func (rt *Router) errorTemplate(title, description string, backLink bool) string {
	visibility := "display:none"
	if backLink {
		visibility = "display:inline-block"
	}

	replacer := strings.NewReplacer(
		"{{ErrorTitle}}", html.EscapeString(title),
		"{{ErrorHeader}}", html.EscapeString(title),
		"{{ErrorDesc}}", html.EscapeString(description),
		"{{BackLinkVisibilityStyle}}", visibility,
		"{{BackLink}}", backLinkText,
		"{{BackHref}}", "./",
	)

	template, err := os.ReadFile(rt.paths.VersionFile("app/templates/Error.html"))
	if nil != err {
		return "<!DOCTYPE html>\n<html lang=\"en\" dir=\"ltr\"><head><meta charset=\"utf-8\">" +
			"<title>" + html.EscapeString(title) + "</title></head><body><h1>" +
			html.EscapeString(title) + "</h1><p>" + html.EscapeString(description) +
			"</p><p><a href=\"./\">" + backLinkText + "</a></p></body></html>\n"
	}

	return replacer.Replace(string(template))
}
