package imapclient

// The folder models of MailSo\Imap: Folder, FolderCollection,
// FolderInformation, NamespaceResult and Responses\ACL.
//
// The JSON field names (and the null-versus-absent distinction) are part of the
// contract with the frontend, so the port keeps every key of jsonSerialize(),
// including the ones that are always null on a server without the matching
// capability.

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"strconv"
	"strings"
)

// The RFC 5464 metadata keys of MailSo\Imap\Enumerations\MetadataKeys.
const (
	// MetadataAdminShared is the server wide admin area.
	MetadataAdminShared = "/shared/admin"
	// MetadataComment is the mailbox comment.
	MetadataComment = "/private/comment"
	// MetadataCommentShared is the shared mailbox comment.
	MetadataCommentShared = "/shared/comment"
	// MetadataSpecialUse is the RFC 6154 special-use marker.
	MetadataSpecialUse = "/private/specialuse"
	// MetadataKolabCtype/KolabCtypeShared hold the Kolab folder type.
	MetadataKolabCtype       = "/private/vendor/kolab/folder-type"
	MetadataKolabCtypeShared = "/shared/vendor/kolab/folder-type"
	// MetadataKolabColor/KolabColorShared hold the Kolab folder colour.
	MetadataKolabColor       = "/private/vendor/kolab/color"
	MetadataKolabColorShared = "/shared/vendor/kolab/color"
	// MetadataKolabName/KolabNameShared hold the Kolab display name.
	MetadataKolabName       = "/private/vendor/kolab/displayname"
	MetadataKolabNameShared = "/shared/vendor/kolab/displayname"
	// MetadataKolabUIDShared holds the Kolab unique id.
	MetadataKolabUIDShared = "/shared/vendor/kolab/uniqueid"
	// MetadataCyrusUIDShared holds the Cyrus unique id.
	MetadataCyrusUIDShared = "/shared/vendor/cmu/cyrus-imapd/uniqueid"
)

// folderStatus is MailSo\Imap\Traits\Status: the STATUS/SELECT counters of a
// mailbox. The values are pointers because PHP distinguishes "not reported"
// (null, which json_encode() writes as null) from zero.
type folderStatus struct {
	FullName      string
	HasStatus     bool
	Messages      *int
	Recent        *int
	UIDNext       *int
	UIDValidity   *int
	Unseen        *int
	HighestModSeq *int
	AppendLimit   *int
	MailboxID     string
	Size          *int
	ETag          string
}

// setStatusFromResponse is Traits\Status::setStatusFromResponse().
func (s *folderStatus) setStatusFromResponse(reply response) bool {
	result := false

	switch reply.kind {
	case respStatus:
		// OK untagged responses carry the optional [CODE args] block.
		if "" == reply.code || "PERMANENTFLAGS" == reply.code {
			return false
		}
		if 0 == len(reply.codeArgs) {
			return false
		}
		return s.setStatusItem(reply.code, token{kind: tokenAtom, text: reply.codeArgs[0]})

	case respData:
		if 3 > len(reply.values) {
			return false
		}

		// STATUS command: "* STATUS "name" (MESSAGES 5 …)".
		if "STATUS" == reply.name && tokenList == reply.values[2].kind {
			items := reply.values[2].list
			for index := 0; index+1 < len(items); index += 2 {
				result = s.setStatusItem(tokenText(items[index]), items[index+1]) || result
			}
			s.HasStatus = result
			return result
		}

		// SELECT/EXAMINE: "* 5 EXISTS".
		if number, isNumber := integerToken(reply.values[0]); isNumber {
			name := tokenText(reply.values[1])
			// UNSEEN is deprecated in IMAP4rev2; the caller resets it.
			if "UNSEEN" != name {
				result = s.setStatusItem(name, token{kind: tokenAtom, text: strconv.Itoa(number)})
			}
		}
	}

	return result
}

// setStatusItem is Traits\Status::setStatusItem().
func (s *folderStatus) setStatusItem(name string, value token) bool {
	switch name {
	case "EXISTS":
		name = "MESSAGES"
	case "X-GUID":
		name = "MAILBOXID"
	}

	switch name {
	case "MESSAGES":
		s.Messages = intPointer(integerValue(value))
	case "RECENT":
		s.Recent = intPointer(integerValue(value))
	case "UIDNEXT":
		s.UIDNext = intPointer(integerValue(value))
	case "UIDVALIDITY":
		s.UIDValidity = intPointer(integerValue(value))
	case "UNSEEN":
		s.Unseen = intPointer(integerValue(value))
	case "HIGHESTMODSEQ":
		s.HighestModSeq = intPointer(integerValue(value))
	case "APPENDLIMIT":
		s.AppendLimit = intPointer(integerValue(value))
	case "SIZE":
		s.Size = intPointer(integerValue(value))
	case "MAILBOXID":
		if tokenList == value.kind {
			if 0 == len(value.list) {
				return false
			}
			value = value.list[0]
		}
		text, ok := value.textValue()
		if !ok {
			// Cyrus sends an invalid MAILBOXID; PHP logs and drops it.
			return false
		}
		s.MailboxID = base64.StdEncoding.EncodeToString([]byte(text))
		return true
	default:
		return false
	}

	return true
}

// generateETag is Traits\Status::generateETag().
func (s *folderStatus) generateETag(client *Client) {
	if !s.HasStatus || nil == s.Messages || nil == s.UIDNext {
		return
	}
	s.ETag = md5Hex("FolderHash/" + strings.Join([]string{
		s.FullName,
		strconv.Itoa(*s.Messages),
		strconv.Itoa(*s.UIDNext),
		pointerString(s.UIDValidity),
		pointerString(s.Unseen),
		pointerString(s.HighestModSeq),
		client.Hash(),
	}, "-"))
}

// pointerString is PHP's (string) cast of a nullable int, where null is "".
func pointerString(value *int) string {
	if nil == value {
		return ""
	}
	return strconv.Itoa(*value)
}

// intPointer returns a pointer to value.
func intPointer(value int) *int { return &value }

// pointerValue dereferences value, reporting 0 for a nil pointer.
func pointerValue(value *int) int {
	if nil == value {
		return 0
	}
	return *value
}

// integerValue is PHP's (int) cast of a token.
func integerValue(value token) int {
	if number, ok := integerToken(value); ok {
		return number
	}
	return 0
}

// integerToken parses a token as an integer, like is_numeric()/(int).
func integerToken(value token) (int, bool) {
	text, ok := value.textValue()
	if !ok {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimSpace(text))
	if nil != err {
		return 0, false
	}
	return number, true
}

// tokenText is the string value of a token, "" for NIL and lists.
func tokenText(value token) string {
	text, _ := value.textValue()
	return text
}

// Folder is MailSo\Imap\Folder.
type Folder struct {
	folderStatus
	delimiter  string
	attributes []string
	metadata   map[string]string
}

// NewFolder is Folder::__construct(). An empty full name yields nil, which is
// what the PHP ValueError turns into at the call sites that catch it.
func NewFolder(fullName string, delimiter string, attributes ...string) *Folder {
	if "" == fullName {
		return nil
	}

	folder := &Folder{delimiter: delimiter, metadata: map[string]string{}}
	folder.FullName = fullName
	folder.setAttributes(attributes)
	return folder
}

// setAttributes is Folder::setAttributes(): the flags are lower cased.
func (f *Folder) setAttributes(attributes []string) {
	f.attributes = make([]string, len(attributes))
	for index, attribute := range attributes {
		f.attributes[index] = strings.ToLower(attribute)
	}
}

// setDelimiter is Folder::setDelimiter().
func (f *Folder) setDelimiter(delimiter string) { f.delimiter = delimiter }

// SetSubscribed is Folder::setSubscribed().
func (f *Folder) SetSubscribed() {
	if f.IsSubscribed() {
		return
	}
	f.attributes = append(f.attributes, `\subscribed`)
}

// Name is Folder::Name(): the last path element.
func (f *Folder) Name() string {
	if "" == f.delimiter {
		return f.FullName
	}
	parts := strings.Split(f.FullName, f.delimiter)
	return parts[len(parts)-1]
}

// Delimiter is Folder::Delimiter().
func (f *Folder) Delimiter() string { return f.delimiter }

// Attributes returns the (lower case) attributes of the LIST response.
func (f *Folder) Attributes() []string { return f.attributes }

// Selectable is Folder::Selectable().
func (f *Folder) Selectable() bool {
	return !containsExact(f.attributes, `\noselect`) && !containsExact(f.attributes, `\nonexistent`)
}

// IsSubscribed is Folder::IsSubscribed().
func (f *Folder) IsSubscribed() bool { return containsExact(f.attributes, `\subscribed`) }

// IsInbox is Folder::IsInbox().
func (f *Folder) IsInbox() bool {
	return "INBOX" == strings.ToUpper(f.FullName) || containsExact(f.attributes, `\inbox`)
}

// SetMetadata is Folder::SetMetadata().
func (f *Folder) SetMetadata(name, value string) {
	if nil == f.metadata {
		f.metadata = map[string]string{}
	}
	f.metadata[name] = value
}

// SetAllMetadata is Folder::SetAllMetadata().
func (f *Folder) SetAllMetadata(metadata map[string]string) {
	if nil == metadata {
		metadata = map[string]string{}
	}
	f.metadata = metadata
}

// GetMetadata is Folder::GetMetadata().
func (f *Folder) GetMetadata(name string) (string, bool) {
	value, ok := f.metadata[name]
	return value, ok
}

// Metadata is Folder::Metadata().
func (f *Folder) Metadata() map[string]string { return f.metadata }

// roleKeywords is the fixed priority list of Folder::Role().
var roleKeywords = []string{
	`\inbox`,
	`\all`, // \allmail
	`\archive`,
	`\drafts`,
	`\flagged`, // \starred
	`\important`,
	`\junk`, // \spam
	`\sent`, // \sentmail
	`\trash`, // \bin
}

// Role is Folder::Role(): the JMAP (RFC 8621) role of the mailbox.
func (f *Folder) Role() any {
	role := strings.ToLower(f.metadata[MetadataSpecialUse])
	if "" == role {
		for _, keyword := range roleKeywords {
			if containsExact(f.attributes, keyword) {
				role = keyword
				break
			}
		}
		if "" == role && "INBOX" == strings.ToUpper(f.FullName) {
			return "inbox"
		}
	}
	if "" == role {
		return nil
	}
	return strings.TrimLeft(role, `\`)
}

// JSONSerialize is Folder::jsonSerialize().
func (f *Folder) JSONSerialize() any {
	attributes := f.attributes
	if nil == attributes {
		attributes = []string{}
	}
	metadata := f.metadata
	if nil == metadata {
		metadata = map[string]string{}
	}

	result := map[string]any{
		"@Object":      "Object/Folder",
		"name":         f.Name(),
		"fullName":     f.FullName,
		"delimiter":    f.delimiter,
		"attributes":   attributes,
		"metadata":     metadata,
		"uidNext":      f.UIDNext,
		"totalEmails":  f.Messages,
		"unreadEmails": f.Unseen,
		"id":           nullableString(f.MailboxID),
		"size":         f.Size,
		"role":         f.Role(),
	}

	if "" != f.ETag {
		result["etag"] = f.ETag
	}

	return result
}

// nullableString reports a stored string as value-or-nil, the way PHP's
// json_encode() writes an unset property.
func nullableString(value string) any {
	if "" == value {
		return nil
	}
	return value
}

// FolderCollection is MailSo\Imap\FolderCollection: the folders of a LIST,
// keyed by full name and iterated in response order.
type FolderCollection struct {
	names []string
	items map[string]*Folder
}

// NewFolderCollection returns an empty collection.
func NewFolderCollection() *FolderCollection {
	return &FolderCollection{items: map[string]*Folder{}}
}

// Count is Collection::count().
func (c *FolderCollection) Count() int {
	if nil == c {
		return 0
	}
	return len(c.names)
}

// Get returns the folder of a full name.
func (c *FolderCollection) Get(fullName string) (*Folder, bool) {
	if nil == c {
		return nil, false
	}
	folder, ok := c.items[fullName]
	return folder, ok
}

// Set stores a folder under its full name, appending it in response order.
func (c *FolderCollection) Set(folder *Folder) {
	if nil == c || nil == folder {
		return
	}
	if nil == c.items {
		c.items = map[string]*Folder{}
	}
	if _, exists := c.items[folder.FullName]; !exists {
		c.names = append(c.names, folder.FullName)
	}
	c.items[folder.FullName] = folder
}

// Iterate returns the folders in response order.
func (c *FolderCollection) Iterate() []*Folder {
	if nil == c {
		return nil
	}
	result := make([]*Folder, 0, len(c.names))
	for _, name := range c.names {
		result = append(result, c.items[name])
	}
	return result
}

// JSONSerialize is FolderCollection::jsonSerialize().
func (c *FolderCollection) JSONSerialize() any {
	folders := make([]any, 0, c.Count())
	for _, folder := range c.Iterate() {
		folders = append(folders, folder)
	}

	return map[string]any{
		"@Object":     "Collection/FolderCollection",
		"@Collection": folders,
	}
}

// SortedNames returns the full names in response order; it exists for the
// handler that has to merge several collections.
func (c *FolderCollection) SortedNames() []string {
	if nil == c {
		return nil
	}
	return append([]string(nil), c.names...)
}

// NamespaceEntry is one entry of NamespaceResult::{aPersonal,aOtherUsers,aShared}.
type NamespaceEntry struct {
	Prefix    string   `json:"prefix"`
	Delimiter string   `json:"delimiter"`
	Extension []string `json:"extension"`
}

// NamespaceResult is MailSo\Imap\NamespaceResult (RFC 2342).
type NamespaceResult struct {
	Personal   []NamespaceEntry `json:"personal"`
	OtherUsers []NamespaceEntry `json:"users"`
	Shared     []NamespaceEntry `json:"shared"`
}

// GetPersonalPrefix is NamespaceResult::GetPersonalPrefix().
func (n *NamespaceResult) GetPersonalPrefix() string {
	if nil == n || 0 == len(n.Personal) {
		return ""
	}

	prefix := n.Personal[0].Prefix
	delimiter := n.Personal[0].Delimiter
	if "INBOX"+delimiter == strings.ToUpper(prefix)[:minInt(6, len(prefix))] {
		prefix = "INBOX" + delimiter + safeSubstring(prefix, 6)
	}
	return prefix
}

// JSONSerialize is NamespaceResult::jsonSerialize().
func (n *NamespaceResult) JSONSerialize() any {
	personal, users, shared := []NamespaceEntry{}, []NamespaceEntry{}, []NamespaceEntry{}
	if nil != n {
		if nil != n.Personal {
			personal = n.Personal
		}
		if nil != n.OtherUsers {
			users = n.OtherUsers
		}
		if nil != n.Shared {
			shared = n.Shared
		}
	}

	return map[string]any{
		"@Object":  "Object/Namespaces",
		"personal": personal,
		"users":    users,
		"shared":   shared,
	}
}

// safeSubstring is PHP's substr($value, $from) for a non-negative offset.
func safeSubstring(value string, from int) string {
	if from >= len(value) {
		return ""
	}
	return value[from:]
}

// minInt is PHP's min() for two ints.
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// newNamespaceResult is NamespaceResult::__construct().
func newNamespaceResult(reply response) *NamespaceResult {
	result := &NamespaceResult{}

	sections := []struct {
		index int
		into  *[]NamespaceEntry
	}{
		{1, &result.Personal},
		{2, &result.OtherUsers},
		{3, &result.Shared},
	}

	for _, section := range sections {
		if section.index >= len(reply.values) {
			continue
		}
		entries := reply.values[section.index]
		if tokenList != entries.kind {
			continue
		}
		for _, entry := range entries.list {
			if tokenList != entry.kind || 2 > len(entry.list) {
				continue
			}
			// json_encode() writes an empty PHP array as [], not null.
			extension := tokenTexts(entry.list[2:])
			if nil == extension {
				extension = []string{}
			}
			*section.into = append(*section.into, NamespaceEntry{
				Prefix:    tokenText(entry.list[0]),
				Delimiter: tokenText(entry.list[1]),
				Extension: extension,
			})
		}
	}

	return result
}

// aclNames maps the FolderACL constant names onto their right letter.
var aclNames = map[string]string{
	"ADMINISTER": "a",
	"LOOKUP":     "l",
	"READ":       "r",
	"SEEN":       "s",
	"WRITE":      "w",
	"INSERT":     "i",
	"POST":       "p",
	"CREATE":     "k",
	"DELETE":     "x",
	"DELETED":    "t",
	"EXPUNGE":    "e",
}

// ACL is MailSo\Imap\Responses\ACL.
type ACL struct {
	Identifier string
	Rights     string
	Mine       bool
}

// HasRight is ACL::hasRight(): the right name is mapped through the FolderACL
// constants; an unknown name is looked up literally.
func (a ACL) HasRight(right string) bool {
	if letter, ok := aclNames[strings.ToUpper(right)]; ok {
		right = letter
	}
	return strings.Contains(a.Rights, right)
}

// IdentifierValue is ACL::identifier().
func (a ACL) IdentifierValue() string { return a.Identifier }

// JSONSerialize is ACL::jsonSerialize().
func (a ACL) JSONSerialize() any {
	return map[string]any{
		"@Object":    "Object/FolderACLRights",
		"identifier": a.Identifier,
		"rights":     a.Rights,
		"mine":       a.Mine,
	}
}

// MetadataEntry is one key/value pair of a SETMETADATA command; a nil Value
// removes the entry ("NIL").
type MetadataEntry struct {
	Key   string
	Value *string
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}
