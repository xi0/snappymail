package imapclient

// The folder command layer of MailSo\Imap\Commands\Folders (LIST/LSUB/STATUS/
// SELECT/EXAMINE/CREATE/DELETE/RENAME/SUBSCRIBE/EXPUNGE), Commands\Metadata,
// Commands\ACL, Commands\Quota and the NAMESPACE extension.
//
// The command lines are built with paramLine(), which reproduces
// ImapClient::prepareParamLine(): a string is appended verbatim after a space
// and a nested array becomes a parenthesized group.

import (
	"errors"
	"io"
	"strconv"
	"strings"
)

// The framing errors of the folder commands.
var (
	// ErrNonEmptyFolder is MailSo\Mail\Exceptions\NonEmptyFolder.
	ErrNonEmptyFolder = errors.New("imapclient: folder is not empty")
	// ErrEmptyFolderName is the ValueError of an empty folder name.
	ErrEmptyFolderName = errors.New("imapclient: empty folder name")
)

// The store actions of MailSo\Imap\Enumerations\StoreAction.
const (
	// StoreAddFlags is StoreAction::ADD_FLAGS.
	StoreAddFlags = "+FLAGS"
	// StoreAddFlagsSilent is StoreAction::ADD_FLAGS_SILENT.
	StoreAddFlagsSilent = "+FLAGS.SILENT"
	// StoreRemoveFlags is StoreAction::REMOVE_FLAGS.
	StoreRemoveFlags = "-FLAGS"
	// StoreRemoveFlagsSilent is StoreAction::REMOVE_FLAGS_SILENT.
	StoreRemoveFlagsSilent = "-FLAGS.SILENT"
)

// param is one argument of a command line: a plain string is sent verbatim and
// a group becomes a parenthesized list.
type param struct {
	text  string
	group []param
}

// paramLine is ImapClient::prepareParamLine().
func paramLine(params []param) string {
	var builder strings.Builder
	for _, item := range params {
		if nil != item.group {
			builder.WriteString(" (")
			builder.WriteString(strings.TrimSpace(paramLine(item.group)))
			builder.WriteString(")")
			continue
		}
		builder.WriteString(" ")
		builder.WriteString(item.text)
	}
	return builder.String()
}

// commandLine sends "<tag> <line>" and reads the reply up to the tag.
func (c *Client) commandLine(line string) ([]response, error) {
	if nil == c.conn {
		return nil, errNotConnected
	}

	tag := c.nextTag()
	if err := c.writeLine(tag + " " + line); nil != err {
		return nil, err
	}
	return c.readReply(tag)
}

// sendParams sends "<name><params>" and reads the reply up to the tag.
func (c *Client) sendParams(name string, params []param) ([]response, error) {
	return c.commandLine(name + paramLine(params))
}

// Hash is ImapClient::Hash(): md5("ImapClientHash/" + username + "@" + host +
// ":" + port). It feeds the folder ETags.
func (c *Client) Hash() string {
	host, port := "", 0
	if nil != c.Settings {
		host, port = c.Settings.Host, c.Settings.Port
	}
	return md5Hex("ImapClientHash/" + c.username + "@" + host + ":" + strconv.Itoa(port))
}

// IsSelected is ImapClient::IsSelected().
func (c *Client) IsSelected() bool { return c.IsLoggedIn() && nil != c.current }

// CurrentFolder returns the selected mailbox information, or nil.
func (c *Client) CurrentFolder() *FolderInformation { return c.current }

// EscapeFolderName is ImapClient::EscapeFolderName().
func (c *Client) EscapeFolderName(name string) string {
	if c.utf8 {
		return quoteString(name)
	}
	return quoteString(UTF8ToMUTF7(name))
}

// toUTF8 is ImapClient::toUTF8().
func (c *Client) toUTF8(value string) string {
	if c.utf8 {
		return value
	}
	return MUTF7ToUTF8(value)
}

// FolderStatusItems is Folders::FolderStatusItems(): the STATUS items the server
// can report with the current capability set.
func (c *Client) FolderStatusItems() []string {
	items := []string{"MESSAGES", "UNSEEN", "UIDNEXT", "UIDVALIDITY"}
	// RFC 4551 / RFC 5162
	if c.HasCapability("CONDSTORE") || c.HasCapability("QRESYNC") {
		items = append(items, "HIGHESTMODSEQ")
	}
	// RFC 7889
	if c.HasCapability("APPENDLIMIT") {
		items = append(items, "APPENDLIMIT")
	}
	// RFC 8438
	if c.HasCapability("STATUS=SIZE") {
		items = append(items, "SIZE")
	}
	// RFC 8474
	if c.HasCapability("OBJECTID") {
		items = append(items, "MAILBOXID")
	}
	return items
}

// FolderStatus is Folders::FolderStatus(): the STATUS counters of a mailbox,
// optionally reselecting it afterwards.
func (c *Client) FolderStatus(name string, selectFolder bool) (*FolderInformation, error) {
	current := c.current
	reselect := false
	writable := false

	if nil != current && name == current.FullName {
		if current.HasStatus {
			return current, nil
		}
		// A STATUS command after SELECT/EXAMINE on the same mailbox is a
		// long standing IMAP CLIENTBUG, so the mailbox is unselected first.
		writable = current.IsWritable
		reselect = true
		c.FolderUnselect()
	}

	info := NewFolderInformation(name, false)
	if err := c.folderStatus(info); nil != err {
		return nil, err
	}

	if reselect || selectFolder {
		// Don't use FolderExamine, else PERMANENTFLAGS is empty in Dovecot.
		selected, err := c.selectOrExamineFolder(name, selectFolder || writable, false)
		if nil != err {
			return nil, err
		}

		selected.Messages = intPointer(maxInt(0, pointerValue(selected.Messages), pointerValue(info.Messages)))
		// SELECT/EXAMINE then UNSEEN is the sequence number of the first unseen
		// message, which IMAP4rev2 deprecated; the count is what the UI wants.
		selected.Unseen = intPointer(maxInt(0, pointerValue(info.Unseen)))
		selected.UIDNext = intPointer(maxInt(0, pointerValue(selected.UIDNext), pointerValue(info.UIDNext)))
		selected.UIDValidity = intPointer(maxInt(0, pointerValue(selected.UIDValidity), pointerValue(info.UIDValidity)))
		selected.HighestModSeq = intPointer(maxInt(0, pointerValue(info.HighestModSeq)))
		selected.AppendLimit = intPointer(maxInt(0, pointerValue(selected.AppendLimit), pointerValue(info.AppendLimit)))
		if "" == selected.MailboxID {
			selected.MailboxID = info.MailboxID
		}
		selected.HasStatus = info.HasStatus
		info = selected
	}

	info.generateETag(c)
	return info, nil
}

// FolderStatusAndSelect is Folders::FolderStatusAndSelect().
func (c *Client) FolderStatusAndSelect(name string) (*FolderInformation, error) {
	return c.FolderStatus(name, true)
}

// folderStatus runs the STATUS command and feeds the untagged responses into
// info.
func (c *Client) folderStatus(info *FolderInformation) error {
	items := make([]param, 0, 6)
	for _, item := range c.FolderStatusItems() {
		items = append(items, param{text: item})
	}

	replies, err := c.sendParams("STATUS", []param{
		{text: c.EscapeFolderName(info.FullName)},
		{group: items},
	})
	if nil != err {
		return protocolFailure(err)
	}

	for _, reply := range replies {
		if respData == reply.kind && "STATUS" == reply.name {
			info.setStatusFromResponse(reply)
		}
	}

	if _, err := expectOK(replies); nil != err {
		return err
	}
	return nil
}

// FolderSelect is Folders::FolderSelect().
func (c *Client) FolderSelect(name string, forceReselect bool) (*FolderInformation, error) {
	return c.selectOrExamineFolder(name, true, forceReselect)
}

// FolderExamine is Folders::FolderExamine().
func (c *Client) FolderExamine(name string, forceReselect bool) (*FolderInformation, error) {
	writable := false
	if nil != c.Settings {
		writable = c.Settings.ForceSelect
	}
	return c.selectOrExamineFolder(name, writable, forceReselect)
}

// selectOrExamineFolder is Folders::selectOrExamineFolder().
func (c *Client) selectOrExamineFolder(name string, writable, forceReselect bool) (*FolderInformation, error) {
	if !forceReselect && nil != c.current && name == c.current.FullName &&
		(writable == c.current.IsWritable || c.current.IsWritable) {
		return c.current, nil
	}

	if "" == strings.TrimSpace(name) {
		return nil, protocolFailure(ErrEmptyFolderName)
	}

	result := NewFolderInformation(name, writable)
	command := "EXAMINE"
	if writable {
		command = "SELECT"
	}

	replies, err := c.sendParams(command, []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		return nil, protocolFailure(err)
	}

	for _, reply := range replies {
		if "*" != reply.tag {
			continue
		}

		if result.setStatusFromResponse(reply) {
			continue
		}

		// OK untagged responses.
		if respStatus == reply.kind {
			switch reply.code {
			case "PERMANENTFLAGS":
				result.PermanentFlags = utf7ToUTF8All(reply.codeArgs)
			case "READ-ONLY":
				result.IsWritable = false
			case "READ-WRITE":
				result.IsWritable = true
			}
			continue
		}

		// SELECT/EXAMINE deliver the message flags here; PHP deliberately does
		// not use them because they need not be permanent.
	}

	// IMAP4rev2 deprecated UNSEEN on SELECT/EXAMINE.
	result.Unseen = nil
	c.current = result

	if _, err := expectOK(replies); nil != err {
		c.current = nil
		return nil, err
	}

	return result, nil
}

// FolderCreate is Folders::FolderCreate().
func (c *Client) FolderCreate(name string, subscribe bool) error {
	replies, err := c.sendParams("CREATE", []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		return protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return err
	}

	if subscribe {
		return c.FolderSubscribe(name)
	}
	return nil
}

// FolderDelete is Folders::FolderDelete().
func (c *Client) FolderDelete(name string) error {
	if "" == name || "INBOX" == name {
		return protocolFailure(ErrEmptyFolderName)
	}

	var (
		info *FolderInformation
		err  error
	)
	if c.HasCapability("IMAP4rev2") {
		info, err = c.FolderExamine(name, false)
	} else {
		info, err = c.FolderStatus(name, false)
	}
	if nil != err {
		return err
	}
	if 0 < pointerValue(info.Messages) {
		return ErrNonEmptyFolder
	}

	if err := c.FolderUnsubscribe(name); nil != err {
		return err
	}
	c.FolderUnselect()

	replies, err := c.sendParams("DELETE", []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		return protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return err
	}

	// The rename round trip works around a Dovecot issue
	// (https://github.com/the-djmaze/snappymail/issues/124); a failure is only
	// logged, exactly like the PHP try/catch.
	if renameErr := c.FolderRename(name, name+"-dummy"); nil == renameErr {
		if renameErr := c.FolderRename(name+"-dummy", name); nil != renameErr {
			c.logWarning("FolderDelete(%s) rename back failed: %v", name, renameErr)
		}
	}

	return nil
}

// FolderSubscribe is Folders::FolderSubscribe().
func (c *Client) FolderSubscribe(name string) error {
	replies, err := c.sendParams("SUBSCRIBE", []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderUnsubscribe is Folders::FolderUnsubscribe().
func (c *Client) FolderUnsubscribe(name string) error {
	replies, err := c.sendParams("UNSUBSCRIBE", []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderRename is Folders::FolderRename().
func (c *Client) FolderRename(oldName, newName string) error {
	replies, err := c.sendParams("RENAME", []param{
		{text: c.EscapeFolderName(oldName)},
		{text: c.EscapeFolderName(newName)},
	})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderClear is Folders::FolderClear(): mark every message \Deleted and expunge.
func (c *Client) FolderClear(name string) error {
	info, err := c.FolderSelect(name, false)
	if nil != err {
		return err
	}
	if 0 >= pointerValue(info.Messages) {
		return nil
	}

	if err := c.MessageStoreFlag("1:*", false, []string{`\Deleted`}, StoreAddFlagsSilent); nil != err {
		return err
	}
	return c.FolderExpunge()
}

// FolderExpunge is Folders::FolderExpunge(): permanently remove every message
// that carries \Deleted from the selected mailbox.
func (c *Client) FolderExpunge() error {
	return c.FolderExpungeRange(nil)
}

// FolderExpungeRange is Folders::FolderExpunge($oUidRange): an RFC 4315
// "UID EXPUNGE <range>" when a UID range is given and the server supports
// UIDPLUS, else a plain EXPUNGE.
func (c *Client) FolderExpungeRange(uidRange *SequenceSet) error {
	command := "EXPUNGE"
	var args []param

	if nil != uidRange && 0 < uidRange.Count() && uidRange.UID && c.HasCapability("UIDPLUS") {
		command = "UID EXPUNGE"
		args = append(args, param{text: uidRange.String()})
	}

	replies, err := c.sendParams(command, args)
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderUnselect is Folders::FolderUnselect().
func (c *Client) FolderUnselect() {
	if c.IsSelected() {
		if c.HasCapability("UNSELECT") {
			if replies, err := c.sendParams("UNSELECT", nil); nil != err {
				c.logWarning("UNSELECT failed: %v", err)
			} else if _, err := expectOK(replies); nil != err {
				c.logWarning("UNSELECT failed: %v", err)
			}
		} else if replies, err := c.sendParams("SELECT", []param{{text: `""`}}); nil == err {
			_, _ = expectOK(replies)
		}
	}
	c.current = nil
}

// FolderHierarchyDelimiter is Folders::FolderHierarchyDelimiter(). The boolean
// result is false when the server answered NIL, which PHP reports as null.
func (c *Client) FolderHierarchyDelimiter(name string) (string, bool) {
	replies, err := c.sendParams("LIST", []param{
		{text: `""`},
		{text: c.EscapeFolderName(name)},
	})
	if nil != err {
		return "", false
	}

	if 0 == len(replies) || "LIST" != replies[0].name || 3 > len(replies[0].values) {
		return "", false
	}
	if tokenNil == replies[0].values[2].kind {
		return "", false
	}

	return tokenText(replies[0].values[2]), true
}

// FolderList is Folders::FolderList(): the LIST/LSUB answer with the RFC 5258,
// RFC 6154, RFC 5819 and RFC 5464 extras.
func (c *Client) FolderList(parent, pattern string, subscribed, useListStatus bool) (*FolderCollection, error) {
	command := "LIST"
	var params []param
	var returnParams []param

	if c.HasCapability("LIST-EXTENDED") {
		// RFC 5258
		returnParams = append(returnParams, param{text: "SUBSCRIBED"})
		if subscribed {
			params = append(params, param{group: []param{{text: "SUBSCRIBED"}}})
		}
		// RFC 6154
		if c.HasCapability("SPECIAL-USE") {
			returnParams = append(returnParams, param{text: "SPECIAL-USE"})
		}
	} else if subscribed {
		// IMAP4rev2 deprecated
		command = "LSUB"
	}

	params = append(params,
		param{text: c.EscapeFolderName(parent)},
		param{text: quoteString(strings.TrimSpace(pattern))},
	)

	// RFC 5819
	if useListStatus && !subscribed && c.HasCapability("LIST-STATUS") {
		items := make([]param, 0, 6)
		for _, item := range c.FolderStatusItems() {
			items = append(items, param{text: item})
		}
		returnParams = append(returnParams, param{text: "STATUS"}, param{group: items})
	}

	if 0 < len(returnParams) {
		params = append(params, param{text: "RETURN"}, param{group: returnParams})
	}

	// RFC 5464
	var metadata map[string]map[string]string
	if !subscribed && c.HasCapability("METADATA") {
		metadata = c.allMetadata()
	}

	replies, err := c.sendParams(command, params)
	if nil != err {
		return nil, protocolFailure(err)
	}

	collection := NewFolderCollection()
	delimiter := ""
	inbox := false

	for _, reply := range replies {
		if respData != reply.kind {
			continue
		}

		// LIST-EXTENDED RUNGET: "* STATUS "name" (MESSAGES 5 …)".
		if "STATUS" == reply.name && 3 <= len(reply.values) {
			fullName := c.toUTF8(tokenText(reply.values[1]))
			folder, ok := collection.Get(fullName)
			if !ok {
				if folder = NewFolder(fullName, ""); nil == folder {
					continue
				}
				collection.Set(folder)
			}
			folder.setStatusFromResponse(reply)
			folder.generateETag(c)
			continue
		}

		if command != reply.name || 4 > len(reply.values) {
			continue
		}

		fullName := c.toUTF8(tokenText(reply.values[3]))
		delim := tokenText(reply.values[2])
		attributes := tokenTexts(reply.values[1].list)

		folder, exists := collection.Get(fullName)
		if exists {
			folder.setDelimiter(delim)
			folder.setAttributes(attributes)
		} else {
			if folder = NewFolder(fullName, delim, attributes...); nil == folder {
				continue
			}
			collection.Set(folder)
		}

		inbox = inbox || folder.IsInbox()
		if "" == delimiter {
			delimiter = folder.Delimiter()
		}

		if raw, ok := metadata[tokenText(reply.values[3])]; ok {
			folder.SetAllMetadata(raw)
		}
	}

	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	// RFC 5464: Dovecot supports fetching all METADATA at once, but when it did
	// not answer, every selectable folder is asked individually.
	if nil != metadata && 0 == len(metadata) {
		for _, folder := range collection.Iterate() {
			if !folder.Selectable() {
				continue
			}
			values, err := c.getMetadata(folder.FullName,
				[]string{"/shared", "/private"},
				map[string]string{"DEPTH": "infinity"})
			if nil == err {
				folder.SetAllMetadata(values)
			}
		}
	}

	if !inbox && "" == parent {
		if _, ok := collection.Get("INBOX"); !ok {
			if folder := NewFolder("INBOX", delimiter); nil != folder {
				collection.Set(folder)
			}
		}
	}

	return collection, nil
}

// FolderSubscribeList is Folders::FolderSubscribeList().
func (c *Client) FolderSubscribeList(parent, pattern string) (*FolderCollection, error) {
	return c.FolderList(parent, pattern, true, false)
}

// FolderStatusList is Folders::FolderStatusList().
func (c *Client) FolderStatusList(parent, pattern string) (*FolderCollection, error) {
	return c.FolderList(parent, pattern, false, true)
}

// GetNamespaces is ImapClient::GetNamespaces(): nil when the server has no
// NAMESPACE support or the command failed, like the PHP try/catch.
func (c *Client) GetNamespaces() *NamespaceResult {
	if !c.HasCapability("NAMESPACE") {
		return nil
	}

	replies, err := c.sendParams("NAMESPACE", nil)
	if nil != err {
		c.logWarning("NAMESPACE failed: %v", err)
		return nil
	}

	for _, reply := range replies {
		if respData == reply.kind && "NAMESPACE" == reply.name {
			return newNamespaceResult(reply)
		}
	}

	c.logWarning("NAMESPACE answer is missing")
	return nil
}

// QuotaRoot is Commands\Quota::QuotaRoot(): [used, limit, usedMessages,
// limitMessages] in kilobytes, or nil when QUOTA is not supported.
func (c *Client) QuotaRoot(folder string) []int {
	return c.getQuota(true, folder)
}

// Quota is Commands\Quota::Quota().
func (c *Client) Quota(root string) []int {
	return c.getQuota(false, root)
}

// getQuota is Commands\Quota::getQuota().
func (c *Client) getQuota(isRoot bool, name string) []int {
	if !c.HasCapability("QUOTA") {
		return nil
	}

	command := "GETQUOTA "
	if isRoot {
		command = "GETQUOTAROOT "
	}

	replies, err := c.commandLine(command + c.EscapeFolderName(name))
	if nil != err {
		return nil
	}

	result := []int{0, 0, 0, 0}
	for _, reply := range replies {
		if respData != reply.kind || "QUOTA" != reply.name || 3 > len(reply.values) {
			continue
		}

		items := reply.values[2]
		if tokenList != items.kind || 3 > len(items.list) {
			continue
		}
		if "STORAGE" != strings.ToUpper(tokenText(items.list[0])) {
			continue
		}

		usage, usageOK := integerToken(items.list[1])
		limit, limitOK := integerToken(items.list[2])
		if !usageOK || !limitOK {
			continue
		}

		result = []int{usage, limit, 0, 0}

		if 6 <= len(items.list) && "MESSAGE" == strings.ToUpper(tokenText(items.list[3])) {
			if messages, ok := integerToken(items.list[4]); ok {
				if messageLimit, ok := integerToken(items.list[5]); ok {
					result[2] = messages
					result[3] = messageLimit
				}
			}
		}
	}

	return result
}

// allMetadata is Commands\Metadata::getAllMetadata().
func (c *Client) allMetadata() map[string]map[string]string {
	if nil != c.allMetadataFetched {
		return c.allMetadataFetched
	}
	c.allMetadataFetched = map[string]map[string]string{}

	replies, err := c.sendParams("GETMETADATA", []param{
		{text: "(DEPTH infinity)"},
		{text: quoteString("*")},
		{group: []param{{text: quoteString("/shared")}, {text: quoteString("/private")}}},
	})
	if nil != err {
		return c.allMetadataFetched
	}

	for _, reply := range replies {
		if respData != reply.kind || "METADATA" != reply.name || 3 > len(reply.values) {
			continue
		}
		if tokenList != reply.values[2].kind {
			continue
		}

		values := map[string]string{}
		for index := 0; index+1 < len(reply.values[2].list); index += 2 {
			values[tokenText(reply.values[2].list[index])] = tokenText(reply.values[2].list[index+1])
		}
		c.allMetadataFetched[c.toUTF8(tokenText(reply.values[1]))] = values
	}

	return c.allMetadataFetched
}

// getMetadata is Commands\Metadata::getMetadata().
func (c *Client) getMetadata(name string, entries []string, options map[string]string) (map[string]string, error) {
	var params []param

	if 0 < len(options) {
		var parts []param
		for _, key := range []string{"MAXSIZE", "DEPTH"} {
			value, ok := options[key]
			if !ok {
				continue
			}
			if "MAXSIZE" == key {
				number, err := strconv.Atoi(strings.TrimSpace(value))
				if nil != err || 0 >= number {
					continue
				}
				parts = append(parts, param{text: "MAXSIZE " + strconv.Itoa(number)})
				continue
			}
			if "1" == value || "infinity" == value {
				parts = append(parts, param{text: "DEPTH " + value})
			}
		}
		if 0 < len(parts) {
			params = append(params, param{group: parts})
		}
	}

	entriesParams := make([]param, 0, len(entries))
	for _, entry := range entries {
		entriesParams = append(entriesParams, param{text: quoteString(entry)})
	}

	params = append(params,
		param{text: c.EscapeFolderName(name)},
		param{group: entriesParams},
	)

	replies, err := c.sendParams("GETMETADATA", params)
	if nil != err {
		return nil, protocolFailure(err)
	}

	result := map[string]string{}
	for _, reply := range replies {
		if respData != reply.kind || "METADATA" != reply.name || 3 > len(reply.values) {
			continue
		}
		if tokenList != reply.values[2].kind {
			continue
		}
		for index := 0; index+1 < len(reply.values[2].list); index += 2 {
			result[tokenText(reply.values[2].list[index])] = tokenText(reply.values[2].list[index+1])
		}
	}

	return result, nil
}

// FolderGetMetadata is Commands\Metadata::FolderGetMetadata().
func (c *Client) FolderGetMetadata(name string, entries []string, options map[string]string) (map[string]string, error) {
	if !c.HasCapability("METADATA") {
		return map[string]string{}, nil
	}
	return c.getMetadata(name, entries, options)
}

// FolderSetMetadata is Commands\Metadata::FolderSetMetadata().
func (c *Client) FolderSetMetadata(name string, entries []MetadataEntry) error {
	if !c.HasCapability("METADATA") {
		return nil
	}
	if 0 == len(entries) {
		return protocolFailure(errors.New("imapclient: wrong argument for SETMETADATA"))
	}

	items := make([]param, 0, len(entries))
	for _, entry := range entries {
		value := "NIL"
		if nil != entry.Value {
			value = quoteString(*entry.Value)
		}
		items = append(items, param{text: quoteString(entry.Key) + " " + value})
	}

	replies, err := c.sendParams("SETMETADATA", []param{
		{text: c.EscapeFolderName(name)},
		{group: items},
	})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderRemoveMetadata is Commands\Metadata::FolderRemoveMetadata().
func (c *Client) FolderRemoveMetadata(name string, keys []string) error {
	entries := make([]MetadataEntry, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, MetadataEntry{Key: key})
	}
	return c.FolderSetMetadata(name, entries)
}

// aclCommandAllowed is Commands\ACL::ACLAllow() for the commands DoFolder*
// issues: without the ACL capability nothing is sent.
func (c *Client) aclCommandAllowed() bool { return c.HasCapability("ACL") }

// FolderMyRights is Commands\ACL::FolderMyRights().
func (c *Client) FolderMyRights(name string) ACL {
	rights := ""

	if c.aclCommandAllowed() {
		if replies, err := c.sendParams("MYRIGHTS", []param{{text: c.EscapeFolderName(name)}}); nil == err {
			for _, reply := range replies {
				if respData != reply.kind || "MYRIGHTS" != reply.name || 3 > len(reply.values) {
					continue
				}
				if name != tokenText(reply.values[1]) {
					continue
				}
				rights = tokenText(reply.values[2])
				break
			}
		} else {
			c.logWarning("MYRIGHTS failed: %v", err)
		}
	}

	return ACL{Identifier: c.username, Rights: rights, Mine: true}
}

// FolderGetACL is Commands\ACL::FolderGetACL().
func (c *Client) FolderGetACL(name string) []ACL {
	result := []ACL{c.FolderMyRights(name)}
	if !result[0].HasRight("ADMINISTER") {
		return result
	}

	replies, err := c.sendParams("GETACL", []param{{text: c.EscapeFolderName(name)}})
	if nil != err {
		c.logWarning("GETACL failed: %v", err)
		return result
	}

	for _, reply := range replies {
		if respData != reply.kind || "ACL" != reply.name || 4 > len(reply.values) {
			continue
		}
		if name != tokenText(reply.values[1]) {
			continue
		}
		for index := 2; index+1 < len(reply.values); index += 2 {
			identifier := tokenText(reply.values[index])
			if identifier == c.username {
				continue
			}
			result = append(result, ACL{
				Identifier: identifier,
				Rights:     tokenText(reply.values[index+1]),
			})
		}
	}

	return result
}

// FolderSetACL is Commands\ACL::FolderSetACL().
func (c *Client) FolderSetACL(name, identifier, rights string) error {
	if !c.aclCommandAllowed() {
		return nil
	}

	replies, err := c.sendParams("SETACL", []param{
		{text: c.EscapeFolderName(name)},
		{text: quoteString(identifier)},
		{text: quoteString(rights)},
	})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FolderDeleteACL is Commands\ACL::FolderDeleteACL().
func (c *Client) FolderDeleteACL(name, identifier string) error {
	if !c.aclCommandAllowed() {
		return nil
	}

	replies, err := c.sendParams("DELETEACL", []param{
		{text: c.EscapeFolderName(name)},
		{text: quoteString(identifier)},
	})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// MessageStoreFlag is Commands\Messages::MessageStoreFlag().
func (c *Client) MessageStoreFlag(
	sequenceSet string,
	uid bool,
	flags []string,
	action string,
) error {
	if "" == strings.TrimSpace(sequenceSet) || "" == strings.TrimSpace(action) || 0 == len(flags) {
		return nil
	}

	command := "STORE"
	if uid {
		command = "UID STORE"
	}

	items := make([]param, 0, len(flags))
	for _, flag := range flags {
		items = append(items, param{text: quoteString(UTF8ToMUTF7(flag))})
	}

	replies, err := c.sendParams(command, []param{
		{text: sequenceSet},
		{text: action},
		{group: items},
	})
	if nil != err {
		return protocolFailure(err)
	}
	_, err = expectOK(replies)
	return err
}

// FlagRecord is one message of a FETCH UID FLAGS answer.
type FlagRecord struct {
	// UID is the unique id of the message.
	UID int
	// Flags are the flags, lower cased and UTF-7 decoded like
	// MailClient::FolderInformation() does.
	Flags []string
}

// FetchFlags is the FETCH UID FLAGS of MailClient::FolderInformation().
func (c *Client) FetchFlags(sequenceSet string, uid bool) ([]FlagRecord, error) {
	command := "FETCH"
	if uid {
		command = "UID FETCH"
	}

	replies, err := c.sendParams(command, []param{
		{text: sequenceSet},
		{group: []param{{text: "UID"}, {text: "FLAGS"}}},
	})
	if nil != err {
		return nil, protocolFailure(err)
	}

	var result []FlagRecord
	for _, reply := range replies {
		if respData != reply.kind || "FETCH" != reply.name || 2 > len(reply.values) {
			continue
		}
		items := reply.values[1]
		if tokenList != items.kind {
			continue
		}

		record := FlagRecord{}
		for index := 0; index+1 < len(items.list); index += 2 {
			switch strings.ToUpper(tokenText(items.list[index])) {
			case "UID":
				record.UID = integerValue(items.list[index+1])
			case "FLAGS":
				flags := items.list[index+1]
				if tokenList == flags.kind {
					record.Flags = utf7ToUTF8AllLower(tokenTexts(flags.list))
				}
			}
		}
		result = append(result, record)
	}

	if _, err := expectOK(replies); nil != err {
		return nil, err
	}

	return result, nil
}

// MessageAppendStream is Commands\Messages::MessageAppendStream(): APPEND a
// message with optional flags and return the [APPENDUID] value when the server
// reports one.
func (c *Client) MessageAppendStream(folder string, source io.Reader, size int64, flags ...string) (int, bool, error) {
	if "" == folder {
		return 0, false, protocolFailure(ErrEmptyFolderName)
	}
	if 1 > size {
		return 0, false, protocolFailure(errors.New("imapclient: empty message stream"))
	}

	line := c.EscapeFolderName(folder)
	if 0 < len(flags) {
		items := make([]param, 0, len(flags))
		for _, flag := range flags {
			items = append(items, param{text: quoteString(UTF8ToMUTF7(flag))})
		}
		line += paramLine([]param{{group: items}})
	}
	line += " {" + strconv.FormatInt(size, 10) + "}"

	tag := c.nextTag()
	line = tag + " APPEND " + line
	if err := c.writeLine(line); nil != err {
		return 0, false, protocolFailure(err)
	}

	prompt, err := c.readReply(tag)
	if nil != err {
		return 0, false, protocolFailure(err)
	}
	if last := prompt[len(prompt)-1]; respContinuation != last.kind {
		_, err := expectOK(prompt)
		return 0, false, err
	}

	c.setDeadline()
	if _, err := io.Copy(c.conn, source); nil != err {
		return 0, false, protocolFailure(err)
	}
	if err := c.writeLine(""); nil != err {
		return 0, false, protocolFailure(err)
	}

	replies, err := c.readReply(tag)
	if nil != err {
		return 0, false, protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return 0, false, err
	}

	for _, reply := range replies {
		if "APPENDUID" != reply.code || 0 == len(reply.codeArgs) {
			continue
		}
		last := reply.codeArgs[len(reply.codeArgs)-1]
		if number, err := strconv.Atoi(last); nil == err {
			return number, true, nil
		}
	}

	return 0, false, nil
}

// utf7ToUTF8All is array_map('Utf7ModifiedToUtf8', …).
func utf7ToUTF8All(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = MUTF7ToUTF8(value)
	}
	return result
}

// utf7ToUTF8AllLower is array_map('mb_strtolower', array_map(
// 'Utf7ModifiedToUtf8', …)) of MailClient::FolderInformation().
func utf7ToUTF8AllLower(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToLower(MUTF7ToUTF8(value))
	}
	return result
}

// maxInt is PHP's max() over any number of ints.
func maxInt(values ...int) int {
	if 0 == len(values) {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
