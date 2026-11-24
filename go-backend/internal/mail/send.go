package mail

// The save/send helpers of MailSo\Mail\MailClient (Step 19): APPEND a rendered
// message into a mailbox and locate the UID of an appended message by its
// Message-ID header.
//
// DoSaveMessage uses the [APPENDUID] of the APPEND when the server supports
// UIDPLUS; the Message-ID lookup is the fallback for servers that do not.

import (
	"errors"
	"io"
	"strings"
)

// AppendMessage is the APPEND part of Actions\Messages::DoSaveMessage() and
// DoSendMessage(): append size bytes of source to folder with the given flags
// (usually \Seen) and return the new UID when the server reports one.
func (c *Client) AppendMessage(folder string, source io.Reader, size int64, flags ...string) (int, bool, error) {
	if nil == c {
		return 0, false, errNoImap
	}
	return c.ImapClient().MessageAppendStream(folder, source, size, flags...)
}

// FindMessageUIDByMessageID is MailClient::FindMessageUidByMessageId(): the UID
// of the single message with the given Message-ID, or (0, false).
func (c *Client) FindMessageUIDByMessageID(folder, messageID string) (int, bool, error) {
	if nil == c {
		return 0, false, errNoImap
	}
	if "" == messageID {
		return 0, false, nil
	}

	imap := c.ImapClient()
	if _, err := imap.FolderExamine(folder, false); nil != err {
		return 0, false, err
	}

	// MailClient::FindMessageUidByMessageId() does not escape the value, but the
	// search layer always quotes user supplied strings; keeping the escaping
	// here prevents an IMAP injection through a Message-ID header.
	uids, err := imap.MessageSearch("HEADER Message-ID "+escapeSearchString(imap, strings.TrimSpace(messageID)), true)
	if nil != err {
		return 0, false, err
	}
	if 1 == len(uids) {
		return uids[0], true, nil
	}
	return 0, false, nil
}

// errNoImap is the error of a nil client (PHP would fatal on ->oImapClient).
var errNoImap = errors.New("mail: no IMAP client")
