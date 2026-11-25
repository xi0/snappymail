package handler

// The contact actions of \RainLoop\Actions\Contacts (Step 22):
// DoContacts, DoContactSave, DoContactsDelete, DoContactsSync,
// DoSaveContactsSyncData and DoTestContactsSyncData.
//
// The address book (internal/contacts) is handed in through Options.Contacts; a
// nil provider reports every account as having no address book, which matches
// an installation with contacts.enable = false.
//
// The CardDAV synchronisation is not ported (the DAV client / Sabre\* is out of
// scope per SPEC.md). DoContactsSync and DoTestContactsSyncData therefore report
// ContactsSyncError (140) exactly like PHP does when no DAV client is available,
// and DoSaveContactsSyncData persists the mode/user/url (the password is not
// escrowed through the main account crypt key because no DAV consumer exists).

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// AddressBook is the Actions\Contacts::AddressBookProvider() surface the contact
// actions use. *contacts.Provider satisfies it.
type AddressBook interface {
	// IsActive is Providers\AddressBook::IsActive().
	IsActive(account *account.Account) bool
	// GetContacts returns the JSON documents of a page of contacts plus the
	// total count.
	GetContacts(account *account.Account, offset, limit int, search string) ([]any, int)
	// ContactSave stores a contact from its jCard and returns the resulting id.
	ContactSave(account *account.Account, jcard any, uid string) (string, bool)
	// DeleteContacts removes the contact ids.
	DeleteContacts(account *account.Account, ids []int) bool
	// SyncData is getContactsSyncData().
	SyncData(account *account.Account) map[string]any
	// SaveSyncData is setContactsSyncData().
	SaveSyncData(account *account.Account, data map[string]any) bool
}

// RegisterContacts registers the DoContacts* actions.
func RegisterContacts(d *Dispatcher) {
	d.Register("Contacts", doContacts)
	d.Register("ContactSave", doContactSave)
	d.Register("ContactsDelete", doContactsDelete)
	d.Register("ContactsSync", doContactsSync)
	d.Register("SaveContactsSyncData", doSaveContactsSyncData)
	d.Register("TestContactsSyncData", doTestContactsSyncData)
}

// addressBook returns the wired address book, or nil.
func (c *Context) addressBook() AddressBook {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	return c.dispatcher.contacts
}

// doContacts is Actions\Contacts::DoContacts().
func doContacts(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	search := strings.TrimSpace(ctx.stringParam("Search", ""))
	offset := phpIntValue(ctx.GetParam("Offset", 0))
	limit := phpIntValue(ctx.GetParam("Limit", 20))

	if offset < 0 {
		offset = 0
	}
	if limit < 0 {
		limit = 20
	}

	list := []any{}
	count := 0

	if provider := ctx.addressBook(); nil != provider && provider.IsActive(resolved) {
		if result, total := provider.GetContacts(resolved, offset, limit, search); nil != result {
			list = result
			count = total
		}
	}

	return ctx.DefaultResponse(map[string]any{
		"Offset": offset,
		"Limit":  limit,
		"Count":  count,
		"Search": search,
		"List":   list,
	}, nil), nil
}

// doContactSave is Actions\Contacts::DoContactSave().
func doContactSave(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	resultID := ""
	result := false

	if ctx.HasParam("uid") && ctx.HasParam("jCard") {
		if provider := ctx.addressBook(); nil != provider && provider.IsActive(resolved) {
			jcard := ctx.GetParam("jCard", "")
			uid := ctx.stringParam("uid", "")
			if id, ok := provider.ContactSave(resolved, jcard, uid); ok {
				result = true
				resultID = id
			}
		}
	}

	return ctx.DefaultResponse(map[string]any{
		"ResultID": resultID,
		"Result":   result,
	}, nil), nil
}

// doContactsDelete is Actions\Contacts::DoContactsDelete().
func doContactsDelete(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	ids := []int{}
	for _, part := range strings.Split(ctx.stringParam("uids", ""), ",") {
		if id := phpIntValue(strings.TrimSpace(part)); 0 != id {
			ids = append(ids, id)
		}
	}

	result := false
	if 0 < len(ids) {
		if provider := ctx.addressBook(); nil != provider && provider.IsActive(resolved) {
			result = provider.DeleteContacts(resolved, ids)
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doContactsSync is Actions\Contacts::DoContactsSync().
//
// PdoAddressBook::Sync() needs a CardDAV client, which the Go backend does not
// have, so the call fails like PHP does without a client.
func doContactsSync(ctx *Context) (any, error) {
	if _, err := ctx.requireAccount(); err != nil {
		return nil, err
	}
	return nil, clientError(appctx.ContactsSyncError, "AddressBookProvider->Sync() failed")
}

// doSaveContactsSyncData is Actions\Contacts::DoSaveContactsSyncData().
func doSaveContactsSyncData(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	provider := ctx.addressBook()
	if nil == provider || !provider.IsActive(resolved) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	data := map[string]any{
		"Mode":     phpIntValue(ctx.GetParam("Mode", "0")),
		"User":     ctx.stringParam("User", ""),
		"Password": ctx.stringParam("Password", ""),
		"Url":      ctx.stringParam("Url", ""),
	}

	return ctx.DefaultResponse(provider.SaveSyncData(resolved, data), nil), nil
}

// doTestContactsSyncData is Actions\Contacts::DoTestContactsSyncData().
func doTestContactsSyncData(ctx *Context) (any, error) {
	if !ctx.hasCapa("Contacts") {
		return nil, clientError(appctx.ContactsSyncError, "Disallowed")
	}
	if _, err := ctx.requireAccount(); err != nil {
		return nil, err
	}

	// No CardDAV client is available in the Go backend.
	return nil, clientError(appctx.ContactsSyncError, "No client")
}
