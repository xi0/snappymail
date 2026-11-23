package handler

// Tests of the Step 17 DoMessage action and the two pieces of
// Actions\Response::responseObject() the view adds: the RawKey account-hash
// validation and the read-receipt suppression.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// fakeMail is a handler.Mail that hands out a fixed client (or failure).
type fakeMail struct {
	resolved *account.Account
	client   *mail.Client
	err      error
}

// Connect is Actions::initMailClientConnection().
func (f fakeMail) Connect() (*account.Account, *mail.Client, error) {
	return f.resolved, f.client, f.err
}

// mailDispatcher builds a dispatcher whose mail layer is the fake.
func mailDispatcher(session Session, fake fakeMail) *Dispatcher {
	return NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Mail:    func(http.ResponseWriter, *http.Request) Mail { return fake },
	})
}

// TestMessageNeedsAnAccount checks the InvalidToken failure (101) of a request
// without a session or a mail layer.
func TestMessageNeedsAnAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	_, err := doMessage(folderContext(dispatcher, nil, map[string]any{}))
	if appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("guest code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
	}
}

// TestMessagePropagatesTheConnectionError checks that the errors of
// initMailClientConnection() are not swallowed.
func TestMessagePropagatesTheConnectionError(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := mailDispatcher(session, fakeMail{
		resolved: resolved,
		err:      appctx.NewClientError(appctx.AuthError, nil, ""),
	})

	ctx := folderContext(dispatcher, session, map[string]any{"folder": "INBOX", "uid": 42})
	_, err := doMessage(ctx)
	if appctx.AuthError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.AuthError)
	}
}

// TestMessageReportsCantGetMessage checks the CantGetMessage failure (202) when
// the mailbox has no answer for the requested message.
func TestMessageReportsCantGetMessage(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := mailDispatcher(session, fakeMail{resolved: resolved})

	cases := []map[string]any{
		{"folder": "INBOX", "uid": 0},
		{"folder": "INBOX", "uid": 42},
		{"RawKey": "!!!not-base64!!!"},
	}

	for _, params := range cases {
		_, err := doMessage(folderContext(dispatcher, session, params))
		if appctx.CantGetMessage != clientCodeOf(err) {
			t.Errorf("params %v code = %d, want %d", params, clientCodeOf(err), appctx.CantGetMessage)
		}
	}
}

// TestDecodeRawKey checks Actions::decodeRawKey(): the object form, the indexed
// form of DoMessage and the account-hash validation.
func TestDecodeRawKey(t *testing.T) {
	hash := "account-hash"

	encode := func(value any) string {
		document, err := json.Marshal(value)
		if nil != err {
			t.Fatalf("marshal: %v", err)
		}
		return cryptox.URLSafeBase64Encode(string(document))
	}

	// The object form of the RawKey endpoints.
	objectForm := decodeRawKey(encode(map[string]any{
		"accountHash": hash,
		"folder":      "INBOX",
		"uid":         42,
	}), hash)
	if nil == objectForm || "INBOX" != phpString(objectForm["folder"]) {
		t.Fatalf("object form = %v", objectForm)
	}

	// The indexed form of DoMessage: [folder, uid, useThreads, accountHash].
	indexedForm := decodeRawKey(encode([]any{"INBOX", 42, true, hash}), hash)
	if nil == indexedForm {
		t.Fatal("indexed form was not decoded")
	}
	if "INBOX" != phpString(indexedForm["0"]) || 42 != phpIntValue(indexedForm["1"]) {
		t.Errorf("indexed form = %v", indexedForm)
	}

	// A foreign or missing account hash is refused.
	if nil != decodeRawKey(encode([]any{"INBOX", 42, true, "other"}), hash) {
		t.Error("a foreign account hash must be refused")
	}
	if nil != decodeRawKey(encode([]any{"INBOX", 42, true}), hash) {
		t.Error("a missing account hash must be refused")
	}
	// Without a current account the hash is not checked.
	if nil == decodeRawKey(encode([]any{"INBOX", 42, true}), "") {
		t.Error("an empty account hash must not be checked")
	}

	// Invalid input.
	if nil != decodeRawKey("", hash) {
		t.Error("an empty RawKey must decode to nil")
	}
	if nil != decodeRawKey("!!!not-base64!!!", hash) {
		t.Error("an invalid RawKey must decode to nil")
	}
	if nil != decodeRawKey(cryptox.URLSafeBase64Encode(`"text"`), hash) {
		t.Error("a JSON string must decode to nil")
	}
}

// memoryCacher is an in-memory Actions::Cacher($oAccount).
type memoryCacher struct {
	values map[string]string
}

// GetDefault is CacheClient::Get($sKey, $mDefault).
func (m *memoryCacher) GetDefault(key, defaultValue string) string {
	if value, ok := m.values[key]; ok {
		return value
	}
	return defaultValue
}

// receiptContext builds a context with an account and a cache.
func receiptContext(cacher Cacher) *Context {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Salt:    "salt",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Cacher:  func(*account.Account) Cacher { return cacher },
	})
	return folderContext(dispatcher, session, map[string]any{})
}

// receiptDocument is the JSON shape of a viewed message with a read receipt.
func receiptDocument(flags []string) map[string]any {
	return map[string]any{
		"@Object":     "Object/Message",
		"folder":      "INBOX",
		"uid":         42,
		"readReceipt": "Alice <alice@example.com>",
		"flags":       flags,
	}
}

// TestReadReceiptSuppression checks the readReceipt branch of
// Actions\Response::responseObject(): a receipt that was already sent is hidden.
func TestReadReceiptSuppression(t *testing.T) {
	key := storage.ReadReceiptCacheKey(testEmail, "INBOX", 42)

	cacher := &memoryCacher{values: map[string]string{key: "1"}}
	document := receiptDocument([]string{"\\seen"})
	suppressReadReceipt(receiptContext(cacher), document)
	if "" != document["readReceipt"] {
		t.Errorf("readReceipt = %v, want an empty string", document["readReceipt"])
	}

	// Without the cache mark the hint stays.
	empty := receiptDocument([]string{"\\seen"})
	suppressReadReceipt(receiptContext(&memoryCacher{values: map[string]string{}}), empty)
	if "Alice <alice@example.com>" != empty["readReceipt"] {
		t.Errorf("readReceipt = %v", empty["readReceipt"])
	}

	// $mdnsent and \answered mean "already handled" as well.
	for _, flag := range []string{"$mdnsent", "\\answered"} {
		marked := receiptDocument([]string{"\\seen", flag})
		suppressReadReceipt(receiptContext(cacher), marked)
		if "Alice <alice@example.com>" != marked["readReceipt"] {
			t.Errorf("readReceipt with %s = %v", flag, marked["readReceipt"])
		}
	}

	// Without a cache nothing happens.
	uncached := receiptDocument([]string{"\\seen"})
	suppressReadReceipt(receiptContext(nil), uncached)
	if "Alice <alice@example.com>" != uncached["readReceipt"] {
		t.Errorf("readReceipt without a cache = %v", uncached["readReceipt"])
	}
}

// TestAccountHashUsesTheSalt checks that the RawKey validation uses APP_SALT.
func TestAccountHashUsesTheSalt(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Salt:    "the-salt",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
	})

	ctx := folderContext(dispatcher, session, map[string]any{})
	if got, want := ctx.accountHash(), resolved.Hash("the-salt"); got != want {
		t.Errorf("accountHash = %q, want %q", got, want)
	}

	// Without an account the hash is empty, so decodeRawKey() skips the check.
	guest := folderContext(dispatcher, nil, map[string]any{})
	if "" != guest.accountHash() {
		t.Errorf("guest accountHash = %q", guest.accountHash())
	}
}
