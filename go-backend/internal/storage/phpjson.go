package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// object is an ordered JSON object: the Go stand-in for the PHP arrays that
// RainLoop\Settings::jsonSerialize() returns. A Go map would lose the key order,
// which is part of the file layout PHP writes.
type object []field

// field is one key/value pair of an object.
type field struct {
	key   string
	value any
}

// array is a JSON list, kept distinct from an object the way a PHP list differs
// from a PHP array with string keys.
type array []any

// encodeJSON renders value like json_encode($value) without flags: no
// whitespace, escaped "/" and \uXXXX for everything outside the printable ASCII
// range. Numbers are written as decoded, so an integer stays an integer.
func encodeJSON(value any) ([]byte, error) {
	var out strings.Builder
	if err := writeJSON(&out, value); nil != err {
		return nil, err
	}
	return []byte(out.String()), nil
}

// writeJSON writes one value.
func writeJSON(out *strings.Builder, value any) error {
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
	case json.Number:
		out.WriteString(typed.String())
	case string:
		writeString(out, typed)
	case object:
		out.WriteByte('{')
		for index, entry := range typed {
			if 0 < index {
				out.WriteByte(',')
			}
			writeString(out, entry.key)
			out.WriteByte(':')
			if err := writeJSON(out, entry.value); nil != err {
				return err
			}
		}
		out.WriteByte('}')
	case array:
		out.WriteByte('[')
		for index, item := range typed {
			if 0 < index {
				out.WriteByte(',')
			}
			if err := writeJSON(out, item); nil != err {
				return err
			}
		}
		out.WriteByte(']')
	case []any:
		return writeJSON(out, array(typed))
	case map[string]any:
		// A plain Go map has no meaningful order; sorting keeps the output
		// deterministic instead of randomising the file on every save.
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		ordered := make(object, 0, len(keys))
		for _, key := range keys {
			ordered = append(ordered, field{key, typed[key]})
		}
		return writeJSON(out, ordered)
	default:
		return fmt.Errorf("storage: cannot encode %T as JSON", value)
	}
	return nil
}

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

// decodeJSON is json_decode($content, true) that keeps the object key order and
// the literal form of the numbers.
//
// The result is nil, false or a JSON value built from object, array, string,
// bool, nil and json.Number.
func decodeJSON(content []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()

	value, err := decodeValue(decoder)
	if nil != err {
		return nil, err
	}

	// A second value (or any trailing byte) means the document is not what PHP's
	// json_decode() would accept either.
	switch _, trailing := decoder.Token(); {
	case nil == trailing:
		return nil, fmt.Errorf("storage: trailing data after JSON value")
	case errors.Is(trailing, io.EOF):
		return value, nil
	default:
		return nil, trailing
	}
}

// decodeValue reads one JSON value, keeping objects ordered.
func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if nil != err {
		return nil, err
	}

	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			result := object{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if nil != err {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("storage: unexpected object key %v", keyToken)
				}
				value, err := decodeValue(decoder)
				if nil != err {
					return nil, err
				}
				result = append(result, field{key, value})
			}
			if _, err := decoder.Token(); nil != err {
				return nil, err
			}
			return result, nil

		case '[':
			result := array{}
			for decoder.More() {
				value, err := decodeValue(decoder)
				if nil != err {
					return nil, err
				}
				result = append(result, value)
			}
			if _, err := decoder.Token(); nil != err {
				return nil, err
			}
			return result, nil
		}
		return nil, fmt.Errorf("storage: unexpected delimiter %v", typed)
	}

	return token, nil
}

// phpString ports PHP's (string) cast for the value types decodeJSON and
// Settings.SetConf() produce.
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
	case json.Number:
		return typed.String()
	}
	return ""
}

// objectFrom returns value as an ordered object, or nil when it is not one
// (json_decode() of a list, a scalar or null).
func objectFrom(value any) object {
	if typed, ok := value.(object); ok {
		return typed
	}
	return nil
}
