package httpx

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, target string) *http.Request {
	t.Helper()

	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestHostValidation(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"example.com", "example.com"},
		{"www.example.com", "www.example.com"},
		{"example.com:8080", "example.com:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"[::1]:8080", "[::1]:8080"},
		{"[2001:db8::1]", "[2001:db8::1]"},
		{"sub-domain.example.co.uk", "sub-domain.example.co.uk"},
		// Host header injection attempts
		{"", ""},
		{"   ", ""},
		{"example..com", ""},
		{"evil.com/path", ""},
		{"evil.com\\path", ""},
		{"user@example.com", ""},
		{"example.com:80:80", ""},
		{"<script>alert(1)</script>", ""},
		{"example.com:123456", ""},
	}

	for _, test := range tests {
		r := request(t, "/")
		r.Host = test.host

		if got := ValidHost(test.host); got != test.want {
			t.Errorf("ValidHost(%q) = %q, want %q", test.host, got, test.want)
		}
		if got := Host(r, false, false); got != test.want {
			t.Errorf("Host(%q) = %q, want %q", test.host, got, test.want)
		}
	}
}

func TestHostOptions(t *testing.T) {
	r := request(t, "/")
	r.Host = "www.Example.com:8080"

	if got := Host(r, true, false); "Example.com:8080" != got {
		t.Errorf("Host(withoutWWW) = %q, want Example.com:8080", got)
	}
	if got := Host(r, false, true); "www.Example.com" != got {
		t.Errorf("Host(withoutPort) = %q, want www.Example.com", got)
	}
	if got := Host(r, true, true); "Example.com" != got {
		t.Errorf("Host(withoutWWW, withoutPort) = %q, want Example.com", got)
	}

	r.Host = "[::1]:8080"
	if got := Host(r, false, true); "[::1]" != got {
		t.Errorf("Host(bracketed IPv6, withoutPort) = %q, want [::1]", got)
	}

	r.Host = ""
	if got := Host(r, false, false); "" != got {
		t.Errorf("Host(empty) = %q, want an empty string", got)
	}
}

func TestClientIP(t *testing.T) {
	r := request(t, "/")
	r.RemoteAddr = "1.2.3.4:12345"

	if got := ClientIP(r, false); "1.2.3.4" != got {
		t.Errorf("ClientIP = %q, want 1.2.3.4", got)
	}

	// Proxy headers are ignored unless they are trusted.
	r.Header.Set("X-Forwarded-For", "9.9.9.9")
	if got := ClientIP(r, false); "1.2.3.4" != got {
		t.Errorf("ClientIP(untrusted proxy) = %q, want 1.2.3.4", got)
	}
	if got := ClientIP(r, true); "9.9.9.9" != got {
		t.Errorf("ClientIP(trusted proxy) = %q, want 9.9.9.9", got)
	}

	r.Header.Set("X-Forwarded-For", "garbage, 8.8.8.8, 7.7.7.7")
	if got := ClientIP(r, true); "8.8.8.8" != got {
		t.Errorf("ClientIP(first valid) = %q, want 8.8.8.8", got)
	}

	r.Header.Set("Client-IP", "6.6.6.6")
	if got := ClientIP(r, true); "6.6.6.6" != got {
		t.Errorf("ClientIP(Client-IP wins) = %q, want 6.6.6.6", got)
	}

	// An invalid remote address yields no IP at all.
	r.Header.Del("Client-IP")
	r.Header.Del("X-Forwarded-For")
	r.RemoteAddr = "not-an-ip"
	if got := ClientIP(r, false); "" != got {
		t.Errorf("ClientIP(invalid) = %q, want an empty string", got)
	}
}

func TestIsSecureAndScheme(t *testing.T) {
	if IsSecure(request(t, "http://example.com/")) {
		t.Error("an http:// request must not be secure")
	}

	if !IsSecure(request(t, "https://example.com/")) {
		t.Error("an https:// request must be secure")
	}

	// Behind a TLS terminating proxy.
	r := request(t, "http://example.com/")
	r.Header.Set("X-Forwarded-Proto", "https")
	if !IsSecure(r) {
		t.Error("X-Forwarded-Proto: https must be secure")
	}
	if "https" != Scheme(r) {
		t.Errorf("Scheme = %q, want https", Scheme(r))
	}

	r = request(t, "http://example.com/")
	r.Header.Set("X-Forwarded-Ssl", "on")
	if !IsSecure(r) {
		t.Error("X-Forwarded-Ssl: on must be secure")
	}

	// SERVER_PORT 443 without any proxy header.
	r = request(t, "http://example.com/")
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}))
	if !IsSecure(r) {
		t.Error("a local port of 443 must be secure")
	}

	r = request(t, "http://example.com/")
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}))
	if IsSecure(r) {
		t.Error("a local port of 8080 must not be secure")
	}
	if "http" != Scheme(r) {
		t.Errorf("Scheme = %q, want http", Scheme(r))
	}
}

func TestValidIP(t *testing.T) {
	valid := []string{"127.0.0.1", "::1", "2001:db8::1", "255.255.255.255"}
	for _, value := range valid {
		if !IsValidIP(value) {
			t.Errorf("IsValidIP(%q) = false, want true", value)
		}
	}

	invalid := []string{"", " ", "localhost", "1.2.3", "999.1.1.1", "::1%eth0"}
	for _, value := range invalid {
		if IsValidIP(value) {
			t.Errorf("IsValidIP(%q) = true, want false", value)
		}
	}
}

func TestIsPostAndGet(t *testing.T) {
	if !IsPost(httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Error("IsPost(POST) = false")
	}
	if !IsGet(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("IsGet(GET) = false")
	}
	if IsPost(nil) || IsGet(nil) {
		t.Error("IsPost/IsGet(nil) must be false")
	}
}

func TestRequestURI(t *testing.T) {
	r := request(t, "/?/Ping/0/")
	if got := RequestURI(r); "/?/Ping/0/" != got {
		t.Errorf("RequestURI = %q, want /?/Ping/0/", got)
	}

	if got := RequestURI(nil); "" != got {
		t.Errorf("RequestURI(nil) = %q, want an empty string", got)
	}
}

func TestHeaders(t *testing.T) {
	r := request(t, "/")
	r.Header.Set("X-SM-Token", "")

	if !HasHeader(r, "X-SM-Token") {
		t.Error("HasHeader must see a header with an empty value")
	}
	if HasHeader(r, "X-Missing") {
		t.Error("HasHeader must not see a missing header")
	}
	if "" != GetHeader(r, "X-Missing") {
		t.Error("GetHeader must return an empty string for a missing header")
	}
	if "" != GetHeader(nil, "X-SM-Token") {
		t.Error("GetHeader(nil) must return an empty string")
	}
	if HasHeader(nil, "X-SM-Token") {
		t.Error("HasHeader(nil) must be false")
	}
}

func TestNoStore(t *testing.T) {
	recorder := httptest.NewRecorder()
	NoStore(recorder)

	header := recorder.Header()
	if "no-store" != header.Get("Cache-Control") {
		t.Errorf("Cache-Control = %q, want no-store", header.Get("Cache-Control"))
	}
	if "no-cache" != header.Get("Pragma") {
		t.Errorf("Pragma = %q, want no-cache", header.Get("Pragma"))
	}
	if "Mon, 26 Jul 1997 05:00:00 GMT" != header.Get("Expires") {
		t.Errorf("Expires = %q", header.Get("Expires"))
	}
	if "" == header.Get("Last-Modified") {
		t.Error("Last-Modified must be set")
	}
}

func TestLocation(t *testing.T) {
	recorder := httptest.NewRecorder()
	Location(recorder, "https://example.com/?/Ping/0/", http.StatusFound)

	if http.StatusFound != recorder.Code {
		t.Errorf("status = %d, want 302", recorder.Code)
	}
	if "https://example.com/?/Ping/0/" != recorder.Header().Get("Location") {
		t.Errorf("Location = %q", recorder.Header().Get("Location"))
	}
}

func TestSetContentDisposition(t *testing.T) {
	recorder := httptest.NewRecorder()
	SetContentDisposition(recorder, Disposition{Type: "attachment", Filename: "report 2024.pdf", Size: 1234})

	want := `attachment; filename="report 2024.pdf"; size=1234`
	if got := recorder.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}

	// Non ASCII file names use the RFC 5987 form (PHP: rawurlencode).
	recorder = httptest.NewRecorder()
	SetContentDisposition(recorder, Disposition{Type: "inline", Filename: "täst.txt"})
	want = "inline; filename*=utf-8''t%C3%A4st.txt"
	if got := recorder.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}

	// Dates use DATE_RFC822.
	recorder = httptest.NewRecorder()
	SetContentDisposition(recorder, Disposition{
		Type:             "inline",
		Filename:         "a.txt",
		CreationDate:     time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC),
		ModificationDate: time.Date(2024, time.February, 3, 4, 5, 6, 0, time.UTC),
	})
	want = `inline; filename="a.txt"; creation-date=Tue, 02 Jan 24 03:04:05 +0000; modification-date=Sat, 03 Feb 24 04:05:06 +0000`
	if got := recorder.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
}

func TestRawURLEncode(t *testing.T) {
	tests := map[string]string{
		"":            "",
		"abc":         "abc",
		"a b":         "a%20b",
		"a~b_c-d.e":   "a~b_c-d.e",
		"a+b":         "a%2Bb",
		"täst.txt":    "t%C3%A4st.txt",
		"100%":        "100%25",
		"a/b\\c":      "a%2Fb%5Cc",
		"!*'()":       "%21%2A%27%28%29",
		"\x00":        "%00",
		"keys: value": "keys%3A%20value",
	}

	for value, want := range tests {
		if got := RawURLEncode(value); got != want {
			t.Errorf("RawURLEncode(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		value     float64
		precision int
		want      float64
	}{
		{1.5, 0, 2},
		{2.4, 0, 2},
		{1.234, 2, 1.23},
		{1.236, 2, 1.24},
		{-1.5, 0, -2},
		{-1.234, 2, -1.23},
		{1024, 2, 1024},
	}

	for _, test := range tests {
		if got := Round(test.value, test.precision); got != test.want {
			t.Errorf("Round(%v, %d) = %v, want %v", test.value, test.precision, got, test.want)
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		size  int64
		round int
		want  string
	}{
		{0, 2, "0B"},
		{512, 2, "512B"},
		{1024, 2, "1024B"},
		{1536, 2, "1.5KB"},
		{1342177, 2, "1.28MB"},
		{1536, 0, "2KB"},
	}

	for _, test := range tests {
		if got := FormatFileSize(test.size, test.round); got != test.want {
			t.Errorf("FormatFileSize(%d, %d) = %q, want %q", test.size, test.round, got, test.want)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	tests := map[float64]string{
		0:     "0",
		1.5:   "1.5",
		2:     "2",
		-0.25: "-0.25",
		1.28:  "1.28",
	}

	for value, want := range tests {
		if got := FormatFloat(value); got != want {
			t.Errorf("FormatFloat(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestStatusText(t *testing.T) {
	tests := map[int]string{
		200: "OK",
		206: "Partial Content",
		302: "Found",
		304: "Not Modified",
		403: "Forbidden",
		404: "Not Found",
		416: "Requested range not satisfiable",
		500: "Internal Server Error",
		999: "",
	}

	for code, want := range tests {
		if got := StatusText(code); got != want {
			t.Errorf("StatusText(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestStripPort(t *testing.T) {
	// stripPort is only reachable through Host(withoutPort), which validates
	// first, so exercise it through a table of valid hosts.
	tests := map[string]string{
		"example.com":       "example.com",
		"example.com:80":    "example.com",
		"example.com:8080":  "example.com",
		"example.com:80:80": "example.com:80:80",
		"[::1]:8080":        "[::1]",
		"[::1]":             "[::1]",
		"127.0.0.1:1234":    "127.0.0.1",
	}

	for host, want := range tests {
		if got := stripPort(host); got != want {
			t.Errorf("stripPort(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestWebPaths(t *testing.T) {
	root := t.TempDir()

	paths := WebPaths{Version: "0.0.0", VersionRoot: root}
	if "/" != paths.Path() {
		t.Errorf("Path = %q, want /", paths.Path())
	}
	if "/snappymail/v/0.0.0/" != paths.VersionPath() {
		t.Errorf("VersionPath = %q", paths.VersionPath())
	}
	if "/snappymail/v/0.0.0/static/" != paths.StaticRoot() {
		t.Errorf("StaticRoot = %q", paths.StaticRoot())
	}

	// Missing files get no cache buster.
	if got := paths.StaticPath("js/app.js"); "/snappymail/v/0.0.0/static/js/app.js" != got {
		t.Errorf("StaticPath(missing) = %q", got)
	}

	// Existing files get "?v=<mtime>".
	if err := writeFile(t, paths.StaticFile("js/app.js"), "x"); nil != err {
		t.Fatalf("write static file: %v", err)
	}
	expected := "/snappymail/v/0.0.0/static/js/app.js?v="
	if got := paths.StaticPath("js/app.js"); !strings.HasPrefix(got, expected) {
		t.Errorf("StaticPath = %q, want the prefix %q", got, expected)
	}

	// A subdirectory install keeps its prefix.
	sub := WebPaths{AppPath: "/mail/", Version: "1.2.3", VersionRoot: root}
	if "/mail/" != sub.Path() {
		t.Errorf("Path = %q, want /mail/", sub.Path())
	}
	if "/mail/snappymail/v/1.2.3/" != sub.VersionPath() {
		t.Errorf("VersionPath = %q", sub.VersionPath())
	}

	// VersionFile resolves below the version root.
	if got := paths.VersionFile("app/templates/Error.html"); !strings.HasSuffix(got, "app/templates/Error.html") {
		t.Errorf("VersionFile = %q", got)
	}
}

// writeFile creates name (and its directories) for the WebStaticPath test.
func writeFile(t *testing.T, name, content string) error {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(name), 0o700); nil != err {
		return err
	}

	return os.WriteFile(name, []byte(content), 0o600)
}
