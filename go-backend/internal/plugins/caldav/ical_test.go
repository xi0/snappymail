package caldav

// The step 30 acceptance checks for the iCalendar layer: the unfold rule, the
// TEXT escaping, the date/TZID handling, the ATTENDEE/ORGANIZER parsing and
// parseICalendar() against a fixture VCALENDAR (a weekly series with an EXDATE, an
// RDATE, an attendee and a RECURRENCE-ID override).

import (
	"testing"
)

func TestUnfoldIcs(t *testing.T) {
	input := "BEGIN:VCALENDAR\r\nSUMMARY:Hello\r\n World\r\n\tand more\r\nEND:VCALENDAR\r\n"

	want := []string{"BEGIN:VCALENDAR", "SUMMARY:HelloWorldand more", "END:VCALENDAR", ""}
	got := unfoldIcs(input)

	if len(want) != len(got) {
		t.Fatalf("unfoldIcs = %#v, want %#v", got, want)
	}
	for index := range want {
		if want[index] != got[index] {
			t.Errorf("unfoldIcs[%d] = %q, want %q", index, got[index], want[index])
		}
	}

	// A continuation of the very first line is dropped (PHP's "$aResult ?: …").
	if got := unfoldIcs(" folded\r\nBEGIN:VEVENT\r\n"); 2 != len(got) || "BEGIN:VEVENT" != got[0] {
		t.Errorf("leading continuation = %#v", got)
	}

	// A bare CR is a line break too.
	if got := unfoldIcs("A\rB"); 2 != len(got) || "B" != got[1] {
		t.Errorf("CR only = %#v", got)
	}
}

func TestEscapeICS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a,b;c", `a\,b\;c`},
		{"a\nb", `a\nb`},
		{"a\r\nb", `a\nb`},
		{"a\rb", `a\nb`},
		// escapeICS() is the simple variant: a backslash stays untouched.
		{`a\b`, `a\b`},
	}

	for _, test := range cases {
		if got := escapeICS(test.in); test.want != got {
			t.Errorf("escapeICS(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestEscapeICSTextRoundTrip(t *testing.T) {
	if got := escapeICSText(`a\b, c; d`); `a\\b\, c\; d` != got {
		t.Errorf("escapeICSText = %q", got)
	}

	value := "line1\nline2, with; separators and a \\ backslash"
	if got := unescapeICSText(escapeICSText(value)); value != got {
		t.Errorf("round trip = %q, want %q", got, value)
	}

	// strtr() is a single pass: an escaped backslash followed by "n" is a
	// literal backslash and an "n", not a line break.
	if got := unescapeICSText(`a\\nb`); `a\nb` != got {
		t.Errorf("single pass = %q, want %q", got, `a\nb`)
	}

	// "\N" is the same as "\n" (RFC 5545 allows both).
	if got := unescapeICSText(`a\Nb`); "a\nb" != got {
		t.Errorf("\\\\N = %q", got)
	}
}

func TestParseICalDate(t *testing.T) {
	cases := []struct{ value, tzid, want string }{
		{"", "", ""},
		{"20251106", "", "2025-11-06"},
		{" 20251106 ", "", "2025-11-06"},
		{"20251106T143000Z", "", "2025-11-06T14:30:00Z"},
		{"20251106T143000", "", "2025-11-06T14:30:00Z"},
		// An unknown zone is treated as UTC, like PHP's fallback.
		{"20251106T143000", "Unknown/Zone", "2025-11-06T14:30:00Z"},
		{"20251106T143000", "UTC", "2025-11-06T14:30:00Z"},
		{"20251106T143000", "+02:00", "2025-11-06T12:30:00Z"},
		// Anything else is returned unchanged.
		{"2025-11-06T14:30:00Z", "", "2025-11-06T14:30:00Z"},
		{"nonsense", "", "nonsense"},
	}

	for _, test := range cases {
		if got := parseICalDate(test.value, test.tzid); test.want != got {
			t.Errorf("parseICalDate(%q, %q) = %q, want %q", test.value, test.tzid, got, test.want)
		}
	}

	// A TZID from the system tz database is converted (Europe/Berlin is UTC+1 in
	// November). Skip when the database is missing, e.g. in a minimal container.
	if _, ok := icalLocation("Europe/Berlin"); ok {
		if got := parseICalDate("20251106T143000", "Europe/Berlin"); "2025-11-06T13:30:00Z" != got {
			t.Errorf("parseICalDate(Berlin) = %q", got)
		}
		if got := parseICalDate("20250706T143000", `"Europe/Berlin"`); "2025-07-06T12:30:00Z" != got {
			t.Errorf("parseICalDate(Berlin summer) = %q, want the DST offset", got)
		}
	}
}

func TestTzidToUtcBasic(t *testing.T) {
	cases := []struct {
		basic string
		tzid  string
		want  string
		ok    bool
	}{
		{"20251106T143000", "UTC", "20251106T143000Z", true},
		{"20251106T143000", "", "20251106T143000Z", true},
		{"20251106T143000", `"UTC"`, "20251106T143000Z", true},
		{"20251106T143000", "+0200", "20251106T123000Z", true},
		{"20251106T143000", "-05:00", "20251106T193000Z", true},
		// The global "/vendor/…/Region/City" identifiers are reduced to their last
		// segment, which is the IANA name.
		{"20251106T143000", "/mozilla.org/20050126_1/UTC", "20251106T143000Z", true},
		{"20251106T143000", "Not/AZone", "", false},
		{"20251106", "UTC", "", false},
	}

	for _, test := range cases {
		got, ok := tzidToUtcBasic(test.basic, test.tzid)
		if test.ok != ok || test.want != got {
			t.Errorf("tzidToUtcBasic(%q, %q) = (%q, %v), want (%q, %v)",
				test.basic, test.tzid, got, ok, test.want, test.ok)
		}
	}
}

func TestFormatIcsDateValue(t *testing.T) {
	cases := []struct {
		value  string
		allDay bool
		want   string
	}{
		{"", false, ""},
		{"2025-11-06", true, "20251106"},
		{"20251106", true, "20251106"},
		{"2025-11-06T14:30:00.000Z", true, "20251106"},
		{"2025-11-06T14:30:00.000Z", false, "20251106T143000Z"},
		{"20251106T143000Z", false, "20251106T143000Z"},
		{"2025-11-06", false, "20251106T000000Z"},
		// An unparsable value is passed through (PHP's catch branch).
		{"nonsense", false, "nonsense"},
	}

	for _, test := range cases {
		if got := formatIcsDateValue(test.value, test.allDay); test.want != got {
			t.Errorf("formatIcsDateValue(%q, %v) = %q, want %q", test.value, test.allDay, got, test.want)
		}
	}
}

func TestBuildDatePropertyLines(t *testing.T) {
	if got := buildDatePropertyLines("RDATE", "  ", false); "" != got {
		t.Errorf("empty value = %q, want the empty string", got)
	}

	// Newline and comma separated values, all-day format.
	if got, want := buildDatePropertyLines("EXDATE", "2025-11-06\n2025-11-07,2025-11-08", true),
		"EXDATE;VALUE=DATE:20251106\r\nEXDATE;VALUE=DATE:20251107\r\nEXDATE;VALUE=DATE:20251108\r\n"; want != got {
		t.Errorf("EXDATE lines = %q, want %q", got, want)
	}

	// Empty items are dropped, an unparsable one is kept as it is.
	if got, want := buildDatePropertyLines("RDATE", " , nonsense", false), "RDATE:nonsense\r\n"; want != got {
		t.Errorf("RDATE lines = %q, want %q", got, want)
	}
}

func TestInviteMailAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mailto:jane@example.com", "jane@example.com"},
		{"MAILTO:jane@example.com", "jane@example.com"},
		{" <jane@example.com> ", "jane@example.com"},
		{"jane@example.com", "jane@example.com"},
		{`"jane@example.com"`, "jane@example.com"},
		{"", ""},
	}

	for _, test := range cases {
		if got := inviteMailAddress(test.in); test.want != got {
			t.Errorf("inviteMailAddress(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestParseAttendeeLines(t *testing.T) {
	attendees := parseAttendeeLines([]string{
		`ATTENDEE;CUTYPE=GROUP;ROLE=OPT-PARTICIPANT;PARTSTAT=accepted;RSVP=FALSE;CN="Jane Doe":mailto:jane@example.com`,
		`ATTENDEE:mailto:bob@example.com`,
		`ATTENDEE;CN=NoAddress:mailto:`,
		`not a property line`,
	})

	if 2 != len(attendees) {
		t.Fatalf("parseAttendeeLines = %#v, want the two usable lines", attendees)
	}

	first, _ := attendees[0].(map[string]any)
	if "jane@example.com" != first["email"] || "Jane Doe" != first["name"] {
		t.Errorf("attendees[0] = %#v", first)
	}
	// The PARTSTAT and ROLE values are uppercased, RSVP=FALSE disables the reply
	// request and CUTYPE is passed through.
	if "ACCEPTED" != first["partstat"] || "OPT-PARTICIPANT" != first["role"] || "GROUP" != first["cutype"] {
		t.Errorf("attendees[0] = %#v", first)
	}
	if false != first["rsvp"] {
		t.Errorf("attendees[0].rsvp = %v, want false", first["rsvp"])
	}

	second, _ := attendees[1].(map[string]any)
	if "NEEDS-ACTION" != second["partstat"] || "REQ-PARTICIPANT" != second["role"] || "INDIVIDUAL" != second["cutype"] {
		t.Errorf("attendees[1] = %#v, want the defaults", second)
	}
	if true != second["rsvp"] || "" != second["name"] {
		t.Errorf("attendees[1] = %#v", second)
	}
}

func TestParseOrganizerLine(t *testing.T) {
	email, name := parseOrganizerLine(`ORGANIZER;CN="Org Name":mailto:org@example.com`)
	if "org@example.com" != email || "Org Name" != name {
		t.Errorf("parseOrganizerLine = (%q, %q)", email, name)
	}

	if email, name := parseOrganizerLine("ORGANIZER:mailto:org@example.com"); "org@example.com" != email || "" != name {
		t.Errorf("parseOrganizerLine(no CN) = (%q, %q)", email, name)
	}

	if email, name := parseOrganizerLine("nonsense"); "" != email || "" != name {
		t.Errorf("parseOrganizerLine(nonsense) = (%q, %q)", email, name)
	}
}

func TestParseAttendeeInput(t *testing.T) {
	cases := []struct{ in, email, name string }{
		{"jane@example.com", "jane@example.com", ""},
		{"Jane Doe <jane@example.com>", "jane@example.com", "Jane Doe"},
		{` "Jane" <jane@example.com> `, "jane@example.com", "Jane"},
		{"mailto:jane@example.com", "jane@example.com", ""},
		{"<jane@example.com>", "jane@example.com", ""},
	}

	for _, test := range cases {
		parsed := parseAttendeeInput(test.in)
		if test.email != parsed.Email || test.name != parsed.Name {
			t.Errorf("parseAttendeeInput(%q) = (%q, %q), want (%q, %q)",
				test.in, parsed.Email, parsed.Name, test.email, test.name)
		}
	}
}

func TestEscapeICSParamValue(t *testing.T) {
	if got, want := escapeICSParamValue(`a"b\c`+"\r\n"), `"a\"b\\c  "`; want != got {
		t.Errorf("escapeICSParamValue = %q, want %q", got, want)
	}
}

func TestBuildAttendeeLines(t *testing.T) {
	participants := []attendee{
		// The PARTSTAT is uppercased, the organizer and a duplicate address are
		// skipped, an invalid PARTSTAT falls back and a participant without an
		// address is dropped.
		{Email: "jane@example.com", Name: "Jane Doe", Partstat: "accepted"},
		{Email: "ORG@example.com", Name: "Organizer"},
		{Email: "jane@example.com", Name: "Duplicate"},
		{Email: "bob@example.com", Partstat: "NOT-A-PARTSTAT"},
		{Email: "  ", Name: "No address"},
	}

	lines := buildAttendeeLines(participants, "org@example.com", " Org Name ")

	want := []string{
		`ORGANIZER;CN="Org Name":mailto:org@example.com`,
		`ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;RSVP=TRUE;CN="Jane Doe":mailto:jane@example.com`,
		`ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:bob@example.com`,
	}

	if len(want) != len(lines) {
		t.Fatalf("buildAttendeeLines = %#v, want %#v", lines, want)
	}
	for index := range want {
		if want[index] != lines[index] {
			t.Errorf("line %d = %q, want %q", index, lines[index], want[index])
		}
	}

	// Without an organizer address there is no ORGANIZER line.
	if lines := buildAttendeeLines(nil, "", ""); 0 != len(lines) {
		t.Errorf("buildAttendeeLines(nil) = %#v, want none", lines)
	}
}

// fixtureCalendar is the VCALENDAR of TestParseICalendar: a weekly series with an
// EXDATE, an RDATE, an organizer, one attendee and a single moved occurrence.
const fixtureCalendar = `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTIMEZONE
TZID:Europe/Berlin
END:VTIMEZONE
BEGIN:VEVENT
UID:series@example.com
DTSTAMP:20251001T120000Z
DTSTART;TZID=UTC:20251106T143000
DTEND;TZID=UTC:20251106T153000
SUMMARY:Weekly\, sync
DESCRIPTION:Line1\nLine2
LOCATION:Room 1\, HQ
RRULE:FREQ=WEEKLY;BYDAY=TH
EXDATE;TZID=UTC:20251113T143000,20251120T143000
RDATE:20251201T143000Z
SEQUENCE:3
STATUS:CONFIRMED
ORGANIZER;CN="Org Name":mailto:org@example.com
ATTENDEE;PARTSTAT=ACCEPTED;CN=Jane:mailto:jane@example.com
END:VEVENT
BEGIN:VEVENT
UID:series@example.com
RECURRENCE-ID:20251127T143000Z
DTSTART:20251127T160000Z
DTEND:20251127T170000Z
SUMMARY:Moved sync
END:VEVENT
END:VCALENDAR`

// eventDocument is one event of parseICalendar() as a document.
func eventDocument(t *testing.T, events []any, index int) map[string]any {
	t.Helper()

	if index >= len(events) {
		t.Fatalf("parseICalendar returned %d events, want at least %d", len(events), index+1)
	}
	document, ok := events[index].(map[string]any)
	if !ok {
		t.Fatalf("event %d = %#v, want a document", index, events[index])
	}
	return document
}

func TestParseICalendar(t *testing.T) {
	events := parseICalendar(fixtureCalendar, "/dav/cal/user@example.com/personal/series.ics", `"etag-1"`)

	if 2 != len(events) {
		t.Fatalf("parseICalendar returned %d events, want the master and its override", len(events))
	}

	master := eventDocument(t, events, 0)

	// The scalar properties, with the TEXT values unescaped and the TZID values
	// converted to UTC.
	if `series@example.com` != master["uid"] {
		t.Errorf("uid = %v", master["uid"])
	}
	if "Weekly, sync" != master["summary"] {
		t.Errorf("summary = %v", master["summary"])
	}
	if "2025-11-06T14:30:00Z" != master["dtstart"] || "2025-11-06T15:30:00Z" != master["dtend"] {
		t.Errorf("dtstart/dtend = %v/%v", master["dtstart"], master["dtend"])
	}
	if "Line1\nLine2" != master["description"] {
		t.Errorf("description = %v", master["description"])
	}
	if "Room 1, HQ" != master["location"] {
		t.Errorf("location = %v", master["location"])
	}
	if false != master["allDay"] {
		t.Errorf("allDay = %v", master["allDay"])
	}

	// href/etag identify the resource the frontend PUTs and DELETEs.
	if "/dav/cal/user@example.com/personal/series.ics" != master["href"] || `"etag-1"` != master["etag"] {
		t.Errorf("href/etag = %v/%v", master["href"], master["etag"])
	}

	if "FREQ=WEEKLY;BYDAY=TH" != master["rrule"] || "3" != master["sequence"] || "CONFIRMED" != master["status"] {
		t.Errorf("rrule/sequence/status = %v/%v/%v", master["rrule"], master["sequence"], master["status"])
	}
	if "" != master["recurrenceId"] {
		t.Errorf("recurrenceId = %v, want empty on the master", master["recurrenceId"])
	}

	// The multi-value properties: one entry per comma separated value.
	exdate, _ := master["exdate"].([]any)
	if 2 != len(exdate) || "2025-11-13T14:30:00Z" != exdate[0] || "2025-11-20T14:30:00Z" != exdate[1] {
		t.Errorf("exdate = %#v", master["exdate"])
	}
	rdate, _ := master["rdate"].([]any)
	if 1 != len(rdate) || "2025-12-01T14:30:00Z" != rdate[0] {
		t.Errorf("rdate = %#v", master["rdate"])
	}

	// ORGANIZER and ATTENDEE.
	if "org@example.com" != master["organizer"] || "Org Name" != master["organizerName"] {
		t.Errorf("organizer = %v/%v", master["organizer"], master["organizerName"])
	}
	attendees, _ := master["attendees"].([]any)
	if 1 != len(attendees) {
		t.Fatalf("attendees = %#v", master["attendees"])
	}
	first, _ := attendees[0].(map[string]any)
	if "jane@example.com" != first["email"] || "ACCEPTED" != first["partstat"] || "Jane" != first["name"] {
		t.Errorf("attendees[0] = %#v", first)
	}

	// The moved occurrence: keyed by its RECURRENCE-ID, with the defaults of a
	// missing SEQUENCE and an empty recurrence list.
	override := eventDocument(t, events, 1)
	if "2025-11-27T14:30:00Z" != override["recurrenceId"] {
		t.Errorf("override.recurrenceId = %v", override["recurrenceId"])
	}
	if "Moved sync" != override["summary"] || "2025-11-27T16:00:00Z" != override["dtstart"] {
		t.Errorf("override = %#v", override)
	}
	if "0" != override["sequence"] || "" != override["status"] {
		t.Errorf("override.sequence/status = %v/%v", override["sequence"], override["status"])
	}
	if rdate, _ := override["rdate"].([]any); 0 != len(rdate) {
		t.Errorf("override.rdate = %#v, want the empty list", override["rdate"])
	}
}

func TestParseICalendarDefaults(t *testing.T) {
	// An all-day event without SUMMARY, ORGANIZER and ATTENDEE.
	events := parseICalendar("BEGIN:VEVENT\nDTSTART;VALUE=DATE:20251106\nDTEND;VALUE=DATE:20251107\nEND:VEVENT", "", "")

	document := eventDocument(t, events, 0)

	if "Untitled" != document["summary"] {
		t.Errorf("summary = %v, want the Untitled default", document["summary"])
	}
	if true != document["allDay"] || "2025-11-06" != document["dtstart"] {
		t.Errorf("allDay/dtstart = %v/%v", document["allDay"], document["dtstart"])
	}
	if "" != document["organizer"] || "" != document["organizerName"] {
		t.Errorf("organizer = %v/%v", document["organizer"], document["organizerName"])
	}
	if attendees, _ := document["attendees"].([]any); 0 != len(attendees) {
		t.Errorf("attendees = %#v, want none", document["attendees"])
	}

	// A property line without a colon and text outside a VEVENT are ignored.
	if events := parseICalendar("BEGIN:VCALENDAR\nBROKEN\nEND:VCALENDAR", "h", "e"); 0 != len(events) {
		t.Errorf("parseICalendar(text without VEVENT) = %#v, want none", events)
	}
}
