package service

// The guest and user states of AppData: Actions::AppData()'s
// "if ($oAccount) … else …" branches plus Actions\Accounts::getAccountData()
// and the "settings"/"settings_local" overlay.

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// signMeTypes is the Enumerations\SignMeType table of the "signMe" value.
var signMeTypes = map[string]int{
	"DefaultOff": 0,
	"DefaultOn":  1,
	"Unused":     2,
}

// guestAppData is the "else" branch of AppData(): no account is signed in.
func (a *AppData) guestAppData(result map[string]any) {
	config := a.config()

	if a.options.Dev {
		result["DevEmail"] = config.GetString("labs", "dev_email", "")
		result["DevPassword"] = config.GetString("labs", "dev_password", "")
	} else {
		result["DevEmail"] = ""
		result["DevPassword"] = ""
	}

	// PHP: [...][(string) $oConfig->Get('login', 'sign_me_auto', …)]; a value
	// outside the table is an undefined array key and yields null.
	signMe, ok := signMeTypes[config.GetString("login", "sign_me_auto", "DefaultOff")]
	if ok {
		result["signMe"] = signMe
	} else {
		result["signMe"] = nil
	}
}

// userAppData is the "if ($oAccount)" branch of AppData().
func (a *AppData) userAppData(
	result, system map[string]any,
	resolved *account.Account,
	mainEmail string,
	session *Session,
) {
	config := a.config()

	result["Auth"] = true
	result["accountSignMe"] = hasSignMe(session)
	result["allowSpellcheck"] = config.GetBool("defaults", "allow_spellcheck", false)
	result["ViewHTML"] = config.GetBool("defaults", "view_html", true)
	result["ViewImages"] = config.GetString("defaults", "view_images", "ask")
	result["ViewImagesWhitelist"] = ""
	result["RemoveColors"] = config.GetBool("defaults", "remove_colors", false)
	result["AllowStyles"] = false
	result["ListInlineAttachments"] = false
	result["CollapseBlockquotes"] = config.GetBool("defaults", "collapse_blockquotes", true)
	result["MaxBlockquotesLevel"] = 0
	result["simpleAttachmentsList"] = false
	result["listGrouped"] = config.GetBool("defaults", "mail_list_grouped", false)
	result["MessagesPerPage"] = phpMax(10,
		defaultIfZero(config.GetInt("webmail", "messages_per_page", 25), 25))
	result["messageNewWindow"] = false
	result["markdown"] = false
	result["messageReadAuto"] = true
	result["MessageReadDelay"] = config.GetInt("webmail", "message_read_delay", 5)
	result["MsgDefaultAction"] = config.GetInt("defaults", "msg_default_action", 1)
	result["SoundNotification"] = true
	result["NotificationSound"] = "new-mail"
	result["DesktopNotifications"] = true
	result["Layout"] = config.GetInt("defaults", "view_layout", 1)
	result["EditorDefaultType"] = stripForced(config.GetString("defaults", "view_editor_type", ""))
	result["editorWysiwyg"] = "Squire"
	result["UseCheckboxesInList"] = config.GetBool("defaults", "view_use_checkboxes", true)
	result["showNextMessage"] = config.GetBool("defaults", "view_show_next_message", false)
	result["AutoLogout"] = config.GetInt("defaults", "autologout", 30)
	result["keyPassForget"] = 15
	result["AllowDraftAutosave"] = config.GetBool("defaults", "allow_draft_autosave", true)
	result["ContactsAutosave"] = config.GetBool("defaults", "contacts_autosave", true)

	// MainAccount or AdditionalAccount
	for key, value := range a.accountData(resolved, mainEmail) {
		result[key] = value
	}

	attachmentsActions := []any{}
	if phpBool(a.capa(false, resolved)["AttachmentsActions"]) {
		// PHP: class_exists('PharData') || class_exists('ZipArchive').
		attachmentsActions = append(attachmentsActions, "zip")
	}

	system["allowAppendMessage"] = config.GetBool("labs", "allow_message_append", false)
	system["folderSpecLimit"] = config.GetInt("labs", "folders_spec_limit", 50)
	system["listPermanentFiltered"] = "" != strings.TrimSpace(
		config.GetString("imap", "message_list_permanent_filter", ""))
	system["attachmentsActions"] = attachmentsActions
	system["customLogoutLink"] = config.GetString("labs", "custom_logout_link", "")

	a.contactsSync(result, resolved)
	a.mailTo(session, result)
	a.overlaySettings(result, resolved)
	result["newMailSounds"] = a.newMailSounds()
}

// accountData is Actions\Accounts::getAccountData(): the per-account fields of
// the "settings_local" document on top of the defaults.
func (a *AppData) accountData(resolved *account.Account, mainEmail string) map[string]any {
	config := a.config()
	minRefreshInterval := config.GetInt("webmail", "min_refresh_interval", 5)

	result := map[string]any{
		"Email":              resolved.Email(),
		"accountHash":        resolved.Hash(a.options.Salt),
		"mainEmail":          mainEmail,
		"localDomains":       a.localDomains(),
		"contactsAllowed":    a.addressBookActive(resolved),
		"HideUnsubscribed":   false,
		"defaultSort":        "",
		"useThreads":         config.GetBool("defaults", "mail_use_threads", false),
		"threadAlgorithm":    "",
		"ReplySameFolder":    config.GetBool("defaults", "mail_reply_same_folder", false),
		"HideDeleted":        true,
		"ShowUnreadCount":    false,
		"UnhideKolabFolders": false,
		"CheckMailInterval":  phpMax(15, minRefreshInterval),
	}

	settings := a.localSettings(resolved)
	if nil == settings {
		return result
	}

	result["SentFolder"] = phpString(settings.GetConf("SentFolder", ""))
	result["DraftsFolder"] = phpString(settings.GetConf("DraftsFolder", ""))
	result["JunkFolder"] = phpString(settings.GetConf("JunkFolder", ""))
	result["TrashFolder"] = phpString(settings.GetConf("TrashFolder", ""))
	result["ArchiveFolder"] = phpString(settings.GetConf("ArchiveFolder", ""))
	result["HideUnsubscribed"] = phpBool(settings.GetConf("HideUnsubscribed", result["HideUnsubscribed"]))
	result["defaultSort"] = phpString(settings.GetConf("defaultSort", result["defaultSort"]))
	result["useThreads"] = phpBool(settings.GetConf("UseThreads", result["useThreads"]))
	result["threadAlgorithm"] = phpString(settings.GetConf("threadAlgorithm", result["threadAlgorithm"]))
	result["ReplySameFolder"] = phpBool(settings.GetConf("ReplySameFolder", result["ReplySameFolder"]))
	result["HideDeleted"] = phpBool(settings.GetConf("HideDeleted", result["HideDeleted"]))
	result["ShowUnreadCount"] = phpBool(settings.GetConf("ShowUnreadCount", result["ShowUnreadCount"]))
	result["UnhideKolabFolders"] = phpBool(settings.GetConf("UnhideKolabFolders", result["UnhideKolabFolders"]))
	result["CheckMailInterval"] = phpMax(
		phpInt(settings.GetConf("CheckMailInterval", result["CheckMailInterval"])), minRefreshInterval)

	return result
}

// contactsSync is the "if ($aResult['contactsAllowed'] && contacts.allow_sync)"
// block.
func (a *AppData) contactsSync(result map[string]any, resolved *account.Account) {
	if !phpBool(result["contactsAllowed"]) || !a.config().GetBool("contacts", "allow_sync", false) {
		return
	}
	if nil == a.options.AddressBook {
		return
	}

	data := a.options.AddressBook.SyncData(resolved)
	if nil == data {
		data = map[string]any{"Mode": 0, "Url": "", "User": ""}
	}

	// The password is never sent to the client; dummy marks "one is stored".
	if phpEmpty(data["Password"]) {
		data["Password"] = ""
	} else {
		data["Password"] = dummy
	}
	data["Interval"] = phpMax(20, phpMin(320, a.config().GetInt("contacts", "sync_interval", 20)))
	delete(data, "PasswordHMAC")

	result["ContactsSync"] = data
}

// mailTo is the single-use smmailtoauth cookie of the "mailto:" handler
// (ServiceActions::ServiceMailto): its address is reported once and the cookie
// is dropped.
func (a *AppData) mailTo(session *Session, result map[string]any) {
	if nil == session || nil == session.Jar || nil == session.Authenticator {
		return
	}
	if !session.Jar.Has(auth.AuthMailtoTokenKey) {
		return
	}

	token := session.Jar.Get(auth.AuthMailtoTokenKey)
	session.Jar.Clear(session.Response, auth.AuthMailtoTokenKey)

	data := session.Authenticator.DecodeKeyValuesQ(token, "")
	if phpEmpty(data["MailTo"]) || "MailTo" != phpString(data["MailTo"]) || phpEmpty(data["To"]) {
		return
	}

	result["mailToEmail"] = domain.EmailToUTF8(phpString(data["To"]))
}

// overlaySettings is the MainAccount "settings" overlay of AppData().
func (a *AppData) overlaySettings(result map[string]any, resolved *account.Account) {
	settings := a.mainSettings(resolved)
	if nil == settings {
		return
	}

	result["hourCycle"] = phpString(settings.GetConf("hourCycle", ""))

	// PHP migrates "MPP" into MessagesPerPage in memory (not saved).
	if !phpBool(settings.GetConf("MessagesPerPage", nil)) {
		settings.SetConf("MessagesPerPage", settings.GetConf("MPP", result["MessagesPerPage"]))
	}

	result["EditorDefaultType"] = stripForced(
		phpString(settings.GetConf("EditorDefaultType", result["EditorDefaultType"])))
	result["editorWysiwyg"] = phpString(settings.GetConf("editorWysiwyg", result["editorWysiwyg"]))
	result["requestReadReceipt"] = phpBool(settings.GetConf("requestReadReceipt", false))
	result["requestDsn"] = phpBool(settings.GetConf("requestDsn", false))
	result["requireTLS"] = phpBool(settings.GetConf("requireTLS", false))
	result["pgpSign"] = phpBool(settings.GetConf("pgpSign", false))
	result["pgpEncrypt"] = phpBool(settings.GetConf("pgpEncrypt", false))
	result["allowSpellcheck"] = phpBool(settings.GetConf("allowSpellcheck", result["allowSpellcheck"]))

	result["ViewHTML"] = phpBool(settings.GetConf("ViewHTML", result["ViewHTML"]))
	showImages := phpBool(settings.GetConf("ShowImages", false))
	fallbackViewImages := result["ViewImages"]
	if showImages {
		fallbackViewImages = "always"
	}
	// No cast: ViewImages is a string ("ask"/"match"/"always").
	result["ViewImages"] = settings.GetConf("ViewImages", fallbackViewImages)
	result["ViewImagesWhitelist"] = settings.GetConf("ViewImagesWhitelist", "")
	result["RemoveColors"] = phpBool(settings.GetConf("RemoveColors", result["RemoveColors"]))
	result["AllowStyles"] = phpBool(settings.GetConf("AllowStyles", result["AllowStyles"]))
	result["ListInlineAttachments"] = phpBool(
		settings.GetConf("ListInlineAttachments", result["ListInlineAttachments"]))
	result["CollapseBlockquotes"] = phpBool(
		settings.GetConf("CollapseBlockquotes", result["CollapseBlockquotes"]))
	result["MaxBlockquotesLevel"] = phpInt(
		settings.GetConf("MaxBlockquotesLevel", result["MaxBlockquotesLevel"]))
	result["simpleAttachmentsList"] = phpBool(
		settings.GetConf("simpleAttachmentsList", result["simpleAttachmentsList"]))
	result["listGrouped"] = phpBool(settings.GetConf("listGrouped", result["listGrouped"]))
	result["ContactsAutosave"] = phpBool(settings.GetConf("ContactsAutosave", result["ContactsAutosave"]))
	result["MessagesPerPage"] = phpMax(10, defaultIfZero(
		phpInt(settings.GetConf("MessagesPerPage", result["MessagesPerPage"])),
		phpInt(result["MessagesPerPage"])))
	result["messageNewWindow"] = phpBool(settings.GetConf("messageNewWindow", result["messageNewWindow"]))
	result["markdown"] = phpBool(settings.GetConf("markdown", result["markdown"]))
	result["messageReadAuto"] = phpInt(settings.GetConf("messageReadAuto", result["messageReadAuto"]))
	result["MessageReadDelay"] = phpInt(settings.GetConf("MessageReadDelay", result["MessageReadDelay"]))
	result["MsgDefaultAction"] = phpInt(settings.GetConf("MsgDefaultAction", result["MsgDefaultAction"]))
	result["SoundNotification"] = phpBool(settings.GetConf("SoundNotification", result["SoundNotification"]))
	result["NotificationSound"] = phpString(
		settings.GetConf("NotificationSound", result["NotificationSound"]))
	result["DesktopNotifications"] = phpBool(
		settings.GetConf("DesktopNotifications", result["DesktopNotifications"]))
	result["UseCheckboxesInList"] = phpBool(
		settings.GetConf("UseCheckboxesInList", result["UseCheckboxesInList"]))
	result["showNextMessage"] = phpBool(settings.GetConf("showNextMessage", result["showNextMessage"]))
	result["AllowDraftAutosave"] = phpBool(
		settings.GetConf("AllowDraftAutosave", result["AllowDraftAutosave"]))
	result["AutoLogout"] = phpInt(settings.GetConf("AutoLogout", result["AutoLogout"]))
	result["keyPassForget"] = phpInt(settings.GetConf("keyPassForget", result["keyPassForget"]))
	result["Layout"] = phpInt(settings.GetConf("Layout", result["Layout"]))
	result["Resizer4Width"] = phpInt(settings.GetConf("Resizer4Width", 0))
	result["Resizer5Width"] = phpInt(settings.GetConf("Resizer5Width", 0))
	result["Resizer5Height"] = phpInt(settings.GetConf("Resizer5Height", 0))

	result["fontSansSerif"] = settings.GetConf("fontSansSerif", "")
	result["fontSerif"] = settings.GetConf("fontSerif", "")
	result["fontMono"] = settings.GetConf("fontMono", "")

	if phpBool(a.capa(false, resolved)["UserBackground"]) {
		result["userBackgroundName"] = phpString(settings.GetConf("UserBackgroundName", ""))
		result["userBackgroundHash"] = phpString(settings.GetConf("UserBackgroundHash", ""))
	}
}

// mainSettings is SettingsProvider()->Load($oAccount).
func (a *AppData) mainSettings(resolved *account.Account) *storage.Settings {
	if nil == resolved || nil == a.options.Settings {
		return nil
	}
	return a.options.Settings.Load(resolved)
}

// localSettings is SettingsProvider(true)->Load($oAccount).
func (a *AppData) localSettings(resolved *account.Account) *storage.Settings {
	if nil == resolved || nil == a.options.LocalSettings {
		return nil
	}
	return a.options.LocalSettings.Load(resolved)
}

// addressBookActive is AddressBookProvider($oAccount)->IsActive().
func (a *AppData) addressBookActive(resolved *account.Account) bool {
	if nil == a.options.AddressBook {
		return false
	}
	return a.options.AddressBook.IsActive(resolved)
}

// stripForced is str_replace('Forced', ”, $value): the "Forced" editor types
// are internal only.
func stripForced(value string) string {
	return strings.ReplaceAll(value, "Forced", "")
}

// defaultIfZero is PHP's "$value ?: $default" for an int.
func defaultIfZero(value, fallback int) int {
	if 0 == value {
		return fallback
	}
	return value
}
