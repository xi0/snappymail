package handler

// Tests of the Step 22 contact actions: the DoContacts envelope, the uid
// parsing of DoContactsDelete, the DoContactSave result shape and the
// CardDAV-is-not-ported behaviour of DoContactsSync.

import (
	"net/http"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// fakeAddressBook is a handler.AddressBook with canned responses.
type fakeAddressBook struct {
	active   bool
	list     []any
	count    int
	savedID  string
	saveOK   bool
	deleted  [][]int
	deleteOK bool
	syncData map[string]any
	saved    map[string]any
}

func (f *fakeAddressBook) IsActive(*account.Account) bool { return f.active }

func (f *fakeAddressBook) GetContacts(_ *account.Account, _, _ int, _ string) ([]any, int) {
	return f.list, f.count
}

func (f *fakeAddressBook) ContactSave(_ *account.Account, _ any, _ string) (string, bool) {
	return f.savedID, f.saveOK
}

func (f *fakeAddressBook) DeleteContacts(_ *account.Account, ids []int) bool {
	f.deleted = append(f.deleted, ids)
	return f.deleteOK
}

func (f *fakeAddressBook) SyncData(*account.Account) map[string]any { return f.syncData }

func (f *fakeAddressBook) SaveSyncData(_ *account.Account, data map[string]any) bool {
	f.saved = data
	return true
}

// contactsDispatcher builds a dispatcher wired to the given address book.
func contactsDispatcher(session Session, book AddressBook) *Dispatcher {
	return NewDispatcher(Options{
		Version:  "0.0.0",
		Session:  func(http.ResponseWriter, *http.Request) Session { return session },
		Contacts: book,
	})
}

// TestContactsEnvelope checks the DoContacts result keys and the offset/limit
// clamping.
func TestContactsEnvelope(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	book := &fakeAddressBook{
		active: true,
		list:   []any{map[string]any{"@Object": "Object/Contact", "id": "1"}},
		count:  3,
	}
	dispatcher := contactsDispatcher(session, book)

	ctx := &Context{
		dispatcher: dispatcher,
		session:    session,
		params:     map[string]any{"Search": "  bob  ", "Offset": -1, "Limit": -1},
	}

	response := mustResponse(doContacts(ctx))
	if nil == response {
		t.Fatal("doContacts failed")
	}

	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if 0 != result["Offset"] {
		t.Errorf("Offset = %v, want 0", result["Offset"])
	}
	if 20 != result["Limit"] {
		t.Errorf("Limit = %v, want 20", result["Limit"])
	}
	if 3 != result["Count"] {
		t.Errorf("Count = %v, want 3", result["Count"])
	}
	if "bob" != result["Search"] {
		t.Errorf("Search = %v, want the trimmed bob", result["Search"])
	}
	list, ok := result["List"].([]any)
	if !ok || 1 != len(list) {
		t.Errorf("List = %#v", result["List"])
	}
}

// TestContactsWithoutAccount checks the InvalidToken failure.
func TestContactsWithoutAccount(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})
	ctx := &Context{dispatcher: dispatcher, params: map[string]any{}}

	if _, err := doContacts(ctx); appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
	}
}

// TestContactsDeleteParsesUids checks the intval/array_filter of DoContactsDelete.
func TestContactsDeleteParsesUids(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	book := &fakeAddressBook{active: true, deleteOK: true}
	dispatcher := contactsDispatcher(session, book)

	ctx := &Context{
		dispatcher: dispatcher,
		session:    session,
		params:     map[string]any{"uids": "1, 2,abc,0"},
	}

	response := mustResponse(doContactsDelete(ctx))
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
	if 1 != len(book.deleted) {
		t.Fatalf("DeleteContacts calls = %d, want 1", len(book.deleted))
	}
	ids := book.deleted[0]
	if 2 != len(ids) || 1 != ids[0] || 2 != ids[1] {
		t.Errorf("deleted ids = %v, want [1 2]", ids)
	}
}

// TestContactSaveResult checks the ResultID/Result document of DoContactSave.
func TestContactSaveResult(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	book := &fakeAddressBook{active: true, saveOK: true, savedID: "5"}
	dispatcher := contactsDispatcher(session, book)

	ctx := &Context{
		dispatcher: dispatcher,
		session:    session,
		params: map[string]any{
			"uid":   0,
			"jCard": `["vcard",[["fn",{},"text","X"]]]`,
		},
	}

	response := mustResponse(doContactSave(ctx))
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if true != result["Result"] {
		t.Errorf("Result.Result = %v, want true", result["Result"])
	}
	if "5" != result["ResultID"] {
		t.Errorf("Result.ResultID = %v, want 5", result["ResultID"])
	}
}

// TestContactsSyncNotPorted checks that DoContactsSync reports ContactsSyncError
// (no CardDAV client in the Go backend).
func TestContactsSyncNotPorted(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := contactsDispatcher(session, &fakeAddressBook{active: true})

	ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{}}
	if _, err := doContactsSync(ctx); appctx.ContactsSyncError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.ContactsSyncError)
	}
}

// TestSaveContactsSyncData checks the sync blob write.
func TestSaveContactsSyncData(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	book := &fakeAddressBook{active: true}
	dispatcher := contactsDispatcher(session, book)

	ctx := &Context{
		dispatcher: dispatcher,
		session:    session,
		params:     map[string]any{"Mode": "2", "User": "u", "Url": "https://dav", "Password": "p"},
	}

	response := mustResponse(doSaveContactsSyncData(ctx))
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}
	if 2 != book.saved["Mode"] {
		t.Errorf("saved Mode = %v, want 2", book.saved["Mode"])
	}
	if "u" != book.saved["User"] || "https://dav" != book.saved["Url"] {
		t.Errorf("saved = %#v", book.saved)
	}
}

// TestTestContactsSyncData checks the capability gate and the missing client.
func TestTestContactsSyncData(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Config:  userConfig{}, // contacts.enable defaults to false
	})
	ctx := &Context{dispatcher: dispatcher, session: session, params: map[string]any{}}

	// Disabled: "Disallowed".
	if _, err := doTestContactsSyncData(ctx); appctx.ContactsSyncError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.ContactsSyncError)
	}

	// Enabled: "No client" (no DAV in the Go backend).
	dispatcher2 := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Config:  userConfig{bools: map[string]bool{"contacts.enable": true}},
	})
	ctx2 := &Context{dispatcher: dispatcher2, session: session, params: map[string]any{}}
	if _, err := doTestContactsSyncData(ctx2); appctx.ContactsSyncError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.ContactsSyncError)
	}
}
