package l10n

// Port of Actions\Localization::compileLanguage(): the JavaScript text served
// by ?/Lang/0/{App,Admin}/<language>/<version>/ and inlined into the index
// page.
//
// The output has three parts:
//
//	document.documentElement.lang = '<language>';
//	rl.I18N = <langs.json merged with the language texts>;
//	rl.relativeTime = <relativetimeformat.js of the language, base or en>;
//
// The JSON part is encoded with JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES
// | JSON_NUMERIC_CHECK, so numeric-looking strings become JSON numbers.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// LangReader is the plugin part of compileLanguage(): Plugins\Manager::ReadLang().
//
// A nil plugin manager means no plugin texts.
type LangReader interface {
	ReadLang(language string, lang map[string]any)
}

// leadingWhitespacePattern is the '/^\s+/' of the relativetimeformat.js read.
var leadingWhitespacePattern = regexp.MustCompile(`^\s+`)

// CompileLanguage is Actions\Localization::compileLanguage().
func (c *Catalog) CompileLanguage(language string, admin bool, plugins LangReader) (string, error) {
	language = strings.ReplaceAll(language, "_", "-")

	document := map[string]any{}
	if nil != c {
		if langs, ok := c.readLangsJSON(); ok {
			document = langs
		}

		// array_flip(getLanguages()) then array_intersect_key().
		available := map[string]bool{}
		for _, code := range c.Languages(admin) {
			available[code] = true
		}
		for _, key := range []string{"LANGS_NAMES", "LANGS_NAMES_EN"} {
			if names, ok := document[key].(map[string]any); ok {
				document[key] = IntersectKeys(names, available)
			}
		}

		area := "user"
		if admin {
			area = "admin"
		}
		document = MergeRecursive(document, c.Load(language, area))
	}

	if nil != plugins {
		plugins.ReadLang(language, document)
	}

	encoded, err := encodeNumeric(document)
	if nil != err {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString("document.documentElement.lang = '")
	builder.WriteString(language)
	builder.WriteString("';\nrl.I18N=")
	builder.Write(encoded)
	builder.WriteString(";\nrl.relativeTime = ")
	builder.WriteString(c.relativeTimeFormat(language))
	builder.WriteString(";")

	return builder.String(), nil
}

// readLangsJSON reads app/localization/langs.json.
func (c *Catalog) readLangsJSON() (map[string]any, bool) {
	content, err := os.ReadFile(filepath.Join(c.localizationDir, "langs.json"))
	if nil != err {
		return nil, false
	}

	var document map[string]any
	if err := json.Unmarshal(content, &document); nil != err || nil == document {
		return nil, false
	}

	return document, true
}

// relativeTimeFormat is the relativetimeformat.js lookup of compileLanguage():
// the requested language, its two letter base and finally en. The leading
// whitespace of the file is stripped; a missing file yields "".
func (c *Catalog) relativeTimeFormat(language string) string {
	base := language
	if 2 < len(base) {
		base = base[:2]
	}

	if nil == c {
		return ""
	}

	for _, candidate := range []string{language, base, "en"} {
		path := filepath.Join(c.versionRoot, "app", "localization", candidate, "relativetimeformat.js")

		content, err := os.ReadFile(path)
		if nil != err {
			continue
		}

		return leadingWhitespacePattern.ReplaceAllString(string(content), "")
	}

	return ""
}

// numericStringPattern is the subset of is_numeric() that JSON_NUMERIC_CHECK
// converts: an optional sign, an integer with optional fraction and exponent,
// surrounded by optional whitespace. Hexadecimal strings are not numeric in
// modern PHP and stay strings.
var numericStringPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// integerStringPattern matches the numeric strings json_encode() would emit as
// an integer (no fraction, no exponent).
var integerStringPattern = regexp.MustCompile(`^[+-]?[0-9]+$`)

// encodeNumeric is json_encode($value, JSON_UNESCAPED_UNICODE |
// JSON_UNESCAPED_SLASHES | JSON_NUMERIC_CHECK).
func encodeNumeric(value any) ([]byte, error) {
	var builder strings.Builder

	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(numericCheck(value)); nil != err {
		return nil, err
	}

	return []byte(strings.TrimRight(builder.String(), "\n")), nil
}

// numericCheck recursively converts numeric-looking strings to json.Number.
func numericCheck(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = numericCheck(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = numericCheck(item)
		}
		return result
	case string:
		return numericString(typed)
	}
	return value
}

// numericString returns a json.Number for a string PHP would treat as numeric,
// and the original string otherwise.
func numericString(value string) any {
	trimmed := strings.TrimSpace(value)
	if "" == trimmed || !numericStringPattern.MatchString(trimmed) {
		return value
	}

	if integerStringPattern.MatchString(trimmed) {
		if number, err := strconv.ParseInt(trimmed, 10, 64); nil == err {
			return json.Number(strconv.FormatInt(number, 10))
		}
	}

	if number, err := strconv.ParseFloat(trimmed, 64); nil == err {
		// PHP's serialize_precision = -1 uses the shortest round-trippable
		// representation, which FormatFloat('g', -1) matches.
		return json.Number(strconv.FormatFloat(number, 'g', -1, 64))
	}

	return value
}
