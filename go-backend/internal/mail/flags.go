package mail

// The message flag mutations of MailSo\Mail\MailClient (Step 18).
//
// Only MessageSetFlag() lives on the MailClient in PHP; MOVE/COPY/DELETE are
// ImapClient commands (imapclient.MessageMove/MessageCopy/MessageDelete) which
// the DoMessage* actions call through MailClient::__call(). The distinction is
// kept, so the handlers read exactly like the PHP ones.
//
// Deliberate deviation: the flag atom is sent as an IMAP quoted string, so a
// client supplied keyword (DoMessageSetKeyword) cannot inject a command. PHP
// appends the value verbatim, which is an IMAP injection hole there.

import (
	"fmt"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// MessageSetFlag is MailClient::MessageSetFlag(): add/remove one flag on the
// range, but only when the server reports the flag as permanent for that
// mailbox.
//
// setAction false removes the flag (STORE -FLAGS.SILENT), true adds it
// (STORE +FLAGS.SILENT). skipUnsupportedFlag swallows the "flag is not
// supported" failure, which the callers use for the flags a server may not know
// (\\Deleted, $Junk, …).
func (c *Client) MessageSetFlag(
	folderName string,
	rangeSet *imapclient.SequenceSet,
	messageFlag string,
	setAction bool,
	skipUnsupportedFlag bool,
) error {
	if nil == rangeSet || 0 == rangeSet.Count() {
		return nil
	}

	info, err := c.ImapClient().FolderSelect(folderName, false)
	if nil != err {
		return err
	}

	if info.IsFlagSupported(messageFlag) {
		action := imapclient.StoreAddFlagsSilent
		if !setAction {
			action = imapclient.StoreRemoveFlagsSilent
		}
		return c.ImapClient().MessageStoreFlag(rangeSet.String(), rangeSet.UID, []string{messageFlag}, action)
	}

	if !skipUnsupportedFlag {
		return fmt.Errorf("Message flag %q is not supported.", messageFlag)
	}

	return nil
}
