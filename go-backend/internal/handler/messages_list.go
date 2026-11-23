package handler

// The message actions of \RainLoop\Actions\Messages. Step 16 ports the message
// list (DoMessageList); steps 17–20 add the view, flags/delete/move and send
// actions in their own files.

import (
	"encoding/base64"
	"encoding/json"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/mail"
)

// RegisterMessages registers the DoMessageList action.
func RegisterMessages(d *Dispatcher) {
	d.Register("MessageList", doMessageList)
}

// doMessageList is Actions\Messages::DoMessageList().
func doMessageList(ctx *Context) (any, error) {
	params := mail.NewMessageListParams()

	hash := ""
	if values := decodeRawKeyMap(ctx.stringParam("RawKey", "")); 7 <= len(values) {
		// GET: the RawKey carries the whole list request (it has no account
		// hash; it only selects a folder/list of the token authenticated
		// account).
		hash = phpString(values["hash"])
		params.FolderName = phpString(values["folder"])
		params.Limit = phpIntValue(values["limit"])
		params.Offset = phpIntValue(values["offset"])
		params.Search = phpString(values["search"])
		params.Sort = phpString(values["sort"])
		if uidNext, ok := values["uidNext"]; ok {
			params.PrevUIDNext = phpIntValue(uidNext)
		}
		params.UseThreads = phpTruthy(values["useThreads"])
		if params.UseThreads {
			if value, ok := values["threadUid"]; ok {
				params.ThreadUid = phpIntValue(value)
			}
			if value, ok := values["threadAlgorithm"]; ok {
				params.ThreadAlgorithm = phpString(value)
			}
		}
	} else {
		// POST
		params.FolderName = ctx.stringParam("folder", "")
		params.Offset = phpIntValue(ctx.GetParam("offset", 0))
		params.Limit = phpIntValue(ctx.GetParam("limit", 10))
		params.Search = ctx.stringParam("search", "")
		params.Sort = ctx.stringParam("sort", "")
		params.PrevUIDNext = phpIntValue(ctx.GetParam("uidNext", 0))
		params.UseThreads = phpTruthy(ctx.GetParam("useThreads", "0"))
		if params.UseThreads {
			params.ThreadUid = phpIntValue(ctx.GetParam("threadUid", ""))
			params.ThreadAlgorithm = ctx.stringParam("threadAlgorithm", "")
		}
	}

	if "" == params.FolderName {
		return nil, appctx.NewClientError(appctx.CantGetMessageList, nil, "")
	}

	// Strict whitelist for the sort criteria (SORT/ESORT injection guard). The
	// capability dependent SORT=DISPLAY criteria are checked again in
	// mail.Client.MessageList().
	if !mail.ValidateSortCriteria(params.Sort, true) {
		return nil, appctx.NewClientError(appctx.InvalidInputArgument, nil, "Invalid sort criteria")
	}
	params.Sort = mail.NormalizeSortCriteria(params.Sort, true)

	account, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, appctx.NewClientError(appctx.CantGetMessageList, nil, "")
	}

	settings := ctx.LocalSettings(account)
	params.HideDeleted = phpTruthy(settings.GetConf("HideDeleted", 1))

	params.UseSort = true

	// The "hash" only feeds the HTTP 304 cache of Step 33; verifyCacheByKey()/
	// cacheByKey() are no-op stubs until then.
	_ = hash

	collection, err := client.MessageList(params)
	if nil != err {
		return nil, appctx.NewClientError(appctx.CantGetMessageList, err, "")
	}

	return ctx.DefaultResponse(collection, nil), nil
}

// decodeRawKeyMap is Actions::decodeRawKey() for a RawKey without an account
// hash: URL safe base64 of a JSON object, or nil.
func decodeRawKeyMap(rawKey string) map[string]any {
	if "" == rawKey {
		return nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(rawKey)
	if nil != err {
		if decoded, err = base64.URLEncoding.DecodeString(rawKey); nil != err {
			return nil
		}
	}

	var values map[string]any
	if nil != json.Unmarshal(decoded, &values) {
		return nil
	}
	return values
}
