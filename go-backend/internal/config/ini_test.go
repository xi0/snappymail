package config

import (
	"strings"
	"testing"
)

func TestParseINI(t *testing.T) {
	content := strings.Join([]string{
		"; a comment",
		"# another comment",
		"",
		`root_key = "ignored"`,
		"[first]",
		"plain = value",
		"spaced = value with spaces   ",
		`quoted = "  keep  spacing  "`,
		`escaped = "a \"quote\" and a \\ backslash"`,
		"unknownEscape = \"a \\n stays\"",
		"inline = value ; comment",
		`inlineQuoted = "value ; not a comment"`,
		"on = On",
		"off = Off",
		"yes = yes",
		"no = no",
		"none = none",
		"null = null",
		"number = 20",
		"decimal = 1.5",
		"hash = a#b",
		"[second]",
		"empty =",
		`emptyQuoted = ""`,
	}, "\n")

	document, err := parseINI(content)
	if nil != err {
		t.Fatalf("parseINI: %v", err)
	}

	want := []iniEntry{
		{"first", "plain", "value"},
		{"first", "spaced", "value with spaces"},
		{"first", "quoted", "  keep  spacing  "},
		{"first", "escaped", `a "quote" and a \ backslash`},
		{"first", "unknownEscape", `a \n stays`},
		{"first", "inline", "value"},
		{"first", "inlineQuoted", "value ; not a comment"},
		{"first", "on", "1"},
		{"first", "off", ""},
		{"first", "yes", "1"},
		{"first", "no", ""},
		{"first", "none", ""},
		{"first", "null", ""},
		{"first", "number", "20"},
		{"first", "decimal", "1.5"},
		{"first", "hash", "a#b"},
		{"second", "empty", ""},
		{"second", "emptyQuoted", ""},
	}

	if len(want) != len(document.entries) {
		t.Fatalf("entries = %d, want %d: %+v", len(document.entries), len(want), document.entries)
	}
	for index, entry := range want {
		if entry != document.entries[index] {
			t.Errorf("entry %d = %+v, want %+v", index, document.entries[index], entry)
		}
	}

	// One root key plus two sections, like count(parse_ini_file(..., true)).
	if 3 != document.topLevel {
		t.Errorf("topLevel = %d, want 3", document.topLevel)
	}
}

func TestParseINIEmpty(t *testing.T) {
	for _, content := range []string{"", "\n\n", "; only a comment\n# and another\n"} {
		document, err := parseINI(content)
		if nil != err {
			t.Fatalf("parseINI(%q): %v", content, err)
		}
		if 0 != document.topLevel {
			t.Errorf("parseINI(%q).topLevel = %d, want 0", content, document.topLevel)
		}
	}
}

func TestParseINIErrors(t *testing.T) {
	for _, content := range []string{
		"[no closing bracket\nkey = value\n",
		"key without separator\n",
		"= value\n",
		"[section]\nkey = \"unterminated\n",
		"[section] junk\n",
		"[section]\nkey = \"value\" junk\n",
	} {
		if _, err := parseINI(content); nil == err {
			t.Errorf("parseINI(%q) accepted an invalid file", content)
		}
	}
}

// TestWriterParserRoundTrip checks that every value Save() writes is read back
// unchanged, which the PHP backend relies on.
func TestWriterParserRoundTrip(t *testing.T) {
	values := []any{
		true,
		false,
		0,
		20,
		0.0,
		1.5,
		"",
		"plain",
		`a "quoted" \ backslash`,
		"line;with;semicolons",
		"hash#tag",
		"trailing space ",
		";starts with a semicolon",
	}

	for _, value := range values {
		rendered := renderValue(value)

		parsed, err := parseIniValue(rendered)
		if nil != err {
			t.Errorf("parseIniValue(%q) from %#v: %v", rendered, value, err)
			continue
		}

		if got, want := castLike(value, parsed), value; got != want {
			t.Errorf("%#v rendered as %q parsed back as %#v, want %#v", value, rendered, got, want)
		}
	}
}
