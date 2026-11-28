package caldav

// The discovery half of step 29: DoGetCalendars() against a mock CalDAV server
// (the same list as PHP, the read-only fallback when discovery fails) and the URL
// and multistatus helpers the collection/event actions of steps 30–32 build on.

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// calendarHomeMultistatus is a calendar home PROPFIND response: the home itself
// (a plain collection), one calendar with a #RRGGBBAA colour and one without a
// colour and without a display name.
const calendarHomeMultistatus = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">
  <D:response>
    <D:href>/dav/cal/user@example.com/</D:href>
    <D:propstat>
      <D:prop><D:displayname>Home</D:displayname><D:resourcetype><D:collection/></D:resourcetype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/cal/user@example.com/personal/</D:href>
    <D:propstat>
      <D:prop>
        <D:displayname>Personal</D:displayname>
        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>
        <A:calendar-color>#ff0000ff</A:calendar-color>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/cal/user@example.com/work%20calendar/</D:href>
    <D:propstat>
      <D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

// recordedRequest is one request the mock server received.
type recordedRequest struct {
	method string
	uri    string
	header http.Header
	body   string
}

// calDAVServer is the mock CalDAV server.
type calDAVServer struct {
	// server is the httptest server.
	server *httptest.Server
	// requests are the requests the plugin made.
	requests []recordedRequest
	// handler answers a request; nil answers the calendar home PROPFIND.
	handler func(w http.ResponseWriter, r *http.Request, body string)
}

// newCalDAVServer starts the mock server.
func newCalDAVServer(t *testing.T) *calDAVServer {
	t.Helper()

	mock := &calDAVServer{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mock.requests = append(mock.requests, recordedRequest{
			method: r.Method,
			uri:    r.URL.RequestURI(),
			header: r.Header.Clone(),
			body:   string(body),
		})

		if nil != mock.handler {
			mock.handler(w, r, string(body))
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, calendarHomeMultistatus)
	}))
	t.Cleanup(mock.server.Close)

	return mock
}

// calendarHome is the calendar home URL the plugin derives from caldav_server.
func (s *calDAVServer) calendarHome() string { return s.server.URL + "/dav/cal/user@example.com" }

// newDiscoveryFixture builds the fixture against the mock server and runs the
// login hook, so the "calendar_sync" blob exists.
func newDiscoveryFixture(t *testing.T, mock *calDAVServer) *fixture {
	t.Helper()

	f := newFixture(t, func(config map[string]any) {
		config["caldav_server"] = mock.server.URL + "/dav/cal"
	})
	f.instance.HTTP = mock.server.Client()
	f.manager.RunHook("login.success", f.main, f.env)

	return f
}

// getCalendars runs DoPluginGetCalendars through the manager and returns the
// "Result" document of the envelope.
func getCalendars(t *testing.T, f *fixture, actions *testActions) map[string]any {
	t.Helper()
	return runPluginAction(t, f, actions, "GetCalendars")
}

// calendarList is the "calendars" member of a GetCalendars payload.
func calendarList(t *testing.T, document map[string]any) []any {
	t.Helper()

	calendars, ok := document["calendars"].([]any)
	if !ok {
		t.Fatalf("calendars = %#v, want a list", document["calendars"])
	}
	return calendars
}

func TestGetCalendarsDiscoversCollections(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	document := getCalendars(t, f, f.actions(&f.main.Account))

	if _, failed := document["error"]; failed {
		t.Fatalf("GetCalendars reported %v", document["error"])
	}

	calendars := calendarList(t, document)
	if 2 != len(calendars) {
		t.Fatalf("calendars = %#v, want the two calendar collections", calendars)
	}

	// The non-calendar home collection is skipped; the untouched href is turned
	// into an absolute URL and the #RRGGBBAA colour is normalised.
	first, _ := calendars[0].(map[string]any)
	if "personal" != first["id"] || "Personal" != first["name"] || "#ff0000" != first["color"] {
		t.Errorf("calendars[0] = %#v", calendars[0])
	}
	if want := mock.calendarHome() + "/personal/"; want != first["url"] {
		t.Errorf("calendars[0].url = %v, want %q", first["url"], want)
	}

	// Without a displayname the (decoded) URL segment is the name, and without a
	// calendar-color the next palette entry is used.
	second, _ := calendars[1].(map[string]any)
	if "work calendar" != second["id"] || "work calendar" != second["name"] {
		t.Errorf("calendars[1] = %#v", calendars[1])
	}
	if "#16a765" != second["color"] {
		t.Errorf("calendars[1].color = %v, want the second palette entry", second["color"])
	}
	if want := mock.calendarHome() + "/work%20calendar/"; want != second["url"] {
		t.Errorf("calendars[1].url = %v, want %q", second["url"], want)
	}

	// The request itself: a PROPFIND of the calendar home with Depth: 1 and the
	// account credentials.
	if 1 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want 1", len(mock.requests))
	}
	request := mock.requests[0]
	if "PROPFIND" != request.method {
		t.Errorf("method = %s, want PROPFIND", request.method)
	}
	if want := "/dav/cal/user@example.com/"; want != request.uri {
		t.Errorf("uri = %q, want %q", request.uri, want)
	}
	if "1" != request.header.Get("Depth") {
		t.Errorf("Depth = %q, want 1", request.header.Get("Depth"))
	}
	if "application/xml; charset=utf-8" != request.header.Get("Content-Type") {
		t.Errorf("Content-Type = %q", request.header.Get("Content-Type"))
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:secret")); want != request.header.Get("Authorization") {
		t.Errorf("Authorization = %q, want %q", request.header.Get("Authorization"), want)
	}
	if !strings.Contains(request.body, "<D:propfind") {
		t.Errorf("body = %q, want the PROPFIND document", request.body)
	}
}

func TestGetCalendarsFallsBackToAReadOnlyCalendar(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	}

	f := newDiscoveryFixture(t, mock)

	calendars := calendarList(t, getCalendars(t, f, f.actions(&f.main.Account)))

	if 1 != len(calendars) {
		t.Fatalf("calendars = %#v, want the fallback entry", calendars)
	}

	fallback, _ := calendars[0].(map[string]any)
	if defaultCalendarID != fallback["id"] {
		t.Errorf("id = %v, want %q", fallback["id"], defaultCalendarID)
	}
	if "Calendar" != fallback["name"] {
		t.Errorf("name = %v, want the translation", fallback["name"])
	}
	if defaultCalendarColor != fallback["color"] {
		t.Errorf("color = %v, want %q", fallback["color"], defaultCalendarColor)
	}
	if true != fallback["readOnly"] {
		t.Errorf("readOnly = %v, want true", fallback["readOnly"])
	}
}

func TestGetCalendarsFallsBackOnAnEmptyMultistatus(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<D:multistatus xmlns:D="DAV:"></D:multistatus>`)
	}

	f := newDiscoveryFixture(t, mock)

	calendars := calendarList(t, getCalendars(t, f, f.actions(&f.main.Account)))
	if 1 != len(calendars) {
		t.Errorf("calendars = %#v, want the fallback entry", calendars)
	}
}

func TestGetCalendarsWithoutAnAccount(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	// A guest: PHP reports ERROR_NOT_LOGGED_IN and never contacts the server.
	guest := &testActions{params: map[string]any{}, main: f.main, env: f.env}

	document := getCalendars(t, f, guest)

	if 0 != len(calendarList(t, document)) {
		t.Errorf("calendars = %#v, want none", document["calendars"])
	}
	if "Please log in first" != document["message"] {
		t.Errorf("message = %v", document["message"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("a guest must not reach the CalDAV server: %#v", mock.requests)
	}
}

func TestGetCalendarsWithoutAConfiguration(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newFixture(t, func(config map[string]any) {
		config["caldav_server"] = mock.server.URL + "/dav/cal"
		config["auto_sync"] = false
	})
	f.instance.HTTP = mock.server.Client()

	document := getCalendars(t, f, f.actions(&f.main.Account))

	if 0 != len(calendarList(t, document)) {
		t.Errorf("calendars = %#v, want none", document["calendars"])
	}
	if "Calendar not configured yet. Please check settings." != document["message"] {
		t.Errorf("message = %v", document["message"])
	}
}

func TestGetCalendarsWithoutACryptKey(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	// A stored blob without a crypt engine: PHP's getDecryptedPassword() returns
	// null, which DoGetCalendars() reports as ERROR_NO_ENCRYPTION_KEY.
	f.env.Crypt = nil

	document := getCalendars(t, f, f.actions(&f.main.Account))

	if 0 != len(calendarList(t, document)) {
		t.Errorf("calendars = %#v, want none", document["calendars"])
	}
	if "Cannot access encryption key" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
}

func TestGetCalendarsUsesTheDanishTranslation(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	actions := &testActions{params: map[string]any{}, main: f.main, env: f.env, language: "da"}
	actions.current = nil

	document := getCalendars(t, f, actions)

	if "Log venligst ind foerst" != document["message"] {
		t.Errorf("message = %v, want the Danish text", document["message"])
	}
}

func TestCalendarURL(t *testing.T) {
	config := &calendarConfig{URL: "https://dav.example.com/cal/user@example.com"}

	if got := calendarUrl(config, ""); "https://dav.example.com/cal/user@example.com/default" != got {
		t.Errorf("calendarUrl(empty) = %q", got)
	}
	// rawurlencode() escapes the space (and everything outside [A-Za-z0-9-_.~]).
	if got := calendarUrl(config, " work calendar "); "https://dav.example.com/cal/user@example.com/work%20calendar" != got {
		t.Errorf("calendarUrl(name) = %q", got)
	}
	if got := calendarUrl(nil, "x"); "" != got {
		t.Errorf("calendarUrl(nil) = %q, want the empty string", got)
	}
}

func TestResolveEventURL(t *testing.T) {
	config := &calendarConfig{URL: "https://dav.example.com:8443/cal/user@example.com"}

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"https://other.example.com/x.ics", "https://other.example.com/x.ics"},
		{"HTTP://other.example.com/x.ics", "HTTP://other.example.com/x.ics"},
		{"//other.example.com/x.ics", "https://other.example.com/x.ics"},
		{"/cal/user@example.com/x.ics", "https://dav.example.com:8443/cal/user@example.com/x.ics"},
		{"cal/user/x.ics", "https://dav.example.com:8443/cal/user/x.ics"},
	}

	for _, test := range cases {
		if got := resolveEventUrl(config, test.in); test.want != got {
			t.Errorf("resolveEventUrl(%q) = %q, want %q", test.in, got, test.want)
		}
	}

	// Without a host in the configuration the value is kept as it is.
	if got := resolveEventUrl(&calendarConfig{URL: "/cal"}, "/x.ics"); "/x.ics" != got {
		t.Errorf("resolveEventUrl(no host) = %q", got)
	}
}

func TestResolveCollectionURL(t *testing.T) {
	config := &calendarConfig{URL: "https://dav.example.com/cal/user@example.com"}

	// The server reported URL wins when it points at the configured host.
	if got := resolveCollectionUrl(config, "id", "https://DAV.example.com/cal/user@example.com/x/"); "https://DAV.example.com/cal/user@example.com/x/" != got {
		t.Errorf("resolveCollectionUrl(same host) = %q", got)
	}
	// A foreign host is refused and the id is used instead (the request carries
	// the user's credentials).
	if got := resolveCollectionUrl(config, "id", "https://evil.example.com/x/"); "https://dav.example.com/cal/user@example.com/id/" != got {
		t.Errorf("resolveCollectionUrl(foreign host) = %q", got)
	}
	if got := resolveCollectionUrl(config, "", "https://evil.example.com/x/"); "" != got {
		t.Errorf("resolveCollectionUrl(no id) = %q, want the empty string", got)
	}
}

func TestNormalizeCalendarColor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"#a1b2c3", "#a1b2c3"},
		{"a1b2c3", "#a1b2c3"},
		{"#A1B2C3FF", "#A1B2C3"},
		{" #a1b2c3 ", "#a1b2c3"},
		{"", ""},
		{"#a1b2", ""},
		{"red", ""},
		{"#a1b2c3d4e5", ""},
	}

	for _, test := range cases {
		if got := normalizeCalendarColor(test.in); test.want != got {
			t.Errorf("normalizeCalendarColor(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestSlugifyCalendarName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Personal", "personal"},
		{"Møder & Aftaler", "moeder-aftaler"},
		{"  Work   Calendar  ", "work-calendar"},
		{"UGE 42 / 2025", "uge-42-2025"},
	}

	for _, test := range cases {
		if got := slugifyCalendarName(test.in); test.want != got {
			t.Errorf("slugifyCalendarName(%q) = %q, want %q", test.in, got, test.want)
		}
	}

	// A name without any usable character gets the random fallback.
	slug := slugifyCalendarName("!!!")
	if !strings.HasPrefix(slug, "calendar-") || 8 != len(strings.TrimPrefix(slug, "calendar-")) {
		t.Errorf("slugifyCalendarName(!!!) = %q, want calendar-<8 hex>", slug)
	}

	// The result is capped at 64 characters.
	if got := slugifyCalendarName(strings.Repeat("a", 100)); 64 != len(got) {
		t.Errorf("long slug = %d characters, want 64", len(got))
	}
}

func TestXMLText(t *testing.T) {
	if got := xmlText(`Møder & "aftaler" <x>`); `Møder &amp; &quot;aftaler&quot; &lt;x&gt;` != got {
		t.Errorf("xmlText = %q", got)
	}
}

func TestCalDAVMultistatusHasErrors(t *testing.T) {
	// A failed property in a PROPPATCH multistatus.
	failed := `<D:multistatus xmlns:D="DAV:"><D:response><D:href>/c/</D:href>
<D:propstat><D:prop><D:displayname/></D:prop><D:status>HTTP/1.1 403 Forbidden</D:status></D:propstat>
</D:response></D:multistatus>`
	if !calDAVMultistatusHasErrors(failed) {
		t.Error("a 403 propstat must be reported as failure")
	}

	// A failed response without a propstat.
	if !calDAVMultistatusHasErrors(`<D:multistatus xmlns:D="DAV:"><D:response>` +
		`<D:status>HTTP/1.1 409 Conflict</D:status></D:response></D:multistatus>`) {
		t.Error("a 409 response status must be reported as failure")
	}

	ok := `<D:multistatus xmlns:D="DAV:"><D:response><D:propstat><D:prop><D:displayname>x</D:displayname>` +
		`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`
	if calDAVMultistatusHasErrors(ok) {
		t.Error("a 200 multistatus must not be a failure")
	}

	// Empty and unparsable bodies count as success (the HTTP code already passed).
	if calDAVMultistatusHasErrors("  ") {
		t.Error("an empty body must not be a failure")
	}
	if calDAVMultistatusHasErrors("not xml") {
		t.Error("an unparsable body must not be a failure")
	}
}

func TestRawURLEncode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcXYZ019-_.~", "abcXYZ019-_.~"},
		{"a b", "a%20b"},
		{"a/b", "a%2Fb"},
		{"æ", "%C3%A6"},
		{"a+b", "a%2Bb"},
	}

	for _, test := range cases {
		if got := rawURLEncode(test.in); test.want != got {
			t.Errorf("rawURLEncode(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}
