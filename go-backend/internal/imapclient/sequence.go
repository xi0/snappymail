package imapclient

// MailSo\Imap\FolderInformation and MailSo\Imap\SequenceSet.

import (
	"regexp"
	"strconv"
	"strings"
)

// FolderInformation is MailSo\Imap\FolderInformation: the SELECT/EXAMINE (or
// STATUS) result of one mailbox.
type FolderInformation struct {
	folderStatus

	// IsWritable is the "IsWritable" property: false after EXAMINE or a
	// [READ-ONLY] response.
	IsWritable bool
	// Flags are the flags the server reported with SELECT/EXAMINE; PHP keeps
	// them but does not use them because they need not be permanent.
	Flags []string
	// PermanentFlags is the [PERMANENTFLAGS] list; empty after EXAMINE.
	PermanentFlags []string
}

// NewFolderInformation is FolderInformation::__construct().
func NewFolderInformation(fullName string, writable bool) *FolderInformation {
	return &FolderInformation{IsWritable: writable, folderStatus: folderStatus{FullName: fullName}}
}

// IsFlagSupported is FolderInformation::IsFlagSupported().
func (f *FolderInformation) IsFlagSupported(flag string) bool {
	return containsExact(f.PermanentFlags, `\*`) || containsExact(f.PermanentFlags, flag)
}

// JSONSerialize is FolderInformation::jsonSerialize().
func (f *FolderInformation) JSONSerialize() any {
	result := map[string]any{
		"id":          nullableString(f.MailboxID),
		"name":        f.FullName,
		"uidNext":     f.UIDNext,
		"uidValidity": f.UIDValidity,
	}

	if nil != f.Messages {
		result["totalEmails"] = f.Messages
		result["unreadEmails"] = f.Unseen
	}
	if nil != f.HighestModSeq {
		result["highestModSeq"] = f.HighestModSeq
	}
	if nil != f.AppendLimit {
		result["appendLimit"] = f.AppendLimit
	}
	if nil != f.Size {
		result["size"] = f.Size
	}
	if "" != f.ETag {
		result["etag"] = f.ETag
	}
	if 0 < len(f.PermanentFlags) {
		result["permanentFlags"] = f.PermanentFlags
	}

	return result
}

// sequenceItemPattern is the RFC 3501 §2.3.1.1 sequence-set item accepted by
// SequenceSet::__construct(); everything else is discarded, which prevents IMAP
// command injection through client supplied UID values.
var sequenceItemPattern = regexp.MustCompile(`^(\d+|\*)(:(\d+|\*))?$`)

// SequenceSet is MailSo\Imap\SequenceSet: a validated list of message numbers or
// UIDs which renders as the compressed "1:3,5" form.
type SequenceSet struct {
	// UID reports whether the numbers are unique ids (the default).
	UID bool
	// data holds int values for single numbers and strings for "*" or ranges.
	data []any
}

// NewSequenceSet is SequenceSet::__construct(): items may be a []any, a []string,
// a string or an int.
func NewSequenceSet(items any, uid bool) *SequenceSet {
	set := &SequenceSet{UID: uid}

	switch typed := items.(type) {
	case []any:
		for _, item := range typed {
			set.appendScalar(item)
		}
	case []string:
		for _, item := range typed {
			set.appendScalar(item)
		}
	case string:
		set.appendScalar(typed)
	case int:
		set.appendScalar(strconv.Itoa(typed))
	}

	return set
}

// appendScalar is the array_map() body of SequenceSet::__construct().
func (s *SequenceSet) appendScalar(item any) {
	var text string
	switch typed := item.(type) {
	case string:
		text = typed
	case int:
		text = strconv.Itoa(typed)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case float64:
		text = strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if !typed {
			return
		}
		text = "1"
	default:
		return
	}

	if !sequenceItemPattern.MatchString(text) {
		return
	}
	if "*" == text || strings.Contains(text, ":") {
		s.data = append(s.data, text)
		return
	}
	if number, err := strconv.Atoi(text); nil == err {
		s.data = append(s.data, number)
	}
}

// Count is SequenceSet::count().
func (s *SequenceSet) Count() int {
	if nil == s {
		return 0
	}
	return len(s.data)
}

// Contains is SequenceSet::contains().
func (s *SequenceSet) Contains(value int) bool {
	if nil == s {
		return false
	}
	for _, item := range s.data {
		if number, ok := item.(int); ok && number == value {
			return true
		}
	}
	return false
}

// Values is SequenceSet::getArrayCopy().
func (s *SequenceSet) Values() []any {
	if nil == s {
		return nil
	}
	return append([]any(nil), s.data...)
}

// String is SequenceSet::__toString(): consecutive numbers are collapsed into
// ranges.
func (s *SequenceSet) String() string {
	if nil == s || 0 == len(s.data) {
		return ""
	}

	var result []string
	var start, previous any

	flush := func() {
		if nil == start {
			return
		}
		if start == previous {
			result = append(result, itemString(start))
			return
		}
		result = append(result, itemString(start)+":"+itemString(previous))
	}

	for _, item := range s.data {
		if text, ok := item.(string); ok && strings.Contains(text, ":") {
			result = append(result, text)
			continue
		}

		if nil == start || nil == previous {
			start = item
			previous = item
			continue
		}

		if number, ok := previous.(int); ok {
			if next, ok := item.(int); ok && next == number+1 {
				previous = item
				continue
			}
		}

		flush()
		start = item
		previous = item
	}

	flush()

	return strings.Join(result, ",")
}

// itemString is PHP's string concatenation of an int or string item.
func itemString(item any) string {
	if number, ok := item.(int); ok {
		return strconv.Itoa(number)
	}
	if text, ok := item.(string); ok {
		return text
	}
	return ""
}
