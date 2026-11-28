package caldav

// The iCalendar (RFC 5545) helpers of the plugin: the unfold rule, the TEXT
// escaping, the date/TZID handling of parseICalDate()/tzidToUtcBasic(), the
// ATTENDEE and ORGANIZER parsing, the date property lines of the create path,
// and parseICalendar() — the VEVENT reader that turns a stored calendar object
// resource into the event JSON shape calendar-dialog.js consumes:
//
//	uid summary dtstart dtend description location allDay href etag rrule
//	rdate exdate recurrenceId sequence status organizer organizerName attendees
//
// Deliberate deviations from the PHP plugin (kept small and documented):
//
//   - `new \DateTimeZone($sTzid)` becomes time.LoadLocation(): the IANA names and
//     the legacy aliases of the system tz database resolve the same way, a
//     numeric offset ("+02:00") is handled explicitly, and a zone that cannot be
//     resolved yields null/false — which makes parseICalDate() fall back to
//     "treat the wall clock as UTC", exactly like PHP when the DateTimeZone
//     constructor throws;
//   - `new \DateTime($sValue)` (SnappyMail runs with date_default_timezone_set
//     ('UTC')) becomes an explicit layout list: the frontend only sends
//     Date#toISOString() values and the date-only picker strings, so a
//     malformed value is reported instead of guessed. An empty value is "now",
//     exactly like PHP's DateTime.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

// phpTrimCutset is the default charlist of PHP's trim()/rtrim()/ltrim().
const phpTrimCutset = " \t\n\r\x00\x0B"

// The regular expressions of the ported PHP code.
var (
	// icalDatePattern is the '/^(\d{4})(\d{2})(\d{2})$/' branch of
	// parseICalDate() (a DATE value).
	icalDatePattern = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})$`)
	// icalDateTimeUTCPattern is the '/…Z$/' branch (an already UTC value).
	icalDateTimeUTCPattern = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})Z$`)
	// icalDateTimePattern is the floating time / TZID branch, which is also the
	// shape tzidToUtcBasic() expects.
	icalDateTimePattern = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})T([0-9]{2})([0-9]{2})([0-9]{2})$`)
	// icalTZIDPattern is the '/;TZID=([^;:]+)/i' parameter match of
	// parseICalendar()/convertTzidLine().
	icalTZIDPattern = regexp.MustCompile(`(?i);TZID=([^;:]+)`)
	// icalCNPattern is the '/;CN=([^;:]+)/i' parameter match of
	// parseOrganizerLine().
	icalCNPattern = regexp.MustCompile(`(?i);CN=([^;:]+)`)
	// icalDatePrefixPattern is the '/^(\d{4})(\d{2})(\d{2})/' test of
	// formatIcsDateValue().
	icalDatePrefixPattern = regexp.MustCompile(`^([0-9]{4})([0-9]{2})([0-9]{2})`)
	// icalBasicDateTimePattern is the '/^\d{8}T\d{6}Z?$/' test of
	// formatIcsDateValue().
	icalBasicDateTimePattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z?$`)
	// icalLineSplitPattern is the preg_split('/[\r\n]+/') of
	// buildDatePropertyLinesArr().
	icalLineSplitPattern = regexp.MustCompile(`[\r\n]+`)
	// icalOffsetPattern is the numeric DateTimeZone offset ("+02:00", "-0500").
	icalOffsetPattern = regexp.MustCompile(`^([+-])([0-9]{2}):?([0-9]{2})$`)
	// attendeeInputPattern is the '/^(.*)<([^>]+)>\s*$/u' of
	// parseAttendeeInput(): the greedy ".*" keeps the last angle bracket pair.
	attendeeInputPattern = regexp.MustCompile(`^(.*)<([^>]+)>\s*$`)
)

// icalMultiValueKeys is the $aMulti list of parseICalendar(): the properties
// that may legally occur more than once in a VEVENT.
var icalMultiValueKeys = map[string]bool{"rdate": true, "exdate": true, "categories": true}

// icalParticipantParts is the PARTSTAT whitelist of buildAttendeeLines().
var icalParticipantParts = map[string]bool{
	"NEEDS-ACTION": true,
	"ACCEPTED":     true,
	"DECLINED":     true,
	"TENTATIVE":    true,
	"DELEGATED":    true,
}

// icalDateTimeLayouts are the layouts icalDateTime() accepts. The frontend sends
// Date#toISOString() ("2006-01-02T15:04:05.000Z") or a date-only string from the
// pickers; the iCalendar basic forms are accepted as well because a client may
// send them back unchanged.
var icalDateTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
	"20060102T150405Z",
	"20060102T150405",
}

// attendee is one participant: the {email,name,partstat} triple of
// readAttendeesParam() and the input of buildAttendeeLines().
type attendee struct {
	// Email is the mail address without the "mailto:" prefix.
	Email string
	// Name is the display name (CN) when one is known.
	Name string
	// Partstat is the participation status (empty defaults to NEEDS-ACTION).
	Partstat string
}

/* ------------------------------------------------------------------ lines */

// unfoldIcs is CaldavPlugin::unfoldIcs(): an iCalendar text as an array of
// logical lines (RFC 5545 3.1).
//
// A line starting with a space or a tab continues the previous one; a
// continuation of the very first line is dropped, like PHP's "$aResult ?: …"
// guard.
func unfoldIcs(text string) []string {
	normalised := strings.ReplaceAll(text, "\r\n", "\n")
	normalised = strings.ReplaceAll(normalised, "\r", "\n")

	result := []string{}
	for _, line := range strings.Split(normalised, "\n") {
		if "" != line && (' ' == line[0] || '\t' == line[0]) {
			if 0 < len(result) {
				result[len(result)-1] += line[1:]
			}
			continue
		}
		result = append(result, line)
	}

	return result
}

// escapeICS is CaldavPlugin::escapeICS(): the escape of DoCreateCalendarEvent(),
// which also escapes "," and ";" but leaves a backslash untouched (unlike
// escapeICSText(), the RFC 5545 TEXT escape).
func escapeICS(text string) string {
	// str_replace() with arrays runs the replacements one after the other, so the
	// order below is the observable behaviour.
	text = strings.ReplaceAll(text, "\r\n", "\\n")
	text = strings.ReplaceAll(text, "\n", "\\n")
	text = strings.ReplaceAll(text, "\r", "\\n")
	text = strings.ReplaceAll(text, ",", "\\,")
	text = strings.ReplaceAll(text, ";", "\\;")
	return text
}

// escapeICSText is CaldavPlugin::escapeICSText(): the RFC 5545 3.3.11 TEXT
// escape (backslash first, so nothing is escaped twice).
func escapeICSText(text string) string {
	text = strings.ReplaceAll(text, "\\", "\\\\")
	text = strings.ReplaceAll(text, "\r\n", "\\n")
	text = strings.ReplaceAll(text, "\n", "\\n")
	text = strings.ReplaceAll(text, "\r", "\\n")
	text = strings.ReplaceAll(text, ",", "\\,")
	text = strings.ReplaceAll(text, ";", "\\;")
	return text
}

// unescapeICSText is CaldavPlugin::unescapeICSText(): the inverse of
// escapeICSText(), done the way PHP's strtr() does it — a single pass, so a
// literal "\\" is never confused with the "\n" / "\," / "\;" escapes.
func unescapeICSText(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))

	for index := 0; index < len(text); index++ {
		character := text[index]
		if '\\' == character && index+1 < len(text) {
			switch text[index+1] {
			case 'n', 'N':
				builder.WriteByte('\n')
				index++
				continue
			case ',':
				builder.WriteByte(',')
				index++
				continue
			case ';':
				builder.WriteByte(';')
				index++
				continue
			case '\\':
				builder.WriteByte('\\')
				index++
				continue
			}
		}
		builder.WriteByte(character)
	}

	return builder.String()
}

/* ------------------------------------------------------------------ dates */

// parseICalDate is CaldavPlugin::parseICalDate(): an iCalendar date value as an
// ISO string.
//
// A floating local time that carries a TZID is converted to UTC so the instant is
// preserved regardless of the viewer's timezone; a value that is neither a DATE
// nor a DATE-TIME is returned unchanged.
func parseICalDate(value, tzid string) string {
	text := strings.TrimSpace(value)
	if "" == text {
		return ""
	}

	if match := icalDatePattern.FindStringSubmatch(text); nil != match {
		return match[1] + "-" + match[2] + "-" + match[3]
	}

	if match := icalDateTimeUTCPattern.FindStringSubmatch(text); nil != match {
		return match[1] + "-" + match[2] + "-" + match[3] + "T" +
			match[4] + ":" + match[5] + ":" + match[6] + "Z"
	}

	if match := icalDateTimePattern.FindStringSubmatch(text); nil != match {
		if "" != tzid {
			if utc, ok := tzidToUtcBasic(text, tzid); ok {
				// PHP recurses into itself: the UTC value takes the Z branch.
				return parseICalDate(utc, "")
			}
		}
		// Unknown/absent zone: preserve the previous behaviour (treat as UTC).
		return match[1] + "-" + match[2] + "-" + match[3] + "T" +
			match[4] + ":" + match[5] + ":" + match[6] + "Z"
	}

	return text
}

// tzidToUtcBasic is CaldavPlugin::tzidToUtcBasic(): a TZID wall-clock value
// (YYYYMMDDTHHmmss) as a UTC value, or ok = false when the zone is unknown.
func tzidToUtcBasic(basic, tzid string) (string, bool) {
	match := icalDateTimePattern.FindStringSubmatch(basic)
	if nil == match {
		return "", false
	}

	zone := strings.Trim(tzid, `"`)
	if "" == zone || strings.EqualFold(zone, "UTC") {
		return basic + "Z", true
	}

	// Try the TZID as-is, then the common variants: a leading slash and the
	// global "/vendor/.../Region/City" identifiers.
	candidates := []string{zone}
	if strings.Contains(zone, "/") {
		candidates = append(candidates,
			strings.TrimLeft(zone, "/"),
			zone[strings.LastIndex(zone, "/")+1:])
	}

	wall := match[1] + "-" + match[2] + "-" + match[3] + " " +
		match[4] + ":" + match[5] + ":" + match[6]

	for _, candidate := range candidates {
		location, ok := icalLocation(candidate)
		if !ok {
			continue
		}
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", wall, location)
		if nil != err {
			continue
		}
		return parsed.UTC().Format("20060102T150405Z"), true
	}

	return "", false
}

// icalLocation resolves a TZID to a *time.Location.
//
// The system tz database provides the IANA names (and the legacy abbreviations
// such as "CET" or "EST" that PHP's DateTimeZone also accepts); a numeric offset
// is handled explicitly because time.LoadLocation does not know "+02:00".
func icalLocation(zone string) (*time.Location, bool) {
	name := strings.TrimSpace(zone)
	if "" == name {
		return nil, false
	}

	if match := icalOffsetPattern.FindStringSubmatch(name); nil != match {
		hours, _ := strconv.Atoi(match[2])
		minutes, _ := strconv.Atoi(match[3])
		offset := (hours*60 + minutes) * 60
		if "-" == match[1] {
			offset = -offset
		}
		return time.FixedZone(name, offset), true
	}

	location, err := time.LoadLocation(name)
	if nil != err {
		return nil, false
	}
	return location, true
}

// icalDateTime is `new \DateTime($sValue, new \DateTimeZone('UTC'))`: the value
// as a UTC instant.
//
// PHP's DateTime reads an empty string as "now" (it is the same as "now"), so the
// create path keeps that behaviour: a request without a Start/End stores the
// current instant instead of failing.
func icalDateTime(value string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if "" == text {
		return time.Now().UTC(), nil
	}
	for _, layout := range icalDateTimeLayouts {
		if parsed, err := time.ParseInLocation(layout, text, time.UTC); nil == err {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("Failed to parse time string (%s): not a supported date", text)
}

// icalTimestamp is gmdate('Ymd\THis\Z') of the current instant (DTSTAMP).
func icalTimestamp() string { return time.Now().UTC().Format("20060102T150405Z") }

// icalICSDate is format('Ymd\THis\Z') of a UTC instant.
func icalICSDate(value time.Time) string { return value.UTC().Format("20060102T150405Z") }

/* ------------------------------------------------------ RDATE/EXDATE lines */

// formatIcsDateValue is CaldavPlugin::formatIcsDateValue(): a client supplied
// date value (ISO or iCalendar basic) as an iCalendar property value. A timed
// value is converted to UTC, an all-day value to YYYYMMDD.
func formatIcsDateValue(value string, allDay bool) string {
	text := strings.TrimSpace(value)
	if "" == text {
		return ""
	}

	if allDay {
		if match := icalDatePrefixPattern.FindStringSubmatch(text); nil != match {
			return match[1] + match[2] + match[3]
		}
		head := text
		if 10 < len(head) {
			head = head[:10]
		}
		return strings.ReplaceAll(head, "-", "")
	}

	if icalBasicDateTimePattern.MatchString(text) {
		return text
	}

	parsed, err := icalDateTime(text)
	if nil != err {
		return text
	}
	return icalICSDate(parsed)
}

// buildDatePropertyLinesArr is CaldavPlugin::buildDatePropertyLinesArr(): the
// RDATE/EXDATE lines of a client supplied value list (comma and/or newline
// separated, ISO or iCalendar form).
func buildDatePropertyLinesArr(name, values string, allDay bool) []string {
	out := []string{}

	trimmed := strings.TrimSpace(values)
	if "" == trimmed {
		return out
	}

	valueParameter := ""
	if allDay {
		valueParameter = ";VALUE=DATE"
	}

	for _, line := range icalLineSplitPattern.Split(trimmed, -1) {
		for _, value := range strings.Split(line, ",") {
			formatted := formatIcsDateValue(value, allDay)
			if "" != formatted {
				out = append(out, name+valueParameter+":"+formatted)
			}
		}
	}

	return out
}

// buildDatePropertyLines is CaldavPlugin::buildDatePropertyLines(): the lines of
// buildDatePropertyLinesArr() ready to concatenate into an ICS blob (each line
// terminated, and nothing at all for an empty value list).
func buildDatePropertyLines(name, values string, allDay bool) string {
	lines := buildDatePropertyLinesArr(name, values, allDay)
	if 0 == len(lines) {
		return ""
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

/* -------------------------------------------------------------- attendees */

// inviteMailAddress is CaldavPlugin::inviteMailAddress(): the e-mail address of
// a CAL-ADDRESS / mailto value.
func inviteMailAddress(value string) string {
	text := strings.TrimSpace(value)
	if 7 <= len(text) && strings.EqualFold(text[:7], "mailto:") {
		text = text[7:]
	}
	return strings.Trim(text, " \t<>\"")
}

// icalParameters is the parameter map of a property line head ("NAME;A=B;C=D"):
// the property name is dropped and the parameter names are uppercased, exactly
// like the explode()/array_shift()/trim('"') loops of the PHP code.
func icalParameters(head string) map[string]string {
	params := map[string]string{}

	parts := strings.Split(head, ";")
	if 0 < len(parts) {
		parts = parts[1:]
	}

	for _, part := range parts {
		name, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		params[strings.ToUpper(strings.TrimSpace(name))] = strings.Trim(value, `"`)
	}

	return params
}

// parseAttendeeLines is CaldavPlugin::parseAttendeeLines(): the raw ATTENDEE
// lines (with their parameters) as the {email,name,partstat,role,rsvp,cutype}
// documents the frontend renders.
func parseAttendeeLines(lines []string) []any {
	result := []any{}

	for _, line := range lines {
		position := strings.IndexByte(line, ':')
		if 0 > position {
			continue
		}

		value := line[position+1:]
		params := icalParameters(line[:position])

		email := inviteMailAddress(value)
		if "" == email {
			continue
		}

		partstat := "NEEDS-ACTION"
		if parameter, ok := params["PARTSTAT"]; ok {
			partstat = strings.ToUpper(parameter)
		}

		role := "REQ-PARTICIPANT"
		if parameter, ok := params["ROLE"]; ok {
			role = strings.ToUpper(parameter)
		}

		// PHP: empty($aParams['RSVP']) || 0 !== strcasecmp($aParams['RSVP'], 'FALSE')
		rsvp := true
		if parameter, ok := params["RSVP"]; ok && !phpEmpty(parameter) && strings.EqualFold(parameter, "FALSE") {
			rsvp = false
		}

		cutype := "INDIVIDUAL"
		if parameter, ok := params["CUTYPE"]; ok {
			cutype = parameter
		}

		name := ""
		if parameter, ok := params["CN"]; ok {
			name = parameter
		}

		result = append(result, map[string]any{
			"email":    email,
			"name":     name,
			"partstat": partstat,
			"role":     role,
			"rsvp":     rsvp,
			"cutype":   cutype,
		})
	}

	return result
}

// parseOrganizerLine is CaldavPlugin::parseOrganizerLine(): a raw ORGANIZER line
// as email + display name.
func parseOrganizerLine(line string) (email, name string) {
	position := strings.IndexByte(line, ':')
	if 0 > position {
		return "", ""
	}

	head := line[:position]

	if match := icalCNPattern.FindStringSubmatch(head); nil != match {
		name = strings.Trim(match[1], `"`)
	}

	return inviteMailAddress(line[position+1:]), name
}

// parseAttendeeInput is CaldavPlugin::parseAttendeeInput(): a free-form
// "Name <email>" (or plain "email") participant.
func parseAttendeeInput(input string) attendee {
	value := strings.TrimSpace(input)
	name := ""
	email := value

	if match := attendeeInputPattern.FindStringSubmatch(value); nil != match {
		name = strings.Trim(match[1], " \t\"'")
		email = strings.TrimSpace(match[2])
	}

	if 7 <= len(email) && strings.EqualFold(email[:7], "mailto:") {
		email = email[7:]
	}

	return attendee{Email: strings.Trim(email, " \t<>\"'"), Name: name}
}

// escapeICSParamValue is CaldavPlugin::escapeICSParamValue(): a quoted
// iCalendar parameter value (a CN, for instance).
func escapeICSParamValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return `"` + value + `"`
}

// buildAttendeeLines is CaldavPlugin::buildAttendeeLines(): the ORGANIZER line
// (when the organizer has an address) followed by one ATTENDEE line per unique
// participant that is not the organizer.
//
// PHP also accepts plain strings in $aAttendees and runs parseAttendeeInput() on
// them; the only callers build the list from readAttendeesParam(), which already
// normalises strings, so the struct list is enough here.
func buildAttendeeLines(attendees []attendee, organizerEmail, organizerName string) []string {
	out := []string{}

	orgEmail := strings.TrimSpace(organizerEmail)
	orgName := strings.TrimSpace(organizerName)

	if "" != orgEmail {
		head := "ORGANIZER"
		if "" != orgName {
			head += ";CN=" + escapeICSParamValue(orgName)
		}
		out = append(out, head+":mailto:"+orgEmail)
	}

	seen := map[string]bool{}

	for _, participant := range attendees {
		email := strings.TrimSpace(participant.Email)
		if "" == email || strings.EqualFold(email, orgEmail) {
			continue
		}

		lower := strings.ToLower(email)
		if seen[lower] {
			continue
		}
		seen[lower] = true

		partstat := strings.ToUpper(strings.TrimSpace(participant.Partstat))
		if !icalParticipantParts[partstat] {
			partstat = "NEEDS-ACTION"
		}

		head := "ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=" + partstat + ";RSVP=TRUE"
		if name := strings.TrimSpace(participant.Name); "" != name {
			head += ";CN=" + escapeICSParamValue(name)
		}

		out = append(out, head+":mailto:"+email)
	}

	return out
}

// readAttendeesParam is CaldavPlugin::readAttendeesParam(): the client supplied
// "Attendees" parameter, as a list of {email,name,partstat}.
//
// A single JSON string is decoded first (the plugin accepts both shapes); a
// decoded JSON object is treated as an empty list, because PHP would iterate its
// values in insertion order, which a Go map cannot reproduce.
func readAttendeesParam(ctx plugin.ActionContext) []attendee {
	value := ctx.GetParam("Attendees", []any{})

	if text, ok := value.(string); ok && "" != strings.TrimSpace(text) {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); nil == err {
			value = decoded
		} else {
			value = []any{}
		}
	}

	list, ok := value.([]any)
	if !ok {
		return nil
	}

	result := []attendee{}

	for _, item := range list {
		var parsed attendee

		switch typed := item.(type) {
		case string:
			parsed = parseAttendeeInput(typed)
		case map[string]any:
			parsed = attendee{
				Email:    phpString(typed["email"]),
				Name:     phpString(typed["name"]),
				Partstat: phpString(typed["partstat"]),
			}
		default:
			continue
		}

		if "" != strings.TrimSpace(parsed.Email) {
			result = append(result, parsed)
		}
	}

	return result
}

/* -------------------------------------------------------- parseICalendar */

// icalEventBuilder collects the properties of one VEVENT while parseICalendar()
// walks the unfolded lines. It is the $currentEvent array of the PHP method.
type icalEventBuilder struct {
	// scalars are the single valued properties, keyed by their lowercased name.
	scalars map[string]string
	// tzids holds the TZID parameter of the properties that carried one.
	tzids map[string]string
	// multi are the properties that may legally occur more than once, each
	// comma separated value already parsed.
	multi map[string][]any
	// attendees are the raw ATTENDEE lines (parameters included).
	attendees []string
	// organizerLine is the raw ORGANIZER line, organizer its value.
	organizerLine string
	organizer     string
}

// newICalEvent starts a VEVENT builder.
func newICalEvent() *icalEventBuilder {
	return &icalEventBuilder{
		scalars:   map[string]string{},
		tzids:     map[string]string{},
		multi:     map[string][]any{},
		attendees: []string{},
	}
}

// multiValues is $currentEvent[$sKey] ?? []: the parsed list, never nil so the
// JSON is "[]" and not "null".
func (e *icalEventBuilder) multiValues(key string) []any {
	if values, ok := e.multi[key]; ok {
		return values
	}
	return []any{}
}

// scalarOrDefault is (string)($currentEvent[$sKey] ?? $sDefault).
func (e *icalEventBuilder) scalarOrDefault(key, defaultValue string) string {
	if value, ok := e.scalars[key]; ok {
		return value
	}
	return defaultValue
}

// document is the array literal of parseICalendar()'s END:VEVENT branch.
func (e *icalEventBuilder) document(href, etag string) map[string]any {
	recurrenceRaw := e.scalars["recurrence-id"]

	organizer, organizerName := parseOrganizerLine(e.organizerLine)
	if "" == organizer {
		organizer = inviteMailAddress(e.organizer)
	}

	summary := e.scalarOrDefault("summary", "Untitled")

	dtstart, hasStart := e.scalars["dtstart"]

	recurrenceID := ""
	if "" != recurrenceRaw {
		recurrenceID = parseICalDate(recurrenceRaw, e.tzids["recurrence-id"])
	}

	return map[string]any{
		"uid":           e.scalars["uid"],
		"summary":       unescapeICSText(summary),
		"dtstart":       parseICalDate(dtstart, e.tzids["dtstart"]),
		"dtend":         parseICalDate(e.scalars["dtend"], e.tzids["dtend"]),
		"description":   unescapeICSText(e.scalars["description"]),
		"location":      unescapeICSText(e.scalars["location"]),
		"allDay":        !hasStart || !strings.Contains(dtstart, "T"),
		"href":          href,
		"etag":          etag,
		"rrule":         e.scalars["rrule"],
		"rdate":         e.multiValues("rdate"),
		"exdate":        e.multiValues("exdate"),
		"recurrenceId":  recurrenceID,
		"sequence":      e.scalarOrDefault("sequence", "0"),
		"status":        e.scalars["status"],
		"organizer":     organizer,
		"organizerName": organizerName,
		"attendees":     parseAttendeeLines(e.attendees),
	}
}

// parseICalendar is CaldavPlugin::parseICalendar(): the events of an iCalendar
// text.
//
// Master VEVENTs and their per-occurrence overrides (VEVENTs carrying a
// RECURRENCE-ID) are both returned; the recurrence data (RRULE/RDATE/EXDATE) is
// preserved so the client can expand the series into occurrences.
func parseICalendar(icalData, href, etag string) []any {
	events := []any{}

	var current *icalEventBuilder

	for _, unfolded := range unfoldIcs(icalData) {
		line := strings.TrimRight(unfolded, phpTrimCutset)
		if "" == line {
			continue
		}

		if strings.EqualFold(line, "BEGIN:VEVENT") {
			current = newICalEvent()
			continue
		}
		if strings.EqualFold(line, "END:VEVENT") && nil != current {
			events = append(events, current.document(href, etag))
			current = nil
			continue
		}
		if nil == current {
			continue
		}

		position := strings.IndexByte(line, ':')
		if 0 > position {
			continue
		}

		head := line[:position]
		value := line[position+1:]
		key := strings.ToLower(phpStrtok(head, ";"))

		tzid := ""
		if match := icalTZIDPattern.FindStringSubmatch(head); nil != match {
			tzid = strings.Trim(match[1], `"`)
		}

		switch {
		case "attendee" == key:
			// Keep the full line (parameters included) so the caller can read
			// CN/PARTSTAT/ROLE/RSVP.
			current.attendees = append(current.attendees, line)
		case "organizer" == key:
			current.organizerLine = line
			current.organizer = value
		case icalMultiValueKeys[key]:
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if "" != item {
					current.multi[key] = append(current.multi[key], parseICalDate(item, tzid))
				}
			}
		default:
			current.scalars[key] = value
			if "" != tzid {
				current.tzids[key] = tzid
			}
		}
	}

	return events
}
