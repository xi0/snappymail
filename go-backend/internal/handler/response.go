package handler

// Response ports \RainLoop\Actions\Response: the JSON envelope builders and the
// responseObject() model conversion.
//
// The envelope is a compatibility contract with the frontend: it always carries
// "Action" and "Result" (plus "epoch", added by the dispatcher), and a failure
// adds "code", "message", "messageAdditional" and "ExceptionCode".

// JSONSerializable is \JsonSerializable: a model that knows its own JSON shape
// (MailSo\Mail\Message, MailSo\Imap\Folder, …). responseObject() passes the
// result of JSONSerialize() straight through, like PHP returns jsonSerialize().
//
// The Message read-receipt suppression and the Folder "checkable" flag are
// added by the steps that port those models (15–17).
type JSONSerializable interface {
	JSONSerialize() any
}

// responseObject is Actions\Response::responseObject(): convert a handler result
// into something json_encode() can render, recursing into arrays.
//
// parent is the PHP $sParent argument; it is not needed yet (only the Message
// special case checks it) but is threaded through so the later models can use
// it without changing call sites.
func (d *Dispatcher) responseObject(value any, parent string) any {
	switch typed := value.(type) {
	case JSONSerializable:
		return typed.JSONSerialize()

	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = d.responseObject(item, "Array")
		}
		return result

	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = d.responseObject(item, "Array")
		}
		return result

	default:
		return value
	}
}

// defaultResponse is Actions\Response::DefaultResponse().
func (d *Dispatcher) defaultResponse(result any, additional map[string]any) map[string]any {
	response := map[string]any{
		// PHP: 'Action' => $sActionName (default ''), overwritten by the
		// dispatcher once the action name is known.
		"Action": "",
		"Result": d.responseObject(result, ""),
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
	return c.dispatcher.defaultResponse(result, additional)
}

// TrueResponse is Actions\Response::TrueResponse().
func (c *Context) TrueResponse(additional map[string]any) map[string]any {
	return c.dispatcher.defaultResponse(true, additional)
}

// FalseResponse is Actions\Response::FalseResponse().
func (c *Context) FalseResponse(code int, message, additionalMessage string) map[string]any {
	return c.dispatcher.defaultResponse(false, map[string]any{
		"code":              code,
		"message":           message,
		"messageAdditional": additionalMessage,
	})
}

// isFalse reports whether result is the boolean false that triggers the
// code/message defaults (PHP: "false === $mResult").
func isFalse(result any) bool {
	value, ok := result.(bool)
	return ok && !value
}
