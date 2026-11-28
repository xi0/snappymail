package caldav

// CalDAV discovery: the calendar home PROPFIND of CaldavPlugin::DoGetCalendars()
// and the URL/XML helpers the collection and event actions of steps 30–32 build
// on (calendarUrl, resolveEventUrl, resolveCollectionUrl, isSameDavHost,
// normalizeCalendarColor, xmlText, slugifyCalendarName, uniqueCalendarId,
// calDAVMultistatusHasErrors).
//
// The multistatus parsing replaces DOMDocument/DOMXPath with encoding/xml. Go
// matches elements by local name here (the PHP code registers the "D"/"C"/"A"
// prefixes explicitly), so a server that returns unqualified elements is
// tolerated as well; the properties the plugin requests are the same three.

import (
	"encoding/xml"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

// The values DoGetCalendars() falls back to.
const (
	// defaultCalendarID is the id of the fallback collection.
	defaultCalendarID = "default"
	// defaultCalendarColor is the first entry of the PHP colour palette, which
	// the fallback calendar uses.
	defaultCalendarColor = "#00639a"
)

// calendarPalette is the $aPalette of parseCalendarsResponse(): the colour a
// collection without a usable calendar-color gets.
var calendarPalette = []string{
	"#00639a", "#16a765", "#e67c73", "#f6bf26",
	"#8e24aa", "#f4511e", "#039be5", "#7cb342",
}

// calendarHomePropfind is the PROPFIND body of DoGetCalendars(): the collection
// properties of the calendar home.
const calendarHomePropfind = `<?xml version="1.0" encoding="utf-8" ?>` + "\n" +
	`<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/">` + "\n" +
	"  <D:prop>\n" +
	"    <D:displayname />\n" +
	"    <D:resourcetype />\n" +
	"    <A:calendar-color />\n" +
	"  </D:prop>\n" +
	"</D:propfind>"

// calendarHomePropfindShort is the PROPFIND body of uniqueCalendarId(): only the
// resource type decides whether a collection name is taken.
const calendarHomePropfindShort = `<?xml version="1.0" encoding="utf-8" ?>` +
	`<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype /></D:prop></D:propfind>`

// calendarColorPattern is the (already normalised) '/^#([0-9a-fA-F]{6})/' test of
// parseCalendarsResponse().
var calendarColorPattern = regexp.MustCompile(`^#([0-9a-fA-F]{6})`)

// clientColorPattern is normalizeCalendarColor()'s
// '/^#?([0-9a-fA-F]{6})(?:[0-9a-fA-F]{2})?$/'.
var clientColorPattern = regexp.MustCompile(`(?i)^#?([0-9a-f]{6})(?:[0-9a-f]{2})?$`)

// statusCodePattern is the 'HTTP/\d(?:\.\d)?\s+(\d{3})' match of
// calDAVMultistatusHasErrors().
var statusCodePattern = regexp.MustCompile(`(?i)HTTP/[0-9](?:\.[0-9])?\s+([0-9]{3})`)

// slugPattern is the '/[^a-z0-9]+/' replacement of slugifyCalendarName().
var slugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// slugTransliterations is the str_replace() table of slugifyCalendarName(): the
// letters that would otherwise be dropped, so "Møder & Aftaler" becomes
// "moeder-aftaler" instead of "m-der".
var slugTransliterations = strings.NewReplacer(
	"æ", "ae", "ø", "oe", "å", "aa", "ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "á", "a", "à", "a", "â", "a",
	"í", "i", "ì", "i", "î", "i", "ó", "o", "ò", "o", "ô", "o",
	"ú", "u", "ù", "u", "û", "u", "ñ", "n", "ç", "c",
	"Æ", "Ae", "Ø", "Oe", "Å", "Aa", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue",
	"É", "E", "È", "E", "Ê", "E", "Ë", "E", "Á", "A", "À", "A", "Â", "A",
	"Í", "I", "Ì", "I", "Î", "I", "Ó", "O", "Ò", "O", "Ô", "O",
	"Ú", "U", "Ù", "U", "Û", "U", "Ñ", "N", "Ç", "C",
)

// xmlTextReplacer is htmlspecialchars($sText, ENT_QUOTES | ENT_XML1, 'UTF-8').
var xmlTextReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#039;",
)

// doPluginGetCalendars is CaldavPlugin::DoGetCalendars(): the calendar
// collections of the account's calendar home.
//
// A discovered list is returned as-is; without one (a failed or empty PROPFIND)
// at least one usable calendar is reported so the UI keeps working. That fallback
// is flagged read-only, because the collection was never discovered on the server
// and therefore cannot be renamed or deleted.
func (p *Plugin) doPluginGetCalendars(env plugin.Env, ctx plugin.ActionContext) (any, error) {
	respond := func(result any) (any, error) {
		return ctx.DefaultResponse(result, nil), nil
	}

	request := requestFromActions(ctx)
	if nil == request || nil == request.current {
		return respond(map[string]any{
			"calendars": []any{},
			"message":   msg(env, request, "ERROR_NOT_LOGGED_IN", nil, "Please log in first"),
		})
	}

	config := calendarConfigFor(env, request)
	if nil == config {
		return respond(map[string]any{
			"calendars": []any{},
			"message": msg(env, request, "ERROR_NOT_CONFIGURED_MSG", nil,
				"Calendar not configured yet. Please check settings."),
		})
	}

	password, usable, err := decryptedPassword(request, config)
	if nil != err {
		return respond(map[string]any{"calendars": []any{}, "error": err.Error()})
	}
	if !usable {
		return respond(map[string]any{
			"calendars": []any{},
			"error":     msg(env, request, "ERROR_NO_ENCRYPTION_KEY", nil, "Cannot access encryption key"),
		})
	}

	result := p.makeCalDAVRequest(
		strings.TrimRight(config.URL, "/")+"/",
		"PROPFIND",
		config.User,
		password,
		calendarHomePropfind,
		"Content-Type: application/xml; charset=utf-8",
		"Depth: 1",
	)

	var calendars []any
	if 207 == result.Code {
		calendars = parseCalendarsResponse(result.Body, config)
	}

	if 0 == len(calendars) {
		calendars = []any{map[string]any{
			"id":       defaultCalendarID,
			"name":     msg(env, request, "CALENDAR", nil, "Calendar"),
			"color":    defaultCalendarColor,
			"readOnly": true,
		}}
	}

	return respond(map[string]any{"calendars": calendars})
}

// calendarUrl is CaldavPlugin::calendarUrl(): the absolute URL of a calendar
// collection inside the user's calendar home.
func calendarUrl(config *calendarConfig, calendarID string) string {
	id := strings.TrimSpace(calendarID)
	if "" == id {
		id = defaultCalendarID
	}
	if nil == config {
		return ""
	}
	return strings.TrimRight(config.URL, "/") + "/" + rawURLEncode(id)
}

// resolveEventUrl is CaldavPlugin::resolveEventUrl(): turn a server reported href
// into an absolute URL.
//
// A client supplied EventUrl has to be used unchanged for the path (the server
// already URL-encoded it), so only the scheme/host is added when the server
// returned a server-relative path.
func resolveEventUrl(config *calendarConfig, rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if "" == value {
		return ""
	}
	if isAbsoluteHTTPURL(value) {
		return value
	}

	scheme := "https"
	host := ""
	if nil != config {
		if base, err := url.Parse(config.URL); nil == err {
			if "" != base.Scheme {
				scheme = base.Scheme
			}
			host = base.Host
		}
	}

	if strings.HasPrefix(value, "//") {
		// Protocol-relative href.
		return scheme + ":" + value
	}

	if "" != host {
		return scheme + "://" + host + "/" + strings.TrimLeft(value, "/")
	}

	return value
}

// resolveCollectionUrl is CaldavPlugin::resolveCollectionUrl(): the URL of a
// calendar collection for the management requests.
//
// The server reported URL is preferred, but only when it points at the configured
// CalDAV host: the requests carry the user's credentials, so a client supplied
// URL must never be able to target another host.
func resolveCollectionUrl(config *calendarConfig, calendarID, collectionURL string) string {
	if resolved := resolveEventUrl(config, collectionURL); "" != resolved && isSameDavHost(config, resolved) {
		return resolved
	}

	id := strings.TrimSpace(calendarID)
	if "" == id {
		return ""
	}

	return calendarUrl(config, id) + "/"
}

// isSameDavHost is CaldavPlugin::isSameDavHost().
func isSameDavHost(config *calendarConfig, rawURL string) bool {
	if nil == config {
		return false
	}

	base, err := url.Parse(config.URL)
	if nil != err || "" == base.Hostname() {
		return false
	}

	other, err := url.Parse(rawURL)
	if nil != err || "" == other.Hostname() {
		return false
	}

	// PHP compares the hosts with strcasecmp(), so the port is ignored.
	return strings.EqualFold(other.Hostname(), base.Hostname())
}

// normalizeCalendarColor is CaldavPlugin::normalizeCalendarColor(): "#RRGGBB",
// or "" when the value is not a usable colour.
func normalizeCalendarColor(value any) string {
	color := strings.TrimSpace(phpString(value))

	match := clientColorPattern.FindStringSubmatch(color)
	if nil == match {
		return ""
	}

	return "#" + match[1]
}

// xmlText is CaldavPlugin::xmlText(): an XML text/attribute escape.
func xmlText(text string) string { return xmlTextReplacer.Replace(text) }

// slugifyCalendarName is CaldavPlugin::slugifyCalendarName(): a URL-safe
// collection name built from a human readable calendar name.
func slugifyCalendarName(name string) string {
	slug := strings.ToLower(slugTransliterations.Replace(name))
	slug = strings.Trim(slugPattern.ReplaceAllString(slug, "-"), "-")

	if "" == slug {
		slug = "calendar-" + randomSuffix()
	}

	if 64 < len(slug) {
		slug = slug[:64]
	}

	return slug
}

// uniqueCalendarId is CaldavPlugin::uniqueCalendarId(): a collection name for a
// new calendar that is not in use yet.
func (p *Plugin) uniqueCalendarId(config *calendarConfig, password, name string) string {
	base := slugifyCalendarName(name)
	if nil == config {
		return base
	}

	home := strings.TrimRight(config.URL, "/") + "/"

	for attempt := 1; 20 >= attempt; attempt++ {
		id := base
		if 1 != attempt {
			id = base + "-" + strconv.Itoa(attempt)
		}

		result := p.makeCalDAVRequest(
			home+rawURLEncode(id)+"/",
			"PROPFIND",
			config.User,
			password,
			calendarHomePropfindShort,
			"Content-Type: application/xml; charset=utf-8",
			"Depth: 0",
		)

		// 404/410 mean the name is still free.
		if 404 == result.Code || 410 == result.Code {
			return id
		}
		// Anything but "it exists" (an auth or server error) is not worth
		// retrying; the MKCALENDAR reports the real problem to the user.
		if 207 != result.Code {
			return base
		}
	}

	return base + "-" + randomSuffix()
}

// calDAVMultistatusHasErrors is CaldavPlugin::calDAVMultistatusHasErrors():
// whether a PROPPATCH/PROPFIND multistatus contains a failed property (a 4xx/5xx
// status of a D:propstat/D:status or D:response/D:status element).
//
// An unparsable body counts as success, because the HTTP status already passed.
func calDAVMultistatusHasErrors(document string) bool {
	if "" == strings.TrimSpace(document) {
		return false
	}

	decoder := xml.NewDecoder(strings.NewReader(document))

	inStatus := false
	for {
		token, err := decoder.Token()
		if nil != err {
			return false
		}

		switch typed := token.(type) {
		case xml.StartElement:
			inStatus = "status" == typed.Name.Local
		case xml.EndElement:
			inStatus = false
		case xml.CharData:
			if !inStatus {
				continue
			}
			if match := statusCodePattern.FindStringSubmatch(string(typed)); nil != match {
				if code, err := strconv.Atoi(match[1]); nil == err && 400 <= code {
					return true
				}
			}
		}
	}
}

// rawURLEncode is PHP's rawurlencode(): every byte outside [A-Za-z0-9-_.~] is
// percent encoded, so a calendar id or UID is escaped exactly like PHP escapes it.
func rawURLEncode(value string) string {
	const hexadecimal = "0123456789ABCDEF"

	var builder strings.Builder
	builder.Grow(len(value))

	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case 'A' <= character && 'Z' >= character,
			'a' <= character && 'z' >= character,
			'0' <= character && '9' >= character,
			'-' == character, '_' == character, '.' == character, '~' == character:
			builder.WriteByte(character)
		default:
			builder.WriteByte('%')
			builder.WriteByte(hexadecimal[character>>4])
			builder.WriteByte(hexadecimal[character&0x0F])
		}
	}

	return builder.String()
}

// isAbsoluteHTTPURL is the '#^https?://#i' test of resolveEventUrl().
func isAbsoluteHTTPURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// The multistatus documents of a PROPFIND/REPORT response. The element names are
// matched by local name (see the package comment of this file).
type davMultistatus struct {
	// Responses is //D:response.
	Responses []davResponse `xml:"response"`
}

// davResponse is one D:response of a multistatus document.
type davResponse struct {
	// Href is ./D:href: the resource URL.
	Href string `xml:"href"`
	// Propstat holds the property groups of the response.
	Propstat []davPropstat `xml:"propstat"`
}

// davPropstat is one D:propstat element.
type davPropstat struct {
	// Prop is the property group.
	Prop davProperty `xml:"prop"`
}

// davProperty is the D:prop element with the properties DoGetCalendars()
// requests.
type davProperty struct {
	// DisplayName is D:displayname.
	DisplayName string `xml:"displayname"`
	// ResourceType is D:resourcetype.
	ResourceType davResourceType `xml:"resourcetype"`
	// CalendarColor is A:calendar-color.
	CalendarColor string `xml:"calendar-color"`
}

// davResourceType is D:resourcetype; only the caldav "calendar" child matters.
type davResourceType struct {
	// Calendar is C:calendar, present for a calendar collection.
	Calendar *struct{} `xml:"calendar"`
}

// isCalendar reports whether the resource is a calendar collection.
func (t davResourceType) isCalendar() bool { return nil != t.Calendar }

// merged is the property set of the response.
//
// A response may carry several propstat groups (the found properties in a 200
// group, the missing ones in a 404 group), so the first non-empty value of each
// property wins — which is what the PHP XPath queries return.
func (r davResponse) merged() davProperty {
	result := davProperty{}

	for _, propstat := range r.Propstat {
		if "" == result.DisplayName {
			result.DisplayName = propstat.Prop.DisplayName
		}
		if "" == result.CalendarColor {
			result.CalendarColor = propstat.Prop.CalendarColor
		}
		if nil == result.ResourceType.Calendar {
			result.ResourceType = propstat.Prop.ResourceType
		}
	}

	return result
}

// parseCalendarsResponse is CaldavPlugin::parseCalendarsResponse(): the calendar
// collections of a calendar home PROPFIND multistatus.
//
// A non-calendar collection (the home itself, address books, …) is skipped, and a
// collection without a calendar-color gets the next entry of the palette.
func parseCalendarsResponse(document string, config *calendarConfig) []any {
	calendars := []any{}

	var multistatus davMultistatus
	if err := xml.Unmarshal([]byte(document), &multistatus); nil != err {
		// PHP swallows the DOMDocument warning and returns the empty list.
		return calendars
	}

	index := 0

	for _, response := range multistatus.Responses {
		property := response.merged()
		if !property.ResourceType.isCalendar() {
			continue
		}

		fullHref := strings.TrimSpace(response.Href)
		if "" == fullHref {
			continue
		}

		// The untouched href is kept for later PROPPATCH/DELETE requests; the
		// last URL segment (decoded) is the calendar id.
		trimmed := strings.TrimRight(fullHref, "/")
		if "" == trimmed {
			continue
		}

		segments := strings.Split(trimmed, "/")
		id := rawURLDecode(segments[len(segments)-1])
		if "" == id {
			continue
		}

		name := strings.TrimSpace(property.DisplayName)
		if "" == name {
			name = id
		}

		// Normalize #RRGGBBAA to #RRGGBB.
		color := strings.TrimSpace(property.CalendarColor)
		if match := calendarColorPattern.FindStringSubmatch(color); nil != match {
			color = "#" + match[1]
		} else {
			color = calendarPalette[index%len(calendarPalette)]
		}

		calendars = append(calendars, map[string]any{
			"id":    id,
			"name":  name,
			"color": color,
			// The exact collection URL reported by the server (used for
			// PROPPATCH/DELETE so redirects and non-standard paths work).
			"url": resolveEventUrl(config, fullHref),
		})
		index++
	}

	return calendars
}

// rawURLDecode is PHP's rawurldecode().
func rawURLDecode(value string) string {
	decoded, err := url.PathUnescape(value)
	if nil != err {
		return value
	}
	return decoded
}
