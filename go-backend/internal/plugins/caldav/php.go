package caldav

// The small PHP helpers the ported plugin code needs. Keeping them here makes it
// obvious which quirks are reproduced on purpose.

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// phpString is PHP's (string) cast for the values a decoded JSON document, an
// account model or a plugin setting holds.
func phpString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	return ""
}

// phpEmpty ports PHP's empty() for the values the plugin sees.
func phpEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return "" == typed || "0" == typed
	case bool:
		return !typed
	case int:
		return 0 == typed
	case int64:
		return 0 == typed
	case float64:
		return 0 == typed
	case []any:
		return 0 == len(typed)
	case map[string]any:
		return 0 == len(typed)
	}
	return false
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// jsonQuote is json_encode() of a single string with PHP's default flags: the
// escaping of "/" as "\/" and of every non-ASCII rune as \uXXXX (with surrogate
// pairs above the BMP) are what make the "calendar_sync" document byte identical
// to the one the PHP backend writes.
func jsonQuote(value string) string {
	var builder strings.Builder

	builder.WriteByte('"')
	for index := 0; index < len(value); {
		character := value[index]

		// The escaped ASCII characters first, so a multi byte sequence never
		// has to be inspected byte by byte.
		switch character {
		case '"':
			builder.WriteString(`\"`)
			index++
			continue
		case '\\':
			builder.WriteString(`\\`)
			index++
			continue
		case '/':
			builder.WriteString(`\/`)
			index++
			continue
		case '\b':
			builder.WriteString(`\b`)
			index++
			continue
		case '\f':
			builder.WriteString(`\f`)
			index++
			continue
		case '\n':
			builder.WriteString(`\n`)
			index++
			continue
		case '\r':
			builder.WriteString(`\r`)
			index++
			continue
		case '\t':
			builder.WriteString(`\t`)
			index++
			continue
		}

		if 0x20 > character {
			builder.WriteString(uEscape(uint16(character)))
			index++
			continue
		}

		r, size := utf8.DecodeRuneInString(value[index:])
		if utf8.RuneError == r && 1 == size {
			// Invalid UTF-8: PHP's json_encode() fails; the replacement
			// character keeps the document parseable instead.
			builder.WriteRune(utf8.RuneError)
			index++
			continue
		}
		index += size

		if 0x80 <= r {
			// json_encode() escapes every non-ASCII rune (0x7F stays as it is);
			// utf16.Encode produces the surrogate pair above the BMP.
			for _, unit := range utf16.Encode([]rune{r}) {
				builder.WriteString(uEscape(unit))
			}
			continue
		}

		builder.WriteRune(r)
	}
	builder.WriteByte('"')

	return builder.String()
}

// uEscape is the \uXXXX sequence of one UTF-16 code unit.
func uEscape(unit uint16) string {
	const digits = "0123456789abcdef"

	return string([]byte{
		'\\', 'u',
		digits[(unit>>12)&0xF],
		digits[(unit>>8)&0xF],
		digits[(unit>>4)&0xF],
		digits[unit&0xF],
	})
}

// randomSuffix is substr(md5(uniqid('', true)), 0, 8): the fallback a
// non-alphanumeric calendar name gets (and the last resort of
// uniqueCalendarId()).
func randomSuffix() string {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); nil != err {
		return "00000000"
	}
	return md5Hex(hex.EncodeToString(buffer[:]))[:8]
}
