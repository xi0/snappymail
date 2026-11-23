package mail

// The paging/searching/sorting/threading core of MailSo\Mail\MailClient::
// MessageList(). It ties together the IMAP command layer (SORT/SEARCH/THREAD/
// FETCH), the search criteria parser and the Message/MessageCollection models.
//
// The behaviour is ported as-is, including the "message_list_limit" optimization
// (ID/PEEK windows for large mailboxes) and the thread collapsing rules.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// Cacher is the MailSo\Cache\CacheClient surface MessageList() uses.
type Cacher interface {
	IsInited() bool
	Get(key string) string
	Set(key, value string)
}

// MessageListParams is MailSo\Mail\MessageListParams.
type MessageListParams struct {
	FolderName      string
	Search          string
	Sort            string
	ThreadAlgorithm string
	Cacher          Cacher

	UseSort     bool
	UseThreads  bool
	HideDeleted bool
	SearchFuzzy bool

	Offset      int
	Limit       int
	PrevUIDNext int
	ThreadUid   int

	SequenceSet *imapclient.SequenceSet
}

// NewMessageListParams builds the params with the PHP defaults (UseSort true,
// HideDeleted true).
func NewMessageListParams() *MessageListParams {
	return &MessageListParams{UseSort: true, HideDeleted: true}
}

// clone copies the params, so an all-messages variant can be derived.
func (p *MessageListParams) clone() *MessageListParams {
	copied := *p
	return &copied
}

// hash is MessageListParams::hash().
func (p *MessageListParams) hash() string {
	threadUid := ""
	threadAlgorithm := ""
	if p.UseThreads {
		threadUid = strconv.Itoa(p.ThreadUid)
		threadAlgorithm = p.ThreadAlgorithm
	}
	sortValue := "0"
	if p.UseSort {
		sortValue = p.Sort
	}
	return md5Hex(strings.Join([]string{
		p.FolderName,
		strconv.Itoa(p.Offset),
		strconv.Itoa(p.Limit),
		boolDigit(p.HideDeleted),
		p.Search,
		boolDigit(p.SearchFuzzy),
		sortValue,
		threadUid,
		threadAlgorithm,
		strconv.Itoa(p.PrevUIDNext),
	}, "-"))
}

// MessageList is MailClient::MessageList().
func (c *Client) MessageList(params *MessageListParams) (*MessageCollection, error) {
	if nil == params {
		return nil, errors.New("mail: missing message list params")
	}
	if 0 > params.Offset || 0 > params.Limit {
		return nil, errors.New("mail: invalid offset/limit")
	}
	if 10 > params.Limit {
		params.Limit = 10
	} else if 999 < params.Limit {
		params.Limit = 50
	}

	imap := c.ImapClient()
	display := imap.HasCapability("SORT=DISPLAY")

	if !ValidateSortCriteria(params.Sort, display) {
		return nil, errors.New("mail: invalid SORT criteria")
	}
	params.Sort = NormalizeSortCriteria(params.Sort, display)

	search := strings.TrimSpace(params.Search)

	collection := &MessageCollection{
		FolderName: params.FolderName,
		Offset:     params.Offset,
		Limit:      params.Limit,
		Search:     search,
		ThreadUid:  params.ThreadUid,
	}

	info, err := imap.FolderStatusAndSelect(params.FolderName)
	if nil != err {
		return nil, err
	}
	collection.FolderInfo = info
	total := intOrZero(info.Messages)
	collection.TotalEmails = total

	if _, ok := imap.CapabilityValue("THREAD"); !ok {
		params.UseThreads = false
	}
	if 0 != params.ThreadUid && !params.UseThreads {
		return nil, errors.New("mail: THREAD not supported")
	}

	if 0 == total || params.Offset > total {
		return collection, nil
	}

	if 0 == params.ThreadUid {
		if newMessages, infoErr := c.getFolderNextMessageInformation(params.FolderName, params.PrevUIDNext, intOrZero(info.UIDNext)); nil == infoErr {
			collection.NewMessages = newMessages
		}
	}

	useSort := (params.UseSort || "" != params.Sort) && imap.HasCapability("SORT")
	params.UseSort = useSort
	params.Search = search

	var (
		allThreads [] []int
		unseen     []int
		aUids      []int
		hasUids    bool
	)

	messageListLimit := 0
	if nil != imap.Settings {
		messageListLimit = imap.Settings.MessageListLimit
	}
	if 100 > messageListLimit || messageListLimit > total {
		messageListLimit = 0
	}

	allParams := params.clone()
	allParams.Search = ""
	allParams.SequenceSet = nil

	if 0 != messageListLimit && 0 == params.ThreadUid && nil != params.Cacher && params.Cacher.IsInited() {
		if cached, cacheErr := c.getUids(allParams, info, true); nil == cacheErr && nil != cached {
			aUids = cached
			hasUids = true
			messageListLimit = 0
			collection.Sort = allParams.Sort
		}
	}

	if 0 != messageListLimit && !hasUids {
		collection.Limited = true
		if "" != search {
			params.UseSort = false
			if fetched, fetchErr := c.getUids(params, info, false); nil == fetchErr {
				aUids = fetched
				hasUids = true
			}
		} else {
			if useSort {
				end := minInt(total, maxInt(1, total-params.Offset+params.Limit))
				start := maxInt(1, end-(params.Limit*3)+1)
				params.SequenceSet = imapclient.NewSequenceSet(intsToAny(rangeDesc(end, start)), false)
				requestIndexes, _ := c.getUids(params, info, false)
				offset := 0
				if 0 != params.Offset {
					offset = params.Limit
				}
				requestIndexes = arraySlice(requestIndexes, offset, params.Limit)
				c.messageListByRequestIndexOrUids(collection, imapclient.NewSequenceSet(intsToAny(requestIndexes), false), nil, nil)
			} else {
				end := maxInt(1, total-params.Offset)
				start := maxInt(1, end-params.Limit+1)
				requestIndexes := rangeDesc(end, start)
				c.messageListByRequestIndexOrUids(collection, imapclient.NewSequenceSet(intsToAny(requestIndexes), false), nil, nil)
			}
		}
		collection.Sort = params.Sort
	} else {
		if params.UseThreads && 0 != params.ThreadUid {
			aUids = []int{params.ThreadUid}
			hasUids = true
		} else if !hasUids {
			if fetched, fetchErr := c.getUids(allParams, info, false); nil == fetchErr {
				aUids = fetched
				hasUids = true
			}
			collection.Sort = allParams.Sort
		}

		if params.UseThreads {
			allThreads = c.threadsMap(params.ThreadAlgorithm, collection, params.Cacher)
			if 0 != params.ThreadUid {
				for _, thread := range allThreads {
					if containsInt(thread, params.ThreadUid) {
						aUids = thread
						break
					}
				}
				allThreads = [][]int{aUids}
			} else {
				old := c.threadsOldUids(allThreads, collection, params.Cacher)
				aUids = arrayDiff(aUids, old)
				unseen = c.MessageListUnseen(params, info)
			}
		}

		if hasUids && 0 < len(aUids) && "" != search {
			params.UseSort = false
			searched, _ := c.getUids(params, info, false)
			if params.UseThreads && 0 == params.ThreadUid {
				var matching []int
				for _, thread := range allThreads {
					if intersectInts(searched, thread) {
						matching = append(matching, thread...)
					}
				}
				aUids = filterInts(aUids, func(uid int) bool {
					return containsInt(searched, uid) || containsInt(matching, uid)
				})
			} else {
				aUids = filterInts(aUids, func(uid int) bool {
					return containsInt(searched, uid)
				})
			}
		}
	}

	if hasUids {
		collection.TotalEmails = len(aUids)
		if 0 < collection.TotalEmails {
			aUids = arraySlice(aUids, params.Offset, params.Limit)
			c.messageListByRequestIndexOrUids(collection, imapclient.NewSequenceSet(intsToAny(aUids), true), allThreads, unseen)
		}
	}

	return collection, nil
}

// getUids is MailClient::GetUids().
func (c *Client) getUids(params *MessageListParams, info *imapclient.FolderInformation, onlyCache bool) ([]int, error) {
	imap := c.ImapClient()
	cacher := params.Cacher
	folderName := params.FolderName

	useSort := params.UseSort && imap.HasCapability("SORT")
	var sortTypes []string
	if useSort {
		if "" != params.Sort && !ValidateSortCriteria(params.Sort, imap.HasCapability("SORT=DISPLAY")) {
			return nil, errors.New("mail: invalid SORT criteria")
		}
		sortValue := NormalizeSortCriteria(params.Sort, imap.HasCapability("SORT=DISPLAY"))
		if "" != sortValue {
			sortTypes = append(sortTypes, sortValue)
		}
		if !strings.Contains(sortValue, SortDate) {
			sortTypes = append(sortTypes, SortReverse+" "+SortDate)
		}
	}
	params.Sort = strings.Join(sortTypes, " ")

	useCache := nil != cacher && cacher.IsInited()
	criterias, useCache := SearchCriteriasFromString(imap, folderName, params.Search, params.HideDeleted, useCache)

	returnUid := true
	if nil != params.SequenceSet {
		returnUid = params.SequenceSet.UID
		prefix := ""
		if returnUid {
			prefix = "UID "
		}
		criterias.Prepend(prefix + params.SequenceSet.String())
	}

	serializedHash := ""
	if useCache && "" != info.ETag {
		kind := "IDS/"
		if returnUid {
			kind = "UIDS/"
		}
		serializedHash = "Get" + kind + params.Sort + "/" + imap.Hash() + "/" + folderName + "/" + criterias.String()
		serialized := cacher.Get(serializedHash)
		if "" != serialized {
			var cached struct {
				FolderHash string
				Uids       []int
			}
			if nil == json.Unmarshal([]byte(serialized), &cached) &&
				info.ETag == cached.FolderHash && nil != cached.Uids {
				return cached.Uids, nil
			}
		}
	}
	if onlyCache {
		return nil, nil
	}

	if _, err := imap.FolderSelect(folderName, false); nil != err {
		return nil, err
	}

	var (
		result []int
		err    error
	)
	if useSort {
		result, err = imap.MessageSort(sortTypes, criterias.String(), returnUid)
	} else {
		result, err = imap.MessageSearch(criterias.String(), returnUid)
	}
	if nil != err {
		return nil, err
	}

	if useCache {
		if encoded, encodeErr := json.Marshal(map[string]any{
			"FolderHash": info.ETag,
			"Uids":       result,
		}); nil == encodeErr {
			cacher.Set(serializedHash, string(encoded))
		}
	}

	return result, nil
}

// MessageListUnseen is MailClient::MessageListUnseen().
func (c *Client) MessageListUnseen(params *MessageListParams, info *imapclient.FolderInformation) []int {
	unseen := NewMessageListParams()
	unseen.FolderName = params.FolderName
	unseen.Search = "unseen"
	unseen.Cacher = params.Cacher
	unseen.UseSort = false
	unseen.UseThreads = false
	unseen.HideDeleted = params.HideDeleted

	result, _ := c.getUids(unseen, info, false)
	return result
}

// threadsMap is MailClient::ThreadsMap().
func (c *Client) threadsMap(algorithm string, collection *MessageCollection, cacher Cacher) [][]int {
	info := collection.FolderInfo
	folderName := ""
	etag := ""
	if nil != info {
		folderName = info.FullName
		etag = info.ETag
	}
	search := "ALL"

	cacheKey := ""
	if nil != cacher && cacher.IsInited() {
		cacheKey = "ThreadsMap/" + algorithm + "/" + search + "/" + etag
		if serialized := cacher.Get(cacheKey); "" != serialized {
			var cached struct{ ThreadsUids [][]int }
			if nil == json.Unmarshal([]byte(serialized), &cached) && nil != cached.ThreadsUids {
				collection.TotalThreads = intPointer(len(cached.ThreadsUids))
				return cached.ThreadsUids
			}
		}
	}

	if _, err := c.ImapClient().FolderSelect(folderName, false); nil != err {
		// PHP catches and yields an empty map.
		collection.TotalThreads = intPointer(0)
		return nil
	}

	var result [][]int
	threads, err := c.ImapClient().MessageThread(search, algorithm, true)
	if nil == err {
		for _, item := range threads {
			result = append(result, flattenThread(item))
		}
	}

	if "" != cacheKey {
		if encoded, encodeErr := json.Marshal(map[string]any{"ThreadsUids": result}); nil == encodeErr {
			cacher.Set(cacheKey, string(encoded))
		}
	}

	collection.TotalThreads = intPointer(len(result))
	return result
}

// threadsOldUids is MailClient::ThreadsOldUids(): every thread UID except the
// most recent one (bThreadSort is always false in PHP, so the simple branch).
func (c *Client) threadsOldUids(allThreads [][]int, collection *MessageCollection, cacher Cacher) []int {
	etag := ""
	if nil != collection.FolderInfo {
		etag = collection.FolderInfo.ETag
	}

	cacheKey := ""
	if nil != cacher && cacher.IsInited() {
		cacheKey = "ThreadsOldUids/" + etag + "/N"
		if serialized := cacher.Get(cacheKey); "" != serialized {
			var cached struct{ ThreadsUids []int }
			if nil == json.Unmarshal([]byte(serialized), &cached) && nil != cached.ThreadsUids {
				return cached.ThreadsUids
			}
		}
	}

	var uids []int
	for _, thread := range allThreads {
		if 0 == len(thread) {
			continue
		}
		maxUID := thread[0]
		maxIndex := 0
		for index, uid := range thread {
			if uid > maxUID || (uid == maxUID && index < maxIndex) {
				maxUID = uid
				maxIndex = index
			}
		}
		for index, uid := range thread {
			if index == maxIndex {
				continue
			}
			uids = append(uids, uid)
		}
	}

	if "" != cacheKey {
		if encoded, encodeErr := json.Marshal(map[string]any{"ThreadsUids": uids}); nil == encodeErr {
			cacher.Set(cacheKey, string(encoded))
		}
	}

	return uids
}

// messageListByRequestIndexOrUids is MailClient::MessageListByRequestIndexOrUids().
func (c *Client) messageListByRequestIndexOrUids(collection *MessageCollection, rangeSet *imapclient.SequenceSet, allThreads [][]int, unseenUIDs []int) {
	if nil == rangeSet || 0 == rangeSet.Count() {
		return
	}

	fetchItems := []string{
		imapclient.FetchUID,
		imapclient.FetchRFC822Size,
		imapclient.FetchInternalDate,
		imapclient.FetchFlags,
		imapclient.FetchBodyStructure,
	}
	if c.ImapClient().HasCapability("PREVIEW") {
		fetchItems = append(fetchItems, imapclient.FetchPreview)
	}
	fetchItems = append(fetchItems, c.envelopeOrHeadersRequestString())

	responses, err := c.ImapClient().Fetch(fetchItems, rangeSet.String(), rangeSet.UID)
	if nil != err {
		return
	}

	byKey := map[int]*Message{}
	for _, response := range responses {
		key := response.FetchInt(imapclient.FetchUID)
		message := MessageFromFetchResponse(collection.FolderName, response)
		if nil == message {
			continue
		}
		if 0 < len(allThreads) {
			uid := message.uid
			for _, threadMap := range allThreads {
				if containsInt(threadMap, uid) {
					message.SetThreads(threadMap)
					message.SetThreadUnseen(filterInts(unseenUIDs, func(value int) bool {
						return containsInt(threadMap, value)
					}))
					break
				}
			}
		}
		byKey[key] = message
	}

	// FETCH does not answer in the SequenceSet order, so emit in that order.
	for _, value := range rangeSet.Values() {
		key, ok := value.(int)
		if !ok {
			continue
		}
		if message, ok := byKey[key]; ok {
			collection.Append(message, false)
		}
	}
}

// getFolderNextMessageInformation is MailClient::getFolderNextMessageInformation().
func (c *Client) getFolderNextMessageInformation(folderName string, prevUIDNext, currentUIDNext int) ([]any, error) {
	imap := c.ImapClient()
	if nil == imap.Settings || !imap.Settings.FetchNewMessages ||
		0 == prevUIDNext || prevUIDNext == currentUIDNext || "INBOX" != folderName {
		return nil, nil
	}

	if _, err := imap.FolderExamine(folderName, false); nil != err {
		return nil, err
	}

	responses, err := imap.Fetch([]string{
		imapclient.FetchUID,
		imapclient.FetchFlags,
		imapclient.BuildBodyCustomHeaderRequest([]string{
			"FROM", "SUBJECT", "CONTENT-TYPE",
		}, true),
	}, strconv.Itoa(prevUIDNext)+":*", true)
	if nil != err {
		return nil, err
	}

	newMessages := []any{}
	for _, response := range responses {
		flags := response.FetchValueStrings(imapclient.FetchFlags)
		seen := false
		for _, flag := range flags {
			if "\\seen" == strings.ToLower(imapclient.MUTF7ToUTF8(flag)) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}

		uid := response.FetchInt(imapclient.FetchUID)

		raw := response.GetHeaderFieldsValue()
		headers := mime.NewHeaderCollection(raw, "")
		charset := ""
		if nil != headers {
			charset = headers.ParameterValue(mime.HeaderContentType, mime.ParameterCharset)
			if "" != charset {
				headers.SetParentCharset(charset)
			}
		}

		var from any
		var subject string
		if nil != headers {
			subject = headers.ValueByName(mime.HeaderSubject)
			from = headers.GetAsEmailCollection(mime.HeaderFrom)
		}

		newMessages = append(newMessages, map[string]any{
			"folder":  folderName,
			"uid":     uid,
			"subject": subject,
			"from":    from,
		})
	}

	return newMessages, nil
}

// envelopeOrHeadersRequestString is MailClient::getEnvelopeOrHeadersRequestString().
func (c *Client) envelopeOrHeadersRequestString() string {
	settings := c.ImapClient().Settings
	if nil != settings && settings.MessageAllHeaders {
		return imapclient.FetchBodyHeaderPeek
	}

	headers := []string{
		"MESSAGE-ID", "CONTENT-TYPE", "FROM", "TO", "CC", "BCC", "SENDER", "REPLY-TO",
		"DELIVERED-TO", "IN-REPLY-TO", "REFERENCES", "DATE", "SUBJECT", "X-MSMAIL-PRIORITY",
		"IMPORTANCE", "X-PRIORITY", "X-DRAFT-INFO", "DISPOSITION-NOTIFICATION-TO",
		"X-CONFIRM-READING-TO", "AUTHENTICATION-RESULTS", "X-DKIM-AUTHENTICATION-RESULTS",
		"LIST-UNSUBSCRIBE", "AUTOCRYPT",
	}

	if nil != settings {
		spamHeaders := strings.Split(settings.SpamHeaders, ",")
		if containsString(spamHeaders, "rspamd") {
			headers = append(headers, "X-SPAMD-RESULT")
		}
		if containsString(spamHeaders, "spamassassin") {
			headers = append(headers, "X-SPAM-STATUS", "X-SPAM-FLAG", "X-SPAM-INFO")
		}
		if containsString(spamHeaders, "bogofilter") {
			headers = append(headers, "X-BOGOSITY")
		}

		virusHeaders := strings.Split(settings.VirusHeaders, ",")
		if containsString(virusHeaders, "rspamd") {
			headers = append(headers, "X-VIRUS")
		}
		if containsString(virusHeaders, "clamav") {
			headers = append(headers, "X-VIRUS-SCANNED", "X-VIRUS-STATUS")
		}
	}

	return imapclient.BuildBodyCustomHeaderRequest(headers, true)
}

// The small helpers.

// intOrZero dereferences a nullable int (nil → 0).
func intOrZero(value *int) int {
	if nil == value {
		return 0
	}
	return *value
}

// intPointer returns a pointer to value.
func intPointer(value int) *int { return &value }

// boolDigit returns "1"/"0" for the params hash.
func boolDigit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// flattenThread flattens a Thread item (int or nested []any) into a UID list.
func flattenThread(item any) []int {
	var result []int
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case int:
			result = append(result, typed)
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(item)
	return result
}

// intsToAny converts an []int to the []any a SequenceSet expects.
func intsToAny(values []int) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

// rangeDesc is PHP's range($end, $start) for end >= start (descending).
func rangeDesc(end, start int) []int {
	if end < start {
		return nil
	}
	result := make([]int, 0, end-start+1)
	for value := end; value >= start; value-- {
		result = append(result, value)
	}
	return result
}

// arraySlice is PHP's array_slice($values, $offset, $limit).
func arraySlice(values []int, offset, limit int) []int {
	if offset >= len(values) {
		return nil
	}
	if 0 > offset {
		offset = len(values) + offset
		if 0 > offset {
			offset = 0
		}
	}
	end := len(values)
	if 0 <= limit && offset+limit < end {
		end = offset + limit
	}
	return values[offset:end]
}

// arrayDiff is array_diff() for int slices (keeps order, first occurrences).
func arrayDiff(values, removes []int) []int {
	removeSet := make(map[int]struct{}, len(removes))
	for _, value := range removes {
		removeSet[value] = struct{}{}
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		if _, ok := removeSet[value]; ok {
			continue
		}
		result = append(result, value)
	}
	return result
}

// filterInts is array_filter() over ints.
func filterInts(values []int, keep func(int) bool) []int {
	result := make([]int, 0, len(values))
	for _, value := range values {
		if keep(value) {
			result = append(result, value)
		}
	}
	return result
}

// containsInt reports whether values contains needle.
func containsInt(values []int, needle int) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// intersectInts reports whether the two int slices share a value.
func intersectInts(left, right []int) bool {
	for _, value := range left {
		if containsInt(right, value) {
			return true
		}
	}
	return false
}

// maxInt is PHP's max() for two ints.
func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// minInt is PHP's min() for two ints.
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
