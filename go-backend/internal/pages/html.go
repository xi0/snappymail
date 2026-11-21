package pages

// The HTML helpers of \RainLoop\Utils and the template rendering.

import (
	"regexp"
	"strings"
)

// The rewrites of Utils::ClearHtmlOutput(), applied in order.
var (
	// clearSelfClosing is '@\s*/>'.
	clearSelfClosing = regexp.MustCompile(`\s*/>`)
	// clearBeforeNbsp is '/\s*&nbsp;/i'.
	clearBeforeNbsp = regexp.MustCompile(`(?i)\s*&nbsp;`)
	// clearAfterNbsp is '/&nbsp;\s*/i'.
	clearAfterNbsp = regexp.MustCompile(`(?i)&nbsp;\s*`)
	// clearControlWhitespace is '/[\r\n\t]+/'.
	clearControlWhitespace = regexp.MustCompile(`[\r\n\t]+`)
	// clearBetweenTags is '/>\s+</'.
	clearBetweenTags = regexp.MustCompile(`>\s+<`)
)

// nonBreakingSpace is the UTF-8 encoded &nbsp; the PHP replacements insert.
const nonBreakingSpace = "\xC2\xA0"

// ClearHTMLOutput is Utils::ClearHtmlOutput(): the whitespace normalisation of
// the compiled templates and the index template.
func ClearHTMLOutput(html string) string {
	result := strings.Trim(html, " \t\n\r\x00\x0B")

	result = clearSelfClosing.ReplaceAllString(result, ">")
	result = clearBeforeNbsp.ReplaceAllString(result, nonBreakingSpace)
	result = clearAfterNbsp.ReplaceAllString(result, nonBreakingSpace)
	result = clearControlWhitespace.ReplaceAllString(result, " ")
	result = clearBetweenTags.ReplaceAllString(result, "> <")

	return result
}

// newlinePattern matches the newlines nl2br() prefixes.
var newlinePattern = regexp.MustCompile(`\r\n|\r|\n`)

// nl2br is PHP's nl2br() with the XHTML "<br />" output.
func nl2br(value string) string {
	return newlinePattern.ReplaceAllStringFunc(value, func(match string) string {
		return "<br />" + match
	})
}

// htmlEscapeReplacer is htmlspecialchars() for ENT_QUOTES (the same entity
// spelling PHP uses).
var htmlEscapeReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#039;",
)

// escapeHTML is htmlspecialchars($value, ENT_QUOTES|ENT_IGNORE, 'UTF-8').
func escapeHTML(value string) string {
	return htmlEscapeReplacer.Replace(value)
}
