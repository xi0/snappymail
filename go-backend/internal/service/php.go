package service

// The PHP value helpers the service endpoints share: the casts of the settings
// documents (which hold the JSON types json_decode() produced) and the JSON
// encoder of Utils::jsonEncode().

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// encodeJSON renders a value like Utils::jsonEncode(): JSON_UNESCAPED_UNICODE
// and no HTML escaping, so a payload matches what the PHP backend sends.
func encodeJSON(value any) ([]byte, error) {
	var builder strings.Builder

	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(value); nil != err {
		return nil, err
	}

	return []byte(strings.TrimRight(builder.String(), "\n")), nil
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// phpString ports PHP's (string) cast for the types a settings document or a
// JSON parameter can hold.
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

// phpInt ports PHP's (int) cast: a leading number wins, everything else is 0.
func phpInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case json.Number:
		if number, err := typed.Int64(); nil == err {
			return int(number)
		}
		if number, err := typed.Float64(); nil == err {
			return int(number)
		}
		return 0
	case string:
		trimmed := strings.TrimSpace(typed)
		if number, err := strconv.Atoi(trimmed); nil == err {
			return number
		}
		if number, err := strconv.ParseFloat(trimmed, 64); nil == err {
			return int(number)
		}
	}
	return 0
}

// phpBool ports PHP's (bool) cast: "" and "0" and 0 and an empty array are
// false, every other string is true.
func phpBool(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return "" != typed && "0" != typed
	case int:
		return 0 != typed
	case int64:
		return 0 != typed
	case float64:
		return 0 != typed
	case []any:
		return 0 < len(typed)
	case map[string]any:
		return 0 < len(typed)
	}
	return true
}

// phpEmpty ports PHP's empty(): a missing key, null, "", "0", 0, false and an
// empty array are empty.
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
	case []string:
		return 0 == len(typed)
	}
	return false
}

// phpMax is PHP's max() for two ints.
func phpMax(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// phpMin is PHP's min() for two ints.
func phpMin(left, right int) int {
	if left < right {
		return left
	}
	return right
}
