package handler

// The message state mutations of \RainLoop\Actions\Messages (Step 18):
// DoMessageSetSeen, DoMessageSetSeenToAll, DoMessageSetFlagged,
// DoMessageSetDeleted, DoMessageSetKeyword, DoMessageDelete, DoMessageMove and
// DoMessageCopy.
//
// Every action starts with initMailClientConnection() (Context.MailClient), so
// the InvalidToken/ConnectionError/AuthError codes of the PHP helper are
// reported unchanged. The flag mutations report MailServerError (901), the
// delete/move/copy actions their own code (203/204/205), exactly like PHP.
//
// Notes on the ported behaviour:
//
//   - $uids is a client supplied comma separated list. Both sides wrap it in a
//     SequenceSet, so anything that is not a valid RFC 3501 sequence-set item is
//     dropped before it can reach the IMAP command. An empty list is a no-op
//     (MessageSetFlag returns early, MessageDelete/Move/Copy fail).
//   - DoMessageDelete returns [null] when the folder has no STATUS hash,
//     because PHP interpolates the never assigned $sFromFolder there; the quirk
//     is ported as-is.
//   - capa.dangerous_actions is a frontend capability only (AppData.Capa);
//     PHP has no server side gate for delete/clear, so none is added here.

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// RegisterMessageActions registers the DoMessage* state mutations.
func RegisterMessageActions(d *Dispatcher) {
	d.Register("MessageSetSeen", doMessageSetSeen)
	d.Register("MessageSetSeenToAll", doMessageSetSeenToAll)
	d.Register("MessageSetFlagged", doMessageSetFlagged)
	d.Register("MessageSetDeleted", doMessageSetDeleted)
	d.Register("MessageSetKeyword", doMessageSetKeyword)
	d.Register("MessageDelete", doMessageDelete)
	d.Register("MessageMove", doMessageMove)
	d.Register("MessageCopy", doMessageCopy)
}

// doMessageSetSeen is Actions\Messages::DoMessageSetSeen().
func doMessageSetSeen(ctx *Context) (any, error) {
	return messageSetFlag(ctx, imapclient.FlagSeen, false)
}

// doMessageSetFlagged is Actions\Messages::DoMessageSetFlagged().
func doMessageSetFlagged(ctx *Context) (any, error) {
	return messageSetFlag(ctx, imapclient.FlagFlagged, true)
}

// doMessageSetDeleted is Actions\Messages::DoMessageSetDeleted().
func doMessageSetDeleted(ctx *Context) (any, error) {
	return messageSetFlag(ctx, imapclient.FlagDeleted, true)
}

// doMessageSetKeyword is Actions\Messages::DoMessageSetKeyword(): a custom
// keyword from the folder's optional tags.
func doMessageSetKeyword(ctx *Context) (any, error) {
	return messageSetFlag(ctx, ctx.stringParam("keyword", ""), true)
}

// messageSetFlag is Actions\Messages::messageSetFlag(): the shared body of the
// four flag actions.
func messageSetFlag(ctx *Context, messageFlag string, skipUnsupportedFlag bool) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		if err := client.MessageSetFlag(
			ctx.stringParam("folder", ""),
			sequenceSetParam(ctx, "uids"),
			messageFlag,
			ctx.boolParam("setAction"),
			skipUnsupportedFlag,
		); nil != err {
			return nil, appctx.NewClientError(appctx.MailServerError, err, "")
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doMessageSetSeenToAll is Actions\Messages::DoMessageSetSeenToAll(): mark the
// whole mailbox (or the given thread uids, i.e. the messages of the expanded
// thread only) as seen/unseen.
func doMessageSetSeenToAll(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		threadUIDs := strings.TrimSpace(ctx.stringParam("threadUids", ""))

		// empty($sThreadUids) ? SequenceSet('1:*', false) : SequenceSet(explode(',')).
		// The all-messages form addresses sequence numbers, the thread form UIDs.
		var rangeSet *imapclient.SequenceSet
		if phpEmpty(threadUIDs) {
			rangeSet = imapclient.NewSequenceSet("1:*", false)
		} else {
			rangeSet = imapclient.NewSequenceSet(strings.Split(threadUIDs, ","), true)
		}

		if err := client.MessageSetFlag(
			ctx.stringParam("folder", ""),
			rangeSet,
			imapclient.FlagSeen,
			ctx.boolParam("setAction"),
			false,
		); nil != err {
			return nil, appctx.NewClientError(appctx.MailServerError, err, "")
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doMessageDelete is Actions\Messages::DoMessageDelete(): expunge the messages
// of the range (UID EXPUNGE with UIDPLUS, or a full EXPUNGE when
// imap.use_expunge_all_on_delete is on).
func doMessageDelete(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		folder := ctx.stringParam("folder", "")

		if err := client.ImapClient().MessageDelete(folder, sequenceSetParam(ctx, "uids"), true); nil != err {
			return nil, appctx.NewClientError(appctx.CantDeleteMessage, err, "")
		}

		if hash := client.FolderHash(folder); "" != hash {
			return ctx.DefaultResponse([]any{folder, hash}, nil), nil
		}

		// PHP: array($sHash ? ... : array($sFromFolder)) where $sFromFolder is
		// never assigned in DoMessageDelete, so the fallback is [null].
		return ctx.DefaultResponse([]any{nil}, nil), nil
	}

	return ctx.TrueResponse(nil), nil
}

// doMessageMove is Actions\Messages::DoMessageMove(): optionally mark the
// messages as read and learn them as spam/ham, then MOVE them.
func doMessageMove(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		fromFolder := ctx.stringParam("fromFolder", "")
		toFolder := ctx.stringParam("toFolder", "")
		rangeSet := sequenceSetParam(ctx, "uids")

		// A failing \Seen/$Junk store must not stop the move, like PHP's
		// "catch { unset($oException); }".
		if ctx.boolParam("markAsRead") {
			_ = client.MessageSetFlag(fromFolder, rangeSet, imapclient.FlagSeen, true, false)
		}

		switch ctx.stringParam("learning", "") {
		case "SPAM":
			_ = client.MessageSetFlag(fromFolder, rangeSet, imapclient.FlagJunk, true, false)
			_ = client.MessageSetFlag(fromFolder, rangeSet, imapclient.FlagNotJunk, false, false)
		case "HAM":
			_ = client.MessageSetFlag(fromFolder, rangeSet, imapclient.FlagNotJunk, true, false)
			_ = client.MessageSetFlag(fromFolder, rangeSet, imapclient.FlagJunk, false, false)
		}

		if err := client.ImapClient().MessageMove(fromFolder, toFolder, rangeSet); nil != err {
			return nil, appctx.NewClientError(appctx.CantMoveMessage, err, "")
		}

		if hash := client.FolderHash(fromFolder); "" != hash {
			return ctx.DefaultResponse([]any{fromFolder, hash}, nil), nil
		}
		return ctx.DefaultResponse([]any{fromFolder}, nil), nil
	}

	return ctx.TrueResponse(nil), nil
}

// doMessageCopy is Actions\Messages::DoMessageCopy(): COPY the messages and
// return the [folder, hash] pair of the destination.
func doMessageCopy(ctx *Context) (any, error) {
	if _, client, err := ctx.MailClient(); nil != err {
		return nil, err
	} else if nil != client {
		toFolder := ctx.stringParam("toFolder", "")

		if err := client.ImapClient().MessageCopy(
			ctx.stringParam("fromFolder", ""),
			toFolder,
			sequenceSetParam(ctx, "uids"),
		); nil != err {
			return nil, appctx.NewClientError(appctx.CantCopyMessage, err, "")
		}

		if hash := client.FolderHash(toFolder); "" != hash {
			return ctx.DefaultResponse([]any{toFolder, hash}, nil), nil
		}
		return ctx.DefaultResponse([]any{toFolder}, nil), nil
	}

	return ctx.TrueResponse(nil), nil
}

// sequenceSetParam is new SequenceSet(explode(',', (string) GetActionParam($key, ''))):
// the validated set of the comma separated UID/sequence list.
func sequenceSetParam(ctx *Context, key string) *imapclient.SequenceSet {
	return imapclient.NewSequenceSet(strings.Split(ctx.stringParam(key, ""), ","), true)
}
