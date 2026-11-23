// Package mail ports MailSo\Mail\MailClient: the high level mail operations on
// top of an authenticated internal/imapclient connection.
//
// Step 15 provides the folder surface only (Folders, FolderCreate,
// FolderRename, FolderInformation, FolderHash). A *Client wraps the IMAP client
// of the request, so the connection is reused for every command of that
// request, exactly like Actions::MailClient() caches one MailClient.
package mail

import (
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// Client is MailSo\Mail\MailClient.
type Client struct {
	imap *imapclient.Client
}

// New wraps an authenticated IMAP client.
func New(imap *imapclient.Client) *Client {
	return &Client{imap: imap}
}

// ImapClient is MailClient::ImapClient(): the command layer DoFolder* talks to
// directly (MailClient::__call() forwards the rest in PHP).
func (c *Client) ImapClient() *imapclient.Client {
	if nil == c {
		return nil
	}
	return c.imap
}
