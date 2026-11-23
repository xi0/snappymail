package handler

// \RainLoop\Actions\Messages::DoMessage(): one message with all its parts.
//
// The folder/uid pair comes either from the POST parameters or from the RawKey
// of the GET form (?/Json/&q[]=/0/Message/&q[]=/<RawKey>/) which also carries
// the account hash. Failures of the mailbox are reported as CantGetMessage
// (202), exactly like the PHP try/catch.

import (
	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// hookResultMessage is the plugin hook fired before the message is rendered.
const hookResultMessage = "filter.result-message"

// doMessage is Actions\Messages::DoMessage().
func doMessage(ctx *Context) (any, error) {
	// initMailClientConnection() reports InvalidToken/ConnectionError/AuthError
	// and guarantees a logged-in client, like the PHP helper.
	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, appctx.NewClientError(appctx.CantGetMessage, nil, "")
	}

	folder := ""
	uid := 0

	// RawKey: [folder, uid, useThreads, accountHash] or the same as an object.
	if values := decodeRawKey(ctx.stringParam("RawKey", ""), ctx.accountHash()); 2 <= len(values) {
		folder = phpString(values["0"])
		uid = phpIntValue(values["1"])
	} else {
		folder = ctx.stringParam("folder", "")
		uid = ctx.intParam("uid")
	}

	message, err := client.Message(folder, uid, true)
	if nil != err {
		return nil, appctx.NewClientError(appctx.CantGetMessage, err, "")
	}
	if nil == message {
		return nil, appctx.NewClientError(appctx.CantGetMessage, nil, "")
	}

	// The S/MIME and PGP auto-verification of DoMessage() needs the pgp/smime
	// packages of steps 25/26; until they exist the message is returned
	// unverified (the signature parts themselves are still reported, see
	// Message::fromFetchResponse()).
	//
	// Actions::verifyCacheByKey($oMessage->ETag(...)) and cacheByKey() are the
	// HTTP 304 handling of step 33 and are no-op stubs, so the ETag is not
	// needed yet.

	ctx.dispatcher.runHook(hookResultMessage, message)

	return ctx.DefaultResponse(message, nil), nil
}
