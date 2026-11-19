package appctx

import (
	"errors"
	"testing"
)

func TestNotificationMessagesMatchPHP(t *testing.T) {
	tests := map[int]string{
		InvalidToken:         "InvalidToken[101]",
		AuthError:            "AuthError[102]",
		CryptKeyError:        "CryptKeyError[111]",
		AccountSwitchFailed:  "AccountSwitchFailed[803]",
		InvalidInputArgument: "InvalidInputArgument[903]",
		UnknownError:         "UnknownError[999]",
		12345:                "UnknownNotification[12345]",
	}

	for code, want := range tests {
		if got := Message(code, nil); want != got {
			t.Errorf("Message(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestNotificationNumbersNeverChange(t *testing.T) {
	// The frontend switches on these numbers, so a renumbering is a breaking
	// change. Spot-check the ones the ported steps actually return.
	tests := map[string]struct {
		code int
		want int
	}{
		"InvalidToken":         {InvalidToken, 101},
		"AuthError":            {AuthError, 102},
		"DomainNotAllowed":     {DomainNotAllowed, 109},
		"AccountNotAllowed":    {AccountNotAllowed, 110},
		"CryptKeyError":        {CryptKeyError, 111},
		"AccountDoesNotExist":  {AccountDoesNotExist, 802},
		"AccountSwitchFailed":  {AccountSwitchFailed, 803},
		"AccountFilterError":   {AccountFilterError, 804},
		"InvalidInputArgument": {InvalidInputArgument, 903},
	}

	for name, test := range tests {
		if test.want != test.code {
			t.Errorf("%s = %d, want %d", name, test.code, test.want)
		}
		if name != Name(test.code) {
			t.Errorf("Name(%d) = %q, want %q", test.code, Name(test.code), name)
		}
	}
}

func TestClientViewErrorUsesThePreviousMessage(t *testing.T) {
	cause := errors.New("folder does not exist")

	if got := Message(ClientViewError, cause); "folder does not exist" != got {
		t.Errorf("Message(ClientViewError) = %q, want the previous message", got)
	}
	if got := Message(ClientViewError, nil); "ClientViewError[902]" != got {
		t.Errorf("Message(ClientViewError, nil) = %q", got)
	}
}

func TestClientError(t *testing.T) {
	cause := errors.New("connection refused")
	err := NewClientError(AuthError, cause, "")

	if "AuthError[102] (connection refused)" != err.Error() {
		t.Errorf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("the previous error must stay reachable through errors.Is")
	}
	if !errors.Is(err, NewClientError(AuthError, nil, "")) {
		t.Error("two client errors with the same code must compare equal")
	}
	if errors.Is(err, NewClientError(InvalidToken, nil, "")) {
		t.Error("client errors with different codes must not compare equal")
	}
	if "102" == err.Message || "" == err.Message {
		t.Errorf("Message = %q, want the notification message", err.Message)
	}

	// An additional message falls back to the previous message.
	if "connection refused" != err.AdditionalMessage {
		t.Errorf("AdditionalMessage = %q", err.AdditionalMessage)
	}

	explicit := NewClientError(InvalidToken, nil, "Session gone")
	if "InvalidToken[101] (Session gone)" != explicit.Error() {
		t.Errorf("Error() = %q", explicit.Error())
	}
	if nil != explicit.Unwrap() {
		t.Error("Unwrap must be nil without a previous error")
	}

	formatted := NewClientErrorf(DomainNotAllowed, nil, "%s has no domain configuration", "a@example.com")
	if "a@example.com has no domain configuration" != formatted.AdditionalMessage {
		t.Errorf("AdditionalMessage = %q", formatted.AdditionalMessage)
	}
}

func TestClientErrorNilSafety(t *testing.T) {
	var err *ClientError

	if "" != err.Error() {
		t.Errorf("nil Error() = %q", err.Error())
	}
	if nil != err.Unwrap() {
		t.Error("nil Unwrap must be nil")
	}
	if err.Is(NewClientError(InvalidToken, nil, "")) {
		t.Error("a nil error must not match")
	}
}
