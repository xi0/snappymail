package handler

// The attachment actions of Step 20:
//
//   - \RainLoop\Actions\Messages::DoMessageUploadAttachments() — turn the
//     RawKeys of a compose's message parts into files of the files provider and
//     report their temp name and MIME type.
//   - \RainLoop\Actions\Attachments::DoAttachmentsActions() — run an action on a
//     set of RawKeys. The only built-in action is `zip`, which packs the parts
//     into an archive stored in the files provider; other targets are handed to
//     the json.attachments plugin hook.
//
// Both start with initMailClientConnection() (Context.MailClient), so the
// InvalidToken/ConnectionError/AuthError codes of the PHP helper are reported
// unchanged, and both fetch message parts through MailClient::MessageMimeStream
// (internal/mail). A failed part is reported as false for that slot (upload) or
// as MailServerError (upload) / false result (attachments), like PHP.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// hookJSONAttachments is the plugin hook non-zip attachment targets run.
const hookJSONAttachments = "json.attachments"

// RegisterAttachments registers the attachment actions of step 20.
func RegisterAttachments(d *Dispatcher) {
	d.Register("MessageUploadAttachments", doMessageUploadAttachments)
	d.Register("AttachmentsActions", doAttachmentsActions)
}

// attachmentItem is one resolved RawKey of getMimeFileByHash(): the file name,
// the source uid/mimeIndex and the decoded data (data branch) or the stored
// file hash (message-part branch).
type attachmentItem struct {
	fileName  string
	uid       int
	mimeIndex string

	// hasData tells isset($aValues['data']): the RawKey carried the content
	// inline (an empty data value stays a "data" item, exactly like PHP).
	hasData bool
	data    []byte

	// fileHash is the stored blob of the message-part branch.
	fileHash string
}

// doMessageUploadAttachments is Actions\Messages::DoMessageUploadAttachments().
func doMessageUploadAttachments(ctx *Context) (result any, err error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}

	// PHP wraps the whole body in try/catch and reports MailServerError.
	defer func() {
		if recovered := recover(); nil != recovered {
			result = nil
			err = appctx.NewClientError(appctx.MailServerError, asError(recovered), "")
		}
	}()

	files := ctx.dispatcher.files

	switch typed := ctx.GetParam("attachments", []any{}).(type) {
	case []any:
		list := make([]any, len(typed))
		for index, value := range typed {
			item, itemErr := ctx.uploadAttachment(account, client, files, phpString(value))
			if nil != itemErr {
				return nil, itemErr
			}
			list[index] = item
		}
		result = list
	case map[string]any:
		entries := make(map[string]any, len(typed))
		for key, value := range typed {
			item, itemErr := ctx.uploadAttachment(account, client, files, phpString(value))
			if nil != itemErr {
				return nil, itemErr
			}
			entries[key] = item
		}
		result = entries
	default:
		result = []any{}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// uploadAttachment is one iteration of the DoMessageUploadAttachments loop: the
// {tempName, mimeType} map of a stored part, or false when the RawKey could not
// be resolved.
func (c *Context) uploadAttachment(
	account *account.Account,
	client *mail.Client,
	files FilesProvider,
	rawKey string,
) (any, error) {
	if nil == files {
		return false, nil
	}

	values := decodeRawKey(rawKey, c.accountHash())
	if nil == values {
		return false, nil
	}

	folder := phpString(values["folder"])
	uid := phpIntValue(values["uid"])

	mimeIndex, err := c.validatePartID(phpString(values["mimeIndex"]))
	if nil != err {
		// PHP: validatePartId() throws InvalidInputArgument, caught by the
		// enclosing try/catch and re-reported as MailServerError.
		return nil, appctx.NewClientError(appctx.MailServerError, err, "")
	}

	tempName := storage.SHA1(rawKey)

	if files.FileExists(account, tempName) {
		handle, openErr := files.Open(account, tempName)
		if nil != openErr {
			return false, nil
		}
		data, _ := io.ReadAll(handle)
		_ = handle.Close()

		contentType := ""
		if detected := mime.FromContent(data, tempName); "" != detected {
			contentType = detected
		} else if detected := mime.FromFilename(tempName); "" != detected {
			contentType = detected
		} else {
			contentType = "application/octet-stream"
		}

		return map[string]any{"tempName": tempName, "mimeType": contentType}, nil
	}

	if nil == client {
		return false, nil
	}

	stored := false
	resolvedType := ""
	client.MessageMimeStream(func(reader io.ReadSeeker, contentType, fileName, _ string) {
		data, _ := io.ReadAll(reader)
		contentType = resolveUploadContentType(contentType, data, fileName)
		tempName += mime.ToExtension(contentType)
		if putErr := files.PutFile(account, tempName, bytes.NewReader(data)); nil == putErr {
			stored = true
			resolvedType = contentType
		}
	}, folder, uid, mimeIndex)

	if !stored {
		return false, nil
	}

	return map[string]any{"tempName": tempName, "mimeType": resolvedType}, nil
}

// doAttachmentsActions is Actions\Attachments::DoAttachmentsActions().
func doAttachmentsActions(ctx *Context) (any, error) {
	target := ctx.stringParam("target", "")
	folder := ctx.stringParam("folder", "")
	filename := storage.SecureFileName(ctx.stringParam("filename", ""))

	files := ctx.dispatcher.files
	capaAttachmentsActions := ctx.Config().GetBool("capa", "attachments_actions", false)

	if "" == target {
		ctx.dispatcher.logWrite("AttachmentsAction target not set", logx.Info, "")
	}
	if !capaAttachmentsActions {
		ctx.dispatcher.logWrite("AttachmentsAction disabled", logx.Info, "")
	}
	if nil == files {
		ctx.dispatcher.logWrite("AttachmentsAction FilesProvider inactive", logx.Info, "")
	}

	if "" == target || !capaAttachmentsActions || nil == files {
		return ctx.FalseResponse(0, "", ""), nil
	}

	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}

	hashes := arrayValues(ctx.GetParam("hashes", nil))
	if 0 == len(hashes) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	var data []*attachmentItem
	uids := map[int]struct{}{}
	buildError := false

	for _, value := range hashes {
		item, itemErr := ctx.getMimeFileByHash(account, client, files, phpString(value))
		if nil != itemErr {
			return nil, itemErr
		}
		dataEmpty := !item.hasData || 0 == len(item.data)
		if dataEmpty && "" == item.fileHash {
			ctx.dispatcher.logWrite("AttachmentsAction data/fileHash not set", logx.Info, "")
			buildError = true
			break
		}
		data = append(data, item)
		if "" != item.mimeIndex {
			uids[item.uid] = struct{}{}
		}
	}

	multiUID := 1 < len(uids)

	if buildError || 0 == len(data) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	var result any = false
	switch strings.ToLower(target) {
	case "zip":
		zipResult, zipErr := ctx.buildAttachmentZip(account, files, data, multiUID, folder, filename)
		if nil != zipErr {
			return nil, zipErr
		}
		result = zipResult

	default:
		action := &attachmentsAction{Action: target, Items: data, Result: false}
		ctx.dispatcher.runHook(hookJSONAttachments, action)
		result = action.Result
	}

	// The temporary blobs (the fetched message parts and a produced zip) are
	// cleared once the action ran, like PHP's "fileHash && Clear()" loop.
	for _, item := range data {
		if "" != item.fileHash {
			files.Clear(account, item.fileHash)
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// attachmentsAction mirrors SnappyMail\AttachmentsAction for the
// json.attachments plugin hook. Items and Result are the fields a plugin reads
// and writes.
type attachmentsAction struct {
	Action string
	Items  []*attachmentItem
	Result any
}

// buildAttachmentZip is the "zip" branch of DoAttachmentsActions(): pack the
// items into an archive and return the RawKey of the stored zip, or false when
// the archive could not be produced.
func (c *Context) buildAttachmentZip(
	account *account.Account,
	files FilesProvider,
	data []*attachmentItem,
	multiUID bool,
	folder, filename string,
) (any, error) {
	zipHash := sha1Rand("")

	buffer := &bytes.Buffer{}
	archive := zip.NewWriter(buffer)
	_ = archive.SetComment("SnappyMail/" + c.Version())

	failed := false
	for _, item := range data {
		// The fileName is attacker controlled (MIME headers / raw-key), so it is
		// sanitised to prevent zip-slip (../../x.php).
		prefix := ""
		if multiUID {
			prefix = strconv.Itoa(item.uid) + "/"
		} else if "" != folder {
			prefix = strconv.Itoa(item.uid) + "-"
		}
		entryName := prefix + storage.SecureFileName(item.fileName)

		writer, createErr := archive.Create(entryName)
		if nil != createErr {
			failed = true
			break
		}

		if item.hasData {
			if _, writeErr := writer.Write(item.data); nil != writeErr {
				failed = true
				break
			}
			continue
		}

		if "" == item.fileHash {
			failed = true
			break
		}
		handle, openErr := files.Open(account, item.fileHash)
		if nil != openErr {
			failed = true
			break
		}
		_, copyErr := io.Copy(writer, handle)
		_ = handle.Close()
		if nil != copyErr {
			failed = true
			break
		}
	}

	if closeErr := archive.Close(); nil != closeErr {
		failed = true
	}

	if failed {
		return false, nil
	}

	if err := files.PutFile(account, zipHash, bytes.NewReader(buffer.Bytes())); nil != err {
		return false, nil
	}

	baseName := filename
	if "" == baseName {
		if "" != folder {
			baseName = "messages"
		} else {
			baseName = "attachments"
		}
	}

	return c.encodeRawKey(map[string]any{
		"fileName": baseName + time.Now().Format("-20060102150405") + ".zip",
		"mimeType": "application/zip",
		"fileHash": zipHash,
	}), nil
}

// getMimeFileByHash is Actions\Attachments::getMimeFileByHash(): resolve one
// RawKey into an inline data blob or a stored message part.
func (c *Context) getMimeFileByHash(
	account *account.Account,
	client *mail.Client,
	files FilesProvider,
	rawKey string,
) (*attachmentItem, error) {
	values := decodeRawKey(rawKey, c.accountHash())

	fileName := ""
	if nil != values {
		fileName = storage.SecureFileName(phpString(values["fileName"]))
	}
	if "" == fileName {
		fileName = "file.dat"
	}

	item := &attachmentItem{fileName: fileName}
	if nil == values {
		// PHP decodeRawKey() returns [] for an invalid blob; the caller sees an
		// empty data/fileHash pair and reports an error.
		return item, nil
	}

	item.uid = phpIntValue(values["uid"])
	item.mimeIndex = phpString(values["mimeIndex"])

	if raw, ok := values["data"]; ok && nil != raw {
		decoded, _ := cryptox.URLSafeBase64Decode(phpString(raw))
		item.data = decoded
		item.hasData = true
		return item, nil
	}

	folder := phpString(values["folder"])
	uid := phpIntValue(values["uid"])
	if "" != folder && 0 < uid && nil != client {
		mimeIndex, err := c.validatePartID(item.mimeIndex)
		if nil != err {
			return nil, err
		}
		contentTypeIn := phpString(values["mimeType"])

		client.MessageMimeStream(func(reader io.ReadSeeker, _, _, _ string) {
			data, _ := io.ReadAll(reader)
			sHash := sha1Rand(item.fileName + "~" + contentTypeIn)
			if putErr := files.PutFile(account, sHash, bytes.NewReader(data)); nil == putErr {
				item.fileHash = sHash
			}
		}, folder, uid, mimeIndex)
	}

	return item, nil
}

// encodeRawKey is Actions::encodeRawKey(): the url-safe base64 JSON blob with
// the current account hash folded in.
func (c *Context) encodeRawKey(values map[string]any) string {
	document := make(map[string]any, len(values)+1)
	for key, value := range values {
		document[key] = value
	}
	if hash := c.accountHash(); "" != hash {
		document["accountHash"] = hash
	}

	encoded, err := encodeJSON(document)
	if nil != err {
		return ""
	}
	return cryptox.URLSafeBase64Encode(string(encoded))
}

// validatePartID is Actions::validatePartId(): only an empty id, TEXT or dot
// separated integers reach an IMAP FETCH item.
func (c *Context) validatePartID(partID string) (string, error) {
	partID = strings.TrimSpace(partID)
	if !imapclient.IsValidPartId(partID, true) {
		return "", appctx.NewClientError(appctx.InvalidInputArgument, nil, "Invalid MIME part id")
	}
	return partID, nil
}

// resolveUploadContentType is the "contentType ?: fromStream() ?: fromFilename()
// ?: application/octet-stream" chain of DoMessageUploadAttachments().
func resolveUploadContentType(contentType string, data []byte, fileName string) string {
	if "" != contentType {
		return contentType
	}
	if detected := mime.FromContent(data, fileName); "" != detected {
		return detected
	}
	if detected := mime.FromFilename(fileName); "" != detected {
		return detected
	}
	return "application/octet-stream"
}

// arrayValues is the foreach() of a PHP array parameter: the list order for a
// JSON list (preserved) and the key-sorted values for a JSON object (Go maps
// have no insertion order).
func arrayValues(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		result := make([]any, 0, len(keys))
		for _, key := range keys {
			result = append(result, typed[key])
		}
		return result
	}
	return nil
}

// sha1Rand is MailSo\Base\Utils::Sha1Rand(): sha1(additionalSalt . random).
func sha1Rand(additionalSalt string) string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); nil != err {
		return storage.SHA1(additionalSalt + strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	return storage.SHA1(additionalSalt + string(buffer))
}
