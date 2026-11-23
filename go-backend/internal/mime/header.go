package mime

// The MIME header model of MailSo\Mime: Header, Parameter/ParameterCollection
// and HeaderCollection, plus the header value decoding (RFC 2047) and the
// date parsing helpers the message list needs to build its JSON documents.
//
// This is the parse side only; the build side (Step 19) adds the writer. The
// JSON field names below are the frontend contract: HeaderCollection emits
// {"@Object":"Collection/MimeHeaderCollection","@Collection":[Header…]} and a
// Header emits {"@Object":"Object/MimeHeader","name","value","parameters"}.

import (
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
)

// The header names of MailSo\Mime\Enumerations\Header the list/view parse.
const (
	HeaderMessageID                      = "MESSAGE-ID"
	HeaderContentType                    = "CONTENT-TYPE"
	HeaderContentDisposition             = "CONTENT-DISPOSITION"
	HeaderContentTransferEncoding        = "CONTENT-TRANSFER-ENCODING"
	HeaderFrom                           = "FROM"
	HeaderTo                             = "TO"
	HeaderCc                             = "CC"
	HeaderBcc                            = "BCC"
	HeaderSender                         = "SENDER"
	HeaderReplyTo                        = "REPLY-TO"
	HeaderDeliveredTo                    = "DELIVERED-TO"
	HeaderInReplyTo                      = "IN-REPLY-TO"
	HeaderReferences                     = "REFERENCES"
	HeaderDate                           = "DATE"
	HeaderSubject                        = "SUBJECT"
	HeaderXMSMailPriority                = "X-MSMAIL-PRIORITY"
	HeaderImportance                     = "IMPORTANCE"
	HeaderXPriority                      = "X-PRIORITY"
	HeaderXDraftInfo                     = "X-DRAFT-INFO"
	HeaderDispositionNotificationTo      = "DISPOSITION-NOTIFICATION-TO"
	HeaderXConfirmReadingTo              = "X-CONFIRM-READING-TO"
	HeaderAuthenticationResults          = "AUTHENTICATION-RESULTS"
	HeaderXDKIMAuthenticationResults     = "X-DKIM-AUTHENTICATION-RESULTS"
	HeaderListUnsubscribe                = "LIST-UNSUBSCRIBE"
	HeaderAutocrypt                      = "AUTOCRYPT"
	HeaderXSpamdResult                   = "X-SPAMD-RESULT"
	HeaderXSpamStatus                    = "X-SPAM-STATUS"
	HeaderXSpamFlag                      = "X-SPAM-FLAG"
	HeaderXSpamInfo                      = "X-SPAM-INFO"
	HeaderXBogosity                      = "X-BOGOSITY"
	HeaderXVirus                         = "X-VIRUS"
	HeaderXVirusScanned                  = "X-VIRUS-SCANNED"
	HeaderXVirusStatus                   = "X-VIRUS-STATUS"
)

// The MIME parameter names of MailSo\Mime\Enumerations\Parameter.
const (
	ParameterCharset  = "charset"
	ParameterName     = "name"
	ParameterFilename = "filename"
)

// DkimStatus::NONE.
const DkimStatusNone = "none"

// wordDecoder decodes RFC 2047 encoded words using the charsets x/text knows.
var wordDecoder = &mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		encoding, err := htmlindex.Get(charset)
		if nil != err {
			return input, nil
		}
		return encoding.NewDecoder().Reader(input), nil
	},
}

// Parameter is MailSo\Mime\Parameter.
type Parameter struct {
	name  string
	value string
}

// Name is Parameter::Name().
func (p Parameter) Name() string { return p.name }

// Value is Parameter::Value().
func (p Parameter) Value() string { return p.value }

// JSONSerialize is Parameter::jsonSerialize().
func (p Parameter) JSONSerialize() any {
	return map[string]any{"name": p.name, "value": p.value}
}

// ParameterCollection is MailSo\Mime\ParameterCollection: the parameters of a
// header, keyed by their lowercase name but iterated in insertion order.
type ParameterCollection struct {
	order []string
	items map[string]*Parameter
}

// NewParameterCollection returns an empty collection.
func NewParameterCollection() *ParameterCollection {
	return &ParameterCollection{items: map[string]*Parameter{}}
}

// ParseParameterCollection is ParameterCollection::__construct(raw).
func ParseParameterCollection(raw string) *ParameterCollection {
	collection := NewParameterCollection()
	collection.Parse(raw)
	return collection
}

// Count is Collection::count().
func (c *ParameterCollection) Count() int {
	if nil == c {
		return 0
	}
	return len(c.order)
}

// Get is ParameterCollection::getParameter().
func (c *ParameterCollection) Get(name string) (*Parameter, bool) {
	if nil == c {
		return nil, false
	}
	parameter, ok := c.items[strings.ToLower(strings.TrimSpace(name))]
	return parameter, ok
}

// ParameterValueByName is ParameterCollection::ParameterValueByName().
func (c *ParameterCollection) ParameterValueByName(name string) string {
	if parameter, ok := c.Get(name); ok {
		return parameter.value
	}
	return ""
}

// append is ParameterCollection::append(): the lowercase name is the key, so a
// duplicate name overwrites the value in place.
func (c *ParameterCollection) append(parameter *Parameter) {
	if nil == c || nil == parameter {
		return
	}
	if nil == c.items {
		c.items = map[string]*Parameter{}
	}
	key := strings.ToLower(parameter.name)
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = parameter
}

// Parse is ParameterCollection::Parse(): split on ';' and parse name=value.
func (c *ParameterCollection) Parse(raw string) {
	if nil == c {
		return
	}
	c.order = nil
	c.items = map[string]*Parameter{}

	for _, part := range strings.Split(raw, ";") {
		if "" == strings.TrimSpace(part) {
			continue
		}
		parameter := Parameter{}
		pair := strings.SplitN(part, "=", 2)
		parameter.name = strings.Trim(strings.TrimSpace(pair[0]), `"'`)
		if 2 == len(pair) {
			parameter.value = strings.Trim(strings.TrimSpace(pair[1]), `"'`)
		}
		if "" == parameter.name {
			continue
		}
		c.append(&parameter)
	}
}

// JSONSerialize is Collection::jsonSerialize() for the parameter collection.
func (c *ParameterCollection) JSONSerialize() any {
	items := map[string]any{}
	for _, key := range c.order {
		items[key] = c.items[key]
	}
	return map[string]any{
		"@Object":     "Collection/ParameterCollection",
		"@Collection": items,
	}
}

// Header is MailSo\Mime\Header.
type Header struct {
	name         string
	value        string
	fullValue    string
	encodedValue string
	parameters   *ParameterCollection
	parentCharset string
}

// isParameterized reports whether the header value carries parameters.
func isParameterized(name string) bool {
	return HeaderContentType == strings.ToUpper(name) || HeaderContentDisposition == strings.ToUpper(name)
}

// isEmailHeader reports whether the header value is an address list.
func isEmailHeader(name string) bool {
	switch strings.ToUpper(name) {
	case HeaderFrom, HeaderTo, HeaderCc, HeaderBcc, HeaderReplyTo, HeaderSender:
		return true
	}
	return false
}

// NewHeader is Header::__construct().
func NewHeader(name, value string) *Header {
	header := &Header{parameters: NewParameterCollection()}
	header.init(name, value, "")
	return header
}

// init is Header::initInputData().
func (h *Header) init(name, value, encodedForReparse string) {
	h.name = strings.TrimSpace(name)
	h.fullValue = strings.TrimSpace(value)
	h.encodedValue = ""
	if "" != encodedForReparse && (isEmailHeader(h.name) || HeaderSubject == strings.ToUpper(h.name) || isParameterized(h.name)) {
		h.encodedValue = strings.TrimSpace(encodedForReparse)
	}

	if "" != h.fullValue && isParameterized(h.name) {
		parts := strings.SplitN(h.fullValue, ";", 2)
		if 2 == len(parts) {
			h.value = parts[0]
			h.parameters = ParseParameterCollection(parts[1])
		} else {
			h.value = h.fullValue
		}
	} else {
		h.value = h.fullValue
	}
	if nil == h.parameters {
		h.parameters = NewParameterCollection()
	}
}

// NewHeaderFromEncodedString is Header::NewInstanceFromEncodedString().
func NewHeaderFromEncodedString(encoded, charset string) *Header {
	if "" == charset {
		charset = "ISO-8859-1"
	}
	parts := strings.SplitN(strings.ReplaceAll(encoded, "\r", ""), ":", 2)
	if 2 != len(parts) || 0 == len(parts[0]) || 0 == len(parts[1]) {
		return nil
	}
	raw := strings.TrimSpace(parts[1])
	header := &Header{parameters: NewParameterCollection(), parentCharset: charset}
	header.init(strings.TrimSpace(parts[0]), strings.TrimSpace(DecodeHeaderValue(raw, charset)), raw)
	return header
}

// Name is Header::Name().
func (h *Header) Name() string { return h.name }

// Value is Header::Value().
func (h *Header) Value() string { return h.value }

// FullValue is Header::FullValue().
func (h *Header) FullValue() string { return h.fullValue }

// EncodedValue is Header::EncodedValue().
func (h *Header) EncodedValue() string {
	if "" != h.encodedValue {
		return h.encodedValue
	}
	return h.fullValue
}

// Parameters is Header::Parameters().
func (h *Header) Parameters() *ParameterCollection { return h.parameters }

// SetParentCharset is Header::SetParentCharset().
func (h *Header) SetParentCharset(charset string) *Header {
	if h.parentCharset != charset && "" != h.encodedValue {
		h.init(h.name, strings.TrimSpace(DecodeHeaderValue(h.encodedValue, charset)), h.encodedValue)
	}
	h.parentCharset = charset
	return h
}

// ValueWithCharsetAutoDetect is Header::ValueWithCharsetAutoDetect(). The
// mb_detect_encoding() guess is not ported; the decoded value is returned.
func (h *Header) ValueWithCharsetAutoDetect() string { return h.value }

// JSONSerialize is Header::jsonSerialize().
func (h *Header) JSONSerialize() any {
	result := map[string]any{
		"@Object": "Object/MimeHeader",
		"name":    h.name,
		"value":   h.value,
	}
	if h.parameters.Count() > 0 {
		result["parameters"] = h.parameters.JSONSerialize()
	}
	return result
}

// HeaderCollection is MailSo\Mime\HeaderCollection: the ordered headers of a
// message or part.
type HeaderCollection struct {
	items         []*Header
	parentCharset string
}

// NewHeaderCollection is HeaderCollection::__construct(raw, charset).
func NewHeaderCollection(raw, charset string) *HeaderCollection {
	collection := &HeaderCollection{}
	if "" != raw {
		collection.Parse(raw, charset)
	}
	return collection
}

// Count is Collection::count().
func (c *HeaderCollection) Count() int {
	if nil == c {
		return 0
	}
	return len(c.items)
}

// Iterate returns the headers in order.
func (c *HeaderCollection) Iterate() []*Header {
	if nil == c {
		return nil
	}
	return c.items
}

// GetByName is HeaderCollection::GetByName().
func (c *HeaderCollection) GetByName(name string) *Header {
	if nil == c {
		return nil
	}
	name = strings.ToUpper(name)
	for _, header := range c.items {
		if strings.ToUpper(header.name) == name {
			return header
		}
	}
	return nil
}

// ValueByName is HeaderCollection::ValueByName().
func (c *HeaderCollection) ValueByName(name string) string {
	if header := c.GetByName(name); nil != header {
		return header.value
	}
	return ""
}

// ValuesByName is HeaderCollection::ValuesByName().
func (c *HeaderCollection) ValuesByName(name string) []string {
	if nil == c {
		return nil
	}
	name = strings.ToUpper(name)
	var result []string
	for _, header := range c.items {
		if strings.ToUpper(header.name) == name {
			result = append(result, header.value)
		}
	}
	return result
}

// ParameterValue is HeaderCollection::ParameterValue().
func (c *HeaderCollection) ParameterValue(name, parameter string) string {
	if header := c.GetByName(name); nil != header {
		return header.parameters.ParameterValueByName(parameter)
	}
	return ""
}

// GetAsEmailCollection is HeaderCollection::GetAsEmailCollection().
func (c *HeaderCollection) GetAsEmailCollection(name string) *EmailCollection {
	if header := c.GetByName(name); nil != header {
		return NewEmailCollection(header.EncodedValue())
	}
	return NewEmailCollection("")
}

// SetParentCharset is HeaderCollection::SetParentCharset().
func (c *HeaderCollection) SetParentCharset(charset string) *HeaderCollection {
	if "" != charset && c.parentCharset != charset {
		for _, header := range c.items {
			header.SetParentCharset(charset)
		}
		c.parentCharset = charset
	}
	return c
}

// Parse is HeaderCollection::Parse().
func (c *HeaderCollection) Parse(raw, charset string) {
	c.items = nil
	if "" != c.parentCharset {
		c.parentCharset = charset
	}

	lines := strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n")

	var name, value string
	have := false

	flush := func() {
		if !have {
			return
		}
		if header := NewHeaderFromEncodedString(name + ": " + value, c.parentCharset); nil != header {
			c.items = append(c.items, header)
		}
		name, value, have = "", "", false
	}

	for _, line := range lines {
		if "" == line {
			continue
		}
		first := line[0]
		if ' ' != first && '\t' != first && !strings.ContainsRune(line, ':') {
			continue
		}
		if have && (' ' == first || '\t' == first) {
			trimmed := strings.TrimRight(line, " \t")

			if strings.HasSuffix(trimmed, "?=") {
				trimmed = trimmed[:len(trimmed)-2] + "?="
				line = strings.TrimRight(line, " \t")
			}
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "=?") {
				line = strings.TrimLeft(line, " \t")
			}
			if strings.HasPrefix(line, "=?") {
				value += line
			} else {
				value += "\n" + line
			}
			continue
		}

		flush()
		parts := strings.SplitN(line, ":", 2)
		name = parts[0]
		if 2 == len(parts) {
			value = parts[1]
		} else {
			value = ""
		}
		if strings.HasSuffix(strings.TrimRight(value, " \t"), "?=") {
			value = strings.TrimRight(value, " \t")
		}
		have = true
	}
	flush()
}

// AuthStatuses is HeaderCollection::AuthStatuses(): the SPF/DKIM/DMARC results
// from Authentication-Results / X-DKIM-Authentication-Results.
func (c *HeaderCollection) AuthStatuses() map[string][][3]string {
	result := map[string][][3]string{"dkim": {}, "dmarc": {}, "spf": {}}

	headers := c.ValuesByName(HeaderAuthenticationResults)
	if 0 < len(headers) {
		joined := strings.Join(headers, ";")
		joined = collapseWhitespace(joined)
		joined = strings.ReplaceAll(joined, "-bit key;", "-bit key,")
		for _, line := range strings.Split(joined, ";") {
			status, domain, ok := matchAuthStatus(line)
			if !ok {
				continue
			}
			result[status[0]] = append(result[status[0]], [3]string{status[1], domain, strings.TrimSpace(line)})
		}
	}

	if 0 == len(result["dkim"]) {
		for _, header := range c.ValuesByName(HeaderXDKIMAuthenticationResults) {
			header = collapseWhitespace(header)
			status := regexpGroup(header, `(?i)status\s?=\s?"([a-zA-Z0-9]+)"`)
			domain := regexpGroup(header, `(?i)signer\s?=\s?"([^";]+)"`)
			if "" != status && "" != domain {
				result["dkim"] = append(result["dkim"], [3]string{status, strings.TrimSpace(domain), header})
			}
		}
	}

	return result
}

// JSONSerialize is HeaderCollection::jsonSerialize().
func (c *HeaderCollection) JSONSerialize() any {
	items := make([]any, 0, c.Count())
	for _, header := range c.items {
		items = append(items, header)
	}
	return map[string]any{
		"@Object":     "Collection/MimeHeaderCollection",
		"@Collection": items,
	}
}

// DecodeHeaderValue is MailSo\Base\Utils::DecodeHeaderValue(): decode the
// RFC 2047 encoded words; an 8-bit plain value is converted from charset.
func DecodeHeaderValue(value, charset string) string {
	if "" == value {
		return ""
	}
	if decoded, err := wordDecoder.DecodeHeader(value); nil == err {
		if decoded != value {
			return decoded
		}
	}
	if !IsASCII(value) && "" != charset {
		if converted, ok := convertCharset(value, charset); ok {
			return converted
		}
	}
	return value
}

// convertCharset converts value from charset to UTF-8.
func convertCharset(value, charset string) (string, bool) {
	encoding, err := htmlindex.Get(charset)
	if nil != err {
		return value, false
	}
	decoded, err := encoding.NewDecoder().String(value)
	if nil != err {
		return value, false
	}
	return decoded, true
}

// IsASCII reports whether value is 7-bit ASCII.
func IsASCII(value string) bool {
	for index := 0; index < len(value); index++ {
		if 0x80 <= value[index] {
			return false
		}
	}
	return true
}

// Utf8Clear drops the control characters PHP's Utils::Utf8Clear() removes.
func Utf8Clear(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		if 0x09 == character || 0x0A == character || 0x0D == character || 0x20 <= character {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// Trim is Utils::Trim(): collapse runs of whitespace to single spaces.
func Trim(value string) string {
	return collapseWhitespace(strings.TrimSpace(value))
}

// collapseWhitespace replaces every run of whitespace with one space.
func collapseWhitespace(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	space := false
	for _, character := range value {
		if ' ' == character || '\t' == character || '\r' == character || '\n' == character {
			space = true
			continue
		}
		if space && 0 < builder.Len() {
			builder.WriteByte(' ')
		}
		space = false
		builder.WriteRune(character)
	}
	return builder.String()
}

// StripSpaces is Utils::StripSpaces(): collapse whitespace runs to single
// spaces and trim (it does NOT delete the spaces).
func StripSpaces(value string) string {
	return Trim(value)
}

// ParseInternalDateString is DateTimeHelper::ParseInternalDateString().
func ParseInternalDateString(value string) int {
	value = strings.TrimSpace(value)
	if "" == value {
		return 0
	}
	// "Thu, 10 Jun 2010 08:58:33 -0700 (PDT)" — an RFC 2822 date.
	if matchesRFC2822Prefix(value) {
		return ParseRFC2822DateString(value)
	}
	if parsed, err := time.Parse("02-Jan-2006 15:04:05 -0700", value); nil == err {
		return int(parsed.Unix())
	}
	return 0
}

// ParseRFC2822DateString is DateTimeHelper::ParseRFC2822DateString().
func ParseRFC2822DateString(value string) int {
	value = strings.TrimSpace(value)
	if "" == value {
		return 0
	}
	value = stripTrailingZoneComment(value)
	value = stripLeadingDayOfWeek(value)
	value = addOptionalSeconds(value)
	value = strings.TrimSpace(value)

	parsed, err := mail.ParseDate(value)
	if nil != err {
		if parsed, err = time.Parse("02 Jan 2006 15:04:05 -0700", value); nil != err {
			return 0
		}
	}
	// 398045302 is 1982-08-13, the creation date of RFC 822.
	if 398045302 > parsed.Unix() {
		return 0
	}
	return int(parsed.Unix())
}

// matchesRFC2822Prefix reports "/^[a-z]{2,4}, /i".
func matchesRFC2822Prefix(value string) bool {
	comma := strings.IndexByte(value, ',')
	if 2 > comma || 4 < comma {
		return false
	}
	for index := 0; index < comma; index++ {
		character := value[index]
		if ('a' > character || 'z' < character) && ('A' > character || 'Z' < character) {
			return false
		}
	}
	return 2+1 < len(value) && ',' == value[comma] && ' ' == value[comma+1]
}

// stripTrailingZoneComment removes a trailing " (PDT)".
func stripTrailingZoneComment(value string) string {
	return mustRegexp(`\s+\([a-zA-Z0-9]+\)$`).ReplaceAllString(value, "")
}

// stripLeadingDayOfWeek removes a leading "Thu,".
func stripLeadingDayOfWeek(value string) string {
	return mustRegexp(`^[^,]+,`).ReplaceAllString(value, "")
}

// addOptionalSeconds inserts ":00" seconds for a "H:M " time without seconds.
func addOptionalSeconds(value string) string {
	return mustRegexp(` ([0-9]+:[0-9]+) `).ReplaceAllString(value, " $1:00 ")
}

// regexpGroup returns the first capture group of pattern in value, or "".
func regexpGroup(value, pattern string) string {
	re := mustRegexp(pattern)
	match := re.FindStringSubmatch(value)
	if 2 > len(match) {
		return ""
	}
	return match[1]
}

// matchAuthStatus is the "/(dkim|dmarc|spf)=([a-z0-9]+).*?(;|$)/" plus the
// "header.(d|i|from)|smtp.mailfrom" match of AuthStatuses().
func matchAuthStatus(line string) ([2]string, string, bool) {
	statusRegex := mustRegexp(`(?i)(dkim|dmarc|spf)=([a-z0-9]+)`)
	statusMatch := statusRegex.FindStringSubmatch(line)
	if 2 > len(statusMatch) {
		return [2]string{}, "", false
	}
	headerRegex := mustRegexp(`(?i)(?:header\.(?:d|i|from)|smtp.mailfrom)="?([^\s;"]+)`)
	headerMatch := headerRegex.FindStringSubmatch(line)
	if 2 > len(headerMatch) {
		return [2]string{}, "", false
	}
	return [2]string{strings.ToLower(statusMatch[1]), strings.ToLower(statusMatch[2])}, headerMatch[1], true
}
