package handler

import (
	"bytes"
	"encoding/json"
)

// encodeJSON renders a value like Utils::jsonEncode(): JSON_UNESCAPED_UNICODE
// (UTF-8, no \uXXXX for non-ASCII) and no HTML escaping, so "<", ">" and "&"
// stay readable.
//
// Go's default encoder escapes "<", ">" and "&" to \u003c, \u003e, \u0026, which
// PHP does not; SetEscapeHTML(false) turns that off. The trailing newline the
// stream encoder appends is trimmed.
func encodeJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); nil != err {
		return nil, err
	}

	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
