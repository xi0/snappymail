package mail

// MailSo\Mail\Message, MessageCollection and Attachment: the JSON documents of
// the message list (and, from Step 17 on, the message view).
//
// The field set and names of jsonSerialize() are the frontend contract, so the
// port keeps every key, including the ones that are null/absent for a message
// that was only listed (body text, drafts, signatures).

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// Message is MailSo\Mail\Message.
type Message struct {
	folder      string
	subject     string
	messageID   string
	contentType string
	spamResult  string
	inReplyTo   string
	plain       string
	html        string
	references  string
	readReceipt string

	emailID  *string
	threadID *string
	preview  *string

	uid                int
	size               int
	spamScore          int
	internalTimestamp  int
	headerTimestamp    int
	isSpam             bool

	spf   [][3]string
	dkim  [][3]string
	dmarc [][3]string

	flags        []string
	threadUIDs   []int
	threadUnseen []int

	draftInfo []any

	pgpSigned      map[string]any
	pgpEncrypted   map[string]any
	smimeSigned    map[string]any
	smimeEncrypted map[string]any

	from        *mime.EmailCollection
	sender      *mime.EmailCollection
	replyTo     *mime.EmailCollection
	deliveredTo *mime.EmailCollection
	to          *mime.EmailCollection
	cc          *mime.EmailCollection
	bcc         *mime.EmailCollection

	attachments []*Attachment

	headers *mime.HeaderCollection
}

// UID is Message::Uid().
func (m *Message) UID() int {
	if nil == m {
		return 0
	}
	return m.uid
}

// SetThreads is Message::SetThreads().
func (m *Message) SetThreads(uids []int) {
	if nil != m {
		m.threadUIDs = uids
	}
}

// SetThreadUnseen is Message::SetThreadUnseen().
func (m *Message) SetThreadUnseen(uids []int) {
	if nil != m {
		m.threadUnseen = uids
	}
}

// SetPlain is Message::setPlain().
func (m *Message) SetPlain(value string) { m.plain = value }

// SetHTML is Message::setHtml().
func (m *Message) SetHTML(value string) { m.html = value }

// hasForwardedFlags is Message::getFlags(): normalise the keyword aliases.
func (m *Message) getFlags() []string {
	flags := make([]string, len(m.flags))
	seen := make(map[string]struct{}, len(flags))
	result := flags[:0]
	for _, flag := range m.flags {
		switch flag {
		case "$readreceipt":
			flag = "$mdnsent"
		case "$replied":
			flag = "\\answered"
		}
		if _, ok := seen[flag]; ok {
			continue
		}
		seen[flag] = struct{}{}
		result = append(result, flag)
	}
	return result
}

// MessageFromFetchResponse is Message::fromFetchResponse().
//
// Partial (Step 16): the list does not fetch body text, so plain/html stay empty
// exactly like PHP's list (which does not fetch BODY[part] either). Step 17 adds
// the body decoding to the view path.
func MessageFromFetchResponse(folder string, response *imapclient.FetchResponse) *Message {
	if nil == response {
		return nil
	}

	message := &Message{folder: folder}

	bodyStructure := response.GetFetchBodyStructure()

	if values := response.FetchValueStrings(imapclient.FetchFlags); nil != values {
		message.flags = make([]string, len(values))
		for index, flag := range values {
			message.flags[index] = strings.ToLower(imapclient.MUTF7ToUTF8(flag))
		}
	}

	message.uid = response.FetchInt(imapclient.FetchUID)
	message.size = response.FetchInt(imapclient.FetchRFC822Size)
	message.internalTimestamp = mime.ParseInternalDateString(response.FetchString(imapclient.FetchInternalDate))

	// https://www.rfc-editor.org/rfc/rfc8474
	if values := response.FetchValueStrings(imapclient.FetchEmailID); 0 < len(values) {
		message.emailID = pointer(values[0])
	} else if value := response.FetchString("X-GM-MSGID"); "" != value {
		message.emailID = pointer(value)
	}
	if values := response.FetchValueStrings(imapclient.FetchThreadID); 0 < len(values) {
		message.threadID = pointer(values[0])
	} else if value := response.FetchString("X-GM-THRID"); "" != value {
		message.threadID = pointer(value)
	}
	if value := response.FetchString(imapclient.FetchPreview); "" != value {
		message.preview = pointer(value)
	}

	charset := ""
	if nil != bodyStructure {
		charset = normalizeCharset(bodyStructure.SearchCharset())
	}

	rawHeaders := response.GetHeaderFieldsValue()
	if "" != rawHeaders {
		headers := mime.NewHeaderCollection(rawHeaders, charset)
		message.headers = headers

		if contentTypeCharset := headers.ParameterValue(mime.HeaderContentType, mime.ParameterCharset); "" != contentTypeCharset {
			charset = normalizeCharset(contentTypeCharset)
		}
		if "" != charset {
			headers.SetParentCharset(charset)
		}

		message.subject = headers.ValueByName(mime.HeaderSubject)
		message.messageID = headers.ValueByName(mime.HeaderMessageID)
		message.contentType = headers.ValueByName(mime.HeaderContentType)

		message.from = headers.GetAsEmailCollection(mime.HeaderFrom)
		message.to = headers.GetAsEmailCollection(mime.HeaderTo)
		message.cc = headers.GetAsEmailCollection(mime.HeaderCc)
		message.bcc = headers.GetAsEmailCollection(mime.HeaderBcc)
		message.sender = headers.GetAsEmailCollection(mime.HeaderSender)
		message.replyTo = headers.GetAsEmailCollection(mime.HeaderReplyTo)
		message.deliveredTo = headers.GetAsEmailCollection(mime.HeaderDeliveredTo)

		message.inReplyTo = headers.ValueByName(mime.HeaderInReplyTo)
		message.references = mime.StripSpaces(headers.ValueByName(mime.HeaderReferences))

		message.headerTimestamp = mime.ParseRFC2822DateString(headers.ValueByName(mime.HeaderDate))

		readReceipt := strings.TrimSpace(headers.ValueByName(mime.HeaderDispositionNotificationTo))
		if "" == readReceipt {
			readReceipt = strings.TrimSpace(headers.ValueByName(mime.HeaderXConfirmReadingTo))
		}
		if "" != readReceipt && nil == mime.ParseEmail(readReceipt) {
			readReceipt = ""
		}
		message.readReceipt = readReceipt

		message.applySpamHeaders(headers)

		if draftInfo := parseDraftInfo(headers.ValueByName(mime.HeaderXDraftInfo)); nil != draftInfo {
			message.draftInfo = draftInfo
		}

		auth := headers.AuthStatuses()
		message.spf = auth["spf"]
		message.dkim = auth["dkim"]
		message.dmarc = auth["dmarc"]
		if 0 < len(auth["dkim"]) && nil != message.from {
			for _, email := range message.from.Iterate() {
				for _, dkimData := range auth["dkim"] {
					if strings.Contains(email.GetEmail(), dkimData[1]) {
						email.SetDkimStatus(dkimData[0])
					}
				}
			}
		}
	} else if envelope := response.GetEnvelope(); nil != envelope {
		message.applyEnvelope(response, charset)
	}

	if strings.HasPrefix(message.subject, "[Preview]") {
		message.subject = message.subject[10:]
	}

	message.applyBodyStructure(bodyStructure)

	return message
}

// applyEnvelope is the GetEnvelope() else branch of fromFetchResponse.
func (m *Message) applyEnvelope(response *imapclient.FetchResponse, charset string) {
	if "" == charset {
		charset = "ISO-8859-1"
	}
	m.messageID = envelopeString(response, 9)
	m.subject = mime.DecodeHeaderValue(envelopeString(response, 1), charset)
	m.from = response.GetFetchEnvelopeEmailCollection(2, charset)
	m.sender = response.GetFetchEnvelopeEmailCollection(3, charset)
	m.replyTo = response.GetFetchEnvelopeEmailCollection(4, charset)
	m.to = response.GetFetchEnvelopeEmailCollection(5, charset)
	m.cc = response.GetFetchEnvelopeEmailCollection(6, charset)
	m.bcc = response.GetFetchEnvelopeEmailCollection(7, charset)
	m.inReplyTo = envelopeString(response, 8)
}

// applySpamHeaders is the spam-block of fromFetchResponse.
func (m *Message) applySpamHeaders(headers *mime.HeaderCollection) {
	if spam := headers.ValueByName(mime.HeaderXSpamdResult); "" != spam {
		if match := mustRegexp(`\[([\d\.-]+)\s*/\s*([\d\.]+)\];`).FindStringSubmatch(spam); 3 <= len(match) {
			if threshold := atof(match[2]); 0 != threshold {
				m.setSpamScore(int(100 * atof(match[1]) / threshold))
				m.spamResult = match[1] + " / " + match[2]
			}
		}
		m.isSpam = strings.Contains(strings.ToLower(m.subject), "*** spam ***")
		return
	}
	if spam := headers.ValueByName(mime.HeaderXBogosity); "" != spam {
		m.spamResult = spam
		m.isSpam = !strings.Contains(spam, "Ham")
		if match := mustRegexp(`spamicity=([\d\.]+)`).FindStringSubmatch(spam); 2 <= len(match) {
			m.setSpamScore(int(100 * atof(match[1])))
		}
		return
	}
	if spam := headers.ValueByName(mime.HeaderXSpamStatus); "" != spam {
		m.spamResult = spam
		value := mustRegexp(`(?:hits|score)=([\d\.-]+)`).FindStringSubmatch(spam)
		required := mustRegexp(`required=([\d\.-]+)`).FindStringSubmatch(spam)
		if 2 <= len(value) && 2 <= len(required) {
			if threshold := atof(required[1]); 0 != threshold {
				m.setSpamScore(int(100 * atof(value[1]) / threshold))
				m.spamResult = value[1] + " / " + required[1]
			}
		} else if match := mustRegexp(`([\d\.]+)/([\d\.]+)`).FindStringSubmatch(spam); 3 <= len(match) {
			if threshold := atof(match[2]); 0 != threshold {
				m.setSpamScore(int(100 * atof(match[1]) / threshold))
				m.spamResult = match[1] + " / " + match[2]
			}
		} else if match := mustRegexp(`([\d\.]+)/([\d\.]+)`).FindStringSubmatch(headers.ValueByName(mime.HeaderXSpamInfo)); 3 <= len(match) {
			if threshold := atof(match[2]); 0 != threshold {
				m.setSpamScore(int(100 * atof(match[1]) / threshold))
				m.spamResult = match[1] + " / " + match[2]
			}
		}
		m.isSpam = "Yes" == substr(spam, 0, 3) ||
			strings.Contains(strings.ToLower(headers.ValueByName(mime.HeaderXSpamFlag)), "yes")
	}
}

// applyBodyStructure is the "$oBodyStructure" block of fromFetchResponse for the
// parts the list exposes (pgp/smime markers and attachments).
func (m *Message) applyBodyStructure(bodyStructure *imapclient.BodyStructure) {
	if nil == bodyStructure {
		return
	}

	for _, part := range bodyStructure.SearchByContentType("multipart/encrypted") {
		if part.IsPgpEncrypted() && 2 == len(part.SubParts()) {
			m.pgpEncrypted = map[string]any{"partId": part.SubParts()[1].PartID()}
		}
	}

	for _, part := range bodyStructure.SearchByContentTypes([]string{"application/pkcs7-mime", "application/x-pkcs7-mime"}) {
		if part.IsSMimeEncrypted() {
			m.smimeEncrypted = map[string]any{"partId": part.PartID()}
		} else if part.IsSMimeSigned() {
			m.smimeSigned = map[string]any{"partId": part.PartID(), "detached": false}
		}
	}

	for _, part := range bodyStructure.SearchByContentType("multipart/signed") {
		if part.IsPgpSigned() {
			m.pgpSigned = map[string]any{
				"partId":    part.SubParts()[0].PartID(),
				"sigPartId": part.SubParts()[1].PartID(),
			}
		} else if part.IsSMimeSigned() {
			m.smimeSigned = map[string]any{
				"partId":    part.PartID(),
				"sigPartId": part.SubParts()[1].PartID(),
				"detached":  true,
			}
		}
		break
	}

	if parts := bodyStructure.SearchAttachmentsParts(); 0 < len(parts) {
		m.attachments = make([]*Attachment, 0, len(parts))
		for _, part := range parts {
			m.attachments = append(m.attachments, &Attachment{folder: m.folder, uid: m.uid, structure: part})
		}
	}
}

// setSpamScore is Message::setSpamScore().
func (m *Message) setSpamScore(value int) {
	if 0 > value {
		value = 0
	}
	if 100 < value {
		value = 100
	}
	m.spamScore = value
}

// JSONSerialize is Message::jsonSerialize().
func (m *Message) JSONSerialize() any {
	result := map[string]any{
		"@Object":            "Object/Message",
		"folder":             m.folder,
		"uid":                m.uid,
		"hash":               md5Hex(m.folder + strconv.Itoa(m.uid)),
		"subject":            strings.TrimSpace(mime.Utf8Clear(m.subject)),
		"encrypted":          "multipart/encrypted" == m.contentType || nil != m.pgpEncrypted || nil != m.smimeEncrypted,
		"messageId":          m.messageID,
		"spamScore":          spamScore(m),
		"spamResult":         m.spamResult,
		"isSpam":             m.isSpam,
		"dateTimestamp":      dateTimestamp(m),
		"dateTimestampSource": dateTimestampSource(m),
		"from":               emailCollection(m.from),
		"replyTo":            emailCollection(m.replyTo),
		"to":                 emailCollection(m.to),
		"cc":                 emailCollection(m.cc),
		"bcc":                emailCollection(m.bcc),
		"sender":             emailCollection(m.sender),
		"deliveredTo":        emailCollection(m.deliveredTo),
		"readReceipt":        m.readReceipt,
		"attachments":        attachmentCollection(m.attachments),
		"spf":                statusList(m.spf),
		"dkim":               statusList(m.dkim),
		"dmarc":              statusList(m.dmarc),
		"flags":              m.getFlags(),
		"inReplyTo":          m.inReplyTo,
		"id":                 valueOrNil(m.emailID),
		"size":               m.size,
		"preview":            valueOrNil(m.preview),
		"headers":            headerCollection(m.headers),
	}

	if nil != m.draftInfo {
		result["draftInfo"] = m.draftInfo
	}
	if "" != m.references {
		result["references"] = m.references
	}
	if "" != m.html || "" != m.plain {
		result["html"] = m.html
		result["plain"] = m.plain
	}
	if nil != m.pgpSigned {
		result["pgpSigned"] = m.pgpSigned
	}
	if nil != m.pgpEncrypted {
		result["pgpEncrypted"] = m.pgpEncrypted
	}
	if nil != m.smimeSigned {
		result["smimeSigned"] = m.smimeSigned
	}
	if nil != m.smimeEncrypted {
		result["smimeEncrypted"] = m.smimeEncrypted
	}
	if 0 < len(m.threadUIDs) {
		result["threads"] = m.threadUIDs
	}
	if 0 < len(m.threadUnseen) {
		result["threadUnseen"] = m.threadUnseen
	}

	return result
}

// Attachment is MailSo\Mail\Attachment.
type Attachment struct {
	folder    string
	uid       int
	structure *imapclient.BodyStructure
}

// JSONSerialize is Attachment::jsonSerialize().
func (a *Attachment) JSONSerialize() any {
	result, _ := a.structure.JSONSerialize().(map[string]any)
	if nil == result {
		result = map[string]any{}
	}
	result["@Object"] = "Object/Attachment"
	result["folder"] = a.folder
	result["uid"] = a.uid
	return result
}

// MessageCollection is MailSo\Mail\MessageCollection.
type MessageCollection struct {
	TotalEmails  int
	TotalThreads *int
	FolderName   string
	Offset       int
	Limit        int
	Search       string
	Sort         string
	ThreadUid    int
	FolderInfo   *imapclient.FolderInformation
	NewMessages  []any
	Limited      bool
	items        []*Message
}

// Append stores a message, optionally at the top.
func (c *MessageCollection) Append(message *Message, toTop bool) {
	if nil == c || nil == message {
		return
	}
	if toTop {
		c.items = append([]*Message{message}, c.items...)
		return
	}
	c.items = append(c.items, message)
}

// Iterate returns the messages in order.
func (c *MessageCollection) Iterate() []*Message {
	if nil == c {
		return nil
	}
	return c.items
}

// JSONSerialize is MessageCollection::jsonSerialize().
func (c *MessageCollection) JSONSerialize() any {
	collection := make([]any, 0, len(c.items))
	for _, message := range c.items {
		collection = append(collection, message)
	}

	var totalThreads any
	if nil != c.TotalThreads {
		totalThreads = *c.TotalThreads
	}

	newMessages := c.NewMessages
	if nil == newMessages {
		newMessages = []any{}
	}

	var folder any
	if nil != c.FolderInfo {
		folder = c.FolderInfo
	}

	return map[string]any{
		"@Object":     "Collection/MessageCollection",
		"@Collection": collection,
		"totalEmails": c.TotalEmails,
		"totalThreads": totalThreads,
		"threadUid":   c.ThreadUid,
		"newMessages": newMessages,
		"offset":      c.Offset,
		"limit":       c.Limit,
		"search":      c.Search,
		"sort":        c.Sort,
		"limited":     c.Limited,
		"folder":      folder,
	}
}

// The remaining helpers.

// emailCollection renders an EmailCollection, or null when it is nil.
func emailCollection(collection *mime.EmailCollection) any {
	if nil == collection {
		return nil
	}
	return collection
}

// attachmentCollection is AttachmentCollection::jsonSerialize().
func attachmentCollection(attachments []*Attachment) any {
	if nil == attachments {
		return nil
	}
	items := make([]any, 0, len(attachments))
	for _, attachment := range attachments {
		items = append(items, attachment)
	}
	return map[string]any{
		"@Object":     "Collection/AttachmentCollection",
		"@Collection": items,
	}
}

// headerCollection renders a HeaderCollection, or null when it is nil.
func headerCollection(headers *mime.HeaderCollection) any {
	if nil == headers {
		return nil
	}
	return headers
}

// statusList renders the SPF/DKIM/DMARC triples as a list of lists.
func statusList(statuses [][3]string) any {
	result := make([]any, 0, len(statuses))
	for _, status := range statuses {
		result = append(result, []any{status[0], status[1], status[2]})
	}
	return result
}

// valueOrNil renders a *string as value-or-nil.
func valueOrNil(value *string) any {
	if nil == value {
		return nil
	}
	return *value
}

// spamScore is the "spamScore" expression of jsonSerialize().
func spamScore(message *Message) int {
	if message.isSpam {
		return 100
	}
	return message.spamScore
}

// dateTimestamp is the "dateTimestamp" expression of jsonSerialize().
func dateTimestamp(message *Message) int {
	if 0 != message.headerTimestamp {
		return message.headerTimestamp
	}
	return message.internalTimestamp
}

// dateTimestampSource is the "dateTimestampSource" expression of jsonSerialize().
func dateTimestampSource(message *Message) string {
	if 0 != message.headerTimestamp {
		return "header"
	}
	return "internal"
}

// pointer returns a pointer to value.
func pointer(value string) *string { return &value }

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// tokenString is the string value of a token value, "" otherwise.
func tokenString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// envelopeString is GetFetchEnvelopeValue() as a string.
func envelopeString(response *imapclient.FetchResponse, index int) string {
	return tokenString(response.GetFetchEnvelopeValue(index))
}

// parseDraftInfo is the X-Draft-Info parser of fromFetchResponse.
func parseDraftInfo(raw string) []any {
	if "" == raw {
		return nil
	}
	draftType := ""
	folder := ""
	uid := 0
	for _, pair := range strings.Split(raw, ";") {
		parts := strings.SplitN(pair, "=", 2)
		if 2 != len(parts) {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.Trim(strings.TrimSpace(parts[1]), `"`)
		switch name {
		case "type":
			draftType = value
		case "uid":
			uid, _ = strconv.Atoi(strings.TrimSpace(value))
		case "folder":
			folder = base64Decode(value)
		}
	}
	if "" != draftType && "" != folder && 0 != uid {
		return []any{draftType, uid, folder}
	}
	return nil
}

// normalizeCharset is Utils::NormalizeCharset() for the charsets Go handles.
func normalizeCharset(value string) string {
	return strings.TrimSpace(value)
}

// substr is PHP's substr($value, $start, $length) for a non-negative start.
func substr(value string, start, length int) string {
	if start >= len(value) {
		return ""
	}
	end := start + length
	if end > len(value) {
		end = len(value)
	}
	return value[start:end]
}

// atof is PHP's floatval() for the numeric strings the spam headers carry.
func atof(value string) float64 {
	number, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return number
}

// base64Decode is PHP's base64_decode() ignoring errors.
func base64Decode(value string) string {
	decoded, err := base64StdDecode(value)
	if nil != err {
		return ""
	}
	return decoded
}
