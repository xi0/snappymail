package mail

// MailClient::MessageMimeStream(): the message-part stream the attachment
// actions (Step 20) and the Raw message endpoints use.
//
// The method fetches the MIME headers of one part (or the whole message when
// mimeIndex is empty), then the part body, decodes its Content-Transfer-Encoding
// and hands the decoded bytes to the callback together with the media type, the
// file name and the part id, exactly like the PHP callback signature
// ($rResource, $sContentType, $sFileName, $sMimeIndex).
//
// The PHP code has a BINARY capability fast path that asks the server to decode
// the part. Fetching BODY.PEEK[] and decoding locally yields the same bytes, so
// the port always takes that route (documented deviation, semantically equal).

import (
	"bytes"
	"io"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// MessageMimeStream is MailClient::MessageMimeStream(): stream one MIME part of
// a message to handler and report whether it was found.
//
// folderName is the mailbox full name and uid the message UID. mimeIndex is the
// dot separated part id ("1.2"); an empty value streams the whole message
// (message/rfc822, "<subject>.eml").
//
// The handler parameter is the raw function type (not a named type), so
// *Client also satisfies service.MessageStreamer for the ?/Raw message
// endpoints.
func (c *Client) MessageMimeStream(
	handler func(reader io.ReadSeeker, contentType, fileName, mimeIndex string),
	folderName string,
	uid int,
	mimeIndex string,
) bool {
	if nil == c || nil == handler {
		return false
	}

	mimeIndex = strings.TrimSpace(mimeIndex)
	if !imapclient.IsValidPartId(mimeIndex, true) {
		return false
	}
	if uid < 1 {
		return false
	}

	imap := c.ImapClient()
	if nil == imap {
		return false
	}

	if _, err := imap.FolderExamine(folderName, false); nil != err {
		return false
	}

	contentType := ""
	fileName := ""
	encoding := ""

	// 1. The part headers (or the message headers for the whole-message stream).
	headerItem := imapclient.FetchBodyHeaderPeek
	headerKey := imapclient.FetchBodyHeader
	if "" != mimeIndex {
		headerItem = imapclient.FetchBodyPeek + "[" + mimeIndex + ".MIME]"
		headerKey = imapclient.FetchBody + "[" + mimeIndex + ".MIME]"
	}

	if responses, err := imap.Fetch([]string{headerItem}, strconv.Itoa(uid), true); nil == err && 0 < len(responses) {
		if rawMime := responses[0].FetchString(headerKey); "" != rawMime {
			headers := mime.NewHeaderCollection(rawMime, "")
			if "" != mimeIndex {
				fileName = headers.ParameterValue(mime.HeaderContentDisposition, mime.ParameterFilename)
				if "" == fileName {
					fileName = headers.ParameterValue(mime.HeaderContentType, mime.ParameterName)
				}
				encoding = headers.ValueByName(mime.HeaderContentTransferEncoding)
				contentType = headers.ValueByName(mime.HeaderContentType)
			} else {
				subject := headers.ValueByName(mime.HeaderSubject)
				if "" == subject {
					subject = strconv.Itoa(uid)
				}
				fileName = subject + ".eml"
				contentType = "message/rfc822"
			}
		}
	}

	// 2. The part body. BODY.PEEK[] (empty id) addresses the whole message.
	bodyItem := imapclient.FetchBodyPeek + "[" + mimeIndex + "]"
	responses, err := imap.Fetch([]string{bodyItem}, strconv.Itoa(uid), true)
	if nil != err || 0 == len(responses) {
		return false
	}

	body, found := responses[0].GetBodyPartValue(mimeIndex)
	if !found {
		return false
	}

	content := mime.DecodeEncodingValue(body, encoding)
	handler(bytes.NewReader([]byte(content)), contentType, fileName, mimeIndex)

	return true
}
