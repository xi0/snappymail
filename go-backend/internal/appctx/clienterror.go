package appctx

import (
	"errors"
	"fmt"
)

// ClientError is \RainLoop\Exceptions\ClientException: a failure that is
// reported to the frontend as a notification code.
//
// The JSON layer of step 9 turns it into the "code", "message",
// "messageAdditional" and "ExceptionCode" fields of the response envelope, so
// the additional message is kept next to the code instead of being folded into
// Error().
type ClientError struct {
	// Code is the \RainLoop\Notifications value.
	Code int
	// Message is Notifications::GetNotificationsMessage(), the "Name[code]"
	// default message of the exception.
	Message string
	// AdditionalMessage is the PHP $sAdditionalMessage: the specific reason,
	// or the message of the previous error.
	AdditionalMessage string
	// Previous is the wrapped error (PHP's $oPrevious).
	Previous error
}

// NewClientError builds a client exception. additionalMessage falls back to the
// message of previous, like the ClientException constructor.
func NewClientError(code int, previous error, additionalMessage string) *ClientError {
	message := additionalMessage
	if "" == message && nil != previous {
		message = previous.Error()
	}

	return &ClientError{
		Code:              code,
		Message:           Message(code, previous),
		AdditionalMessage: message,
		Previous:          previous,
	}
}

// NewClientErrorf is NewClientError with a formatted additional message.
func NewClientErrorf(code int, previous error, format string, args ...any) *ClientError {
	return NewClientError(code, previous, fmt.Sprintf(format, args...))
}

// Error is the PHP getMessage() half plus the additional message, mirroring
// ClientException::__toString()'s first line.
func (e *ClientError) Error() string {
	if nil == e {
		return ""
	}
	if "" != e.AdditionalMessage {
		return e.Message + " (" + e.AdditionalMessage + ")"
	}
	return e.Message
}

// Unwrap exposes the wrapped error for errors.Is()/errors.As().
func (e *ClientError) Unwrap() error {
	if nil == e {
		return nil
	}
	return e.Previous
}

// Is compares by notification code, which is the only thing the frontend and
// the tests care about: every "AuthError" is the same failure regardless of the
// wrapped cause.
func (e *ClientError) Is(target error) bool {
	var other *ClientError
	if !errors.As(target, &other) {
		return false
	}
	return nil != other && nil != e && other.Code == e.Code
}
