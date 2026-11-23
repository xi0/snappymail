package handler

// Tests of the Step 16 DoMessageList action: the Notification codes of the
// invalid requests and the RawKey decoding.

import (
	"encoding/base64"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

func TestMessageListValidation(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	// An empty folder is refused before the mail connection is opened.
	_, err := doMessageList(folderContext(dispatcher, nil, map[string]any{"folder": ""}))
	if appctx.CantGetMessageList != clientCodeOf(err) {
		t.Errorf("empty folder code = %d, want %d", clientCodeOf(err), appctx.CantGetMessageList)
	}

	// An invalid SORT criteria is refused with InvalidInputArgument.
	_, err = doMessageList(folderContext(dispatcher, nil, map[string]any{
		"folder": "INBOX",
		"sort":   "INVALID()",
	}))
	if appctx.InvalidInputArgument != clientCodeOf(err) {
		t.Errorf("invalid sort code = %d, want %d", clientCodeOf(err), appctx.InvalidInputArgument)
	}

	// A valid request without a session fails in MailClient() with InvalidToken.
	_, err = doMessageList(folderContext(dispatcher, nil, map[string]any{
		"folder": "INBOX",
		"sort":   "REVERSE DATE",
	}))
	if appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("guest code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
	}
}

func TestMessageListAcceptsAWhitelistedSort(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	for _, sort := range []string{"", "DATE", "REVERSE DATE", "FROM", "REVERSE SUBJECT DATE"} {
		_, err := doMessageList(folderContext(dispatcher, nil, map[string]any{
			"folder": "INBOX",
			"sort":   sort,
		}))
		// The sort passes validation; the failure is the missing session.
		if appctx.InvalidToken != clientCodeOf(err) {
			t.Errorf("sort %q code = %d, want InvalidToken", sort, clientCodeOf(err))
		}
	}
}

func TestDecodeRawKeyMap(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte(`{"folder":"INBOX","limit":25,"offset":0}`))
	values := decodeRawKeyMap(raw)
	if nil == values || "INBOX" != phpString(values["folder"]) {
		t.Fatalf("values = %v", values)
	}

	if nil != decodeRawKeyMap("") {
		t.Error("an empty RawKey must decode to nil")
	}
	if nil != decodeRawKeyMap("!!!not base64!!!") {
		t.Error("an invalid RawKey must decode to nil")
	}

	// A JSON list is not an object, so it decodes to nil.
	list := base64.RawURLEncoding.EncodeToString([]byte(`["a","b"]`))
	if nil != decodeRawKeyMap(list) {
		t.Error("a JSON list must decode to nil")
	}
}
