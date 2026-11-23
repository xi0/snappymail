package mail

// Tests of the SORT whitelist and the search string parser (Step 16).

import "testing"

func TestSortCriteriaValidation(t *testing.T) {
	cases := []struct {
		sort    string
		display bool
		valid   bool
	}{
		{"", false, true},
		{"REVERSE DATE", false, true},
		{"FROM", false, true},
		{"REVERSE SUBJECT DATE", false, true},
		{"DISPLAYFROM", false, false},
		{"DISPLAYFROM", true, true},
		{"REVERSE", false, false},
		{"REVERSE REVERSE DATE", false, false},
		{"DATE)", false, false},
		{"BYE", false, false},
	}

	for _, test := range cases {
		if got := ValidateSortCriteria(test.sort, test.display); got != test.valid {
			t.Errorf("ValidateSortCriteria(%q, %v) = %v, want %v", test.sort, test.display, got, test.valid)
		}
	}

	// Normalisation uppercases and collapses whitespace.
	if got := NormalizeSortCriteria("  reverse   date ", false); "REVERSE DATE" != got {
		t.Errorf("normalize = %q", got)
	}
	if got := NormalizeSortCriteria("BYE", false); "" != got {
		t.Errorf("invalid normalize = %q", got)
	}
}

func TestParseQueryString(t *testing.T) {
	values := parseQueryString("from=foo@example.com&subject=hello&unseen&flagged")
	if text, _ := values.get("FROM"); "foo@example.com" != text {
		t.Errorf("FROM = %v", text)
	}
	if text, _ := values.get("SUBJECT"); "hello" != text {
		t.Errorf("SUBJECT = %v", text)
	}
	if value, ok := values.get("UNSEEN"); !ok || true != value {
		t.Errorf("UNSEEN = %v", value)
	}
	if value, ok := values.get("FLAGGED"); !ok || true != value {
		t.Errorf("FLAGGED = %v", value)
	}
}

func TestParseSearchString(t *testing.T) {
	values := parseSearchString(`from:"foo" is:unseen,flagged`)
	if text, _ := values.get("FROM"); "foo" != text {
		t.Errorf("FROM = %v", text)
	}
	if value, ok := values.get("UNSEEN"); !ok || true != value {
		t.Errorf("UNSEEN = %v", value)
	}
	if value, ok := values.get("FLAGGED"); !ok || true != value {
		t.Errorf("FLAGGED = %v", value)
	}
}

func TestParseFriendlySize(t *testing.T) {
	if got := parseFriendlySize("1M"); 1024*1024 != got {
		t.Errorf("1M = %d", got)
	}
	if got := parseFriendlySize("2K"); 2048 != got {
		t.Errorf("2K = %d", got)
	}
	if got := parseFriendlySize("500"); 500 != got {
		t.Errorf("500 = %d", got)
	}
}
