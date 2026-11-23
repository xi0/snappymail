package handler

// The folder actions of \RainLoop\Actions\Folders.
//
// Every action starts with initMailClientConnection() (Context.MailClient),
// which reports InvalidToken/ConnectionError/AuthError exactly like the PHP
// helper, and the folder specific failures use the notification codes of
// Notifications (400 CantCreateFolder … 405, 901 MailServerError).

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// checkableFolderKey is the settings key of the folder check list.
const checkableFolderKey = "CheckableFolder"

// capabilityFilter is the preg_match() of DoFolders(): the capabilities that are
// protocol plumbing rather than features.
var capabilityFilter = regexp.MustCompile(`^(IMAP|AUTH|LOGIN|SASL)`)

// RegisterFolders registers the DoFolder* / DoSystemFoldersUpdate actions.
func RegisterFolders(d *Dispatcher) {
	d.Register("Folders", doFolders)
	d.Register("FolderCreate", doFolderCreate)
	d.Register("FolderRename", doFolderRename)
	d.Register("FolderDelete", doFolderDelete)
	d.Register("FolderClear", doFolderClear)
	d.Register("FolderInformation", doFolderInformation)
	d.Register("FolderInformationMultiply", doFolderInformationMultiply)
	d.Register("FolderSubscribe", doFolderSubscribe)
	d.Register("FolderSetMetadata", doFolderSetMetadata)
	d.Register("FolderSettings", doFolderSettings)
	d.Register("FolderCheckable", doFolderCheckable)
	d.Register("FolderACL", doFolderACL)
	d.Register("FolderDeleteACL", doFolderDeleteACL)
	d.Register("FolderSetACL", doFolderSetACL)
	d.Register("SystemFoldersUpdate", doSystemFoldersUpdate)
	d.Register("FolderAppend", doFolderAppend)
}

// doFolders is Actions\Folders::DoFolders().
func doFolders(ctx *Context) (any, error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return ctx.DefaultResponse(nil, nil), nil
	}

	hideUnsubscribed := false
	if nil != account {
		hideUnsubscribed = phpTruthy(ctx.LocalSettings(account).GetConf("HideUnsubscribed", false))
	}

	collection, err := client.Folders("", "*", hideUnsubscribed)
	if nil != err {
		return nil, err
	}

	imap := client.ImapClient()
	namespaces := imap.GetNamespaces()

	if nil != namespaces {
		// https://github.com/the-djmaze/snappymail/issues/1438: a server that
		// advertises NAMESPACE but refuses the LIST of its folders must not fail
		// the whole folder list.
		for _, entries := range [][]imapclient.NamespaceEntry{namespaces.OtherUsers, namespaces.Shared} {
			if 0 == len(entries) {
				continue
			}
			extra, extraErr := client.Folders(entries[0].Prefix, "*", hideUnsubscribed)
			if nil != extraErr || nil == extra {
				continue
			}
			for _, folder := range extra.Iterate() {
				if nil == collection {
					collection = imapclient.NewFolderCollection()
				}
				collection.Set(folder)
			}
		}
	}

	var result any
	if nil != collection {
		quota := imap.QuotaRoot("INBOX")

		capabilities := []string{}
		for _, capability := range imap.Capability() {
			if !capabilityFilter.MatchString(capability) {
				capabilities = append(capabilities, capability)
			}
		}

		serialized, _ := collection.JSONSerialize().(map[string]any)
		if nil == serialized {
			serialized = map[string]any{}
		}

		serialized["quotaUsage"] = nullableInt(quota, 0)
		serialized["quotaLimit"] = nullableInt(quota, 1)
		serialized["namespace"] = ""
		// PHP writes null (not "no namespaces") when the server has none.
		var namespacesValue any
		if nil != namespaces {
			serialized["namespace"] = namespaces.GetPersonalPrefix()
			namespacesValue = namespaces
		}
		serialized["namespaces"] = namespacesValue
		serialized["capabilities"] = capabilities

		result = serialized
	}

	return ctx.DefaultResponse(result, nil), nil
}

// nullableInt is "$aQuota ? $aQuota[$index] * 1024 : null": an empty quota list
// yields null, and an unreported usage yields 0.
func nullableInt(quota []int, index int) any {
	if nil == quota {
		return nil
	}
	if index >= len(quota) {
		return 0
	}
	return quota[index] * 1024
}

// doFolderCreate is Actions\Folders::DoFolderCreate().
func doFolderCreate(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		folder, err := client.FolderCreate(
			ctx.stringParam("folder", ""),
			ctx.stringParam("parent", ""),
			ctx.boolParam("subscribe"),
		)
		if nil != err {
			return nil, appctx.NewClientError(appctx.CantCreateFolder, err, "")
		}
		return ctx.DefaultResponse(folder, nil), nil
	}

	return ctx.DefaultResponse(nil, nil), nil
}

// doFolderSetMetadata is Actions\Folders::DoFolderSetMetadata().
func doFolderSetMetadata(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		folder := ctx.stringParam("folder", "")
		key := ctx.stringParam("key", "")
		if "" != folder && "" != key {
			if err := client.ImapClient().FolderSetMetadata(folder, []imapclient.MetadataEntry{
				{Key: key, Value: optionalString(ctx.GetParam("value", nil))},
			}); nil != err {
				return nil, err
			}
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderSettings is Actions\Folders::DoFolderSettings().
func doFolderSettings(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		folder := ctx.stringParam("folder", "")

		// DoFolderSubscribe: a failure is ignored here (DoFolderSubscribe
		// reports it on its own).
		if ctx.boolParam("subscribe") {
			_ = client.ImapClient().FolderSubscribe(folder)
		} else {
			_ = client.ImapClient().FolderUnsubscribe(folder)
		}

		// DoFolderCheckable
		if err := ctx.setFolderCheckable(folder, ctx.boolParam("checkable")); nil != err {
			return nil, err
		}

		// DoFolderSetMetadata (Kolab folder type)
		if key, value, ok := kolabParam(ctx); ok {
			_ = client.ImapClient().FolderSetMetadata(folder, []imapclient.MetadataEntry{
				{Key: key, Value: value},
			})
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderSubscribe is Actions\Folders::DoFolderSubscribe().
func doFolderSubscribe(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		folder := ctx.stringParam("folder", "")
		subscribe := ctx.boolParam("subscribe")

		var err error
		if subscribe {
			err = client.ImapClient().FolderSubscribe(folder)
		} else {
			err = client.ImapClient().FolderUnsubscribe(folder)
		}
		if nil != err {
			if subscribe {
				return nil, appctx.NewClientError(appctx.CantSubscribeFolder, err, "")
			}
			return nil, appctx.NewClientError(appctx.CantUnsubscribeFolder, err, "")
		}
	}

	return ctx.TrueResponse(nil), nil
}

// setFolderCheckable is Actions\Folders::SetFolderCheckable().
func (c *Context) setFolderCheckable(fullName string, checkable bool) error {
	account, err := c.requireAccount()
	if nil != err {
		return err
	}

	settings := c.LocalSettings(account)
	folders := phpArrayOf(decodeCheckableFolders(settings.GetConf(checkableFolderKey, "[]")))

	if checkable {
		folders.append(fullName)
	} else if index, ok := folders.indexOf(fullName); ok {
		folders.spliceRemove(index)
	}

	settings.SetConf(checkableFolderKey, folders.unique().encode())

	// The list the cached response objects used is now stale.
	c.checkableDone = true
	c.checkableFolders = folders.unique().valuesCopy()

	return c.SaveLocalSettings(account, settings)
}

// doFolderCheckable is Actions\Folders::DoFolderCheckable().
func doFolderCheckable(ctx *Context) (any, error) {
	err := ctx.setFolderCheckable(ctx.stringParam("folder", ""), ctx.boolParam("checkable"))
	if nil != err {
		return nil, err
	}
	return ctx.DefaultResponse(true, nil), nil
}

// doFolderRename is Actions\Folders::DoFolderRename().
func doFolderRename(ctx *Context) (any, error) {
	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return ctx.TrueResponse(nil), nil
	}

	oldName := ctx.stringParam("oldName", "")
	newName := ctx.stringParam("newName", "")

	delimiter, delimiterOK := client.ImapClient().FolderHierarchyDelimiter(oldName)

	if err := client.FolderRename(oldName, newName); nil != err {
		return nil, appctx.NewClientError(appctx.CantRenameFolder, err, "")
	}

	// DoFolderSubscribe: a failure is ignored.
	if ctx.boolParam("subscribe") {
		_ = client.ImapClient().FolderSubscribe(newName)
	} else {
		_ = client.ImapClient().FolderUnsubscribe(newName)
	}

	// DoFolderCheckable: the renamed folder and its children move along.
	account, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	separator := ""
	if delimiterOK {
		separator = delimiter
	}

	settings := ctx.LocalSettings(account)
	folders := phpArrayOf(decodeCheckableFolders(settings.GetConf(checkableFolderKey, "[]")))

	var remove []string
	for _, folder := range folders.valuesCopy() {
		if !strings.HasPrefix(folder+separator, oldName+separator) {
			continue
		}
		remove = append(remove, folder)
		if folder != oldName {
			folders.append(newName + separator + folder[len(oldName)+1:])
		}
	}
	for _, folder := range remove {
		folders = folders.remove(folder)
	}

	if ctx.boolParam("checkable") {
		folders.append(newName)
	}

	settings.SetConf(checkableFolderKey, folders.unique().encode())
	if err := ctx.SaveLocalSettings(account, settings); nil != err {
		return nil, err
	}

	// DoFolderSetMetadata (Kolab folder type)
	if key, value, ok := kolabParam(ctx); ok {
		_ = client.ImapClient().FolderSetMetadata(newName, []imapclient.MetadataEntry{
			{Key: key, Value: value},
		})
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderDelete is Actions\Folders::DoFolderDelete().
func doFolderDelete(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		if err := client.ImapClient().FolderDelete(ctx.stringParam("folder", "")); nil != err {
			if isNonEmptyFolder(err) {
				return nil, appctx.NewClientError(appctx.CantDeleteNonEmptyFolder, err, "")
			}
			return nil, appctx.NewClientError(appctx.CantDeleteFolder, err, "")
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderClear is Actions\Folders::DoFolderClear().
func doFolderClear(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		if err := client.ImapClient().FolderClear(ctx.stringParam("folder", "")); nil != err {
			return nil, appctx.NewClientError(appctx.MailServerError, err, "")
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderInformation is Actions\Folders::DoFolderInformation().
func doFolderInformation(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		// PHP casts the client supplied list into a validated SequenceSet, so
		// an invalid entry can never reach the IMAP command.
		flagsRange := imapclient.NewSequenceSet(ctx.GetParam("flagsUids", []any{}), true)

		info, err := client.FolderInformation(
			ctx.stringParam("folder", ""),
			ctx.intParam("uidNext"),
			flagsRange,
		)
		if nil != err {
			return nil, appctx.NewClientError(appctx.MailServerError, err, "")
		}
		return ctx.DefaultResponse(info, nil), nil
	}

	return ctx.DefaultResponse(map[string]any{}, nil), nil
}

// doFolderInformationMultiply is Actions\Folders::DoFolderInformationMultiply().
func doFolderInformationMultiply(ctx *Context) (any, error) {
	result := []any{}

	raw := ctx.GetParam("folders", nil)
	folders, isList := raw.([]any)
	if !isList {
		return ctx.DefaultResponse(result, nil), nil
	}

	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}

	seen := make(map[string]struct{}, len(folders))
	for _, item := range folders {
		name, ok := item.(string)
		if !ok {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}

		if nil == client {
			continue
		}

		info, infoErr := client.FolderInformation(name, 0, nil)
		if nil != infoErr {
			ctx.dispatcher.logException(infoErr)
			continue
		}
		result = append(result, info)
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSystemFoldersUpdate is Actions\Folders::DoSystemFoldersUpdate().
func doSystemFoldersUpdate(ctx *Context) (any, error) {
	account, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	settings := ctx.LocalSettings(account)
	settings.SetConf("SentFolder", ctx.stringParam("sent", ""))
	settings.SetConf("DraftsFolder", ctx.stringParam("drafts", ""))
	settings.SetConf("JunkFolder", ctx.stringParam("junk", ""))
	settings.SetConf("TrashFolder", ctx.stringParam("trash", ""))
	settings.SetConf("ArchiveFolder", ctx.stringParam("archive", ""))

	if err := ctx.SaveLocalSettings(account, settings); nil != err {
		return ctx.DefaultResponse(false, nil), nil
	}

	return ctx.DefaultResponse(true, nil), nil
}

// doFolderACL is Actions\Folders::DoFolderACL().
func doFolderACL(ctx *Context) (any, error) {
	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return ctx.DefaultResponse(map[string]any{
			"@Object":     "Collection/FolderACL",
			"@Collection": []any{},
		}, nil), nil
	}

	folder := ctx.stringParam("folder", "")
	rights := client.ImapClient().FolderGetACL(folder)

	collection := make([]any, 0, len(rights))
	for _, acl := range rights {
		collection = append(collection, acl)
	}

	return ctx.DefaultResponse(map[string]any{
		"@Object":     "Collection/FolderACL",
		"@Collection": collection,
	}, nil), nil
}

// doFolderDeleteACL is Actions\Folders::DoFolderDeleteACL().
func doFolderDeleteACL(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		if err := client.ImapClient().FolderDeleteACL(
			ctx.stringParam("folder", ""),
			ctx.stringParam("identifier", ""),
		); nil != err {
			return nil, err
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderSetACL is Actions\Folders::DoFolderSetACL().
func doFolderSetACL(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		if err := client.ImapClient().FolderSetACL(
			ctx.stringParam("folder", ""),
			ctx.stringParam("identifier", ""),
			ctx.stringParam("rights", ""),
		); nil != err {
			return nil, err
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doFolderAppend is Actions\Folders::DoFolderAppend(): append an uploaded rfc822
// message to a mailbox.
//
// Partial: PHP reads $_FILES['appendFile']; Go takes the same multipart field
// from the request the dispatcher already parsed.
func doFolderAppend(ctx *Context) (any, error) {
	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}

	folder := ctx.stringParam("folder", "")

	if !ctx.Config().GetBool("labs", "allow_message_append", false) {
		return ctx.FalseResponse(999, "Permission denied", ""), nil
	}

	uploaded, header, ok := uploadedFile(ctx.Request, "appendFile")
	if !ok {
		return ctx.FalseResponse(999, "No file", ""), nil
	}
	defer uploaded.Close()

	if nil == account || "" == folder || nil == client || nil == ctx.dispatcher.files {
		return ctx.FalseResponse(999, "", ""), nil
	}

	savedName := "append-post-" + md5String(folder+header.Filename+tempFileName(uploaded))
	files := ctx.dispatcher.files

	if err := files.MoveUploadedFile(account, savedName, tempFileName(uploaded)); nil != err {
		return ctx.FalseResponse(999, "", ""), nil
	}

	size, exists := files.FileSize(account, savedName)
	if !exists {
		return ctx.FalseResponse(999, "", ""), nil
	}

	stream, err := files.Open(account, savedName)
	if nil != err {
		return ctx.FalseResponse(999, "", ""), nil
	}
	defer stream.Close()

	if _, _, err := client.ImapClient().MessageAppendStream(folder, stream, size); nil != err {
		return nil, err
	}

	files.Clear(account, savedName)

	return ctx.TrueResponse(nil), nil
}

// uploadedFile is the $_FILES[$field] lookup of a multipart request.
func uploadedFile(r *http.Request, field string) (multipart.File, *multipart.FileHeader, bool) {
	if nil == r || nil == r.MultipartForm || nil == r.MultipartForm.File {
		return nil, nil, false
	}

	headers := r.MultipartForm.File[field]
	if 0 == len(headers) {
		return nil, nil, false
	}

	file, err := headers[0].Open()
	if nil != err {
		return nil, nil, false
	}
	return file, headers[0], true
}

// tempFileName is the $_FILES[...]['tmp_name'] equivalent: the on-disk name of
// the temporary upload, which makes the append key unique per upload.
func tempFileName(file multipart.File) string {
	if named, ok := file.(interface{ Name() string }); ok {
		return named.Name()
	}
	return ""
}

// isNonEmptyFolder reports MailSo\Mail\Exceptions\NonEmptyFolder behind the
// wrapping of the command layer.
func isNonEmptyFolder(err error) bool {
	return errors.Is(err, imapclient.ErrNonEmptyFolder)
}

// md5String is PHP's md5().
func md5String(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// phpIntValue is PHP's (int) cast for the scalars a JSON parameter holds.
func phpIntValue(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		return leadingInteger(typed)
	}
	return 0
}

// leadingInteger is PHP's intval() for a string: the leading decimal number.
func leadingInteger(value string) int {
	value = strings.TrimSpace(value)

	negative := false
	if 0 < len(value) && ('+' == value[0] || '-' == value[0]) {
		negative = '-' == value[0]
		value = value[1:]
	}

	end := 0
	for end < len(value) && '0' <= value[end] && '9' >= value[end] {
		end++
	}
	if 0 == end {
		return 0
	}

	number := 0
	for _, character := range value[:end] {
		number = number*10 + int(character-'0')
	}
	if negative {
		return -number
	}
	return number
}

// stringParam is GetActionParam() with a string default.
func (c *Context) stringParam(key, defaultValue string) string {
	return phpString(c.GetParam(key, defaultValue))
}

// intParam is (int) GetActionParam($key, $default).
func (c *Context) intParam(key string) int {
	return phpIntValue(c.GetParam(key, 0))
}

// boolParam is !empty(GetActionParam($key, 0)).
func (c *Context) boolParam(key string) bool {
	return phpTruthy(c.GetParam(key, 0))
}

// optionalString is "$value ?: null" of the PHP call sites.
func optionalString(value any) *string {
	if phpEmpty(value) {
		return nil
	}
	text := phpString(value)
	return &text
}

// kolabParam is the "kolab" action parameter of DoFolderSettings/DoFolderRename.
func kolabParam(ctx *Context) (key string, value *string, ok bool) {
	kolab, isMap := ctx.GetParam("kolab", nil).(map[string]any)
	if !isMap {
		return "", nil, false
	}

	key, _ = kolab["type"].(string)
	if "" == key {
		return "", nil, false
	}

	return key, optionalString(kolab["value"]), true
}
