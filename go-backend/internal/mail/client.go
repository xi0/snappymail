// Package mail ports MailSo\Mail\MailClient: the high level mail operations on
// top of an authenticated internal/imapclient connection.
//
// Step 15 provides the folder surface, step 16 the message list, step 17 the
// single message view (Message) and step 18 the flag mutation (flags.go). A
// *Client wraps the IMAP client of the request,
// so the connection is reused for every command of that request, exactly like
// Actions::MailClient() caches one MailClient.
package mail

import (
	"errors"
	"strconv"

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

// Message is MailClient::Message(): one message with its headers, flags, the
// BODYSTRUCTURE layout and the decoded text parts.
//
// The BODYSTRUCTURE is fetched first, because the part ids of the html/plain
// bodies are only known from it; the second FETCH asks for exactly those parts.
// The body_text_limit trim of PHP is not reachable from the Go configuration
// (Imap\Settings::fromArray() ignores the key as well), so the full part is
// always requested, like PHP's default of 0.
func (c *Client) Message(folderName string, index int, indexIsUID bool) (*Message, error) {
	if 1 > index {
		return nil, errors.New("mail: invalid message index")
	}

	imap := c.ImapClient()

	if _, err := imap.FolderExamine(folderName, false); nil != err {
		return nil, err
	}

	fetchItems := []string{
		imapclient.FetchUID,
		imapclient.FetchRFC822Size,
		imapclient.FetchInternalDate,
		imapclient.FetchFlags,
		c.envelopeOrHeadersRequestString(),
	}

	indexRange := strconv.Itoa(index)

	var bodyStructure *imapclient.BodyStructure
	if responses, err := imap.Fetch([]string{imapclient.FetchBodyStructure}, indexRange, indexIsUID); nil == err && 0 < len(responses) {
		bodyStructure = responses[0].GetFetchBodyStructure()
		if nil != bodyStructure {
			for _, part := range bodyStructure.GetHtmlAndPlainParts() {
				fetchItems = append(fetchItems, imapclient.FetchBodyPeek+"["+part.PartID()+"]")
			}
		}
	}

	if nil == bodyStructure {
		fetchItems = append(fetchItems, imapclient.FetchBodyStructure)
	}

	responses, err := imap.Fetch(fetchItems, indexRange, indexIsUID)
	if nil != err {
		return nil, err
	}
	if 0 == len(responses) {
		return nil, nil
	}

	return MessageFromFetchResponse(folderName, responses[0], bodyStructure), nil
}
