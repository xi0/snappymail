package handler

// The additional account and identity actions of \RainLoop\Actions\Accounts
// (Step 24): DoAccountSetup, DoAccountUnread, DoAccountImport, DoAccountDelete,
// DoAccountSwitch, DoIdentityUpdate, DoIdentityDelete,
// DoAccountsAndIdentitiesSortOrder and DoAccountsAndIdentities.
//
// The additional account list lives in the main account's CONFIG entry
// "additionalaccounts" and every password is sealed with the main account's
// crypt key (AdditionalAccount::asTokenArray()), so a stored list stays
// interchangeable with the PHP backend. The identity documents live under the
// CONFIG key "identities" of the *local* storage provider (see
// internal/account), which is what the frontend edits through the identity
// popup.
//
// Partial, by design of the step split:
//   - DoAccountImport needs SnappyMail\Imap\Sync, which is not ported; the
//     action reports MailServerError instead of streaming an import.
//   - DoAccountUnread goes through the injected IMAP "STATUS INBOX" callback;
//     without one the action reports ConnectionError.
//   - The legacy "accounts" -> "additionalaccounts" migration
//     (Upgrade::ConvertInsecureAccounts) is not ported; only the current key is
//     read.

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// Accounts is the additional account surface of Actions\Accounts. main wires the
// production implementation (internal/service) on top of the request's
// auth.Authenticator; a nil factory simply reports no additional-account
// support, which the actions surface as FalseResponse.
type Accounts interface {
	// Main is Actions::getMainAccountFromToken().
	Main() (*account.MainAccount, error)
	// Current is Actions::getAccountFromToken().
	Current() (*account.Account, error)
	// GetAccounts is Actions::GetAccounts().
	GetAccounts(main *account.MainAccount) (*account.Accounts, error)
	// SetAccounts is Actions::SetAccounts().
	SetAccounts(main *account.MainAccount, accounts *account.Accounts) error
	// NewAdditional is AdditionalAccount::NewInstanceFromTokenArray().
	NewAdditional(data any, throw bool) (*account.AdditionalAccount, error)
	// Seal is AdditionalAccount::asTokenArray($main).
	Seal(additional *account.AdditionalAccount, main *account.MainAccount) (map[string]any, error)
	// Login is Actions::LoginProcess($email, $password, false): validate the
	// credentials over IMAP and return the additional account.
	Login(email, password string) (*account.AdditionalAccount, error)
	// Unread is loadAdditionalAccountImapClient() + FolderStatus('INBOX'):
	// the UNSEEN counter of the additional account.
	Unread(additional *account.AdditionalAccount) (int, error)
	// Switch is Actions\UserAuth::switchAccount().
	Switch(email string) (bool, error)
	// ClearAdditional is Cookies::clear(AUTH_ADDITIONAL_TOKEN_KEY).
	ClearAdditional() error
}

// AccountsFactory builds the per-request additional account surface from the
// resolved session. A nil factory disables the actions.
type AccountsFactory func(session Session) Accounts

// RegisterAccounts registers the DoAccount* / DoIdentity* / DoAccountsAndIdentities* actions.
func RegisterAccounts(d *Dispatcher) {
	d.Register("AccountSetup", doAccountSetup)
	d.Register("AccountUnread", doAccountUnread)
	d.Register("AccountImport", doAccountImport)
	d.Register("AccountDelete", doAccountDelete)
	d.Register("AccountSwitch", doAccountSwitch)
	d.Register("IdentityUpdate", doIdentityUpdate)
	d.Register("IdentityDelete", doIdentityDelete)
	d.Register("AccountsAndIdentitiesSortOrder", doAccountsAndIdentitiesSortOrder)
	d.Register("AccountsAndIdentities", doAccountsAndIdentities)
}

// accounts is Actions\Accounts::GetAccounts()'s provider: nil when the request
// has no session or the dispatcher is not wired to an accounts factory.
func (c *Context) accounts() Accounts {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newAccounts {
		return nil
	}
	return c.dispatcher.newAccounts(c.session)
}

// identities is Actions\Accounts::IdentitiesProvider().
func (c *Context) identities() *account.Identities {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	return c.dispatcher.identities
}

// mainAuthAccount is Actions::getMainAccountFromToken() as the concrete
// *account.MainAccount, which the additional account token handling needs.
func (c *Context) mainAuthAccount() *account.MainAccount {
	if acc := c.accounts(); nil != acc {
		if main, err := acc.Main(); nil == err {
			return main
		}
	}
	return nil
}

// localDomains is array_column(DomainProvider()->GetList(), 'name').
func (c *Context) localDomains() []string {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.localDomains {
		return []string{}
	}
	return c.dispatcher.localDomains()
}

// contactsActive is AddressBookProvider($oAccount)->IsActive().
func (c *Context) contactsActive(acct *account.Account) bool {
	provider := c.addressBook()
	return nil != provider && provider.IsActive(acct)
}

// getIdentityByID is Actions\Accounts::GetIdentityByID().
func (c *Context) getIdentityByID(acct *account.Account, id string, firstOnEmpty bool) *account.Identity {
	provider := c.identities()
	if nil == provider || nil == acct {
		return nil
	}

	identities := provider.GetSortedIdentities(acct, c.hasCapa("Identities"))
	for _, identity := range identities {
		if nil != identity && id == identity.Id() {
			return identity
		}
	}

	if firstOnEmpty && 0 < len(identities) {
		return identities[0]
	}
	return nil
}

// getAccountData is Actions\Accounts::getAccountData(): the session-scoped flags
// the frontend needs after switching to another account.
func (c *Context) getAccountData(acct *account.Account) map[string]any {
	config := c.Config()

	minRefresh := config.GetInt("webmail", "min_refresh_interval", 5)

	mainEmail := ""
	if main := c.mainAuthAccount(); nil != main {
		mainEmail = main.Email()
	} else if nil != acct {
		mainEmail = acct.Email()
	}

	hash := ""
	if nil != acct && nil != c.dispatcher {
		hash = acct.Hash(c.dispatcher.salt)
	}

	checkMailInterval := minRefresh
	if 15 > checkMailInterval {
		checkMailInterval = 15
	}

	result := map[string]any{
		"Email":              emailOf(acct),
		"accountHash":        hash,
		"mainEmail":          mainEmail,
		"localDomains":       c.localDomains(),
		"contactsAllowed":    c.contactsActive(acct),
		"HideUnsubscribed":   false,
		"defaultSort":        "",
		"useThreads":         config.GetBool("defaults", "mail_use_threads", false),
		"threadAlgorithm":    "",
		"ReplySameFolder":    config.GetBool("defaults", "mail_reply_same_folder", false),
		"HideDeleted":        true,
		"ShowUnreadCount":    false,
		"UnhideKolabFolders": false,
		"CheckMailInterval":  checkMailInterval,
	}

	settings := c.LocalSettings(acct)

	result["SentFolder"] = phpString(settings.GetConf("SentFolder", ""))
	result["DraftsFolder"] = phpString(settings.GetConf("DraftsFolder", ""))
	result["JunkFolder"] = phpString(settings.GetConf("JunkFolder", ""))
	result["TrashFolder"] = phpString(settings.GetConf("TrashFolder", ""))
	result["ArchiveFolder"] = phpString(settings.GetConf("ArchiveFolder", ""))
	result["HideUnsubscribed"] = phpTruthy(settings.GetConf("HideUnsubscribed", result["HideUnsubscribed"]))
	result["defaultSort"] = phpString(settings.GetConf("defaultSort", result["defaultSort"]))
	result["useThreads"] = phpTruthy(settings.GetConf("UseThreads", result["useThreads"]))
	result["threadAlgorithm"] = phpString(settings.GetConf("threadAlgorithm", result["threadAlgorithm"]))
	result["ReplySameFolder"] = phpTruthy(settings.GetConf("ReplySameFolder", result["ReplySameFolder"]))
	result["HideDeleted"] = phpTruthy(settings.GetConf("HideDeleted", result["HideDeleted"]))
	result["ShowUnreadCount"] = phpTruthy(settings.GetConf("ShowUnreadCount", result["ShowUnreadCount"]))
	result["UnhideKolabFolders"] = phpTruthy(settings.GetConf("UnhideKolabFolders", result["UnhideKolabFolders"]))
	result["CheckMailInterval"] = maxIntValue(phpIntValue(settings.GetConf("CheckMailInterval", result["CheckMailInterval"])), minRefresh)

	return result
}

// maxIntValue is PHP's max($a, $b).
func maxIntValue(first, second int) int {
	if first > second {
		return first
	}
	return second
}

// doAccountSetup is Actions\Accounts::DoAccountSetup().
func doAccountSetup(ctx *Context) (any, error) {
	if !ctx.hasCapa("AdditionalAccounts") {
		return ctx.FalseResponse(0, "", ""), nil
	}

	acc := ctx.accounts()
	if nil == acc {
		return ctx.FalseResponse(0, "", ""), nil
	}

	main, err := acc.Main()
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	accounts, err := acc.GetAccounts(main)
	if nil != err {
		return nil, err
	}
	if nil == accounts {
		accounts = account.NewAccounts()
	}

	email := strings.TrimSpace(ctx.stringParam("email", ""))
	password := ctx.stringParam("password", "")
	isNew := !phpEmpty(ctx.GetParam("new", 1))

	var token map[string]any

	if isNew || "" != password {
		additional, loginErr := acc.Login(email, password)
		if nil != loginErr {
			return nil, loginErr
		}
		if nil == additional {
			return nil, clientError(appctx.InvalidInputArgument, "")
		}
		email = additional.Email()

		sealed, sealErr := acc.Seal(additional, main)
		if nil != sealErr {
			return nil, sealErr
		}
		token = sealed
	} else if existing, ok := accounts.Get(email); ok {
		token = account.ConvertAdditionalArray(existing)
	}

	if isNew {
		if main.Email() == email || accounts.Has(email) {
			return nil, clientError(appctx.AccountAlreadyExists, "")
		}
	} else if !accounts.Has(email) {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	accounts.Set(email, token)

	if nil != token {
		token["name"] = strings.TrimSpace(ctx.stringParam("name", ""))
		if err := acc.SetAccounts(main, accounts); nil != err {
			return nil, err
		}
	}

	return ctx.TrueResponse(nil), nil
}

// doAccountUnread is Actions\Accounts::DoAccountUnread().
func doAccountUnread(ctx *Context) (any, error) {
	acc := ctx.accounts()
	if nil == acc {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	additional, err := ctx.additionalAccountFor(acc, ctx.stringParam("email", ""))
	if nil != err {
		return nil, err
	}

	unread, err := acc.Unread(additional)
	if nil != err {
		return nil, err
	}
	if 0 > unread {
		unread = 0
	}

	return ctx.DefaultResponse(map[string]any{"unreadEmails": unread}, nil), nil
}

// doAccountImport is Actions\Accounts::DoAccountImport().
//
// Partial: the PHP method streams the messages imported by
// SnappyMail\Imap\Sync and then exits, which has no equivalent here, so the
// action resolves the source account (for the same error contract) and reports
// MailServerError instead.
func doAccountImport(ctx *Context) (any, error) {
	acc := ctx.accounts()
	if nil == acc {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	if _, err := ctx.additionalAccountFor(acc, ctx.stringParam("email", "")); nil != err {
		return nil, err
	}

	return nil, clientError(appctx.MailServerError, "Account import is not supported")
}

// additionalAccountFor is loadAdditionalAccountImapClient()'s account resolution.
func (c *Context) additionalAccountFor(acc Accounts, email string) (*account.AdditionalAccount, error) {
	email = domain.EmailToASCII(strings.TrimSpace(email))
	if "" == email {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	main, err := acc.Main()
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	accounts, err := acc.GetAccounts(main)
	if nil != err {
		return nil, err
	}
	token, ok := accounts.Get(email)
	if !ok {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	additional, err := acc.NewAdditional(token, true)
	if nil != err {
		return nil, err
	}
	if nil == additional {
		return nil, clientError(appctx.AccountDoesNotExist, "")
	}

	return additional, nil
}

// doAccountDelete is Actions\Accounts::DoAccountDelete().
func doAccountDelete(ctx *Context) (any, error) {
	acc := ctx.accounts()
	if nil == acc {
		return ctx.FalseResponse(0, "", ""), nil
	}

	if !ctx.hasCapa("AdditionalAccounts") {
		return ctx.FalseResponse(0, "", ""), nil
	}

	main, err := acc.Main()
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	email := domain.EmailToASCII(strings.TrimSpace(ctx.stringParam("emailToDelete", "")))

	accounts, err := acc.GetAccounts(main)
	if nil != err {
		return nil, err
	}
	if "" == email || !accounts.Has(email) {
		return ctx.FalseResponse(0, "", ""), nil
	}

	reload := false
	if current, currentErr := acc.Current(); nil == currentErr && nil != current && current.Email() == email {
		_ = acc.ClearAdditional()
		reload = true
	}

	accounts.Delete(email)
	if err := acc.SetAccounts(main, accounts); nil != err {
		return nil, err
	}

	return ctx.TrueResponse(map[string]any{"Reload": reload}), nil
}

// doAccountSwitch is Actions\Accounts::DoAccountSwitch().
func doAccountSwitch(ctx *Context) (any, error) {
	acc := ctx.accounts()
	if nil == acc {
		return ctx.FalseResponse(0, "", ""), nil
	}

	ok, err := acc.Switch(strings.TrimSpace(ctx.stringParam("Email", "")))
	if nil != err {
		return nil, err
	}
	if !ok {
		return ctx.FalseResponse(0, "", ""), nil
	}

	current, err := acc.Current()
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(ctx.getAccountData(current), nil), nil
}

// doIdentityUpdate is Actions\Accounts::DoIdentityUpdate().
func doIdentityUpdate(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	identity := account.NewIdentity("", "")
	if !identity.FromJSON(ctx.Params(), true) {
		return nil, clientError(appctx.InvalidInputArgument, "")
	}

	if provider := ctx.identities(); nil != provider {
		provider.UpdateIdentity(resolved, identity)
	}

	return ctx.TrueResponse(nil), nil
}

// doIdentityDelete is Actions\Accounts::DoIdentityDelete().
func doIdentityDelete(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	if !ctx.hasCapa("Identities") {
		return ctx.FalseResponse(0, "", ""), nil
	}

	id := strings.TrimSpace(ctx.stringParam("idToDelete", ""))
	if "" == id {
		return nil, clientError(appctx.UnknownError, "")
	}

	if provider := ctx.identities(); nil != provider {
		provider.DeleteIdentity(resolved, id)
	}

	return ctx.TrueResponse(nil), nil
}

// doAccountsAndIdentitiesSortOrder is Actions\Accounts::DoAccountsAndIdentitiesSortOrder().
func doAccountsAndIdentitiesSortOrder(ctx *Context) (any, error) {
	rawAccounts, accountsOK := ctx.GetParam("Accounts", nil).([]any)
	rawIdentities, identitiesOK := ctx.GetParam("Identities", nil).([]any)

	if !accountsOK && !identitiesOK {
		return ctx.FalseResponse(0, "", ""), nil
	}

	if acc := ctx.accounts(); nil != acc && accountsOK && 1 < len(rawAccounts) {
		main, err := acc.Main()
		if nil != err {
			return nil, err
		}
		if nil != main {
			accounts, err := acc.GetAccounts(main)
			if nil != err {
				return nil, err
			}

			requested := make([]string, 0, len(rawAccounts))
			for _, item := range rawAccounts {
				if email, ok := item.(string); ok {
					requested = append(requested, email)
				}
			}
			accounts.Reorder(requested)

			if err := acc.SetAccounts(main, accounts); nil != err {
				return nil, err
			}
		}
	}

	ids := make([]string, 0, len(rawIdentities))
	for _, item := range rawIdentities {
		ids = append(ids, phpString(item))
	}

	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	result := false
	if provider := ctx.identities(); nil != provider {
		result = provider.SaveOrder(resolved, ids)
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doAccountsAndIdentities is Actions\Accounts::DoAccountsAndIdentities().
func doAccountsAndIdentities(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	accountsList := []any{}
	if acc := ctx.accounts(); nil != acc {
		main, err := acc.Main()
		if nil != err {
			return nil, err
		}
		if nil != main {
			accounts, err := acc.GetAccounts(main)
			if nil != err {
				return nil, err
			}
			for _, email := range accounts.Order() {
				value, _ := accounts.Get(email)

				display := email
				name := ""
				if document, ok := value.(map[string]any); ok {
					if address := phpString(document["email"]); "" != address {
						display = address
					}
					name = phpString(document["name"])
				}

				accountsList = append(accountsList, map[string]any{
					"email": domain.EmailToUTF8(display),
					"name":  name,
				})
			}
		}
	}

	identitiesList := []any{}
	if provider := ctx.identities(); nil != provider {
		for _, identity := range provider.GetSortedIdentities(resolved, ctx.hasCapa("Identities")) {
			identitiesList = append(identitiesList, identity.JSONSerialize())
		}
	}

	return ctx.DefaultResponse(map[string]any{
		"Accounts":   accountsList,
		"Identities": identitiesList,
	}, nil), nil
}
