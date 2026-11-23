package imapclient

// The message command layer of MailSo\Imap\Commands\Messages: FETCH (with the
// Request/Response parsing of FetchResponse), SEARCH, SORT and THREAD, plus the
// FetchType helpers those commands need.
//
// The FETCH answer is a flat list that alternates attribute names and values,
// which the tokenizer keeps as one parenthesized list. FetchResponse indexes
// that list the way the PHP ResponseList does, so the list/view code can ask for
// "UID", "RFC822.SIZE", "FLAGS", "INTERNALDATE", the custom header block, …
// exactly like MailSo\Mail\Message::fromFetchResponse().

import (
	"errors"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// The FETCH item names of MailSo\Imap\Enumerations\FetchType (the subset the
// list/thread steps use).
const (
	FetchFast          = "FAST"
	FetchAll           = "ALL"
	FetchFull          = "FULL"
	FetchHeader        = "HEADER"
	FetchText          = "TEXT"
	FetchMIME          = "MIME"
	FetchBody          = "BODY"
	FetchBodyPeek      = "BODY.PEEK"
	FetchBodyHeader    = "BODY[HEADER]"
	FetchBodyHeaderPeek = "BODY.PEEK[HEADER]"
	FetchBodyStructure = "BODYSTRUCTURE"
	FetchEnvelope      = "ENVELOPE"
	FetchFlags         = "FLAGS"
	FetchInternalDate  = "INTERNALDATE"
	FetchRFC822Size    = "RFC822.SIZE"
	FetchUID           = "UID"
	FetchModSeq        = "MODSEQ"
	FetchEmailID       = "EMAILID"
	FetchThreadID      = "THREADID"
	FetchPreview       = "PREVIEW"
)

// BuildBodyCustomHeaderRequest is FetchType::BuildBodyCustomHeaderRequest().
func BuildBodyCustomHeaderRequest(headers []string, peek bool) string {
	if 0 == len(headers) {
		return ""
	}
	upper := make([]string, len(headers))
	for index, header := range headers {
		upper[index] = strings.ToUpper(strings.TrimSpace(header))
	}
	prefix := FetchBodyPeek
	if !peek {
		prefix = FetchBody
	}
	return prefix + "[HEADER.FIELDS (" + strings.Join(upper, " ") + ")]"
}

// IsValidPartId is FetchType::isValidPartId(): an empty section, TEXT or a dot
// separated list of positive integers.
func IsValidPartId(partID string, allowEmpty bool) bool {
	if "" == partID {
		return allowEmpty
	}
	if "TEXT" == strings.ToUpper(partID) {
		return true
	}
	return mustRegexp(`\A(?:\d+\.)*\d+\z`).MatchString(partID)
}

// EscapeString is ImapClient::EscapeString(): a quoted string.
func (c *Client) EscapeString(value string) string {
	return quoteString(value)
}

// FetchResponse is MailSo\Imap\FetchResponse: one FETCH answer.
type FetchResponse struct {
	// index is ResponseList[1]: the message sequence number.
	index int
	// items is ResponseList[3]: the flat name/value list.
	items []token
}

// Index is the message sequence number of the answer.
func (f *FetchResponse) Index() int { return f.index }

// GetFetchValue is FetchResponse::GetFetchValue(): the value that follows the
// given attribute name in the flat list.
func (f *FetchResponse) GetFetchValue(itemName string) (token, bool) {
	if nil == f {
		return token{}, false
	}
	for index := 0; index+1 < len(f.items); index++ {
		if tokenAtom != f.items[index].kind && tokenString != f.items[index].kind {
			continue
		}
		if f.items[index].text == itemName {
			return f.items[index+1], true
		}
	}
	// A server may lower/upper case an attribute differently than requested.
	for index := 0; index+1 < len(f.items); index++ {
		if tokenAtom != f.items[index].kind && tokenString != f.items[index].kind {
			continue
		}
		if strings.EqualFold(f.items[index].text, itemName) {
			return f.items[index+1], true
		}
	}
	return token{}, false
}

// GetHeaderFieldsValue is FetchResponse::GetHeaderFieldsValue(): the raw header
// block of the custom header request.
func (f *FetchResponse) GetHeaderFieldsValue() string {	if nil == f {
		return ""
	}
	for index := 0; index+1 < len(f.items); index++ {
		if tokenAtom != f.items[index].kind && tokenString != f.items[index].kind {
			continue
		}
		name := f.items[index].text
		if name == FetchBodyHeader || strings.HasPrefix(name, "BODY[HEADER.FIELDS") || name == "BODY[MIME]" {
			if text, ok := f.items[index+1].textValue(); ok {
				return text
			}
			return ""
		}
	}
	return ""
}

// FetchValueStrings is the flattened string list of the value that follows the
// attribute name: a single atom/string becomes a one element list.
func (f *FetchResponse) FetchValueStrings(itemName string) []string {
	value, ok := f.GetFetchValue(itemName)
	if !ok {
		return nil
	}
	return tokenStringsFromValue(value)
}

// FetchString is the string value that follows the attribute name.
func (f *FetchResponse) FetchString(itemName string) string {
	value, ok := f.GetFetchValue(itemName)
	if !ok {
		return ""
	}
	text, _ := value.textValue()
	return text
}

// FetchInt is the integer value that follows the attribute name.
func (f *FetchResponse) FetchInt(itemName string) int {
	value, ok := f.GetFetchValue(itemName)
	if !ok {
		return 0
	}
	return integerValue(value)
}

// tokenStringsFromValue flattens a token into its string entries.
func tokenStringsFromValue(value token) []string {
	switch value.kind {
	case tokenAtom, tokenString:
		return []string{value.text}
	case tokenList:
		var result []string
		for _, item := range value.list {
			result = append(result, tokenStringsFromValue(item)...)
		}
		return result
	}
	return nil
}

// GetFetchBodyStructure is FetchResponse::GetFetchBodyStructure().
func (f *FetchResponse) GetFetchBodyStructure() *BodyStructure {
	value, ok := f.GetFetchValue(FetchBodyStructure)
	if !ok {
		return nil
	}
	return NewBodyStructure(tokenValue(value), "")
}

// GetEnvelope is FetchResponse::GetEnvelope().
func (f *FetchResponse) GetEnvelope() []any {
	value, ok := f.GetFetchValue(FetchEnvelope)
	if !ok {
		return nil
	}
	list, _ := tokenValue(value).([]any)
	return list
}

// GetFetchEnvelopeValue is FetchResponse::GetFetchEnvelopeValue().
func (f *FetchResponse) GetFetchEnvelopeValue(index int) any {
	envelope := f.GetEnvelope()
	if index < len(envelope) && nil != envelope[index] {
		return envelope[index]
	}
	return nil
}

// GetFetchEnvelopeEmailCollection is FetchResponse::GetFetchEnvelopeEmailCollection().
func (f *FetchResponse) GetFetchEnvelopeEmailCollection(index int, charset string) *mime.EmailCollection {
	collection := mime.NewEmailCollection("")
	emails, ok := f.GetFetchEnvelopeValue(index).([]any)
	if !ok {
		return collection
	}
	for _, raw := range emails {
		emailItem, ok := raw.([]any)
		if !ok || 4 != len(emailItem) {
			continue
		}
		displayName := mime.DecodeHeaderValue(envelopeText(emailItem, 0), charset)
		localPart := envelopeText(emailItem, 2)
		domainPart := envelopeText(emailItem, 3)
		if "" != localPart && "" != domainPart {
			collection.Append(mime.NewEmail(localPart+"@"+domainPart, displayName))
		}
	}
	return collection
}

// envelopeText reads a string at index of an envelope item.
func envelopeText(list []any, index int) string {
	if index < len(list) {
		if text, ok := list[index].(string); ok {
			return text
		}
	}
	return ""
}

// tokenValue converts a token tree into the []any / string / nil model PHP's
// array response uses.
func tokenValue(value token) any {
	switch value.kind {
	case tokenList:
		list := make([]any, len(value.list))
		for index, item := range value.list {
			list[index] = tokenValue(item)
		}
		return list
	case tokenNil:
		return nil
	case tokenAtom, tokenString:
		return value.text
	}
	return nil
}

// Fetch is MailSo\Imap\Commands\Messages::Fetch(): run a FETCH and return the
// answers that carry a UID and a size, like FetchIterate().
func (c *Client) Fetch(inputFetchItems []string, indexRange string, indexIsUid bool) ([]*FetchResponse, error) {
	if "" == strings.TrimSpace(indexRange) {
		return nil, protocolFailure(errEmptyResponse)
	}

	items := make([]param, 0, len(inputFetchItems)+4)
	items = append(items, param{text: FetchUID}, param{text: FetchRFC822Size})
	for _, item := range inputFetchItems {
		switch item {
		case FetchUID, FetchRFC822Size:
			// Already defined by default.
		case FetchFull:
			items = append(items, param{text: FetchBody})
			fallthrough
		case FetchAll:
			items = append(items, param{text: FetchEnvelope})
			fallthrough
		case FetchFast:
			items = append(items, param{text: FetchFlags}, param{text: FetchInternalDate})
		default:
			if "" != item {
				items = append(items, param{text: item})
			}
		}
	}
	if c.HasCapability("OBJECTID") {
		items = append(items, param{text: FetchEmailID}, param{text: FetchThreadID})
	} else if c.HasCapability("X-GM-EXT-1") {
		items = append(items, param{text: "X-GM-MSGID"}, param{text: "X-GM-THRID"})
	}

	command := "FETCH"
	if indexIsUid {
		command = "UID FETCH"
	}

	replies, err := c.sendParams(command, []param{
		{text: indexRange},
		{group: items},
	})
	if nil != err {
		return nil, protocolFailure(err)
	}

	var result []*FetchResponse
	for _, reply := range replies {
		if respData != reply.kind || 3 > len(reply.values) {
			continue
		}
		if "FETCH" != strings.ToUpper(tokenText(reply.values[1])) {
			continue
		}
		if tokenList != reply.values[2].kind {
			continue
		}
		if !fetchHasUIDAndSize(reply.values[2].list) {
			c.logWarning("Skipped FETCH response without UID/RFC822.SIZE")
			continue
		}
		result = append(result, &FetchResponse{
			index: integerValue(reply.values[0]),
			items: reply.values[2].list,
		})
	}

	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	return result, nil
}

// fetchHasUIDAndSize is FetchResponse::hasUidAndSize().
func fetchHasUIDAndSize(items []token) bool {
	hasUID := false
	hasSize := false
	for _, item := range items {
		if text, ok := item.textValue(); ok {
			if FetchUID == text {
				hasUID = true
			}
			if FetchRFC822Size == text {
				hasSize = true
			}
		}
	}
	return hasUID && hasSize
}

// MessageSearch is Commands\Messages::MessageSearch(): the matching UIDs (or
// sequence numbers), reversed like the PHP method.
func (c *Client) MessageSearch(criterias string, returnUid bool) ([]int, error) {
	criteriaParts := make([]param, 0, 2)
	if !c.utf8 && !mime.IsASCII(criterias) {
		criteriaParts = append(criteriaParts, param{text: "CHARSET"}, param{text: "UTF-8"})
	}
	if "" == criterias || "*" == criterias {
		criterias = "ALL"
	}
	criteriaParts = append(criteriaParts, param{text: criterias})

	command := "SEARCH"
	if returnUid {
		command = "UID SEARCH"
	}

	replies, err := c.sendParams(command, criteriaParts)
	if nil != err {
		return nil, protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	uids := collectNumberResponses(replies, "SEARCH", returnUid)
	reverseInts(uids)
	return uids, nil
}

// MessageSort is Commands\Messages::MessageSort(): the sorted UIDs (or sequence
// numbers) of a SORT command.
func (c *Client) MessageSort(sortTypes []string, criterias string, returnUid bool) ([]int, error) {
	if !c.HasCapability("SORT") {
		return nil, protocolFailure(errors.New("SORT is not supported"))
	}
	if 0 == len(sortTypes) {
		return nil, protocolFailure(errors.New("SortTypes are missing"))
	}
	if "" == criterias {
		criterias = "ALL"
	}

	command := "SORT"
	if returnUid {
		command = "UID SORT"
	}

	params := []param{
		{group: []param{{text: strings.Join(sortTypes, " ")}}},
		{text: "UTF-8"},
		{text: criterias},
	}

	replies, err := c.sendParams(command, params)
	if nil != err {
		return nil, protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	return collectNumberResponses(replies, "SORT", returnUid), nil
}

// MessageThread is Commands\Messages::MessageThread(): yield the top level
// threads of a THREAD answer. Each item is either an int (a single message) or a
// nested []any (a list of messages/sub-threads).
func (c *Client) MessageThread(criterias, algorithm string, returnUid bool) ([]any, error) {
	if !hasThreadCapability(c) {
		return nil, protocolFailure(errors.New("THREAD is not supported"))
	}
	if "" == criterias {
		criterias = "ALL"
	}
	if "" == algorithm {
		algorithm = c.threadAlgorithm()
	}

	command := "THREAD"
	if returnUid {
		command = "UID THREAD"
	}

	replies, err := c.sendParams(command, []param{
		{text: algorithm},
		{text: "UTF-8"},
		{text: criterias},
	})
	if nil != err {
		return nil, protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	var result []any
	for _, reply := range replies {
		if respData != reply.kind || 2 > len(reply.values) {
			continue
		}
		offset := 0
		if returnUid && "UID" == strings.ToUpper(tokenText(reply.values[0])) {
			if 3 > len(reply.values) || "THREAD" != strings.ToUpper(tokenText(reply.values[1])) {
				continue
			}
			offset = 1
		}
		if "THREAD" != strings.ToUpper(tokenText(reply.values[offset])) {
			continue
		}
		for index := 1 + offset; index < len(reply.values); index++ {
			if item := validateThreadItem(tokenValue(reply.values[index])); nil != item {
				result = append(result, item)
			}
		}
	}
	return result, nil
}

// threadAlgorithm is the default THREAD algorithm of Requests\THREAD.
func (c *Client) threadAlgorithm() string {
	switch {
	case c.HasCapability("THREAD=REFS"):
		return "REFS"
	case c.HasCapability("THREAD=REFERENCES"):
		return "REFERENCES"
	case c.HasCapability("THREAD=ORDEREDSUBJECT"):
		return "ORDEREDSUBJECT"
	}
	return ""
}

// hasThreadCapability reports whether the server advertises any THREAD algorithm.
func hasThreadCapability(c *Client) bool {
	if value, ok := c.CapabilityValue("THREAD"); ok && "" != value {
		return true
	}
	return c.HasCapability("THREAD=REFS") || c.HasCapability("THREAD=REFERENCES") || c.HasCapability("THREAD=ORDEREDSUBJECT")
}

// validateThreadItem is Requests\THREAD::validateThreadItem(): nil for an
// invalid item, an int for a valid message, else a nested []any.
func validateThreadItem(value any) any {
	switch typed := value.(type) {
	case string:
		if number, ok := atoiPositive(typed); ok {
			return number
		}
	case int:
		if 0 < typed {
			return typed
		}
	case []any:
		if 1 == len(typed) {
			if number, ok := intOf(typed[0]); ok && 0 < number {
				return number
			}
		}
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			if validated := validateThreadItem(item); nil != validated {
				result = append(result, validated)
			}
		}
		return result
	}
	return nil
}

// collectNumberResponses collects the numeric entries of a "* SEARCH …" /
// "* SORT …" answer, honouring the "UID" prefix offset like PHP.
func collectNumberResponses(replies []response, name string, returnUid bool) []int {
	var result []int
	for _, reply := range replies {
		if respData != reply.kind {
			continue
		}
		offset := -1
		if 0 < len(reply.values) && strings.EqualFold(tokenText(reply.values[0]), name) {
			offset = 1
		} else if returnUid && 1 < len(reply.values) &&
			"UID" == strings.ToUpper(tokenText(reply.values[0])) &&
			strings.EqualFold(tokenText(reply.values[1]), name) {
			offset = 2
		}
		if 0 > offset {
			continue
		}
		for index := offset; index < len(reply.values); index++ {
			if number, ok := integerToken(reply.values[index]); ok {
				result = append(result, number)
			}
		}
	}
	return result
}

// atoiPositive parses a numeric string and reports it when positive.
func atoiPositive(value string) (int, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if nil != err || 0 >= number {
		return 0, false
	}
	return number, true
}

// reverseInts reverses a slice in place.
func reverseInts(values []int) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
