package handler

import (
	"errors"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// nested builds a value nested depth arrays deep.
func nested(depth int) any {
	var value any = "leaf"
	for index := 0; index < depth; index++ {
		value = []any{value}
	}
	return value
}

func TestNormalizeParamsAllowsSixteenLevels(t *testing.T) {
	if _, err := normalizeParams(map[string]any{"a": nested(maxParamDepth)}); nil != err {
		t.Fatalf("depth %d must be accepted: %v", maxParamDepth, err)
	}
}

func TestNormalizeParamsRejectsTooDeep(t *testing.T) {
	_, err := normalizeParams(map[string]any{"a": nested(maxParamDepth + 1)})

	var clientErr *appctx.ClientError
	if !errors.As(err, &clientErr) {
		t.Fatalf("error is not a ClientError: %v", err)
	}
	if appctx.InvalidInputArgument != clientErr.Code {
		t.Errorf("code = %d, want %d", clientErr.Code, appctx.InvalidInputArgument)
	}
}

func TestNormalizeParamsKeepsScalars(t *testing.T) {
	params, err := normalizeParams(map[string]any{
		"string": "text",
		"number": 42.0,
		"bool":   true,
		"null":   nil,
		"list":   []any{"a", "b"},
	})
	if nil != err {
		t.Fatalf("normalizeParams: %v", err)
	}

	if "text" != params["string"] || 42.0 != params["number"] || true != params["bool"] {
		t.Errorf("scalars changed: %v", params)
	}
	if _, ok := params["null"]; !ok {
		t.Error("a null value must be kept (but is absent for isset())")
	}
	if list, ok := params["list"].([]any); !ok || 2 != len(list) {
		t.Errorf("list = %v", params["list"])
	}
}

func TestGetParamFallbacks(t *testing.T) {
	context := &Context{params: map[string]any{
		"scalar": "value",
		"list":   []any{"a"},
		"null":   nil,
	}}

	// A null value counts as missing (isset()).
	if got := context.GetParam("null", "default"); "default" != got {
		t.Errorf("null GetParam = %v, want the default", got)
	}
	// A missing key yields the default.
	if got := context.GetParam("missing", "default"); "default" != got {
		t.Errorf("missing GetParam = %v, want the default", got)
	}
	// A scalar value with a scalar default is returned as-is.
	if got := context.GetParam("scalar", "default"); "value" != got {
		t.Errorf("scalar GetParam = %v", got)
	}
	// A list value with a non-nil scalar default falls back to the default.
	if got := context.GetParam("list", "default"); "default" != got {
		t.Errorf("list GetParam with scalar default = %v, want the default", got)
	}
	// A list value with an array default is returned.
	if got, ok := context.GetParam("list", []any{}).([]any); !ok || 1 != len(got) {
		t.Errorf("list GetParam with array default = %v", context.GetParam("list", []any{}))
	}
	// A scalar value with an array default falls back to the default.
	if got, ok := context.GetParam("scalar", []any{}).([]any); !ok || 0 != len(got) {
		t.Errorf("scalar GetParam with array default = %v", context.GetParam("scalar", []any{}))
	}
}

func TestHasParamTreatsNullAsAbsent(t *testing.T) {
	context := &Context{params: map[string]any{
		"set":   "value",
		"null":  nil,
		"empty": "",
	}}

	if !context.HasParam("set") {
		t.Error("set must be present")
	}
	if context.HasParam("null") {
		t.Error("a null value must be absent (isset())")
	}
	// An empty string is set in PHP, so HasActionParam is true.
	if !context.HasParam("empty") {
		t.Error("an empty string must be present")
	}
	if context.HasParam("missing") {
		t.Error("a missing key must be absent")
	}
}

func TestPhpEmpty(t *testing.T) {
	empty := []any{nil, "", "0", false, 0.0, []any{}, map[string]any{}}
	for _, value := range empty {
		if !phpEmpty(value) {
			t.Errorf("phpEmpty(%#v) = false, want true", value)
		}
	}

	notEmpty := []any{"x", true, 1.0, []any{"a"}, map[string]any{"a": 1}}
	for _, value := range notEmpty {
		if phpEmpty(value) {
			t.Errorf("phpEmpty(%#v) = true, want false", value)
		}
	}
}

func TestPhpString(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{"text", "text"},
		{true, "1"},
		{false, ""},
		{42.0, "42"},
		{42.5, "42.5"},
		{nil, ""},
		{[]any{}, ""},
	}
	for _, test := range tests {
		if got := phpString(test.value); test.want != got {
			t.Errorf("phpString(%#v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestScalarString(t *testing.T) {
	if got := scalarString([]any{"x"}); "" != got {
		t.Errorf("scalarString(array) = %q, want empty", got)
	}
	if got := scalarString("x"); "x" != got {
		t.Errorf("scalarString(string) = %q", got)
	}
}
