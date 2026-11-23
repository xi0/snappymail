package handler

// The PHP array and value helpers the settings documents need.
//
// RainLoop\Settings stores the CheckableFolder list as a JSON *string*
// (json_encode() of a PHP array). Because array_unique()/array_diff() and the
// "[]=" append keep the original integer keys, json_encode() writes a JSON list
// for contiguous keys and a JSON object when a key is missing. Both bytes and
// behaviour are visible to the PHP backend, which reads the same file, so the
// gap is reproduced here instead of being normalised away.

import (
	"encoding/json"
	"strconv"
	"strings"
)

// phpArray is a PHP array of strings with explicit integer keys.
type phpArray struct {
	keys   []int
	values []string
}

// phpArrayOf builds a phpArray from values, appending them the way "[]=" does.
func phpArrayOf(values []string) *phpArray {
	array := &phpArray{}
	for _, value := range values {
		array.append(value)
	}
	return array
}

// append is $a[] = $value: the next free integer key.
func (a *phpArray) append(value string) {
	key := 0
	if 0 < len(a.keys) {
		key = a.keys[len(a.keys)-1] + 1
	}
	a.keys = append(a.keys, key)
	a.values = append(a.values, value)
}

// indexOf is array_search().
func (a *phpArray) indexOf(value string) (int, bool) {
	for index, item := range a.values {
		if item == value {
			return index, true
		}
	}
	return 0, false
}

// contains is in_array().
func (a *phpArray) contains(value string) bool {
	_, ok := a.indexOf(value)
	return ok
}

// spliceRemove is array_splice($a, $offset, 1): the remaining keys are
// renumbered.
func (a *phpArray) spliceRemove(offset int) {
	if 0 > offset || offset >= len(a.values) {
		return
	}
	a.values = append(a.values[:offset], a.values[offset+1:]...)
	a.keys = a.keys[:len(a.values)]
	for index := range a.keys {
		a.keys[index] = index
	}
}

// remove is array_diff($a, [$value]): matching entries are dropped and the keys
// of the remaining ones are kept.
func (a *phpArray) remove(value string) *phpArray {
	result := &phpArray{}
	for index, item := range a.values {
		if item == value {
			continue
		}
		result.keys = append(result.keys, a.keys[index])
		result.values = append(result.values, item)
	}
	return result
}

// unique is array_unique(): the first occurrence wins and the keys are kept.
func (a *phpArray) unique() *phpArray {
	result := &phpArray{}
	seen := make(map[string]struct{}, len(a.values))
	for index, item := range a.values {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result.keys = append(result.keys, a.keys[index])
		result.values = append(result.values, item)
	}
	return result
}

// values returns the values in key order.
func (a *phpArray) valuesCopy() []string {
	return append([]string(nil), a.values...)
}

// encode is json_encode(): a list for contiguous keys, an object otherwise.
func (a *phpArray) encode() string {
	list := true
	for index, key := range a.keys {
		if key != index {
			list = false
			break
		}
	}

	var builder strings.Builder
	if list {
		builder.WriteString("[")
		for index, value := range a.values {
			if 0 < index {
				builder.WriteString(",")
			}
			builder.WriteString(jsonString(value))
		}
		builder.WriteString("]")
		return builder.String()
	}

	builder.WriteString("{")
	for index, value := range a.values {
		if 0 < index {
			builder.WriteString(",")
		}
		builder.WriteString(jsonString(strconv.Itoa(a.keys[index])))
		builder.WriteString(":")
		builder.WriteString(jsonString(value))
	}
	builder.WriteString("}")
	return builder.String()
}

// jsonString renders one JSON string like Utils::jsonEncode().
func jsonString(value string) string {
	encoded, err := encodeJSON(value)
	if nil != err {
		return `""`
	}
	return string(encoded)
}

// decodeCheckableFolders is json_decode(GetConf('CheckableFolder', '[]')).
//
// The settings value is a JSON string; a hand edited document may already hold
// a decoded array. A JSON object (the array_unique() gap quirk) is not an array
// in PHP either, so it yields an empty list.
func decodeCheckableFolders(value any) []string {
	if text, ok := value.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); nil != err {
			return []string{}
		}
		return stringList(decoded)
	}
	return stringList(value)
}

// stringList converts a JSON list of strings into []string.
func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{}
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// phpTruthy is PHP's (bool) cast for the values a JSON parameter holds.
func phpTruthy(value any) bool { return !phpEmpty(value) }
