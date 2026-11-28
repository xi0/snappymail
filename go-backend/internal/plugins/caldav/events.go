package caldav

// Reading and creating calendar events: the port of CaldavPlugin::
// DoGetCalendarEvents() and DoCreateCalendarEvent() (plugins/caldav/index.php
// lines 930–1330) plus parseCalDAVResponse(), the multistatus reader of a
// calendar-query REPORT.
//
// The event JSON is consumed by calendar-dialog.js, which groups the VEVENTs that
// share one href (a recurring resource yields the master plus its
// RECURRENCE-ID overrides); the field names are therefore part of the contract
// and come from parseICalendar() in ical.go.
//
// The multistatus parsing replaces DOMDocument/DOMXPath with encoding/xml and
// matches elements by local name (see the package comment of calendars.go), so a
// server that returns unqualified elements is tolerated as well.

import (
	"encoding/xml"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

// calendarEventReport is the REPORT body of DoGetCalendarEvents(): a
// calendar-query for every VEVENT of a collection, with the etag and the
// iCalendar data of each resource.
const calendarEventReport = `<?xml version="1.0" encoding="utf-8" ?>` + "\n" +
	`<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` + "\n" +
	"  <D:prop>\n" +
	"    <D:getetag />\n" +
	"    <C:calendar-data />\n" +
	"  </D:prop>\n" +
	"  <C:filter>\n" +
	`    <C:comp-filter name="VCALENDAR">` + "\n" +
	`      <C:comp-filter name="VEVENT" />` + "\n" +
	"    </C:comp-filter>\n" +
	"  </C:filter>\n" +
	"</C:calendar-query>"

// doPluginGetCalendarEvents is CaldavPlugin::DoGetCalendarEvents(): the events of
// one calendar collection.
//
// A collection that answers with anything but 207 (an auth or server error)
// yields an empty list; only a failed decryption is reported as "error" (the
// PHP method returns the exception message then).
func (p *Plugin) doPluginGetCalendarEvents(env plugin.Env, ctx plugin.ActionContext) (any, error) {
	respond := func(result any) (any, error) {
		return ctx.DefaultResponse(result, nil), nil
	}

	events := func(additional map[string]any) map[string]any {
		document := map[string]any{"events": []any{}}
		for key, value := range additional {
			document[key] = value
		}
		return document
	}

	request := requestFromActions(ctx)
	if nil == request || nil == request.current {
		return respond(events(map[string]any{
			"message": msg(env, request, "ERROR_NOT_LOGGED_IN", nil, "Please log in first"),
		}))
	}

	config := calendarConfigFor(env, request)
	if nil == config {
		return respond(events(map[string]any{
			"message": msg(env, request, "ERROR_NOT_CONFIGURED_MSG", nil,
				"Calendar not configured yet. Please check settings."),
		}))
	}

	// PHP checks the main account itself before decrypting, because
	// getDecryptedPassword() is what needs its crypt key.
	if nil == request.main {
		return respond(events(map[string]any{
			"error": msg(env, request, "ERROR_NO_ENCRYPTION_KEY", nil, "Cannot access encryption key"),
		}))
	}

	password, usable, err := decryptedPassword(request, config)
	if nil != err {
		return respond(events(map[string]any{"error": err.Error()}))
	}
	if !usable {
		return respond(events(map[string]any{
			"error": msg(env, request, "ERROR_NO_ENCRYPTION_KEY", nil, "Cannot access encryption key"),
		}))
	}

	collection := calendarUrl(config, phpString(ctx.GetParam("CalendarId", defaultCalendarID))) + "/"

	result := p.makeCalDAVRequest(
		collection,
		"REPORT",
		config.User,
		password,
		calendarEventReport,
		"Content-Type: application/xml; charset=utf-8",
		"Depth: 1",
	)

	eventList := []any{}
	if 207 == result.Code {
		eventList = parseCalDAVResponse(result.Body)
	}

	count := phpString(len(eventList))

	return respond(map[string]any{
		"events": eventList,
		"message": msg(env, request, "LOADED_EVENTS", map[string]string{"COUNT": count},
			"Loaded "+count+" events"),
	})
}

// doPluginCreateCalendarEvent is CaldavPlugin::DoCreateCalendarEvent(): create a
// single event resource inside a calendar collection.
//
// The iCalendar text is assembled exactly like the PHP string concatenation
// (order of the properties, CRLF line endings, the ";VALUE=DATE" parameter of an
// all-day event), because the resulting resource is what every other CalDAV
// client reads.
func (p *Plugin) doPluginCreateCalendarEvent(env plugin.Env, ctx plugin.ActionContext) (any, error) {
	respond := func(result map[string]any) (any, error) {
		return ctx.DefaultResponse(result, nil), nil
	}

	fail := func(message string) (any, error) {
		return respond(map[string]any{"success": false, "error": message})
	}

	access := p.calendarAccessFor(env, ctx, "Not logged in", "Calendar not configured")
	if access.failed() {
		return fail(access.errorMessage(env))
	}

	config := access.config

	calendarID := phpString(ctx.GetParam("CalendarId", defaultCalendarID))
	title := phpString(ctx.GetParam("Title", ""))
	start := phpString(ctx.GetParam("Start", ""))
	end := phpString(ctx.GetParam("End", ""))
	allDay := !phpEmpty(ctx.GetParam("AllDay", false))
	description := phpString(ctx.GetParam("Description", ""))
	location := phpString(ctx.GetParam("Location", ""))
	rrule := strings.TrimSpace(phpString(ctx.GetParam("Rrule", "")))
	exdate := phpString(ctx.GetParam("Exdate", ""))
	rdate := phpString(ctx.GetParam("Rdate", ""))
	attendees := readAttendeesParam(ctx)

	if phpEmpty(title) {
		return fail(msg(env, access.request, "ERROR_TITLE_REQUIRED", nil, "Event title required"))
	}

	startFormatted, endFormatted, err := eventDateValues(start, end, allDay)
	if nil != err {
		return fail(err.Error())
	}

	// Generate the UID for the event: uniqid('event-') . '@' . <caldav user>.
	uid := phpUniqid("event-") + "@" + config.User

	valueParameter := ""
	if allDay {
		valueParameter = ";VALUE=DATE"
	}

	var ics strings.Builder
	ics.WriteString("BEGIN:VCALENDAR\r\n")
	ics.WriteString("VERSION:2.0\r\n")
	ics.WriteString("PRODID:-//Mailbux//CalDAV Plugin//EN\r\n")
	ics.WriteString("BEGIN:VEVENT\r\n")
	ics.WriteString("UID:" + uid + "\r\n")
	ics.WriteString("DTSTAMP:" + icalTimestamp() + "\r\n")
	ics.WriteString("DTSTART" + valueParameter + ":" + startFormatted + "\r\n")
	ics.WriteString("DTEND" + valueParameter + ":" + endFormatted + "\r\n")
	ics.WriteString("SUMMARY:" + escapeICS(title) + "\r\n")

	if !phpEmpty(description) {
		ics.WriteString("DESCRIPTION:" + escapeICS(description) + "\r\n")
	}
	if !phpEmpty(location) {
		ics.WriteString("LOCATION:" + escapeICS(location) + "\r\n")
	}
	if 0 < len(attendees) {
		for _, line := range buildAttendeeLines(attendees, config.User, accountName(access.request)) {
			ics.WriteString(line + "\r\n")
		}
	}
	if "" != rrule {
		ics.WriteString("RRULE:" + rrule + "\r\n")
	}
	ics.WriteString(buildDatePropertyLines("RDATE", rdate, allDay))
	ics.WriteString(buildDatePropertyLines("EXDATE", exdate, allDay))
	ics.WriteString("END:VEVENT\r\n")
	ics.WriteString("END:VCALENDAR\r\n")

	eventURL := calendarUrl(config, calendarID) + "/" + rawURLEncode(uid) + ".ics"

	result := p.makeCalDAVRequest(
		eventURL,
		"PUT",
		config.User,
		access.password,
		ics.String(),
		"Content-Type: text/calendar; charset=utf-8",
	)

	if 201 == result.Code || 204 == result.Code {
		return respond(map[string]any{"success": true, "uid": uid})
	}

	return fail(caldavError(env, access.request, result.Code))
}

// eventDateValues formats the Start/End parameters of a create/update request.
//
// An all-day event keeps the date as it was sent (only the dashes are removed),
// so the day does not shift for a user east of UTC; a timed event is parsed as
// the ISO string the frontend sends and converted to UTC.
func eventDateValues(start, end string, allDay bool) (string, string, error) {
	if allDay {
		// PHP: str_replace('-', '', $sStart) — no trimming, no timezone maths.
		return strings.ReplaceAll(start, "-", ""), strings.ReplaceAll(end, "-", ""), nil
	}

	dtStart, err := icalDateTime(start)
	if nil != err {
		return "", "", err
	}
	dtEnd, err := icalDateTime(end)
	if nil != err {
		return "", "", err
	}

	return icalICSDate(dtStart), icalICSDate(dtEnd), nil
}

// accountName is `$oAccount->Name() ?: ''` of the organizer block.
func accountName(request *request) string {
	if nil == request || nil == request.current {
		return ""
	}
	return request.current.Name()
}

// caldavError is the ERROR_CALDAV message of a failed request: the translated
// "CalDAV error: %CODE%" text with the HTTP status filled in.
func caldavError(env plugin.Env, request *request, code int) string {
	status := phpString(code)
	return msg(env, request, "ERROR_CALDAV", map[string]string{"CODE": status}, "CalDAV error: "+status)
}

// The multistatus document of a REPORT response. The element names are matched
// by local name, like the PROPFIND document in calendars.go.
type davReportMultistatus struct {
	// Responses is //D:response.
	Responses []davReportResponse `xml:"response"`
}

// davReportResponse is one D:response of a REPORT multistatus document.
type davReportResponse struct {
	// Href is ./D:href: the resource URL of the calendar object.
	Href string `xml:"href"`
	// Propstat holds the property groups of the response.
	Propstat []davReportPropstat `xml:"propstat"`
}

// davReportPropstat is one D:propstat element.
type davReportPropstat struct {
	// Prop is the property group.
	Prop davReportProperty `xml:"prop"`
}

// davReportProperty is the D:prop element of a calendar-query REPORT.
//
// The properties are pointers because the absence of a property and an empty
// value mean different things to parseCalDAVResponse(): a response without a
// calendar-data element (a collection, a deleted resource) is skipped, while an
// empty one is parsed and simply yields no events.
//
// Only the standard "propstat > prop" nesting is read (which every reviewed
// server uses); the PHP XPath query would also find the properties anywhere in
// the response, at the price of the DOM it needs for that.
type davReportProperty struct {
	// ETag is D:getetag.
	ETag *string `xml:"getetag"`
	// CalendarData is C:calendar-data.
	CalendarData *string `xml:"calendar-data"`
}

// merged is the property set of the response: the first present value of each
// property wins, like the PHP XPath query returns the first match in document
// order (a found property is reported in a 200 propstat, a missing one in a 404
// propstat).
func (r davReportResponse) merged() davReportProperty {
	result := davReportProperty{}

	for _, propstat := range r.Propstat {
		if nil == result.ETag {
			result.ETag = propstat.Prop.ETag
		}
		if nil == result.CalendarData {
			result.CalendarData = propstat.Prop.CalendarData
		}
	}

	return result
}

// parseCalDAVResponse is CaldavPlugin::parseCalDAVResponse(): the events of a
// calendar-query REPORT multistatus.
//
// Every response carries the href of the calendar object resource (used by the
// frontend for the PUT/DELETE of the exact resource) and its etag; the
// iCalendar data is parsed into the VEVENT list of parseICalendar().
func parseCalDAVResponse(document string) []any {
	events := []any{}

	var multistatus davReportMultistatus
	if err := xml.Unmarshal([]byte(document), &multistatus); nil != err {
		// PHP swallows the DOMDocument warning and returns the empty list.
		return events
	}

	for _, response := range multistatus.Responses {
		property := response.merged()
		if nil == property.CalendarData {
			continue
		}

		etag := ""
		if nil != property.ETag {
			etag = strings.TrimSpace(*property.ETag)
		}

		events = append(events, parseICalendar(
			*property.CalendarData,
			strings.TrimSpace(response.Href),
			etag,
		)...)
	}

	return events
}
