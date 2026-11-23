package handler

// Tests of the Step 15 folder actions: the settings documents they read and
// write (CheckableFolder, the system folder names) and the notification codes
// of a request without an account.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// testEmail is the address of the folder test account.
const testEmail = "user@example.com"

// folderAccount builds a main account for the folder tests.
func folderAccount() *account.Account {
	main := account.NewMainAccount()
	main.SetCredentials(nil, testEmail, testEmail, "secret", "", "")
	return &main.Account
}

// memorySettings is an in-memory Providers\Settings(true).
type memorySettings struct {
	docs map[string]*storage.Settings
}

// newMemorySettings builds an empty provider.
func newMemorySettings() *memorySettings {
	return &memorySettings{docs: map[string]*storage.Settings{}}
}

// Load returns the stored document, or an empty one.
func (m *memorySettings) Load(resolved storage.Account) *storage.Settings {
	if nil == resolved {
		return storage.NewSettings()
	}
	if document, ok := m.docs[resolved.Email()]; ok {
		return document
	}
	return storage.NewSettings()
}

// Save stores the document.
func (m *memorySettings) Save(resolved storage.Account, document *storage.Settings) error {
	if nil == resolved {
		return nil
	}
	m.docs[resolved.Email()] = document
	return nil
}

// folderSession is the request session of the folder tests.
type folderSession struct {
	account *account.Account
}

// ConnectionToken is the connection token of the fake session.
func (s *folderSession) ConnectionToken() string { return "tok" }

// GetAccountFromToken returns the account, or the InvalidToken failure.
func (s *folderSession) GetAccountFromToken(bool) (*account.Account, error) {
	if nil == s.account {
		return nil, appctx.NewClientError(appctx.InvalidToken, nil, "")
	}
	return s.account, nil
}

// folderDispatcher builds a dispatcher wired to the given session and settings.
func folderDispatcher(session Session, settings SettingsProvider) *Dispatcher {
	return NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		LocalSettings: settings,
	})
}

// folderContext builds the per-request context of one folder action.
func folderContext(dispatcher *Dispatcher, session Session, params map[string]any) *Context {
	return &Context{dispatcher: dispatcher, session: session, params: params}
}

// mustResponse unwraps the envelope of an action, failing on an error.
func mustResponse(result any, err error) map[string]any {
	if nil != err {
		return nil
	}
	response, _ := result.(map[string]any)
	return response
}

// clientCodeOf returns the notification code of a client error, or 0.
func clientCodeOf(err error) int {
	var clientErr *appctx.ClientError
	if nil != err && errors.As(err, &clientErr) && nil != clientErr {
		return clientErr.Code
	}
	return 0
}

// TestFolderActionsNeedAnAccount checks the InvalidToken failure (101) of a
// request without a session.
func TestFolderActionsNeedAnAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	cases := []struct {
		name   string
		action ActionFunc
	}{
		{"Folders", doFolders},
		{"FolderCreate", doFolderCreate},
		{"FolderDelete", doFolderDelete},
		{"FolderClear", doFolderClear},
		{"FolderRename", doFolderRename},
		{"FolderSettings", doFolderSettings},
		{"FolderAppend", doFolderAppend},
	}

	for _, test := range cases {
		// One Context per action: initMailClientConnection() is cached per
		// request, exactly like Actions::MailClient().
		ctx := folderContext(dispatcher, nil, map[string]any{})

		_, err := test.action(ctx)
		if nil == err {
			t.Errorf("%s must report a failure without an account", test.name)
			continue
		}
		if appctx.InvalidToken != clientCodeOf(err) {
			t.Errorf("%s code = %d, want %d", test.name, clientCodeOf(err), appctx.InvalidToken)
		}
	}
}

// TestResponseObjectAddsCheckable checks the "checkable" flag of the folder
// documents (Actions\Response::responseObject()).
func TestResponseObjectAddsCheckable(t *testing.T) {
	resolved := folderAccount()
	settings := newMemorySettings()
	document := storage.NewSettings()
	document.SetConf("CheckableFolder", `["INBOX"]`)
	settings.docs[testEmail] = document

	session := &folderSession{account: resolved}
	dispatcher := folderDispatcher(session, settings)
	ctx := folderContext(dispatcher, session, map[string]any{})

	checked, ok := ctx.DefaultResponse(imapclient.NewFolder("INBOX", "/"), nil)["Result"].(map[string]any)
	if !ok {
		t.Fatal("Result is not a folder document")
	}
	if true != checked["checkable"] {
		t.Errorf("INBOX checkable = %v", checked["checkable"])
	}

	unchecked, _ := ctx.DefaultResponse(imapclient.NewFolder("Archive", "/"), nil)["Result"].(map[string]any)
	if nil == unchecked {
		t.Fatal("Result is not a folder document")
	}
	if false != unchecked["checkable"] {
		t.Errorf("Archive checkable = %v", unchecked["checkable"])
	}

	// Without a session (and so without a settings document) nothing is checked.
	empty := NewDispatcher(Options{Version: "0.0.0"})
	emptyContext := folderContext(empty, &folderSession{}, map[string]any{})
	result, _ := emptyContext.DefaultResponse(imapclient.NewFolder("INBOX", "/"), nil)["Result"].(map[string]any)
	if nil == result {
		t.Fatal("Result is not a folder document")
	}
	if false != result["checkable"] {
		t.Errorf("checkable without a session = %v", result["checkable"])
	}
}

// TestDecodeCheckableFolders checks the json_decode() semantics of the stored
// CheckableFolder value.
func TestDecodeCheckableFolders(t *testing.T) {
	tests := []struct {
		value any
		want  string
	}{
		{`[]`, ""},
		{`["INBOX"]`, "INBOX"},
		{`["INBOX","Sent"]`, "INBOX,Sent"},
		// json_decode() without $assoc turns an object into a stdClass, which
		// is_array() rejects.
		{`{"1":"B"}`, ""},
		{"", ""},
		{nil, ""},
		{[]any{"INBOX"}, "INBOX"},
	}

	for _, test := range tests {
		joined := strings.Join(decodeCheckableFolders(test.value), ",")
		if joined != test.want {
			t.Errorf("decodeCheckableFolders(%#v) = %q, want %q", test.value, joined, test.want)
		}
	}
}

// TestPHPArrayEncoding checks that the settings value is byte-identical to
// json_encode() of the PHP array, including the key gaps array_diff() leaves.
func TestPHPArrayEncoding(t *testing.T) {
	if got := phpArrayOf([]string{"A", "B", "C"}).encode(); `["A","B","C"]` != got {
		t.Errorf("encode = %s", got)
	}

	// array_diff() keeps the keys of the remaining entries.
	if got := phpArrayOf([]string{"A", "B", "C"}).remove("B").encode(); `{"0":"A","2":"C"}` != got {
		t.Errorf("array_diff encode = %s", got)
	}

	// array_unique() keeps the first occurrence and its key.
	if got := phpArrayOf([]string{"A", "B", "A"}).unique().encode(); `["A","B"]` != got {
		t.Errorf("array_unique encode = %s", got)
	}

	// array_splice() renumbers.
	spliced := phpArrayOf([]string{"A", "B", "C"})
	spliced.spliceRemove(1)
	if got := spliced.encode(); `["A","C"]` != got {
		t.Errorf("array_splice encode = %s", got)
	}

	// A quoted string keeps the JSON escaping of Utils::jsonEncode().
	if got := phpArrayOf([]string{`a"b`}).encode(); `["a\"b"]` != got {
		t.Errorf("encode = %s", got)
	}
}

// TestFolderCheckableWritesTheSetting checks SetFolderCheckable().
func TestFolderCheckableWritesTheSetting(t *testing.T) {
	resolved := folderAccount()
	settings := newMemorySettings()
	session := &folderSession{account: resolved}
	dispatcher := folderDispatcher(session, settings)
	ctx := folderContext(dispatcher, session, map[string]any{"folder": "INBOX", "checkable": true})

	response := mustResponse(doFolderCheckable(ctx))
	if nil == response {
		t.Fatal("doFolderCheckable failed")
	}
	if true != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}

	document := settings.docs[testEmail]
	if nil == document {
		t.Fatal("the settings_local document was not saved")
	}
	if `["INBOX"]` != phpString(document.GetConf("CheckableFolder", "[]")) {
		t.Errorf("CheckableFolder = %v", document.GetConf("CheckableFolder", "[]"))
	}

	// Unchecking removes the entry again (array_splice renumbers the keys).
	ctx = folderContext(dispatcher, session, map[string]any{"folder": "INBOX", "checkable": 0})
	if _, err := doFolderCheckable(ctx); nil != err {
		t.Fatalf("doFolderCheckable: %v", err)
	}
	if `[]` != phpString(document.GetConf("CheckableFolder", "[]")) {
		t.Errorf("CheckableFolder = %v", document.GetConf("CheckableFolder", "[]"))
	}
}

// TestSystemFoldersUpdate checks the five folder names of DoSystemFoldersUpdate.
func TestSystemFoldersUpdate(t *testing.T) {
	resolved := folderAccount()
	settings := newMemorySettings()
	session := &folderSession{account: resolved}
	dispatcher := folderDispatcher(session, settings)

	result, err := doSystemFoldersUpdate(folderContext(dispatcher, session, map[string]any{
		"sent":    "Sent",
		"drafts":  "Drafts",
		"junk":    "Junk",
		"trash":   "Trash",
		"archive": "",
	}))
	if nil != err {
		t.Fatalf("doSystemFoldersUpdate: %v", err)
	}
	response := mustResponse(result, err)
	if true != response["Result"] {
		t.Errorf("Result = %v", response["Result"])
	}

	document := settings.docs[testEmail]
	for key, want := range map[string]string{
		"SentFolder":    "Sent",
		"DraftsFolder":  "Drafts",
		"JunkFolder":    "Junk",
		"TrashFolder":   "Trash",
		"ArchiveFolder": "",
	} {
		if got := phpString(document.GetConf(key, "")); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
