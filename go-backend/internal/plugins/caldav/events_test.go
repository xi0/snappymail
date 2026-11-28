package caldav

// The step 30 acceptance checks for the event actions: DoGetCalendarEvents()
// against the mock CalDAV server (the REPORT request and the event list), and
// DoCreateCalendarEvent() with the exact iCalendar bytes of an all-day and a
// timed event, plus the validation and HTTP error payloads.

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// runPluginAction runs a plugin JSON action through the manager and returns the
// "Result" document of the envelope.
func runPluginAction(t *testing.T, f *fixture, actions *testActions, action string) map[string]any {
	t.Helper()

	result, err := f.manager.RunAdditionalJSON(actions, "DoPlugin"+action)
	if nil != err {
		t.Fatalf("DoPlugin%s: %v", action, err)
	}

	envelope, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("DoPlugin%s returned %#v, want the plugin envelope", action, result)
	}

	document, ok := envelope["Result"].(map[string]any)
	if !ok {
		t.Fatalf("DoPlugin%s Result = %#v, want the action payload", action, envelope["Result"])
	}
	return document
}

// actionParams is the request surface of the fixture with the parameters of one
// call.
func actionParams(f *fixture, params map[string]any) *testActions {
	actions := f.actions(&f.main.Account)
	actions.params = params
	return actions
}

// calendarEventsMultistatus is a calendar-query REPORT response: the collection
// itself (no calendar-data, so it yields no event) and one VEVENT resource.
const calendarEventsMultistatus = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>/dav/cal/user@example.com/personal/</D:href>
    <D:propstat>
      <D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
  <D:response>
    <D:href>/dav/cal/user@example.com/personal/event-1.ics</D:href>
    <D:propstat>
      <D:prop>
        <D:getetag>"etag-1"</D:getetag>
        <C:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:event-1
DTSTAMP:20251001T120000Z
DTSTART:20251106T143000Z
DTEND:20251106T153000Z
SUMMARY:Standup
END:VEVENT
END:VCALENDAR</C:calendar-data>
      </D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`

func TestGetCalendarEventsListsTheCollection(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, calendarEventsMultistatus)
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, map[string]any{"CalendarId": "personal"}), "GetCalendarEvents")

	if _, failed := document["error"]; failed {
		t.Fatalf("GetCalendarEvents reported %v", document["error"])
	}

	events, ok := document["events"].([]any)
	if !ok || 1 != len(events) {
		t.Fatalf("events = %#v, want the single event resource", document["events"])
	}

	// The event comes from parseICalendar(), and the response without
	// calendar-data is skipped.
	event, _ := events[0].(map[string]any)
	if "event-1" != event["uid"] || "Standup" != event["summary"] {
		t.Errorf("event = %#v", event)
	}
	if "2025-11-06T14:30:00Z" != event["dtstart"] || "2025-11-06T15:30:00Z" != event["dtend"] {
		t.Errorf("dtstart/dtend = %v/%v", event["dtstart"], event["dtend"])
	}
	// href/etag identify the resource the frontend PUTs and DELETEs.
	if "/dav/cal/user@example.com/personal/event-1.ics" != event["href"] {
		t.Errorf("href = %v", event["href"])
	}
	if `"etag-1"` != event["etag"] {
		t.Errorf("etag = %v", event["etag"])
	}

	// PHP reports the number of parsed events as the message.
	if "Loaded 1 events" != document["message"] {
		t.Errorf("message = %v", document["message"])
	}

	// The REPORT request itself.
	if 1 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want 1", len(mock.requests))
	}
	request := mock.requests[0]
	if "REPORT" != request.method {
		t.Errorf("method = %s, want REPORT", request.method)
	}
	if want := "/dav/cal/user@example.com/personal/"; want != request.uri {
		t.Errorf("uri = %q, want %q", request.uri, want)
	}
	if "1" != request.header.Get("Depth") {
		t.Errorf("Depth = %q, want 1", request.header.Get("Depth"))
	}
	if "application/xml; charset=utf-8" != request.header.Get("Content-Type") {
		t.Errorf("Content-Type = %q", request.header.Get("Content-Type"))
	}
	for _, fragment := range []string{"<C:calendar-query", "<D:getetag />", "<C:calendar-data />", `<C:comp-filter name="VEVENT" />`} {
		if !strings.Contains(request.body, fragment) {
			t.Errorf("body is missing %q: %s", fragment, request.body)
		}
	}
}

func TestGetCalendarEventsOnAServerError(t *testing.T) {
	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	}

	f := newDiscoveryFixture(t, mock)

	document := runPluginAction(t, f, actionParams(f, nil), "GetCalendarEvents")

	if _, failed := document["error"]; failed {
		t.Errorf("an HTTP error must not be reported as an error payload: %v", document["error"])
	}
	if events, _ := document["events"].([]any); 0 != len(events) {
		t.Errorf("events = %#v, want none", document["events"])
	}
	if "Loaded 0 events" != document["message"] {
		t.Errorf("message = %v", document["message"])
	}
}

func TestGetCalendarEventsWithoutAnAccount(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	// A guest: PHP reports ERROR_NOT_LOGGED_IN and never contacts the server.
	guest := &testActions{params: map[string]any{}, main: f.main, env: f.env}

	document := runPluginAction(t, f, guest, "GetCalendarEvents")

	if events, _ := document["events"].([]any); 0 != len(events) {
		t.Errorf("events = %#v, want none", document["events"])
	}
	if "Please log in first" != document["message"] {
		t.Errorf("message = %v", document["message"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("a guest must not reach the CalDAV server: %#v", mock.requests)
	}
}

func TestParseCalDAVResponse(t *testing.T) {
	// A body that is not XML yields no events (PHP swallows the DOMDocument
	// warning) and neither does a multistatus without a response.
	for _, document := range []string{"", "not xml", `<D:multistatus xmlns:D="DAV:"></D:multistatus>`} {
		if events := parseCalDAVResponse(document); 0 != len(events) {
			t.Errorf("parseCalDAVResponse(%.20q) = %#v, want none", document, events)
		}
	}

	// An empty calendar-data element is parsed and simply yields no VEVENT.
	if events := parseCalDAVResponse(`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:response><D:href>/e.ics</D:href><D:propstat><D:prop><C:calendar-data></C:calendar-data>` +
		`</D:prop></D:propstat></D:response></D:multistatus>`); 0 != len(events) {
		t.Errorf("empty calendar-data = %#v, want none", events)
	}
}

// createEventFixture is the fixture of the create tests: the mock server answers
// the PUT of the event resource and records the request.
func createEventFixture(t *testing.T, status int) (*fixture, *calDAVServer) {
	t.Helper()

	mock := newCalDAVServer(t)
	mock.handler = func(w http.ResponseWriter, r *http.Request, _ string) {
		if http.MethodPut == r.Method {
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}

	f := newDiscoveryFixture(t, mock)
	f.main.SetName("User Name")

	return f, mock
}

// normaliseCreatedEvent replaces the generated UID and the DTSTAMP of a PUT body,
// so the rest can be compared byte for byte.
func normaliseCreatedEvent(body string) (uid, normalised string) {
	lines := strings.Split(body, "\r\n")

	for index, line := range lines {
		switch {
		case strings.HasPrefix(line, "UID:"):
			uid = strings.TrimPrefix(line, "UID:")
			lines[index] = "UID:<generated>"
		case strings.HasPrefix(line, "DTSTAMP:"):
			lines[index] = "DTSTAMP:<now>"
		}
	}

	return uid, strings.Join(lines, "\r\n")
}

func TestCreateCalendarEventTimed(t *testing.T) {
	f, mock := createEventFixture(t, http.StatusCreated)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"CalendarId":  "personal",
		"Title":       "Standup, daily; sync",
		"Start":       "2025-11-06T14:30:00.000Z",
		"End":         "2025-11-06T14:45:00.000Z",
		"AllDay":      0,
		"Description": "Daily sync",
		"Location":    "Room 1, HQ",
		"Rrule":       " FREQ=WEEKLY;BYDAY=TH ",
		"Exdate":      "2025-11-13",
		"Rdate":       "",
		"Attendees": []any{
			map[string]any{"email": "jane@example.com", "name": "Jane Doe", "partstat": "accepted"},
			// The organizer never gets an ATTENDEE line of its own.
			map[string]any{"email": "user@example.com", "name": "User Name"},
		},
	}), "CreateCalendarEvent")

	if true != document["success"] {
		t.Fatalf("CreateCalendarEvent = %#v", document)
	}

	uid := phpString(document["uid"])
	// uniqid('event-') . '@' . <caldav user>: 13 hex characters
	// (%08x seconds + %05x microseconds).
	if !strings.HasPrefix(uid, "event-") || !strings.HasSuffix(uid, "@user@example.com") {
		t.Fatalf("uid = %q, want event-<13 hex>@user@example.com", uid)
	}
	if 13 != len(strings.TrimSuffix(strings.TrimPrefix(uid, "event-"), "@user@example.com")) {
		t.Errorf("uid = %q, want 13 hex characters between the prefix and the address", uid)
	}

	if 1 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want 1", len(mock.requests))
	}
	request := mock.requests[0]

	if "PUT" != request.method {
		t.Errorf("method = %s, want PUT", request.method)
	}
	if want := "/dav/cal/user@example.com/personal/" + rawURLEncode(uid) + ".ics"; want != request.uri {
		t.Errorf("uri = %q, want %q", request.uri, want)
	}
	if "text/calendar; charset=utf-8" != request.header.Get("Content-Type") {
		t.Errorf("Content-Type = %q", request.header.Get("Content-Type"))
	}
	// No Depth header: MKCALENDAR/PUT/DELETE must not inherit the PROPFIND one.
	if value := request.header.Get("Depth"); "" != value {
		t.Errorf("Depth = %q, want none", value)
	}

	bodyUID, body := normaliseCreatedEvent(request.body)
	if uid != bodyUID {
		t.Errorf("the UID in the body is %q, the response reports %q", bodyUID, uid)
	}

	want := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Mailbux//CalDAV Plugin//EN",
		"BEGIN:VEVENT",
		"UID:<generated>",
		"DTSTAMP:<now>",
		"DTSTART:20251106T143000Z",
		"DTEND:20251106T144500Z",
		`SUMMARY:Standup\, daily\; sync`,
		"DESCRIPTION:Daily sync",
		`LOCATION:Room 1\, HQ`,
		`ORGANIZER;CN="User Name":mailto:user@example.com`,
		`ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;RSVP=TRUE;CN="Jane Doe":mailto:jane@example.com`,
		"RRULE:FREQ=WEEKLY;BYDAY=TH",
		"EXDATE:20251113T000000Z",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")

	if want != body {
		t.Errorf("PUT body =\n%q\nwant\n%q", body, want)
	}
}

func TestCreateCalendarEventAllDay(t *testing.T) {
	f, mock := createEventFixture(t, http.StatusNoContent)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"CalendarId": "personal",
		"Title":      "Holiday",
		"Start":      "2025-11-06",
		"End":        "2025-11-07",
		"AllDay":     1,
	}), "CreateCalendarEvent")

	if true != document["success"] {
		t.Fatalf("CreateCalendarEvent = %#v", document)
	}

	if 1 != len(mock.requests) {
		t.Fatalf("the plugin made %d requests, want 1", len(mock.requests))
	}

	_, body := normaliseCreatedEvent(mock.requests[0].body)

	// An all-day event keeps the date as it was sent (";VALUE=DATE", no time and
	// no timezone shift), and the empty optional properties are omitted.
	want := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Mailbux//CalDAV Plugin//EN",
		"BEGIN:VEVENT",
		"UID:<generated>",
		"DTSTAMP:<now>",
		"DTSTART;VALUE=DATE:20251106",
		"DTEND;VALUE=DATE:20251107",
		"SUMMARY:Holiday",
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")

	if want != body {
		t.Errorf("PUT body =\n%q\nwant\n%q", body, want)
	}
}

func TestCreateCalendarEventRequiresATitle(t *testing.T) {
	f, mock := createEventFixture(t, http.StatusCreated)

	// PHP: empty($sTitle) — only a truly empty title is refused, a whitespace
	// one is stored (the title is not trimmed, unlike the calendar name).
	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Title": "",
		"Start": "2025-11-06",
		"End":   "2025-11-07",
	}), "CreateCalendarEvent")

	if false != document["success"] {
		t.Errorf("success = %v, want false", document["success"])
	}
	if "Event title required" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("an invalid request must not reach the server: %#v", mock.requests)
	}
}

func TestCreateCalendarEventReportsTheHTTPStatus(t *testing.T) {
	f, mock := createEventFixture(t, http.StatusInternalServerError)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Title": "Standup",
		"Start": "2025-11-06T14:30:00.000Z",
		"End":   "2025-11-06T14:45:00.000Z",
	}), "CreateCalendarEvent")

	if false != document["success"] {
		t.Errorf("success = %v, want false", document["success"])
	}
	// The ERROR_CALDAV message carries the status.
	if "CalDAV error: 500" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
	if 1 != len(mock.requests) {
		t.Errorf("the plugin made %d requests, want 1", len(mock.requests))
	}
}

func TestCreateCalendarEventWithoutAnAccount(t *testing.T) {
	mock := newCalDAVServer(t)
	f := newDiscoveryFixture(t, mock)

	guest := &testActions{params: map[string]any{"Title": "Standup"}, main: f.main, env: f.env}

	document := runPluginAction(t, f, guest, "CreateCalendarEvent")

	// The English fallback of the action ("Not logged in") is only used when the
	// plugin's langs/ folder has no ERROR_NOT_LOGGED_IN entry; the fixture ships
	// one, exactly like the shipped plugin.
	if "Please log in first" != document["error"] {
		t.Errorf("error = %v", document["error"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("a guest must not reach the CalDAV server: %#v", mock.requests)
	}
}

func TestCreateCalendarEventRejectsAnUnparsableDate(t *testing.T) {
	f, mock := createEventFixture(t, http.StatusCreated)

	document := runPluginAction(t, f, actionParams(f, map[string]any{
		"Title": "Standup",
		"Start": "not a date",
		"End":   "not a date",
	}), "CreateCalendarEvent")

	if false != document["success"] {
		t.Errorf("success = %v, want false", document["success"])
	}
	if message := phpString(document["error"]); !strings.Contains(message, "Failed to parse time string") {
		t.Errorf("error = %v", document["error"])
	}
	if 0 != len(mock.requests) {
		t.Errorf("an unparsable date must not reach the server: %#v", mock.requests)
	}
}
