package mime

// The build side of MailSo\Mime: Message, Part, PartCollection, Attachment and
// the header writer of Header::__toString(), as used by DoSaveMessage and
// DoSendMessage (Step 19).
//
// The goal is a byte compatible message for the compose payload: header order
// and folding, the multipart/alternative -> related -> mixed assembly of
// Message::GetRootPart(), quoted-printable text parts and base64 attachments.
// Signing/encryption payloads are supplied by the caller (the pgp/smime steps);
// this package only places them in the MIME structure.
//
// Deliberate deviations are limited to values Go cannot reproduce exactly:
// iconv_mime_encode() has no stdlib equivalent, so a non-ASCII Subject is emitted
// as one or more UTF-8 base64 encoded words folded at the same 74 character
// budget, and Message-ID/Date/boundary values are generated at build time like
// the PHP code does.

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The message priorities of MailSo\Mime\Enumerations\MessagePriority.
const (
	PriorityHigh   = 1
	PriorityNormal = 3
	PriorityLow    = 5
)

// defaultBoundaryPrefix is MailSo\Config::$BoundaryPrefix. RainLoop\Api
// overwrites it with the trimmed labs.boundary_prefix (the default is ""), so
// the builder keeps the caller's value and only uses this constant for parts
// created outside a BuiltMessage.
const defaultBoundaryPrefix = "_Part_"

// BuildOptions configures one built message. The zero value is usable: a random
// Message-ID, the current UTC time and the MailSo boundary prefix are used.
type BuildOptions struct {
	// HideXMailer is security.hide_x_mailer_header: suppress the default
	// "X-Mailer: SnappyMail/<version>" header.
	HideXMailer bool
	// Version is APP_VERSION, used for the default X-Mailer.
	Version string
	// BoundaryPrefix is labs.boundary_prefix.
	BoundaryPrefix string
	// HostName is the domain used for a generated Message-ID; it falls back to
	// os.Hostname() and "localhost" like Message::generateNewMessageId().
	HostName string
	// Now is the clock of the Date header; nil uses time.Now.
	Now func() time.Time
	// Random fills the Message-ID salt; nil uses crypto/rand.Read.
	Random func([]byte) (int, error)
}

// builtParam is one parameter of a parameterized header.
type builtParam struct {
	name   string
	value  string
	quoted bool
}

// builtHeader is one header of the ordered collection. The first matching name
// is rendered; SetByName replaces the value in place (PHP removes and appends,
// which is only observable for duplicate input headers and does not happen for
// a composed message).
type builtHeader struct {
	name          string
	value         string
	params        []builtParam
	parameterized bool
	emails        *EmailCollection
	autocrypt     []string
}

// BuiltMessage is MailSo\Mime\Message.
type BuiltMessage struct {
	options BuildOptions
	values  []builtHeader
	subject string

	subParts    []*buildPart
	attachments []*BuildAttachment

	from  *Email
	to    *EmailCollection
	cc    *EmailCollection
	bcc   *EmailCollection
	reply *EmailCollection
}

// buildPart is MailSo\Mime\Part.
type buildPart struct {
	headers []builtHeader
	body    []byte
	subs    []*buildPart
	// prefix is MailSo\Config::$BoundaryPrefix for the boundaries of this part.
	prefix string
	// raw replaces the generated part entirely (the body of a multipart/signed
	// part is the already rendered MIME document).
	raw []byte
}

// BuildAttachment is MailSo\Mime\Attachment for the builder. The data is held in
// memory so a message can be rendered twice (once with and once without Bcc).
type BuildAttachment struct {
	Data            []byte
	FileName        string
	ContentType     string
	Inline          bool
	Linked          bool
	ContentID       string
	ContentLocation string
}

// NewBuiltMessage is Message::__construct().
func NewBuiltMessage(options BuildOptions) *BuiltMessage {
	if nil == options.Now {
		options.Now = time.Now
	}
	if nil == options.Random {
		options.Random = rand.Read
	}
	return &BuiltMessage{options: options}
}

// Subject is Message::GetSubject().
func (m *BuiltMessage) Subject() string { return m.subject }

// SetSubject is Message::SetSubject().
func (m *BuiltMessage) SetSubject(subject string) {
	m.subject = subject
	m.setValue("Subject", subject)
}

// SetFrom is Message::SetFrom().
func (m *BuiltMessage) SetFrom(email *Email) {
	if nil == email {
		return
	}
	m.from = email
	m.setHeader(builtHeader{name: "From", emails: collectionOf(email)})
}

// From is Message::GetFrom().
func (m *BuiltMessage) From() *Email { return m.from }

// FromEmail is "$oMessage->GetFrom() ? $oFrom->GetEmail() : ''".
func (m *BuiltMessage) FromEmail() string {
	if nil == m || nil == m.from {
		return ""
	}
	return m.from.GetEmail()
}

// SetReplyTo is Message::SetReplyTo().
func (m *BuiltMessage) SetReplyTo(emails *EmailCollection) {
	if nil == emails || 0 == emails.Count() {
		return
	}
	m.reply = emails
	m.setEmails("Reply-To", emails)
}

// SetTo is Message::SetTo().
func (m *BuiltMessage) SetTo(emails *EmailCollection) {
	if nil == emails || 0 == emails.Count() {
		return
	}
	m.to = emails
	m.setEmails("To", emails)
}

// SetCc is Message::SetCc().
func (m *BuiltMessage) SetCc(emails *EmailCollection) {
	if nil == emails || 0 == emails.Count() {
		return
	}
	m.cc = emails
	m.setEmails("Cc", emails)
}

// SetBcc is Message::SetBcc().
func (m *BuiltMessage) SetBcc(emails *EmailCollection) {
	if nil == emails || 0 == emails.Count() {
		return
	}
	m.bcc = emails
	m.setEmails("Bcc", emails)
}

// HasBcc is "!!$oMessage->GetBcc()".
func (m *BuiltMessage) HasBcc() bool {
	return nil != m && nil != m.bcc && 0 < m.bcc.Count()
}

// Recipients is Message::GetRcpt(): To/Cc/Bcc in that order, unique by address.
func (m *BuiltMessage) Recipients() []*Email {
	result := NewEmailCollection("")
	for _, collection := range []*EmailCollection{m.to, m.cc, m.bcc} {
		if nil == collection {
			continue
		}
		for _, email := range collection.Iterate() {
			result.Append(email)
		}
	}
	return result.Unique().Iterate()
}

// SetReadReceipt is Message::SetReadReceipt().
func (m *BuiltMessage) SetReadReceipt(email string) {
	m.setValue("Disposition-Notification-To", email)
	m.setValue("X-Confirm-Reading-To", email)
}

// SetPriority is Message::SetPriority().
func (m *BuiltMessage) SetPriority(priority int) {
	value := ""
	switch priority {
	case PriorityHigh:
		value = "1 (Highest)"
	case PriorityNormal:
		value = "3 (Normal)"
	case PriorityLow:
		value = "5 (Low)"
	}
	if "" != value {
		m.setValue("X-Priority", value)
	}
}

// SetCustomHeader is Message::SetCustomHeader().
func (m *BuiltMessage) SetCustomHeader(name, value string) {
	name = strings.TrimSpace(name)
	if "" != name {
		m.setValue(name, value)
	}
}

// SetDraftInfo is Message::SetDraftInfo(): the X-Draft-Info parameter list.
func (m *BuiltMessage) SetDraftInfo(draftType string, uid int, folder string) {
	parameters := NewParameterCollection()
	parameters.append(&Parameter{name: "type", value: draftType})
	parameters.append(&Parameter{name: "uid", value: strconv.Itoa(uid)})
	parameters.append(&Parameter{name: "folder", value: base64.StdEncoding.EncodeToString([]byte(folder))})
	m.setValue("X-Draft-Info", parameters.ToString(false))
}

// SetInReplyTo is Message::SetInReplyTo().
func (m *BuiltMessage) SetInReplyTo(value string) {
	if value = strings.TrimSpace(value); "" != value {
		m.setValue("In-Reply-To", value)
	}
}

// SetReferences is Message::SetReferences().
func (m *BuiltMessage) SetReferences(value string) {
	if value = Trim(value); "" != value {
		m.setValue("References", value)
	}
}

// SetAutocrypt is Message::SetAutocrypt().
func (m *BuiltMessage) SetAutocrypt(values []string) {
	if 0 == len(values) {
		return
	}
	m.removeValue("Autocrypt")
	m.values = append(m.values, builtHeader{name: "Autocrypt", autocrypt: values})
}

// MessageID is Message::MessageId().
func (m *BuiltMessage) MessageID() string {
	for _, header := range m.values {
		if "MESSAGE-ID" == strings.ToUpper(header.name) {
			return header.value
		}
	}
	return ""
}

// SetMessageID is Message::SetMessageId().
func (m *BuiltMessage) SetMessageID(value string) { m.setValue("Message-ID", value) }

// RegenerateMessageID is Message::RegenerateMessageId().
func (m *BuiltMessage) RegenerateMessageID(host string) {
	m.SetMessageID(m.generateMessageID(host))
}

// generateMessageID is Message::generateNewMessageId().
func (m *BuiltMessage) generateMessageID(host string) string {
	if "" == host {
		host = m.options.HostName
	}
	if "" == host {
		host, _ = os.Hostname()
	}
	if "" == host {
		host = "localhost"
	}

	salt := make([]byte, 16)
	_, _ = m.options.Random(salt)
	sum := sha1.Sum(append([]byte(host+strconv.Itoa(os.Getpid())), salt...))

	return "<" + hex.EncodeToString(sum[:]) + "@" + host + ">"
}

// AddPlain is Part::addPlain(): a quoted-printable text/plain part.
func (m *BuiltMessage) AddPlain(text string) {
	m.subParts = append(m.subParts, textPart("text/plain; charset=utf-8", text))
}

// AddHTML is the multipart/alternative branch of Actions\Messages::buildMessage():
// a text/plain part followed by a quoted-printable text/html part.
func (m *BuiltMessage) AddHTML(html, plain string) {
	alternative := m.newPart("multipart/alternative")
	alternative.subs = append(alternative.subs, textPart("text/plain; charset=utf-8", plain))

	htmlPart := m.newPart("text/html; charset=utf-8")
	htmlPart.headers = append(htmlPart.headers, builtHeader{name: "Content-Transfer-Encoding", value: "quoted-printable"})
	htmlPart.body = []byte(EncodeQuotedPrintable(normalizeCRLF(strings.TrimSpace(html))))
	alternative.subs = append(alternative.subs, htmlPart)

	m.subParts = append(m.subParts, alternative)
}

// AddSigned is the "signed" branch of buildMessage(): the client already built
// the multipart/signed body, so it is carried as the raw body of the root part.
func (m *BuiltMessage) AddSigned(signed, boundary string) {
	signed = strings.ReplaceAll(signed, "\r\n", "\n")
	parts := strings.SplitN(signed, "\n\n", 2)
	body := ""
	if 2 == len(parts) {
		body = parts[1]
	}
	part := m.newPart(`multipart/signed; micalg="pgp-sha256"; protocol="application/pgp-signature"`)
	if "" != boundary {
		part.setParameter("boundary", boundary)
	}
	part.body = []byte(normalizeCRLF(body))
	m.subParts = append(m.subParts, part)
}

// AddPGPEncrypted is Part::addPgpEncrypted().
func (m *BuiltMessage) AddPGPEncrypted(encrypted string) {
	encrypted = normalizeCRLF(strings.TrimSpace(encrypted))

	part := m.newPart(`multipart/encrypted; protocol="application/pgp-encrypted"`)

	versionPart := m.newPart("application/pgp-encrypted")
	versionPart.headers = append(versionPart.headers,
		builtHeader{name: "Content-Disposition", value: "attachment"},
		builtHeader{name: "Content-Transfer-Encoding", value: "7Bit"})
	versionPart.body = []byte("Version: 1")

	dataPart := m.newPart("application/octet-stream")
	dataPart.headers = append(dataPart.headers,
		builtHeader{name: "Content-Disposition", parameterized: true, value: "inline",
			params: []builtParam{{name: "filename", value: "msg.asc", quoted: true}}},
		builtHeader{name: "Content-Transfer-Encoding", value: "7Bit"})
	dataPart.body = []byte(encrypted)

	part.subs = append(part.subs, versionPart, dataPart)
	m.subParts = append(m.subParts, part)
}

// AddAttachment appends one attachment to the message.
func (m *BuiltMessage) AddAttachment(attachment *BuildAttachment) {
	if nil == attachment {
		return
	}
	if "" == attachment.ContentType {
		attachment.ContentType = FromFilename(attachment.FileName)
	}
	if "" == attachment.ContentType {
		attachment.ContentType = "application/octet-stream"
	}
	m.attachments = append(m.attachments, attachment)
}

// ToBytes is Message::ToStream() materialised as a byte slice. The caller can
// render twice (the Sent copy strips Bcc).
func (m *BuiltMessage) ToBytes(withoutBcc bool) []byte {
	root := m.rootPart()

	// Message::ToStream() setDefaultHeaders block: SetByName(..., true) puts the
	// generated defaults on top in this order, so MIME-Version ends up first.
	if !m.hasValue("Date") {
		root.prependHeader(builtHeader{name: "Date", value: gmdateRFC2822(m.options.Now())})
	}
	if !m.hasValue("Message-ID") {
		root.prependHeader(builtHeader{name: "Message-ID", value: m.generateMessageID("")})
	}
	if !m.options.HideXMailer && !m.hasValue("X-Mailer") {
		root.prependHeader(builtHeader{name: "X-Mailer", value: "SnappyMail/" + m.options.Version})
	}
	if !m.hasValue("MIME-Version") {
		root.prependHeader(builtHeader{name: "MIME-Version", value: "1.0"})
	}

	for _, header := range m.values {
		if 0 < len(header.autocrypt) {
			for _, value := range header.autocrypt {
				root.addHeader(builtHeader{name: header.name, value: value})
			}
			continue
		}
		if withoutBcc && "BCC" == strings.ToUpper(header.name) {
			continue
		}
		root.setHeader(header)
	}

	return root.toBytes()
}

// rootPart is Message::GetRootPart().
func (m *BuiltMessage) rootPart() *buildPart {
	if 0 == len(m.subParts) {
		m.subParts = append(m.subParts, textPart("text/plain; charset=utf-8", ""))
	}

	var root, related *buildPart

	if 1 == len(m.subParts) {
		root = m.subParts[0]
		for _, attachment := range m.attachments {
			if !attachment.isLinked() {
				continue
			}
			related = m.newPart("multipart/related")
			related.subs = append(related.subs, root)
			root = related
			break
		}
	} else {
		root = m.newPart("multipart/mixed")
		root.subs = append(root.subs, m.subParts...)
	}

	var mixed *buildPart
	for _, attachment := range m.attachments {
		if nil != related && attachment.isLinked() {
			related.subs = append(related.subs, attachment.part())
			continue
		}
		if nil == mixed {
			mixed = m.newPart("multipart/mixed")
			mixed.subs = append(mixed.subs, root)
			root = mixed
		}
		mixed.subs = append(mixed.subs, attachment.part())
	}

	return root
}

// isLinked is Attachment::isLinked(): inline and carrying a Content-ID.
func (a *BuildAttachment) isLinked() bool {
	return nil != a && a.Linked && "" != strings.TrimSpace(a.ContentID)
}

// part is Attachment::ToPart().
func (a *BuildAttachment) part() *buildPart {
	attachmentPart := newPart(a.ContentType)
	if name := strings.TrimSpace(a.FileName); "" != name {
		attachmentPart.setParameter("name", name)
	}

	disposition := "attachment"
	if a.Inline {
		disposition = "inline"
	}
	dispositionPart := builtHeader{name: "Content-Disposition", parameterized: true, value: disposition}
	if name := strings.TrimSpace(a.FileName); "" != name {
		dispositionPart.params = append(dispositionPart.params, builtParam{name: "filename", value: name, quoted: true})
	}
	attachmentPart.headers = append(attachmentPart.headers, dispositionPart)

	if "" != a.ContentID {
		attachmentPart.headers = append(attachmentPart.headers, builtHeader{name: "Content-ID", value: a.ContentID})
	}
	if "" != a.ContentLocation {
		attachmentPart.headers = append(attachmentPart.headers, builtHeader{name: "Content-Location", value: a.ContentLocation})
	}

	if "message/rfc822" == strings.ToLower(a.ContentType) {
		attachmentPart.body = a.Data
		return attachmentPart
	}

	attachmentPart.headers = append(attachmentPart.headers,
		builtHeader{name: "Content-Transfer-Encoding", value: "base64"})
	attachmentPart.body = []byte(EncodeBase64(a.Data))
	return attachmentPart
}

// newPart builds an empty part with the given Content-Type.
func newPart(contentType string) *buildPart {
	return newPartWithPrefix(defaultBoundaryPrefix, contentType)
}

// newPartWithPrefix builds a part whose child boundaries use prefix.
func newPartWithPrefix(prefix, contentType string) *buildPart {
	part := &buildPart{prefix: prefix}
	base, params := parseContentType(contentType)
	part.headers = append(part.headers, builtHeader{
		name:          "Content-Type",
		value:         base,
		parameterized: true,
		params:        params,
	})
	return part
}

// newPart is the Message::SubParts->append(new Part) helper with the message
// boundary prefix applied.
func (m *BuiltMessage) newPart(contentType string) *buildPart {
	return newPartWithPrefix(m.options.BoundaryPrefix, contentType)
}

// textPart is Part::addPlain()/buildReadReceiptMessage()'s text part.
func textPart(contentType, text string) *buildPart {
	part := newPart(contentType)
	part.headers = append(part.headers, builtHeader{name: "Content-Transfer-Encoding", value: "quoted-printable"})
	part.body = []byte(EncodeQuotedPrintable(normalizeCRLF(strings.TrimSpace(text))))
	return part
}

// parseContentType splits a raw "value; parameter=..." line, keeping the
// parameter order (Header::initInputData()).
func parseContentType(value string) (string, []builtParam) {
	parts := strings.SplitN(value, ";", 2)
	base := strings.TrimSpace(parts[0])
	if 2 > len(parts) {
		return base, nil
	}

	parameters := ParseParameterCollection(parts[1])
	var result []builtParam
	for _, key := range parameters.order {
		parameter := parameters.items[key]
		result = append(result, builtParam{name: parameter.name, value: parameter.value, quoted: true})
	}
	return base, result
}

// prepare ensures the boundary parameter of a multipart part and returns it.
func (p *buildPart) ensureBoundary(prefix string) string {
	if existing := p.parameter("boundary"); "" != existing {
		return existing
	}
	boundary := generateBoundary(prefix)
	p.setParameter("boundary", boundary)
	return boundary
}

// parameter is HeaderCollection::ParameterValue(CONTENT_TYPE, name).
func (p *buildPart) parameter(name string) string {
	for _, header := range p.headers {
		if "CONTENT-TYPE" != strings.ToUpper(header.name) || !header.parameterized {
			continue
		}
		for _, parameter := range header.params {
			if strings.EqualFold(parameter.name, name) {
				return parameter.value
			}
		}
	}
	return ""
}

// setParameter is Header::setParameter().
func (p *buildPart) setParameter(name, value string) {
	for index := range p.headers {
		header := &p.headers[index]
		if "CONTENT-TYPE" != strings.ToUpper(header.name) || !header.parameterized {
			continue
		}
		for parameterIndex := range header.params {
			if strings.EqualFold(header.params[parameterIndex].name, name) {
				header.params[parameterIndex].value = value
				return
			}
		}
		header.params = append(header.params, builtParam{name: name, value: value, quoted: true})
		return
	}
}

// prependHeader is HeaderCollection::SetByName(..., true).
func (p *buildPart) prependHeader(header builtHeader) {
	p.removeHeader(header.name)
	p.headers = append([]builtHeader{header}, p.headers...)
}

// addHeader is HeaderCollection::AddByName().
func (p *buildPart) addHeader(header builtHeader) { p.headers = append(p.headers, header) }

// setHeader is HeaderCollection::SetByName(): remove an existing header and put
// the new value at the end.
func (p *buildPart) setHeader(header builtHeader) {
	p.removeHeader(header.name)
	p.headers = append(p.headers, header)
}

// removeHeader is HeaderCollection::RemoveByName().
func (p *buildPart) removeHeader(name string) {
	name = strings.ToUpper(name)
	result := p.headers[:0]
	for _, header := range p.headers {
		if strings.ToUpper(header.name) != name {
			result = append(result, header)
		}
	}
	p.headers = result
}

// toBytes is Part::ToStream()/PartCollection::ToStream().
func (p *buildPart) toBytes() []byte {
	if nil != p.raw {
		return p.raw
	}

	var builder strings.Builder
	boundary := ""
	if 0 < len(p.subs) {
		boundary = p.ensureBoundary(p.prefix)
	}

	for _, header := range p.headers {
		builder.WriteString(header.toString())
		builder.WriteString("\r\n")
	}

	if 0 < len(p.body) {
		builder.WriteString("\r\n")
		builder.Write(p.body)
	}

	if 0 < len(p.subs) && "" != boundary {
		for _, sub := range p.subs {
			builder.WriteString("\r\n--")
			builder.WriteString(boundary)
			builder.WriteString("\r\n")
			builder.Write(sub.toBytes())
		}
		builder.WriteString("\r\n--")
		builder.WriteString(boundary)
		builder.WriteString("--\r\n")
	}

	return []byte(builder.String())
}

// toString is Header::__toString().
func (h builtHeader) toString() string {
	name := h.name + ": "
	body := h.value

	switch uppercase(h.name) {
	case "SUBJECT":
		if !IsASCIIUnit(body) {
			return encodeMimeSubject(h.name, body)
		}
	case "CONTENT-TYPE", "CONTENT-DISPOSITION":
		if h.parameterized {
			body = h.value
			if parameters := paramsToString(h.params, true); "" != parameters {
				body += "; " + parameters
			}
		}
	case "FROM", "TO", "CC", "BCC", "REPLY-TO", "SENDER":
		if nil != h.emails && 0 < h.emails.Count() {
			body = h.emails.ToString(true, false)
		}
	}

	return wordWrapHeader(name, body)
}

// wordWrapHeader is the wordwrap($sResult, 78 - strlen($sName) - 1, "\r\n ") of
// Header::__toString().
func wordWrapHeader(name, value string) string {
	width := 78 - len(name) - 1
	if width < 1 {
		width = 1
	}
	return name + phpWordWrap(value, width)
}

// phpWordWrap reproduces wordwrap() without a cut point: break at a space within
// the width, otherwise at the next space (a single long word stays intact).
func phpWordWrap(value string, width int) string {
	if width < 1 || len(value) <= width {
		return value
	}

	var result strings.Builder
	lineStart := 0
	for lineStart < len(value) {
		remaining := value[lineStart:]
		if len(remaining) <= width {
			result.WriteString(remaining)
			break
		}

		breakAt := -1
		for index := width; index > 0; index-- {
			if ' ' == remaining[index] {
				breakAt = index
				break
			}
		}
		if 0 > breakAt {
			// No space within the width: find the next space after it.
			if next := strings.IndexByte(remaining, ' '); 0 <= next {
				breakAt = next
			} else {
				result.WriteString(remaining)
				break
			}
		}

		result.WriteString(remaining[:breakAt])
		result.WriteString("\r\n ")
		lineStart += breakAt + 1
	}

	return result.String()
}

// encodeMimeSubject is iconv_mime_encode() with the BASE64_SHORT scheme and a
// line length of 74. libiconv reserves 5 characters of the line budget and then
// fits a whole number of base64 groups (4 characters per 3 input bytes), which
// for "Subject" gives 36 bytes on the first line and 42 on each continuation
// line. Words are split at a space and the space stays in the encoded chunk.
func encodeMimeSubject(name, value string) string {
	const lineLength = 74
	const reserved = 5
	const wrapper = 12 // "=?UTF-8?B?" + "?="

	first := true
	var builder strings.Builder

	for len(value) > 0 {
		prefix := 1 // the leading space of a continuation line
		if first {
			prefix = len(name) + 2 // "Name: "
		}

		// A complete base64 group is four characters for three bytes.
		payload := (lineLength - reserved - prefix - wrapper) / 4 * 4
		if 1 > payload {
			payload = 4
		}
		budget := payload / 4 * 3

		chunk := value
		if len(chunk) > budget {
			cut := budget
			if space := strings.LastIndex(value[:cut], " "); 0 <= space {
				cut = space + 1
			}
			// Never split a UTF-8 sequence: a continuation byte at the cut means
			// the preceding byte is a multi-byte lead.
			for 0 < cut && cut < len(value) && 0x80 == value[cut]&0xC0 {
				cut--
			}
			if 0 == cut {
				cut = budget
			}
			chunk = value[:cut]
		}
		value = value[len(chunk):]

		if !first {
			builder.WriteString("\r\n ")
		}
		first = false
		builder.WriteString("=?UTF-8?B?")
		builder.WriteString(base64.StdEncoding.EncodeToString([]byte(chunk)))
		builder.WriteString("?=")
	}

	if first {
		return name + ": " + value
	}
	return name + ": " + builder.String()
}

// paramsToString is ParameterCollection::ToString().
func paramsToString(parameters []builtParam, convert bool) string {
	items := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		if "" == parameter.name {
			continue
		}
		value := parameter.value
		if convert && ("NAME" == uppercase(parameter.name) || "FILENAME" == uppercase(parameter.name)) {
			value = EncodeHeaderValue(value)
		}
		items = append(items, parameter.name+`="`+value+`"`)
	}
	return strings.Join(items, "; ")
}

// EncodeHeaderValue is MailSo\Base\Utils::EncodeHeaderValue() with the default
// BASE64 method: only non-ASCII values become an encoded word.
func EncodeHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if "" == value || IsASCIIUnit(value) {
		return value
	}
	return "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
}

// IsASCIIUnit is Utils::IsAscii() (the builder is not the message parser, so the
// name avoids a collision with the parse-side helper).
func IsASCIIUnit(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (0x09 > character || 0x7E < character) && 0x0A != character && 0x0D != character {
			return false
		}
	}
	return true
}

// EncodeQuotedPrintable is quoted_printable_encode() for a CRLF normalised body.
func EncodeQuotedPrintable(value string) string {
	var builder strings.Builder
	lineLength := 0

	for index := 0; index < len(value); index++ {
		character := value[index]

		if '\r' == character && index+1 < len(value) && '\n' == value[index+1] {
			builder.WriteString("\r\n")
			index++
			lineLength = 0
			continue
		}

		token := ""
		switch {
		case '=' == character, 0x7F == character, 0x20 > character && '\t' != character:
			token = fmt.Sprintf("=%02X", character)
		case '\t' == character || ' ' == character:
			// A trailing space/tab must be encoded.
			trailing := index+1 == len(value) || '\r' == value[index+1] || '\n' == value[index+1]
			if trailing {
				token = fmt.Sprintf("=%02X", character)
			} else {
				token = string(character)
			}
		case 0x7E < character:
			token = fmt.Sprintf("=%02X", character)
		default:
			token = string(character)
		}

		// Binary::CreateStream() configures line-length 74 for the
		// convert.quoted-printable-encode filter; the soft break '=' is part of
		// that budget.
		if lineLength+len(token) > 73 {
			builder.WriteString("=\r\n")
			lineLength = 0
		}
		builder.WriteString(token)
		lineLength += len(token)
	}

	return builder.String()
}

// EncodeBase64 is the convert.base64-encode stream filter with the
// Binary::CreateStream() line-length of 74 characters and CRLF breaks.
func EncodeBase64(value []byte) string {
	encoded := base64.StdEncoding.EncodeToString(value)
	if "" == encoded {
		return ""
	}
	var builder strings.Builder
	for index := 0; index < len(encoded); index += 74 {
		end := index + 74
		if end > len(encoded) {
			end = len(encoded)
		}
		builder.WriteString(encoded[index:end])
		builder.WriteString("\r\n")
	}
	return strings.TrimSuffix(builder.String(), "\r\n")
}

// boundary state

var (
	boundaryMu        sync.Mutex
	boundaryIncrement int64
)

// generateBoundary is PartCollection::Boundary(): prefix + UUID + '-n'.
func generateBoundary(prefix string) string {
	boundaryMu.Lock()
	boundaryIncrement++
	increment := boundaryIncrement
	boundaryMu.Unlock()

	return prefix + uuidV4() + "-" + strconv.FormatInt(increment, 10)
}

// uuidV4 is SnappyMail\UUID::generate(): a random v4 UUID.
func uuidV4() string {
	buffer := make([]byte, 16)
	_, _ = rand.Read(buffer)
	buffer[6] = (buffer[6] & 0x0F) | 0x40
	buffer[8] = (buffer[8] & 0x3F) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// gmdateRFC2822 is gmdate('r').
func gmdateRFC2822(now time.Time) string {
	return now.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700")
}

// normalizeCRLF is preg_replace('/\r?\n/su', "\r\n", $value).
func normalizeCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

// uppercase is strtoupper() for the ASCII header names.
func uppercase(value string) string { return strings.ToUpper(value) }

// setValue is the aHeadersValue[$name] = $value assignment.
func (m *BuiltMessage) setValue(name, value string) {
	m.setHeader(builtHeader{name: name, value: value})
}

// setEmails stores an address list header.
func (m *BuiltMessage) setEmails(name string, emails *EmailCollection) {
	m.setHeader(builtHeader{name: name, emails: emails})
}

// setHeader replaces a value in place or appends it (PHP array assignment).
func (m *BuiltMessage) setHeader(header builtHeader) {
	for index := range m.values {
		if strings.EqualFold(m.values[index].name, header.name) {
			m.values[index] = header
			return
		}
	}
	m.values = append(m.values, header)
}

// removeValue is unset($this->aHeadersValue[$name]).
func (m *BuiltMessage) removeValue(name string) {
	result := m.values[:0]
	for _, header := range m.values {
		if !strings.EqualFold(header.name, name) {
			result = append(result, header)
		}
	}
	m.values = result
}

// hasValue is isset($this->aHeadersValue[$name]).
func (m *BuiltMessage) hasValue(name string) bool {
	for _, header := range m.values {
		if strings.EqualFold(header.name, name) {
			return true
		}
	}
	return false
}

// collectionOf wraps one Email in an EmailCollection (the SetFrom branch).
func collectionOf(email *Email) *EmailCollection {
	collection := NewEmailCollection("")
	collection.Append(email)
	return collection
}
