package config

import (
	"errors"
	"fmt"
	"strings"
)

// iniEntry is one key/value pair of an INI file: the section it belongs to, the
// key and the raw string value.
type iniEntry struct {
	section string
	key     string
	value   string
}

// iniDocument is a parsed INI file.
type iniDocument struct {
	// entries holds the key/value pairs in file order.
	entries []iniEntry
	// topLevel counts the top level array keys PHP's parse_ini_file() would
	// return: the sections plus any key outside a section. AbstractConfig::Load()
	// treats a file with zero top level keys as unparsable and overwrites it.
	topLevel int
}

// reservedValues maps the INI reserved words to the strings PHP's
// parse_ini_file() substitutes for them (INI_SCANNER_NORMAL is still string
// typed, so only these six words differ from their literal text).
var reservedValues = map[string]string{
	"true":  "1",
	"on":    "1",
	"yes":   "1",
	"false": "",
	"off":   "",
	"no":    "",
	"none":  "",
	"null":  "",
}

// parseINI parses the subset of the INI format PHP's parse_ini_file($file, true)
// accepts: [section] headers, key = value pairs, ";" and "#" comments, quoted
// values with the "," and "\" escapes the writer emits, inline ";" comments and
// the reserved words above.
//
// Values stay strings, like INI_SCANNER_NORMAL: the typing is done by Set(),
// which casts by the type of the default value.
func parseINI(content string) (*iniDocument, error) {
	document := &iniDocument{}

	sectionName := ""
	lineNumber := 0

	for _, line := range splitLines(content) {
		lineNumber++
		trimmed := strings.TrimSpace(line)

		if "" == trimmed || ';' == trimmed[0] || '#' == trimmed[0] {
			continue
		}

		if '[' == trimmed[0] {
			end := strings.IndexByte(trimmed, ']')
			if 0 > end {
				return nil, fmt.Errorf("invalid section header on line %d", lineNumber)
			}

			// PHP tolerates a trailing comment after the closing bracket.
			if rest := strings.TrimSpace(trimmed[end+1:]); "" != rest && ';' != rest[0] && '#' != rest[0] {
				return nil, fmt.Errorf("unexpected %q after section header on line %d", rest, lineNumber)
			}

			sectionName = strings.TrimSpace(trimmed[1:end])
			document.topLevel++

			continue
		}

		separator := strings.IndexByte(trimmed, '=')
		if 0 > separator {
			return nil, fmt.Errorf("invalid line %d: %q", lineNumber, trimmed)
		}

		key := strings.TrimSpace(trimmed[:separator])
		if "" == key {
			return nil, fmt.Errorf("missing key on line %d", lineNumber)
		}

		value, err := parseIniValue(trimmed[separator+1:])
		if nil != err {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}

		if "" == sectionName {
			// Keys outside a section end up in the top level array, which the
			// config loop ignores because it only walks arrays.
			document.topLevel++
			continue
		}

		document.entries = append(document.entries, iniEntry{
			section: sectionName,
			key:     key,
			value:   value,
		})
	}

	return document, nil
}

// splitLines splits content on "\n" and strips a trailing "\r", like PHP reads
// text files.
func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// parseIniValue parses the right hand side of a key = value line.
func parseIniValue(raw string) (string, error) {
	value := strings.TrimLeft(raw, " \t")
	if "" == value {
		return "", nil
	}

	if '"' != value[0] {
		// Unquoted: an inline ";" comment ends the value, the rest is trimmed.
		if comment := strings.IndexByte(value, ';'); 0 <= comment {
			value = value[:comment]
		}
		value = strings.TrimSpace(value)
		if reserved, ok := reservedValues[strings.ToLower(value)]; ok {
			return reserved, nil
		}
		return value, nil
	}

	var result strings.Builder
	index := 1
	for ; index < len(value); index++ {
		character := value[index]

		if '\\' == character && index+1 < len(value) {
			switch value[index+1] {
			case '"', '\\':
				// The only escapes AbstractConfig::Save() writes.
				result.WriteByte(value[index+1])
				index++
				continue
			}
		}

		if '"' == character {
			break
		}

		result.WriteByte(character)
	}

	if index >= len(value) {
		return "", errors.New("unterminated quoted value")
	}

	if rest := strings.TrimSpace(value[index+1:]); "" != rest && ';' != rest[0] && '#' != rest[0] {
		return "", fmt.Errorf("unexpected %q after quoted value", rest)
	}

	return result.String(), nil
}
