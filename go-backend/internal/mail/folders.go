package mail

// The folder operations of MailSo\Mail\MailClient.

import (
	"errors"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// errInvalidFolderName is the \ValueError the folder methods throw for an empty
// or unusable name.
var errInvalidFolderName = errors.New("mail: invalid folder name")

// Folders is MailClient::Folders(): the LIST/LSUB answer, or nil when the
// mailbox list is empty.
func (c *Client) Folders(parent, pattern string, useListSubscribeStatus bool) (*imapclient.FolderCollection, error) {
	collection, err := c.ImapClient().FolderStatusList(parent, pattern)
	if nil != err {
		return nil, err
	}
	if nil == collection || 0 == collection.Count() {
		return nil, nil
	}

	// RFC 5258 is not supported: the subscriptions come from LSUB.
	if useListSubscribeStatus && !c.ImapClient().HasCapability("LIST-EXTENDED") {
		subscribed, err := c.ImapClient().FolderSubscribeList(parent, pattern)
		if nil == err && nil != subscribed {
			for _, folder := range subscribed.Iterate() {
				if target, ok := collection.Get(folder.FullName); ok {
					target.SetSubscribed()
				}
			}
		} else {
			// PHP falls back to "everything is subscribed" when LSUB fails.
			for _, folder := range collection.Iterate() {
				folder.SetSubscribed()
			}
		}
	}

	return collection, nil
}

// FolderCreate is MailClient::FolderCreate(): create the mailbox and return its
// LIST entry.
func (c *Client) FolderCreate(
	name string,
	parent string,
	subscribeOnCreation bool,
) (*imapclient.Folder, error) {
	name = strings.TrimSpace(name)
	parent = strings.TrimSpace(parent)

	if "" == name {
		return nil, errInvalidFolderName
	}

	delimiter, ok := c.ImapClient().FolderHierarchyDelimiter(parent)
	if !ok {
		if "" != parent {
			return nil, errors.New("mail: cannot create folder in non-existent parent folder")
		}
		return nil, errors.New("mail: cannot get folder delimiter")
	}

	if "" != delimiter && "" != parent {
		parent += delimiter
	}

	fullName := parent + name

	if err := c.ImapClient().FolderCreate(fullName, subscribeOnCreation); nil != err {
		return nil, err
	}

	folders, err := c.ImapClient().FolderStatusList(fullName, "")
	if nil != err {
		return nil, err
	}

	if folder, exists := folders.Get(fullName); exists {
		if subscribeOnCreation {
			folder.SetSubscribed()
		}
		return folder, nil
	}

	return nil, nil
}

// FolderRename is MailClient::FolderRename(): rename the mailbox and move its
// subscriptions along (RFC 9051 §6.3.6 leaves them to the client).
func (c *Client) FolderRename(previousFullName, newFullName string) error {
	if "" == previousFullName || "" == newFullName {
		return errInvalidFolderName
	}

	if _, ok := c.ImapClient().FolderHierarchyDelimiter(previousFullName); !ok {
		return errors.New("mail: cannot rename non-existent folder")
	}

	subscribed, subscribeErr := c.ImapClient().FolderSubscribeList(previousFullName, "*")

	if err := c.ImapClient().FolderRename(previousFullName, newFullName); nil != err {
		return err
	}

	if nil == subscribeErr && nil != subscribed {
		for _, folder := range subscribed.Iterate() {
			if !strings.HasPrefix(folder.FullName, previousFullName) {
				continue
			}
			if err := c.ImapClient().FolderUnsubscribe(folder.FullName); nil != err {
				return err
			}
			if err := c.ImapClient().FolderSubscribe(newFullName + folder.FullName[len(previousFullName):]); nil != err {
				return err
			}
		}
	}

	return nil
}

// FolderInformation is MailClient::FolderInformation(): the STATUS/SELECT
// counters, plus the message flags the client asked for.
//
// Partial (step 15): the "newMessages" key of
// getFolderNextMessageInformation() needs the ENVELOPE/MIME header decoding of
// step 16, so a non-zero prevUIDNext does not add it yet.
func (c *Client) FolderInformation(
	name string,
	prevUIDNext int,
	flagsRange *imapclient.SequenceSet,
) (map[string]any, error) {
	var (
		info map[string]any
		err  error
	)

	if nil != flagsRange {
		var status *imapclient.FolderInformation
		status, err = c.ImapClient().FolderStatusAndSelect(name)
		if nil != err {
			return nil, err
		}

		info, _ = status.JSONSerialize().(map[string]any)
		if nil == info {
			info = map[string]any{}
		}

		flags := []any{}
		if 0 < flagsRange.Count() {
			records, fetchErr := c.ImapClient().FetchFlags(flagsRange.String(), flagsRange.UID)
			if nil != fetchErr {
				return nil, fetchErr
			}
			for _, record := range records {
				flags = append(flags, map[string]any{
					"uid":   record.UID,
					"flags": record.Flags,
				})
			}
		}
		info["messagesFlags"] = flags
	} else {
		var status *imapclient.FolderInformation
		status, err = c.ImapClient().FolderStatus(name, false)
		if nil != err {
			return nil, err
		}
		info, _ = status.JSONSerialize().(map[string]any)
		if nil == info {
			info = map[string]any{}
		}
	}

	return info, nil
}

// FolderHash is MailClient::FolderHash(): the folder ETag, or "" on failure.
func (c *Client) FolderHash(name string) string {
	info, err := c.ImapClient().FolderStatus(name, false)
	if nil != err || nil == info {
		return ""
	}
	return info.ETag
}
