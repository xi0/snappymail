package handler

import (
	"testing"
)

// serializableValue is a JSONSerializable model used by the responseObject test.
type serializableValue struct{ value any }

func (s serializableValue) JSONSerialize() any { return s.value }

func TestDefaultResponseFalseAddsDefaults(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	response := dispatcher.defaultResponse(false, nil, nil)

	if "" != response["Action"] {
		t.Errorf("Action = %v, want the empty default", response["Action"])
	}
	if false != response["Result"] {
		t.Errorf("Result = %v, want false", response["Result"])
	}
	if 0 != codeOf(response) {
		t.Errorf("code = %v, want 0", response["code"])
	}
	if message, ok := response["message"]; !ok || "" != message {
		t.Errorf("message = %v, want the empty default", response["message"])
	}
}

func TestDefaultResponseTrueHasNoErrorFields(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	response := dispatcher.defaultResponse(true, nil, nil)
	if true != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}
	if _, ok := response["code"]; ok {
		t.Error("a true result must not add code")
	}
	if _, ok := response["message"]; ok {
		t.Error("a true result must not add message")
	}
}

func TestDefaultResponseKeepsExplicitErrorFields(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	response := dispatcher.defaultResponse(false, map[string]any{
		"code":    903,
		"message": "boom",
	}, nil)

	if 903 != codeOf(response) || "boom" != response["message"] {
		t.Errorf("explicit fields were overwritten: %v", response)
	}
}

func TestFalseResponse(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)
	context := &Context{dispatcher: dispatcher}

	response := context.FalseResponse(101, "InvalidToken[101]", "Session gone")

	if false != response["Result"] || 101 != codeOf(response) {
		t.Errorf("response = %v", response)
	}
	if "Session gone" != response["messageAdditional"] {
		t.Errorf("messageAdditional = %v", response["messageAdditional"])
	}
}

func TestTrueResponse(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)
	context := &Context{dispatcher: dispatcher}

	response := context.TrueResponse(map[string]any{"extra": 1})

	if true != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}
	if 1 != response["extra"] {
		t.Errorf("extra = %v", response["extra"])
	}
}

func TestResponseObjectPassesSerializableThrough(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	input := map[string]any{
		"model": serializableValue{value: map[string]any{"a": 1}},
		"list":  []any{serializableValue{value: "x"}},
	}

	result, ok := dispatcher.responseObject(input, "", nil).(map[string]any)
	if !ok {
		t.Fatalf("responseObject returned %T", dispatcher.responseObject(input, "", nil))
	}

	model, ok := result["model"].(map[string]any)
	if !ok || 1 != model["a"] {
		t.Errorf("model = %v", result["model"])
	}

	list, ok := result["list"].([]any)
	if !ok || 1 != len(list) || "x" != list[0] {
		t.Errorf("list = %v", result["list"])
	}
}

func TestResponseObjectKeepsScalars(t *testing.T) {
	dispatcher := newTestDispatcher(nil, nil, nil)

	for _, value := range []any{"text", true, 42.0, nil, false} {
		if got := dispatcher.responseObject(value, "", nil); got != value {
			t.Errorf("responseObject(%#v) = %#v", value, got)
		}
	}
}
