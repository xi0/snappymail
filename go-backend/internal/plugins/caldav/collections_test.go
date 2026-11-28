package caldav

// The step 30 acceptance checks for the calendar collections: MKCALENDAR with a
// unique collection name (CreateCalendar), PROPPATCH (UpdateCalendar — a 207 that
// reports a failed property is a failure) and DELETE (DeleteCalendar, where a 404
// is the desired end state).

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// collectionMultistatus wraps one D:response with one property status.
func collectionMultistatus(href, displayName, status string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response>
    <D:href>` + href + `</D:href>
    <D:propstat>
      <D:prop><D:displayname>` + displayName + `</D:displayname></D:prop>
      <D:status>` + status + `</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`
}

func TestInitRegistersTheStep30Surface(t *testing.T) {
	f := newFixture(t, nil)

	// rl.pluginRemoteRequest(cb, 'GetCalendarEvents') addresses
	// "PluginGetCalendarEvents" and the method "DoPluginGetCalendarEvents".
	for _, action := range []string{
		"GetCalendarEvents",
		"CreateCalendarEvent",
		"CreateCalendar",
		"UpdateCalendar",
		"DeleteCalendar",
	} {
		if !f.manager.HasAdditionalJSON("DoPlugin" + action) {
			t.Errorf("DoPlugin%s must be registered", action)
		}
	}
}

func TestCreateCalendar(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.Method {
		case "PROPFIND":
			// The uniqueness probe of a free collection name.
			w.WriteHeader(http.StatusNotFound)
		case "MKCALENDAR":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Name":  "Møder & Aftaler",
		"Color": "#16A765",
	}), "CreateCalendar")

	if true != document["success"] {
		t.Fatalf("CreateCalendar = %#v", document)
	}

	// slugifyCalendarName() transliterates the Nordic letters instead of dropping
	// them, and the id is URL-safe.
	if "moeder-aftaler" != document["id"] {
		t.Errorf("id = %v, want moeder-aftaler", document["id"])
	}
	if want := mock.calendarHome() + "/moeder-aftaler/"; want != document["url"] {
		t.Errorf("url = %v, want %q", document["url"], want)
	}
	if "Møder & Aftaler" != document["name"] {
		t.Errorf("name = %v", document["name"])
	}
	if "#16A765" != document["color"] {
		t.Errorf("color = %v", document["color"])
	}

	if 2 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want the uniqueness probe and the MKCALENDAR", len(mock.requests))
	}

	// The uniqueness probe: PROPFIND of the candidate collection with Depth: 0.
	probe := mock.requests[0]
	if "PROPFIND" != probe.method {
		t.Errorf("probe method = %s, want PROPFIND", probe.method)
	}
	if want := "/dav/cal/user@example.com/moeder-aftaler/"; want != probe.uri {
		t.Errorf("probe uri = %q, want %q", probe.uri, want)
	}
	if "0" != probe.header.Get("Depth") {
		t.Errorf("probe Depth = %q, want 0", probe.header.Get("Depth"))
	}
	if !strings.Contains(probe.body, "<D:resourcetype />") {
		t.Errorf("probe body = %q, want the short PROPFIND", probe.body)
	}

	// The MKCALENDAR: the display name is XML escaped, the colour is only sent
	// when one was given, and the collection declares VEVENT support.
	create := mock.requests[1]
	if "MKCALENDAR" != create.method {
		t.Errorf("create method = %s, want MKCALENDAR", create.method)
	}
	if want := "/dav/cal/user@example.com/moeder-aftaler/"; want != create.uri {
		t.Errorf("create uri = %q, want %q", create.uri, want)
	}
	if value := create.header.Get("Depth"); "" != value {
		t.Errorf("Depth = %q, want none (the MKCALENDAR must not inherit Depth: 1)", value)
	}
	for _, fragment := range []string{
		`<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"`,
		"<D:displayname>Møder &amp; Aftaler</D:displayname>",
		"<A:calendar-color>#16A765</A:calendar-color>",
		`<C:comp name="VEVENT" />`,
	} {
		if !strings.Contains(create.body, fragment) {
			t.Errorf("MKCALENDAR body is missing %q: %s", fragment, create.body)
		}
	}
}

func TestCreateCalendarOmitsAnUnknownColour(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.Method {
		case "PROPFIND":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Name":  "Personal",
		"Color": "red",
	}), "CreateCalendar")

	if true != document["success"] {
		t.Fatalf("CreateCalendar = %#v", document)
	}
	if "" != document["color"] {
		t.Errorf("color = %v, want the empty string for an unusable value", document["color"])
	}

	body := mock.requests[len(mock.requests)-1].body
	if strings.Contains(body, "calendar-color") {
		t.Errorf("MKCALENDAR body must not carry a colour: %s", body)
	}
}

func TestCreateCalendarRetriesATakenName(t *testing.T) {
	mock := newCalDAVServer(t)

	probes := 0
	mock.handler = func(w http.ResponseWriter, r *http.Request, _ string) {
		switch r.Method {
		case "PROPFIND":
			probes++
			if 1 == probes {
				// The collection exists already.
				w.WriteHeader(http.StatusMultiStatus)
				_, _ = io.WriteString(w, `<D:multistatus xmlns:D="DAV:"></D:multistatus>`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "MKCALENDAR":
			w.WriteHeader(http.StatusCreated)
		}
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{"Name": "Personal"}), "CreateCalendar")

	// uniqueCalendarId() appends the attempt number: "personal", "personal-2", …
	if "personal-2" != document["id"] {
		t.Errorf("id = %v, want personal-2", document["id"])
	}
	if 3 != len(mock.requests) {
		t.Errorf("the plugin made %d requests, want two probes and the MKCALENDAR", len(mock.requests))
	}
	if want := "/dav/cal/user@example.com/personal/"; want != mock.requests[0].uri {
		t.Errorf("first probe uri = %q, want %q", mock.requests[0].uri, want)
	}
	if want := "/dav/cal/user@example.com/personal-2/"; want != mock.requests[1].uri {
		t.Errorf("second probe uri = %q, want %q", mock.requests[1].uri, want)
	}
}

func TestCreateCalendarRequiresAName(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{"Name": "   "}), "CreateCalendar")

	if false != document["success"] {
		t.Errorf("success = %v, want false", document["success"])
	}
	if "Calendar name required" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("an invalid request must not reach the server: %#v", mock.requests)
	}
}

func TestCalendarActionsRequireAnAccount(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	guest := &testActions{params: map[string]any{"Name": "Personal"}, main: f.main, env: f.env}

	for _, action := range []string{"CreateCalendar", "UpdateCalendar", "DeleteCalendar"} {
		document := runPluginAction(t, f, guest, action)
		// ERROR_NOT_LOGGED_IN comes from the plugin's langs/ folder.
		if "Please log in first" != document["error"] {
			t.Errorf("%s error = %v", action, document["error"])
		}
	}

	if 0 != len(mock.requests) {
		t.Errorf("a guest must not reach the CalDAV server: %#v", mock.requests)
	}
}

func TestUpdateCalendar(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, r *http.Request, _ string) {
		if "PROPPATCH" != r.Method {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, collectionMultistatus("/personal/", "Work", "HTTP/1.1 200 OK"))
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"CalendarId":  "personal",
		"CalendarUrl": mock.calendarHome() + "/personal/",
		"Name":        "Work",
		"Color":       "#16a765",
	}), "UpdateCalendar")

	if true != document["success"] {
		t.Fatalf("UpdateCalendar = %#v", document)
	}
	if "Work" != document["name"] || "#16a765" != document["color"] {
		t.Errorf("name/color = %v/%v", document["name"], document["color"])
	}

	if 1 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want 1", len(mock.requests))
	}
	request := mock.requests[0]
	if "PROPPATCH" != request.method {
		t.Errorf("method = %s, want PROPPATCH", request.method)
	}
	// The server reported URL is used unchanged (it is the same host).
	if want := "/dav/cal/user@example.com/personal/"; want != request.uri {
		t.Errorf("uri = %q, want %q", request.uri, want)
	}
	for _, fragment := range []string{"<D:propertyupdate", "<D:displayname>Work</D:displayname>", "<A:calendar-color>#16a765</A:calendar-color>"} {
		if !strings.Contains(request.body, fragment) {
			t.Errorf("PROPPATCH body is missing %q: %s", fragment, request.body)
		}
	}
}

func TestUpdateCalendarReportsAFailedProperty(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
		// A server may accept the PROPPATCH and still refuse one property.
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, collectionMultistatus("/personal/", "Work", "HTTP/1.1 403 Forbidden"))
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"CalendarId": "personal",
		"Name":       "Work",
	}), "UpdateCalendar")

	if false != document["success"] {
		t.Errorf("success = %v, want false", document["success"])
	}
	if "CalDAV error: 207" != document["error"] {
		t.Errorf("error = %v, want the multistatus status", document["error"])
	}
}

func TestUpdateCalendarRequiresACollection(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	// Without a CalendarId and without a server reported URL there is nothing to
	// address.
	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Name": "Work",
	}), "UpdateCalendar")

	if "Calendar not found" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("an unaddressable calendar must not reach the server: %#v", mock.requests)
	}
}

func TestDeleteCalendar(t *testing.T) {
	cases := []struct {
		status   int
		success  any
		hasError bool
	}{
		{http.StatusNoContent, true, false},
		{http.StatusOK, true, false},
		{http.StatusAccepted, true, false},
		// Already deleted is the desired end state.
		{http.StatusNotFound, true, false},
		{http.StatusForbidden, false, true},
	}

	for _, test := range cases {
		mock := newCalDAVServer(t)
		status := test.status
		mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(status)
		}

		f := newDiscoveryFixture(t, mock)

		document := runPluginAction(t, f, actionParams(f, map[string]any{
			"CalendarId": "personal",
		}), "DeleteCalendar")

		if test.success != document["success"] {
			t.Errorf("DELETE %d: success = %v, want %v", status, document["success"], test.success)
		}
		if _, failed := document["error"]; test.hasError != failed {
			t.Errorf("DELETE %d: error = %v", status, document["error"])
		}

		if 1 != len(mock.requests) {
			t.Fatalf("DELETE %d: the plugin made %d requests, want 1", status, len(mock.requests))
		}
		request := mock.requests[0]
		if "DELETE" != request.method {
			t.Errorf("method = %s, want DELETE", request.method)
		}
		// The URL is derived from the calendar id inside the calendar home.
		if want := "/dav/cal/user@example.com/personal/"; want != request.uri {
			t.Errorf("uri = %q, want %q", request.uri, want)
		}
		if "" != request.body {
			t.Errorf("body = %q, want none", request.body)
		}
	}
}
