package handler

// The save/send actions of \RainLoop\Actions\Messages (Step 19):
// DoSaveMessage, DoSendMessage and DoSendReadReceiptMessage.
//
// The MIME document is built by internal/mime (the builder side of
// MailSo\Mime), appended through the per-request IMAP client and delivered by
// the smtpclient.Sender wired into the dispatcher. Hook names and argument order
// follow the PHP code.
//
// Partial, by design of the step split:
//   - PGP/S/MIME signing and encryption execution belongs to Steps 25/26; only
//     the `signed`/`encrypted` wire shapes supplied by the frontend are rendered.
//   - HtmlUtils::BuildHtml() (inline image extraction) and
//     HtmlUtils::ConvertHtmlToPlain() are not ported, so the plain alternative
//     comes from the request (`plain`) and inline data URLs are not extracted.
//   - The read receipt identity comes from Actions\Accounts::GetIdentityByID()
//     (step 24); without an identity provider the account e-mail/name is used.
//   - The address book frequency counter (IncFrec) is Step 22.
//   - SMTP.usePhpMail reports CantSendMessage (Go has no mail()).

import (
	"bytes"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/smtpclient"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// RegisterMessageSend registers the draft/send actions of step 19.
func RegisterMessageSend(d *Dispatcher) {
	d.Register("SaveMessage", doSaveMessage)
	d.Register("SendMessage", doSendMessage)
	d.Register("SendReadReceiptMessage", doSendReadReceiptMessage)
}

// sender is the configured SMTP sender, or nil.
func (c *Context) sender() Sender {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newSender {
		return nil
	}
	return c.dispatcher.newSender(c.dispatcher.plugins)
}

// doSaveMessage is Actions\Messages::DoSaveMessage().
func doSaveMessage(ctx *Context) (any, error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	draftFolder := ctx.stringParam("saveFolder", "")
	if "" == draftFolder {
		return nil, appctx.NewClientError(appctx.UnknownError, nil, "")
	}

	message := buildMessage(ctx, account, true)
	ctx.dispatcher.runHook("filter.save-message", message)

	var result any = false

	data := message.ToBytes(false)
	if 0 < len(data) {
		newUID, appendUID, appendErr := client.AppendMessage(
			draftFolder, bytes.NewReader(data), int64(len(data)), imapclient.FlagSeen,
		)
		if nil != appendErr {
			return nil, appendErr
		}

		if messageID := message.MessageID(); "" != messageID && (!appendUID || 0 == newUID) {
			if uid, found, findErr := client.FindMessageUIDByMessageID(draftFolder, messageID); nil == findErr && found {
				newUID = uid
			}
		}

		result = true

		// Replace the previous draft version.
		if messageFolder := ctx.stringParam("messageFolder", ""); "" != messageFolder && 0 < ctx.intParam("messageUid") {
			_ = client.ImapClient().MessageDelete(
				messageFolder,
				imapclient.NewSequenceSet(strconv.Itoa(ctx.intParam("messageUid")), true),
				true,
			)
		}

		if 0 < newUID {
			result = map[string]any{"folder": draftFolder, "uid": newUID}
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSendMessage is Actions\Messages::DoSendMessage().
func doSendMessage(ctx *Context) (any, error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	saveFolder := ctx.stringParam("saveFolder", "")
	draftInfo := draftInfoParam(ctx)

	message := buildMessage(ctx, account, false)
	ctx.dispatcher.runHook("filter.send-message", message)

	var result any = false

	data := message.ToBytes(true)
	if 0 < len(data) {
		if err := smtpSendMessage(
			ctx, account, message, &data, true,
			ctx.boolParam("dsn"), ctx.boolParam("requireTLS"),
		); nil != err {
			return nil, err
		}

		// The message the draft was a reply/forward of gets its flag.
		if 3 == len(draftInfo) {
			draftType := strings.ToLower(phpString(draftInfo[0]))
			draftUID := phpIntValue(draftInfo[1])
			draftFolder := phpString(draftInfo[2])
			switch draftType {
			case "reply", "reply-all":
				if flagErr := client.MessageSetFlag(
					draftFolder,
					imapclient.NewSequenceSet(strconv.Itoa(draftUID), true),
					imapclient.FlagAnswered, true, false,
				); nil != flagErr {
					ctx.dispatcher.logException(flagErr)
				}
			case "forward":
				if flagErr := client.MessageSetFlag(
					draftFolder,
					imapclient.NewSequenceSet(strconv.Itoa(draftUID), true),
					imapclient.FlagForwarded, true, false,
				); nil != flagErr {
					ctx.dispatcher.logException(flagErr)
				}
			}
		}

		if "" != saveFolder {
			if err := saveSentCopy(ctx, account, client, message, data, saveFolder); nil != err {
				return nil, err
			}
		}

		deleteMessageAttachments(ctx, account)

		if draftFolder := ctx.stringParam("messageFolder", ""); "" != draftFolder && 0 < ctx.intParam("messageUid") {
			_ = client.ImapClient().MessageDelete(
				draftFolder,
				imapclient.NewSequenceSet(strconv.Itoa(ctx.intParam("messageUid")), true),
				true,
			)
		}

		result = true
	}

	if false == result {
		return nil, appctx.NewClientError(appctx.CantSendMessage, nil, "")
	}

	return ctx.TrueResponse(nil), nil
}

// saveSentCopy is the save-to-Sent block of DoSendMessage(): the Bcc-stripped
// copy is appended to saveFolder, with the account SentFolder as fallback.
func saveSentCopy(ctx *Context, account *account.Account, client interface {
	AppendMessage(string, io.Reader, int64, ...string) (int, bool, error)
}, message *mime.BuiltMessage, data []byte, saveFolder string) error {
	appendData := data
	if message.HasBcc() {
		appendData = message.ToBytes(false)
	}
	appendSize := len(appendData)

	// PHP: RunHook('filter.send-message-stream',
	//   array($oAccount, &$rAppendMessageStream, &$iAppendMessageStreamSize))
	ctx.dispatcher.runHook("filter.send-message-stream", account, &appendData, &appendSize)
	if 0 > appendSize || appendSize > len(appendData) {
		appendSize = len(appendData)
	}

	appendCopy := func(folder string) error {
		_, _, err := client.AppendMessage(folder, bytes.NewReader(appendData), int64(appendSize), imapclient.FlagSeen)
		return err
	}

	if err := appendCopy(saveFolder); nil != err {
		settings := ctx.LocalSettingsForCurrentAccount()
		sentFolder := strings.TrimSpace(phpString(settings.GetConf("SentFolder", "")))
		if "" == sentFolder || sentFolder == saveFolder {
			return appctx.NewClientError(appctx.CantSaveMessage, err, "")
		}
		if retryErr := appendCopy(sentFolder); nil != retryErr {
			return appctx.NewClientError(appctx.CantSaveMessage, retryErr, "")
		}
	}

	return nil
}

// LocalSettingsForCurrentAccount is Actions::SettingsProvider(true)->Load($oAccount)
// for the selected account of the request.
func (c *Context) LocalSettingsForCurrentAccount() *storage.Settings {
	resolved, err := c.Account(false)
	if nil != err || nil == resolved {
		return storage.NewSettings()
	}
	return c.LocalSettings(resolved)
}

// doSendReadReceiptMessage is Actions\Messages::DoSendReadReceiptMessage().
func doSendReadReceiptMessage(ctx *Context) (any, error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	message := buildReadReceiptMessage(ctx, account)
	if nil == message {
		return nil, appctx.NewClientError(appctx.UnknownError, nil, "")
	}
	ctx.dispatcher.runHook("filter.send-read-receipt-message", message, account)

	data := message.ToBytes(true)
	if err := smtpSendMessage(ctx, account, message, &data, false, false, false); nil != err {
		return nil, err
	}

	folder := ctx.stringParam("messageFolder", "")
	uid := ctx.intParam("messageUid")
	if "" != folder && 0 < uid {
		if err := client.MessageSetFlag(
			folder,
			imapclient.NewSequenceSet(strconv.Itoa(uid), true),
			imapclient.FlagMDNSent, true, true,
		); nil != err {
			// The PHP fallback remembers the receipt in the cache so the
			// frontend stops offering it.
			if cacher, ok := ctx.Cacher().(CacherSetter); ok && nil != cacher {
				_ = cacher.Set(storage.ReadReceiptCacheKey(account.Email(), folder, uid), "1")
			}
		}
	}

	return ctx.TrueResponse(nil), nil
}

// smtpSendMessage is Actions\Messages::smtpSendMessage(): validate the
// recipients, run the SMTP hooks and hand the stream to the sender.
func smtpSendMessage(
	ctx *Context,
	account *account.Account,
	message *mime.BuiltMessage,
	data *[]byte,
	addHiddenRcpt bool,
	dsn, requireTLS bool,
) error {
	recipients := message.Recipients()
	if 0 == len(recipients) {
		return appctx.NewClientError(appctx.InvalidRecipients, nil, "")
	}

	size := len(*data)
	// PHP: RunHook('filter.smtp-message-stream',
	//   array($oAccount, &$rMessageStream, &$iMessageStreamSize))
	ctx.dispatcher.runHook("filter.smtp-message-stream", account, data, &size)

	// PHP: RunHook('filter.message-rcpt', array($oAccount, $oRcpt)).
	rcpt := mime.NewEmailCollection("")
	for _, recipient := range recipients {
		rcpt.Append(recipient)
	}
	ctx.dispatcher.runHook("filter.message-rcpt", account, rcpt)
	recipients = rcpt.Iterate()

	from := message.FromEmail()
	if "" == from && nil != account {
		from = account.Email()
	}
	ctx.dispatcher.runHook("filter.smtp-from", account, message, &from)

	hidden := []string{}
	if addHiddenRcpt {
		ctx.dispatcher.runHook("filter.smtp-hidden-rcpt", account, message, &hidden)
	}

	sender := ctx.sender()
	if nil == sender {
		return appctx.NewClientError(appctx.CantSendMessage, nil, "")
	}

	addresses := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		addresses = append(addresses, recipient.GetEmail())
	}

	if err := sender.Send(account, from, addresses, hidden, *data, dsn, requireTLS); nil != err {
		return smtpError(err)
	}
	return nil
}

// smtpError maps the smtpclient failure onto the notification codes of
// smtpSendMessage().
func smtpError(err error) error {
	var smtpErr *smtpclient.Error
	if !errors.As(err, &smtpErr) {
		return appctx.NewClientError(appctx.CantSendMessage, err, "")
	}

	switch smtpErr.Kind {
	case smtpclient.KindConnection:
		if smtpErr.ViewErrors {
			return appctx.NewClientError(appctx.ClientViewError, err, "")
		}
		return appctx.NewClientError(appctx.ConnectionError, err, "")
	case smtpclient.KindLogin:
		return appctx.NewClientError(appctx.AuthError, err, "")
	case smtpclient.KindSize:
		return appctx.NewClientError(appctx.ClientViewError, err, smtpErr.Message)
	case smtpclient.KindUnsupported:
		return appctx.NewClientError(appctx.CantSendMessage, err, "")
	default:
		if smtpErr.ViewErrors {
			return appctx.NewClientError(appctx.ClientViewError, err, "")
		}
		return appctx.NewClientError(appctx.CantSendMessage, err, "")
	}
}

// buildMessage is Actions\Messages::buildMessage().
func buildMessage(ctx *Context, account *account.Account, withDraftInfo bool) *mime.BuiltMessage {
	cfg := ctx.Config()
	options := mime.BuildOptions{
		HideXMailer:    cfg.GetBool("security", "hide_x_mailer_header", true),
		Version:        ctx.Version(),
		BoundaryPrefix: strings.TrimSpace(cfg.GetString("labs", "boundary_prefix", "")),
	}

	message := mime.NewBuiltMessage(options)

	from := mime.ParseEmail(ctx.stringParam("from", ""))
	if nil == from && nil != account {
		from = mime.NewEmail(account.Email(), account.Name())
	}
	if nil != from {
		message.SetFrom(from)
		message.RegenerateMessageID(from.GetDomain())
	} else {
		message.RegenerateMessageID("")
	}

	message.SetReplyTo(mime.NewEmailCollection(ctx.stringParam("replyTo", "")))

	if ctx.boolParam("readReceiptRequest") {
		if nil != from {
			message.SetReadReceipt(from.GetEmail())
		}
	}

	if !ctx.boolParam("requireTLS") {
		message.SetCustomHeader("TLS-Required", "No")
	}
	if ctx.boolParam("markAsImportant") {
		message.SetPriority(mime.PriorityHigh)
	}

	message.SetSubject(ctx.stringParam("subject", ""))
	message.SetTo(mime.NewEmailCollection(ctx.stringParam("to", "")))
	message.SetCc(mime.NewEmailCollection(ctx.stringParam("cc", "")))
	message.SetBcc(mime.NewEmailCollection(ctx.stringParam("bcc", "")))

	if info := draftInfoParam(ctx); withDraftInfo && 3 == len(info) &&
		!phpEmpty(info[0]) && !phpEmpty(info[1]) && !phpEmpty(info[2]) {
		message.SetDraftInfo(phpString(info[0]), phpIntValue(info[1]), phpString(info[2]))
	}

	message.SetInReplyTo(ctx.stringParam("inReplyTo", ""))
	message.SetReferences(ctx.stringParam("references", ""))

	message.SetAutocrypt(autocryptHeaders(ctx.GetParam("autocrypt", nil)))

	if signed := ctx.stringParam("signed", ""); "" != signed {
		message.AddSigned(signed, ctx.stringParam("boundary", ""))
	} else if encrypted := ctx.stringParam("encrypted", ""); "" != encrypted {
		message.AddPGPEncrypted(encrypted)
	} else if html := ctx.stringParam("html", ""); "" != html {
		plain := ctx.stringParam("plain", "")
		ctx.dispatcher.runHook("filter.message-html", account, message, &html)
		ctx.dispatcher.runHook("filter.message-plain", account, message, &plain)
		message.AddHTML(html, plain)
	} else {
		plain := ctx.stringParam("plain", "")
		ctx.dispatcher.runHook("filter.message-plain", account, message, &plain)
		message.AddPlain(plain)
	}

	addMessageAttachments(ctx, account, message)

	ctx.dispatcher.runHook("filter.build-message", message)

	return message
}

// buildReadReceiptMessage is Actions\Messages::buildReadReceiptMessage().
func buildReadReceiptMessage(ctx *Context, account *account.Account) *mime.BuiltMessage {
	readReceipt := ctx.stringParam("readReceipt", "")
	subject := ctx.stringParam("subject", "")
	text := ctx.stringParam("plain", "")

	if "" == readReceipt || "" == subject || "" == text || nil == account {
		// The PHP method throws; the action reports UnknownError (999).
		return nil
	}

	options := mime.BuildOptions{
		HideXMailer:    ctx.Config().GetBool("security", "hide_x_mailer_header", true),
		Version:        ctx.Version(),
		BoundaryPrefix: strings.TrimSpace(ctx.Config().GetString("labs", "boundary_prefix", "")),
	}
	message := mime.NewBuiltMessage(options)

	identity := mime.NewEmail(account.Email(), account.Name())
	replyTo := ""
	if provider := ctx.identities(); nil != provider {
		// Actions\Accounts::GetIdentityByID($oAccount, '', true).
		resolvedIdentity := ctx.getIdentityByID(account, "", true)
		if nil == resolvedIdentity {
			// PHP: a missing identity is UnknownError, which the action reports.
			return nil
		}
		identity = mime.NewEmail(resolvedIdentity.Email(), resolvedIdentity.Name())
		replyTo = resolvedIdentity.ReplyTo()
	}

	message.SetFrom(identity)
	message.RegenerateMessageID(identity.GetDomain())
	if "" != replyTo {
		message.SetReplyTo(mime.NewEmailCollection(replyTo))
	}
	message.SetSubject(subject)
	message.SetTo(mime.NewEmailCollection(readReceipt))

	ctx.dispatcher.runHook("filter.read-receipt-message-plain", account, message, &text)
	message.AddPlain(text)

	ctx.dispatcher.runHook("filter.build-read-receipt-message", message, account)
	return message
}

// addMessageAttachments is the attachments loop of buildMessage().
func addMessageAttachments(ctx *Context, account *account.Account, message *mime.BuiltMessage) {
	raw, ok := ctx.GetParam("attachments", nil).(map[string]any)
	if !ok || 0 == len(raw) || nil == ctx.dispatcher.files {
		return
	}

	// The PHP array preserves the JSON key order; Go maps do not, so the
	// temporary names are sorted to keep the output deterministic.
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, tempName := range names {
		description, _ := raw[tempName].(map[string]any)
		file, err := ctx.dispatcher.files.Open(account, tempName)
		if nil != err {
			continue
		}
		data, readErr := io.ReadAll(file)
		_ = file.Close()
		if nil != readErr {
			continue
		}

		fileName := phpString(description["name"])
		contentType := phpString(description["type"])
		if "" == contentType {
			contentType = mime.FromFilename(fileName)
		}
		inline := phpTruthy(description["inline"])
		cid := phpString(description["cId"])
		location := phpString(description["location"])

		message.AddAttachment(&mime.BuildAttachment{
			Data:            data,
			FileName:        fileName,
			ContentType:     contentType,
			Inline:          inline,
			Linked:          false, // HtmlUtils::BuildHtml() is not ported
			ContentID:       cid,
			ContentLocation: location,
		})
	}
}

// deleteMessageAttachments is Actions\Messages::deleteMessageAttachments().
func deleteMessageAttachments(ctx *Context, account *account.Account) {
	attachments, _ := ctx.GetParam("attachments", nil).(map[string]any)
	if nil == attachments || nil == ctx.dispatcher.files {
		return
	}
	for tempName := range attachments {
		if ctx.dispatcher.files.FileExists(account, tempName) {
			ctx.dispatcher.files.Clear(account, tempName)
		}
	}
}

// draftInfoParam is GetActionParam('draftInfo', null) as the PHP array.
func draftInfoParam(ctx *Context) []any {
	values, ok := ctx.GetParam("draftInfo", nil).([]any)
	if !ok {
		return nil
	}
	return values
}

// autocryptHeaders is the array_map of buildMessage():
// "addr={$header['addr']}; keydata={$header['keydata']}".
func autocryptHeaders(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		header, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, "addr="+phpString(header["addr"])+"; keydata="+phpString(header["keydata"]))
	}
	return result
}
