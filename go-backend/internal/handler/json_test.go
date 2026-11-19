package handler

import (
	"strings"
	"testing"
)

func TestEncodeJSONDoesNotEscapeHTML(t *testing.T) {
	encoded, err := encodeJSON(map[string]any{"html": "<a href=\"/x\">&</a>"})
	if nil != err {
		t.Fatalf("encodeJSON: %v", err)
	}

	text := string(encoded)
	if strings.Contains(text, `\u003c`) || strings.Contains(text, `\u003e`) || strings.Contains(text, `\u0026`) {
		t.Errorf("HTML characters were escaped: %s", text)
	}
	// The quotes inside the JSON string are escaped as \" by any JSON encoder;
	// what matters is that "<", ">" and "&" are written literally.
	if !strings.Contains(text, "<a href=") || !strings.Contains(text, ">&</a>") {
		t.Errorf("HTML characters changed: %s", text)
	}
}

func TestEncodeJSONKeepsUnicode(t *testing.T) {
	encoded, err := encodeJSON(map[string]any{"text": "héllo"})
	if nil != err {
		t.Fatalf("encodeJSON: %v", err)
	}

	if !strings.Contains(string(encoded), "héllo") {
		t.Errorf("unicode was escaped: %s", encoded)
	}
}

func TestEncodeJSONHasNoTrailingNewline(t *testing.T) {
	encoded, err := encodeJSON(map[string]any{"a": 1})
	if nil != err {
		t.Fatalf("encodeJSON: %v", err)
	}

	if strings.HasSuffix(string(encoded), "\n") {
		t.Errorf("encodeJSON kept the encoder newline: %q", encoded)
	}
}
