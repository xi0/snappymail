package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// object is an ordered JSON object: the Go stand-in for the PHP arrays that
// jsonSerialize() returns. A Go map would lose the key order, which is part of
// the file layout PHP writes.
type object []field

// field is one key/value pair of an object.
type field struct {
	key   string
	value any
}

// marshalPretty renders value like json_encode($value, JSON_PRETTY_PRINT):
// four spaces of indentation, '": "' between key and value, escaped '/', and
// \uXXXX for everything outside the printable ASCII range. There is no trailing
// newline, exactly like json_encode().
func marshalPretty(value any) ([]byte, error) {
	var out strings.Builder
	if err := writeJSON(&out, value, 0); nil != err {
		return nil, err
	}
	return []byte(out.String()), nil
}

// writeJSON writes one value at the given nesting depth.
func writeJSON(out *strings.Builder, value any, depth int) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(typed))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		writeFloat(out, typed)
	case string:
		writeString(out, typed)
	case object:
		return writeObject(out, typed, depth)
	case []string:
		return writeArray(out, len(typed), depth, func(index int) error {
			writeString(out, typed[index])
			return nil
		})
	case []any:
		return writeArray(out, len(typed), depth, func(index int) error {
			return writeJSON(out, typed[index], depth+1)
		})
	default:
		return fmt.Errorf("domain: cannot encode %T as JSON", value)
	}
	return nil
}

// writeObject writes an ordered object; an empty one is "{}".
func writeObject(out *strings.Builder, value object, depth int) error {
	if 0 == len(value) {
		out.WriteString("{}")
		return nil
	}

	out.WriteString("{\n")
	for index, entry := range value {
		if 0 < index {
			out.WriteString(",\n")
		}
		out.WriteString(indent(depth + 1))
		writeString(out, entry.key)
		out.WriteString(": ")
		if err := writeJSON(out, entry.value, depth+1); nil != err {
			return err
		}
	}
	out.WriteString("\n")
	out.WriteString(indent(depth))
	out.WriteString("}")

	return nil
}

// writeArray writes a list; an empty one is "[]".
func writeArray(out *strings.Builder, length, depth int, item func(index int) error) error {
	if 0 == length {
		out.WriteString("[]")
		return nil
	}

	out.WriteString("[\n")
	for index := 0; index < length; index++ {
		if 0 < index {
			out.WriteString(",\n")
		}
		out.WriteString(indent(depth + 1))
		if err := item(index); nil != err {
			return err
		}
	}
	out.WriteString("\n")
	out.WriteString(indent(depth))
	out.WriteString("]")

	return nil
}

// indent returns the four spaces per level JSON_PRETTY_PRINT uses.
func indent(depth int) string { return strings.Repeat("    ", depth) }

// writeFloat writes a float the way PHP does with serialize_precision = -1: the
// shortest representation that round-trips, with a ".0" for whole numbers.
func writeFloat(out *strings.Builder, value float64) {
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eEnN") {
		text += ".0"
	}
	out.WriteString(text)
}

// writeString writes a JSON string with PHP's escaping: quote, backslash, slash,
// the short control escapes and \uXXXX for the rest.
func writeString(out *strings.Builder, value string) {
	out.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '/':
			out.WriteString(`\/`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			switch {
			case 0x20 > character:
				fmt.Fprintf(out, `\u%04x`, character)
			case 0x80 > character:
				out.WriteRune(character)
			case 0xFFFF < character:
				// UTF-16 surrogate pair, like json_encode().
				code := character - 0x10000
				fmt.Fprintf(out, `\u%04x\u%04x`, 0xD800+(code>>10&0x3FF), 0xDC00+(code&0x3FF))
			default:
				fmt.Fprintf(out, `\u%04x`, character)
			}
		}
	}
	out.WriteByte('"')
}
