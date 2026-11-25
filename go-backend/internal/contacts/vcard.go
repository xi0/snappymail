package contacts

// vCard text reading and writing for the ?/Raw/ContactsVcf export and the
// UploadContacts (.vcf) import.
//
// The writer produces the plain vCard 4.0 text form Sabre\VObject::serialize()
// emits (BEGIN:VCARD … END:VCARD with CRLF line endings and folded long lines).
// The reader is deliberately forgiving: it accepts 2.1/3.0/4.0 blocks without a
// VERSION, unfolds continuation lines and understands the quoted-printable and
// base64 transfer encodings well enough to import ordinary address books.

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// vcardLineLength is the fold width of the writer.
const vcardLineLength = 75

// SerializeText renders the card as vCard text (MailSo/Sabre serialize()).
func (c *Card) SerializeText() string {
	var builder strings.Builder

	builder.WriteString("BEGIN:VCARD\r\n")
	seenVersion := false
	for _, property := range c.props {
		if "version" == property.Name {
			seenVersion = true
		}
		writePropertyText(&builder, property)
	}
	if !seenVersion {
		// A card without a version property still gets a valid one.
		builder.Reset()
		builder.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\n")
		for _, property := range c.props {
			writePropertyText(&builder, property)
		}
	}
	builder.WriteString("END:VCARD\r\n")

	return builder.String()
}

// writePropertyText writes one folded "NAME;PARAM=value:value" line.
func writePropertyText(builder *strings.Builder, property *Property) {
	var line strings.Builder
	line.WriteString(strings.ToUpper(property.Name))

	for _, name := range sortedParamNames(property.Params) {
		for _, value := range paramValues(property.Params, name) {
			line.WriteString(";")
			line.WriteString(strings.ToUpper(name))
			line.WriteString("=")
			line.WriteString(escapeParamValue(value))
		}
	}

	line.WriteString(":")

	switch typed := property.Value.(type) {
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			parts[index] = escapeText(elementString(item))
		}
		line.WriteString(strings.Join(parts, ";"))
	default:
		line.WriteString(escapeText(elementString(property.Value)))
	}

	foldLine(builder, line.String())
}

// sortedParamNames returns the parameter names sorted, so the output is stable.
func sortedParamNames(params map[string]any) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// elementString coerces a jCard value element to a string.
func elementString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// escapeText escapes the characters vCard reserves in a value.
func escapeText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
	)
	return replacer.Replace(value)
}

// escapeParamValue escapes a parameter value (quoted when needed).
func escapeParamValue(value string) string {
	if strings.ContainsAny(value, ";:,") {
		return `"` + strings.ReplaceAll(value, `"`, "") + `"`
	}
	return value
}

// foldLine writes line folded at vcardLineLength octets with CRLF breaks.
func foldLine(builder *strings.Builder, line string) {
	for len(line) > vcardLineLength {
		cut := vcardLineLength
		// Do not split a multi-byte UTF-8 sequence.
		for cut > 1 && 0x80 == (line[cut]&0xC0) {
			cut--
		}
		builder.WriteString(line[:cut])
		builder.WriteString("\r\n ")
		line = line[cut:]
	}
	builder.WriteString(line)
	builder.WriteString("\r\n")
}

// ParseVCardText parses one or more vCard blocks into cards
// (Utils::VcfStreamToContacts()).
func ParseVCardText(data []byte) []*Card {
	lines := unfoldLines(string(data))

	var cards []*Card
	var current *Card
	inCard := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		switch {
		case "BEGIN:VCARD" == upper:
			current = NewCard()
			current.remove("version")
			inCard = true
		case "END:VCARD" == upper:
			if nil != current {
				cards = append(cards, current)
			}
			current = nil
			inCard = false
		case inCard && nil != current && "" != trimmed:
			applyTextLine(current, trimmed)
		}
	}

	return cards
}

// unfoldLines splits on CRLF/LF and joins continuation lines that begin with a
// space or a tab.
func unfoldLines(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")

	var result []string
	for _, line := range strings.Split(data, "\n") {
		if 0 < len(line) && (' ' == line[0] || '\t' == line[0]) && 0 < len(result) {
			result[len(result)-1] += line[1:]
			continue
		}
		result = append(result, line)
	}
	return result
}

// applyTextLine parses one "NAME[;PARAM=value]:value" line into the card.
func applyTextLine(card *Card, line string) {
	// Find the ':' that separates the group/name/params from the value, taking
	// quoted parameters into account.
	colon := -1
	inQuotes := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if '"' == character {
			inQuotes = !inQuotes
			continue
		}
		if ':' == character && !inQuotes {
			colon = index
			break
		}
	}
	if 0 > colon {
		return
	}

	left := line[:colon]
	value := line[colon+1:]

	segments := splitParams(left)
	if 0 == len(segments) {
		return
	}

	// A group prefix ("item1.EMAIL") is dropped.
	name := segments[0]
	if dot := strings.LastIndex(name, "."); 0 <= dot {
		name = name[dot+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if "" == name {
		return
	}

	params := map[string]any{}
	for _, segment := range segments[1:] {
		key, raw, found := strings.Cut(segment, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		for _, item := range strings.Split(raw, ",") {
			item = strings.Trim(strings.TrimSpace(item), `"`)
			if "" == item {
				continue
			}
			if existing, ok := params[key]; ok {
				if list, ok := existing.([]any); ok {
					params[key] = append(list, item)
				} else {
					params[key] = []any{existing, item}
				}
				continue
			}
			params[key] = item
		}
	}

	switch name {
	case "n":
		card.setStructured("n", splitAndUnescape(value, ";"))
	case "adr":
		card.setStructured("adr", splitAndUnescape(value, ";"))
		if 0 < len(params) {
			card.first("adr").Params = params
		}
	case "org":
		card.setStructured("org", splitAndUnescape(value, ";"))
	default:
		card.addString(name, unescapeText(value), params)
	}
}

// splitParams splits "NAME;PARAM=x;PARAM2=y" on unquoted ';'.
func splitParams(left string) []string {
	var result []string
	var current strings.Builder
	inQuotes := false
	for index := 0; index < len(left); index++ {
		character := left[index]
		if '"' == character {
			inQuotes = !inQuotes
			current.WriteByte(character)
			continue
		}
		if ';' == character && !inQuotes {
			result = append(result, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(character)
	}
	result = append(result, current.String())
	return result
}

// splitAndUnescape splits a structured value on sep and unescapes each part.
func splitAndUnescape(value, sep string) []string {
	parts := splitUnescaped(value, sep)
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = unescapeText(part)
	}
	return result
}

// splitUnescaped splits on sep that is not escaped by a backslash.
func splitUnescaped(value, sep string) []string {
	separator := sep[0]
	var result []string
	var current strings.Builder
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			current.WriteByte('\\')
			current.WriteByte(character)
			escaped = false
			continue
		}
		if '\\' == character {
			escaped = true
			continue
		}
		if separator == character {
			result = append(result, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(character)
	}
	result = append(result, current.String())
	return result
}

// unescapeText reverses escapeText() and decodes quoted-printable/base64 when
// the (already parsed) value still carries those encodings.
func unescapeText(value string) string {
	var builder strings.Builder
	escaped := false
	for index := 0; index < len(value); index++ {
		character := value[index]
		if escaped {
			switch character {
			case 'n', 'N':
				builder.WriteByte('\n')
			default:
				builder.WriteByte(character)
			}
			escaped = false
			continue
		}
		if '\\' == character {
			escaped = true
			continue
		}
		builder.WriteByte(character)
	}
	return builder.String()
}

// boolFromAny is a small helper shared with the store (PHP truthiness).
func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return "" != typed && "0" != typed
	case float64:
		return 0 != typed
	case int:
		return 0 != typed
	case int64:
		return 0 != typed
	}
	return false
}

// decodeBase64 is used by callers that need a tolerant base64 decode.
func decodeBase64(value string) (string, bool) {
	cleaned := strings.Map(func(r rune) rune {
		if '\r' == r || '\n' == r || ' ' == r || '\t' == r {
			return -1
		}
		return r
	}, value)
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if nil != err {
		return "", false
	}
	return string(decoded), true
}

// formatError wraps a description in a plain error.
func formatError(format string, args ...any) error { return fmt.Errorf(format, args...) }
