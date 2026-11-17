package config

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The casts below mirror PHP's (bool)/(int)/(float)/(string) conversions.
// AbstractConfig::Set() casts by the type of the default value, so a value read
// from the INI file (always a string, see parseINI) ends up typed exactly like
// it does in PHP.

// scalar returns value when PHP would call it a scalar, else nil. Non scalar
// values become null before the cast (AbstractConfig::Set()).
func scalar(value any) any {
	switch value.(type) {
	case bool, int, float64, string:
		return value
	}
	return nil
}

// castLike casts value to the PHP type of current.
func castLike(current, value any) any {
	value = scalar(value)

	switch current.(type) {
	case bool:
		return phpBool(value)
	case int:
		return phpInt(value)
	case float64:
		return phpFloat(value)
	default:
		return phpString(value)
	}
}

// phpBool mirrors PHP's (bool) cast: "" and "0" are the only false strings.
func phpBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int:
		return 0 != typed
	case float64:
		return 0 != typed
	case string:
		return "" != typed && "0" != typed
	}
	return false
}

// phpInt mirrors PHP's (int) cast, including the numeric prefix of a string.
func phpInt(value any) int {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1
		}
		return 0
	case int:
		return typed
	case float64:
		return floatToInt(typed)
	case string:
		number, _ := leadingNumber(typed)
		return floatToInt(number)
	}
	return 0
}

// phpFloat mirrors PHP's (float) cast, including the numeric prefix of a
// string.
func phpFloat(value any) float64 {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1
		}
		return 0
	case int:
		return float64(typed)
	case float64:
		return typed
	case string:
		number, _ := leadingNumber(typed)
		return number
	}
	return 0
}

// phpString mirrors PHP's (string) cast: true is "1", false is "" and floats
// use the shortest representation without an exponent ("2", "1.5").
func phpString(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return typed
	}
	return ""
}

// floatToInt truncates towards zero like PHP's float to int conversion, with
// saturation instead of the undefined behaviour of an out of range cast.
func floatToInt(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	if value <= math.MinInt64 {
		return math.MinInt64
	}
	return int(value)
}

// leadingNumber returns the numeric prefix of value, like PHP's string to
// number conversion: leading white space, an optional sign, digits with an
// optional decimal point and an optional exponent. Everything after the number
// is ignored ("1.5abc" is 1.5, "abc" is 0, "0x1A" is 0).
func leadingNumber(value string) (float64, bool) {
	index := 0
	for index < len(value) && isSpace(value[index]) {
		index++
	}

	start := index
	if index < len(value) && ('+' == value[index] || '-' == value[index]) {
		index++
	}

	digits := 0
	for index < len(value) && isDigit(value[index]) {
		index++
		digits++
	}

	if index < len(value) && '.' == value[index] {
		index++
		for index < len(value) && isDigit(value[index]) {
			index++
			digits++
		}
	}

	if 0 == digits {
		return 0, false
	}

	// Only consume the exponent when it is complete, so "1e" stays 1.
	if index < len(value) && ('e' == value[index] || 'E' == value[index]) {
		next := index + 1
		if next < len(value) && ('+' == value[next] || '-' == value[next]) {
			next++
		}
		exponentDigits := 0
		for next < len(value) && isDigit(value[next]) {
			next++
			exponentDigits++
		}
		if 0 < exponentDigits {
			index = next
		}
	}

	number, err := strconv.ParseFloat(value[start:index], 64)
	if nil != err {
		return 0, false
	}

	return number, true
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool { return '0' <= c && '9' >= c }

// isSpace reports whether c is white space PHP's number conversion skips.
func isSpace(c byte) bool {
	return ' ' == c || '\t' == c || '\n' == c || '\r' == c || '\v' == c || '\f' == c
}

// iniKeyPattern is the character class of AbstractConfig::isValidIniKey().
var iniKeyPattern = regexp.MustCompile("[\x00-\x1F\x7F\"#;=\\[\\]]")

// isValidIniKey reports whether key may be written to the file unquoted.
// Keys are written unquoted, so they must not be able to inject a new section
// or parameter (AbstractConfig::isValidIniKey()).
func isValidIniKey(key string) bool {
	return "" != key && strings.TrimSpace(key) == key && !iniKeyPattern.MatchString(key)
}

// languagePattern is a cheap syntax check for a bare language code; the real
// list (en, da, pt-BR, zh-TW, …) comes from internal/l10n in step 11.
var languagePattern = regexp.MustCompile(`^[a-z]{2,3}$`)

// defaultValidateLanguage is the placeholder for
// SnappyMail\L10n::validLanguage() until internal/l10n lands in step 11: it
// only checks the syntax of the code (PHP compares it against the on-disk
// language folders and returns null, i.e. "" here, when the language is
// unknown).
//
// Like PHP, a regional code falls back to the bare language ("sv-SE" → "sv").
// Without the folder list every regional code falls back, so "pt-BR" becomes
// "pt" until step 11 supplies the list.
func defaultValidateLanguage(language string, admin bool) string {
	language = strings.ReplaceAll(strings.TrimSpace(language), "_", "-")

	if languagePattern.MatchString(language) {
		return language
	}

	if base, _, found := strings.Cut(language, "-"); found && languagePattern.MatchString(base) {
		return base
	}

	return ""
}

// DefaultCipherSupported is the placeholder for
// SnappyMail\Crypt::cipherSupported() until internal/cryptox lands in step 6:
// the AES-CBC methods Go can implement, including their -hmac-sha1/-hmac-sha256
// forms. Everything else is replaced by DefaultCipher on load.
func DefaultCipherSupported(cipher string) bool {
	switch strings.ToLower(cipher) {
	case "aes-128-cbc", "aes-192-cbc", "aes-256-cbc",
		"aes-128-cbc-hmac-sha1", "aes-192-cbc-hmac-sha1", "aes-256-cbc-hmac-sha1",
		"aes-128-cbc-hmac-sha256", "aes-192-cbc-hmac-sha256", "aes-256-cbc-hmac-sha256":
		return true
	}
	return false
}
