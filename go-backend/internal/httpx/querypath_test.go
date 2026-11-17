package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// phpPaths is the expected path vector of Service::RunResult() for the given
// request URI.
func phpPaths(t *testing.T, target string) []string {
	t.Helper()

	return Paths(phpRequest(t, target))
}

// phpRequest builds a GET request for a raw request URI. It is used instead of
// httptest.NewRequest() because the PHP path vector allows spaces in the query
// string, which net/http's request line parser rejects.
func phpRequest(t *testing.T, target string) *http.Request {
	t.Helper()

	parsed, err := url.Parse(target)
	if nil != err {
		t.Fatalf("url.Parse(%q): %v", target, err)
	}

	return &http.Request{
		Method:     http.MethodGet,
		URL:        parsed,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{},
		Host:       "example.com",
		RequestURI: target,
	}
}

func TestQueryPathFollowsPHPRunResult(t *testing.T) {
	tests := []struct {
		target string
		want   []string
	}{
		// explode('/', '') yields one empty element.
		{"/", []string{""}},
		{"/?", []string{""}},
		{"/?/", []string{""}},
		{"/?//", []string{""}},
		// plain query-string vectors
		{"/?/Ping/0/", []string{"Ping", "0"}},
		{"/?/AppData/0/123456/", []string{"AppData", "0", "123456"}},
		{"/?/Raw/0/RawDownload/abc/", []string{"Raw", "0", "RawDownload", "abc"}},
		// "q[]" is appended to the query string, element by element
		{"/?/Json/&q[]=/0/", []string{"Json", "0"}},
		{"/?/Json/&q[]=/0/&q[]=/Folders/", []string{"Json", "0", "Folders"}},
		{"/?/Json/&q[]=/0/&q[]=/MessageList/&q[]=/abc/", []string{"Json", "0", "MessageList", "abc"}},
		// the indexed PHP array form counts too, in query-string order
		{"/?/Json/&q[0]=/0/&q[1]=/Folders/", []string{"Json", "0", "Folders"}},
		// a scalar "q" is not an array in PHP, so it is ignored
		{"/?q=/Ping/0/", []string{"q=", "Ping", "0"}},
		{"/?/Ping/&q=/0/", []string{"Ping"}},
		// everything after the first "&" only counts when it is q[]
		{"/?/Ping/0/&foo=/bar/", []string{"Ping", "0"}},
		// the query string is trimmed, elements are not (PHP only trims the
		// ends of the whole string)
		{"/?  /Ping/0/  ", []string{"Ping", "0"}},
		{"/?/Ping/&q[]=/ 0 /", []string{"Ping", "0"}},
		{"/?/ Ping / 0 /", []string{"Ping ", " 0"}},
		// percent encoded values are decoded
		{"/?/Lang/&q[]=/%30/%2F/", []string{"Lang", "0"}},
		// PATH_INFO style front controller requests (Apache AcceptPathInfo)
		{"/index.php/Raw/0/RawDownload/abc/", []string{"Raw", "0", "RawDownload", "abc"}},
		{"/index.php", []string{""}},
		{"/index.php/", []string{""}},
		{"/index.php.bak/Ping", []string{""}},
		{"/mail/index.php/AppData/0/1/", []string{"AppData", "0", "1"}},
		// the admin key is still part of the vector here
		{"/?/admin/Ping/0/", []string{"admin", "Ping", "0"}},
	}

	for _, test := range tests {
		got := phpPaths(t, test.target)
		if strings.Join(got, "|") != strings.Join(test.want, "|") {
			t.Errorf("Paths(%q) = %q, want %q", test.target, got, test.want)
		}
	}
}

func TestQueryPathEmptyAndNil(t *testing.T) {
	if got := QueryPath(nil); "" != got {
		t.Errorf("QueryPath(nil) = %q, want an empty string", got)
	}
	if got := Paths(nil); 1 != len(got) || "" != got[0] {
		t.Errorf("Paths(nil) = %q, want one empty element", got)
	}
}

func TestQueryPathKeepsRequestOrder(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?/Json/&q[]=/b/&q[]=/a/&q[]=/c/", nil)
	if got := QueryPath(r); "Json/b/a/c" != got {
		t.Fatalf("QueryPath = %q, want Json/b/a/c", got)
	}
}

func TestTrimPath(t *testing.T) {
	tests := map[string]string{
		"":            "",
		"/":           "",
		"  /Ping/  ":  "Ping",
		"\t/a/b\r\n":  "a/b",
		"//":          "",
		"/Ping/0/":    "Ping/0",
		"/a/ b /":     "a/ b",
		"\x00/a/\x0b": "a",
	}

	for value, want := range tests {
		if got := TrimPath(value); got != want {
			t.Errorf("TrimPath(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestSplitSegment(t *testing.T) {
	tests := []struct {
		segment string
		name    string
		extra   string
	}{
		{"Ping", "Ping", ""},
		{"", "", ""},
		{"ProxyExternal", "ProxyExternal", ""},
		{"ProxyExternal@aHR0cHM6Ly8", "ProxyExternal", "aHR0cHM6Ly8"},
		// strpos() === 0 is falsey, so there is no extra part
		{"@abc", "", ""},
		// the regex needs a character after "@", so the name keeps it
		{"Ping@", "Ping@", ""},
		{"a@b@c", "a", "b@c"},
	}

	for _, test := range tests {
		name, extra := SplitSegment(test.segment)
		if name != test.name || extra != test.extra {
			t.Errorf("SplitSegment(%q) = (%q, %q), want (%q, %q)",
				test.segment, name, extra, test.name, test.extra)
		}
	}
}
