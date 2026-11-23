package handler

// Response ports \RainLoop\Actions\Response: the JSON envelope builders and the
// responseObject() model conversion.
//
// The envelope is a compatibility contract with the frontend: it always carries
// "Action" and "Result" (plus "epoch", added by the dispatcher), and a failure
// adds "code", "message", "messageAdditional" and "ExceptionCode".

import (
	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// JSONSerializable is \JsonSerializable: a model that knows its own JSON shape
// (MailSo\Mail\Message, MailSo\Imap\Folder, …). responseObject() passes the
// result of JSONSerialize() straight through, like PHP returns jsonSerialize().
type JSONSerializable interface {
	JSONSerialize() any
}

// responseObject is Actions\Response::responseObject(): convert a handler result
// into something json_encode() can render, recursing into arrays.
//
// parent is the PHP $sParent argument: only the Message branch of a *root*
// result ("" parent) applies the read-receipt suppression. ctx resolves the
// per-account settings and cache; it is nil when a response is built outside an
// action.
func (d *Dispatcher) responseObject(value any, parent string, ctx *Context) any {
	switch typed := value.(type) {
	case *mail.Message:
		// MailSo\Mail\Message: jsonSerialize() plus the read-receipt
		// suppression of Actions\Response::responseObject().
		result, _ := typed.JSONSerialize().(map[string]any)
		if nil == result {
			return d.responseObject(typed.JSONSerialize(), parent, ctx)
		}
		if "" == parent {
			suppressReadReceipt(ctx, result)
		}
		return d.responseObject(result, parent, ctx)

	case *imapclient.Folder:
		// MailSo\Imap\Folder: jsonSerialize() plus the per-account "checkable"
		// flag of Actions\Response::responseObject().
		result, _ := typed.JSONSerialize().(map[string]any)
		if nil == result {
			result = map[string]any{}
		}
		result["checkable"] = ctx.isCheckableFolder(typed.FullName)
		return result

	case JSONSerializable:
		// PHP's json_encode() recursively calls jsonSerialize() on nested
		// objects; Go's json.Marshal does not, so recurse here to keep the
		// nested EmailCollection/HeaderCollection/Message documents resolved.
		return d.responseObject(typed.JSONSerialize(), parent, ctx)

	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = d.responseObject(item, "Array", ctx)
		}
		return result

	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = d.responseObject(item, "Array", ctx)
		}
		return result

	default:
		return value
	}
}

// suppressReadReceipt is the read-receipt block of responseObject():
//
//	if (!$sParent && strlen($aResult['readReceipt'])
//	 && !in_array('$mdnsent', $flags) && !in_array('\answered', $flags)
//	 && '1' === $this->Cacher($oAccount)->Get(ReadReceiptCache(…), '0')) {
//		$aResult['readReceipt'] = '';
//	}
//
// i.e. the frontend must not offer to send a receipt that was already sent.
func suppressReadReceipt(ctx *Context, result map[string]any) {
	receipt, _ := result["readReceipt"].(string)
	if "" == receipt || hasFlag(result["flags"], "$mdnsent") || hasFlag(result["flags"], "\\answered") {
		return
	}

	cacher := ctx.Cacher()
	if nil == cacher {
		return
	}

	resolved, err := ctx.Account(false)
	if nil != err {
		return
	}

	folder, _ := result["folder"].(string)
	uid := phpIntValue(result["uid"])

	if "1" == cacher.GetDefault(storage.ReadReceiptCacheKey(emailOf(resolved), folder, uid), "0") {
		result["readReceipt"] = ""
	}
}

// hasFlag is in_array($flag, $aFlags).
func hasFlag(flags any, flag string) bool {
	list, ok := flags.([]string)
	if !ok {
		if generic, isList := flags.([]any); isList {
			for _, item := range generic {
				if text, isString := item.(string); isString && text == flag {
					return true
				}
			}
		}
		return false
	}
	for _, item := range list {
		if item == flag {
			return true
		}
	}
	return false
}

// emailOf is $oAccount->Email() with the "" fallback of a missing account.
func emailOf(resolved *account.Account) string {
	if nil == resolved {
		return ""
	}
	return resolved.Email()
}

// defaultResponse is Actions\Response::DefaultResponse().
func (d *Dispatcher) defaultResponse(result any, additional map[string]any, ctx *Context) map[string]any {
	response := map[string]any{
		// PHP: 'Action' => $sActionName (default ''), overwritten by the
		// dispatcher once the action name is known.
		"Action": "",
		"Result": d.responseObject(result, "", ctx),
	}

	for key, value := range additional {
		response[key] = value
	}

	// PHP adds the code/message defaults when the result is exactly false.
	if isFalse(result) {
		if _, ok := response["code"]; !ok {
			response["code"] = 0
		}
		if _, ok := response["message"]; !ok {
			response["message"] = ""
		}
	}

	return response
}

// DefaultResponse is Actions\Response::DefaultResponse() for handlers.
func (c *Context) DefaultResponse(result any, additional map[string]any) map[string]any {
	return c.dispatcher.defaultResponse(result, additional, c)
}

// TrueResponse is Actions\Response::TrueResponse().
func (c *Context) TrueResponse(additional map[string]any) map[string]any {
	return c.dispatcher.defaultResponse(true, additional, c)
}

// FalseResponse is Actions\Response::FalseResponse().
func (c *Context) FalseResponse(code int, message, additionalMessage string) map[string]any {
	return c.dispatcher.defaultResponse(false, map[string]any{
		"code":              code,
		"message":           message,
		"messageAdditional": additionalMessage,
	}, c)
}

// isFalse reports whether result is the boolean false that triggers the
// code/message defaults (PHP: "false === $mResult").
func isFalse(result any) bool {
	value, ok := result.(bool)
	return ok && !value
}
