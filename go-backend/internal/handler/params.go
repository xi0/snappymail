package handler

import (
	"strconv"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// maxParamDepth is the "if (16 < $iDepth)" guard of normalizeActionParams().
// Request parameters are fully client controlled, so the depth is bounded
// before the handler ever sees them.
const maxParamDepth = 16

// normalizeParams is Actions::normalizeActionParams(): reject non-scalar leaves
// and arbitrarily deep nesting, keep everything else.
//
// Go's JSON decoder only produces nil, bool, float64, string, map[string]any
// and []any, so the PHP object/resource branch can not be triggered from the
// wire. The depth guard stays because a nested JSON document can be deeper than
// the handlers expect.
func normalizeParams(params map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(params))
	for key, value := range params {
		normalized, err := normalizeValue(value, 1)
		if nil != err {
			return nil, err
		}
		result[key] = normalized
	}
	return result, nil
}

// normalizeValue is the recursive part of normalizeActionParams(); depth is the
// $iDepth the value would be processed at.
func normalizeValue(value any, depth int) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if maxParamDepth < depth {
			return nil, nestingTooDeep()
		}
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeValue(item, depth+1)
			if nil != err {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil

	case []any:
		if maxParamDepth < depth {
			return nil, nestingTooDeep()
		}
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeValue(item, depth+1)
			if nil != err {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil

	default:
		// nil and every scalar pass through unchanged.
		return value, nil
	}
}

// nestingTooDeep is the ClientException of the depth guard.
func nestingTooDeep() error {
	return clientError(appctx.InvalidInputArgument, "Action parameter nesting too deep")
}

// clientError builds the ClientException of a parse failure: the notification
// name as the message and the specific reason as the additional message.
func clientError(code int, message string) error {
	return appctx.NewClientError(code, nil, message)
}

// GetParam is Actions::GetActionParam().
//
// Like PHP, a parameter that is present but null counts as missing (isset()),
// and a client controlled value of the wrong shape falls back to the type of
// the default the handler declared.
func (c *Context) GetParam(key string, defaultValue any) any {
	value, ok := c.params[key]
	if !ok || nil == value {
		return defaultValue
	}

	if isArrayLike(defaultValue) {
		if !isArrayLike(value) {
			return defaultValue
		}
	} else if isArrayLike(value) && nil != defaultValue {
		return defaultValue
	}

	return value
}

// HasParam is Actions::HasActionParam() (isset(), so a null value is absent).
func (c *Context) HasParam(key string) bool {
	value, ok := c.params[key]
	return ok && nil != value
}

// Params is Actions::GetActionParams().
func (c *Context) Params() map[string]any { return c.params }

// isArrayLike reports whether value is a PHP array, i.e. a JSON list or object.
func isArrayLike(value any) bool {
	switch value.(type) {
	case []any, map[string]any:
		return true
	}
	return false
}

// phpEmpty ports PHP's empty() for the values a decoded JSON document holds.
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

// phpString ports PHP's (string) cast for the scalar types decoded JSON holds.
func phpString(value any) string {
	switch typed := value.(type) {
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

// scalarString is PHP's "(is_scalar($v) ? (string) $v : '')" used when logging a
// client controlled value.
func scalarString(value any) string {
	switch value.(type) {
	case string, bool, int, int64, float64:
		return phpString(value)
	}
	return ""
}
