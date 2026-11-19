// Package appctx holds the pieces \RainLoop\Actions and its neighbours share
// between requests: the notification code table of \RainLoop\Notifications and
// the client exception of \RainLoop\Exceptions\ClientException.
//
// Phase 1 step 7 only needs the error side of it (an account or session that
// cannot be resolved is a notification code, not a plain error). Step 9 fills in
// the rest of the package: the JSON response envelope, the Capa/Layout/SignMeType
// constants and the plugin hook plumbing.
//
// The numbers below are a compatibility contract with the frontend: the
// JavaScript client switches on them, so they must never be renumbered, and the
// names must stay identical to the PHP constants because
// Notifications::GetNotificationsMessage() builds the default message from them.
package appctx

import (
	"strconv"
)

// The notification codes of \RainLoop\Notifications.
const (
	// InvalidToken is Notifications::InvalidToken: the session or account token
	// is missing, stale or cannot be decrypted.
	InvalidToken = 101
	// AuthError is Notifications::AuthError: the IMAP/SMTP/Sieve login failed.
	AuthError = 102

	// ConnectionError is Notifications::ConnectionError.
	ConnectionError = 104
	// DomainNotAllowed is Notifications::DomainNotAllowed.
	DomainNotAllowed = 109
	// AccountNotAllowed is Notifications::AccountNotAllowed.
	AccountNotAllowed = 110
	// CryptKeyError is Notifications::CryptKeyError: the stored crypt key can
	// not be (re)sealed with the current password.
	CryptKeyError = 111

	// ContactsSyncError is Notifications::ContactsSyncError.
	ContactsSyncError = 140

	// CantGetMessageList is Notifications::CantGetMessageList.
	CantGetMessageList = 201
	// CantGetMessage is Notifications::CantGetMessage.
	CantGetMessage = 202
	// CantDeleteMessage is Notifications::CantDeleteMessage.
	CantDeleteMessage = 203
	// CantMoveMessage is Notifications::CantMoveMessage.
	CantMoveMessage = 204
	// CantCopyMessage is Notifications::CantCopyMessage.
	CantCopyMessage = 205

	// CantSaveMessage is Notifications::CantSaveMessage.
	CantSaveMessage = 301
	// CantSendMessage is Notifications::CantSendMessage.
	CantSendMessage = 302
	// InvalidRecipients is Notifications::InvalidRecipients.
	InvalidRecipients = 303

	// CantSaveFilters is Notifications::CantSaveFilters.
	CantSaveFilters = 351
	// CantGetFilters is Notifications::CantGetFilters.
	CantGetFilters = 352
	// CantActivateFiltersScript is Notifications::CantActivateFiltersScript.
	CantActivateFiltersScript = 353
	// CantDeleteFiltersScript is Notifications::CantDeleteFiltersScript.
	CantDeleteFiltersScript = 354

	// CantCreateFolder is Notifications::CantCreateFolder.
	CantCreateFolder = 400
	// CantRenameFolder is Notifications::CantRenameFolder.
	CantRenameFolder = 401
	// CantDeleteFolder is Notifications::CantDeleteFolder.
	CantDeleteFolder = 402
	// CantSubscribeFolder is Notifications::CantSubscribeFolder.
	CantSubscribeFolder = 403
	// CantUnsubscribeFolder is Notifications::CantUnsubscribeFolder.
	CantUnsubscribeFolder = 404
	// CantDeleteNonEmptyFolder is Notifications::CantDeleteNonEmptyFolder.
	CantDeleteNonEmptyFolder = 405

	// DomainAlreadyExists is Notifications::DomainAlreadyExists.
	DomainAlreadyExists = 601

	// DemoSendMessageError is Notifications::DemoSendMessageError.
	DemoSendMessageError = 750
	// DemoAccountError is Notifications::DemoAccountError.
	DemoAccountError = 751

	// AccountAlreadyExists is Notifications::AccountAlreadyExists.
	AccountAlreadyExists = 801
	// AccountDoesNotExist is Notifications::AccountDoesNotExist.
	AccountDoesNotExist = 802
	// AccountSwitchFailed is Notifications::AccountSwitchFailed.
	AccountSwitchFailed = 803
	// AccountFilterError is Notifications::AccountFilterError.
	AccountFilterError = 804

	// MailServerError is Notifications::MailServerError.
	MailServerError = 901
	// ClientViewError is Notifications::ClientViewError: the message is taken
	// from the wrapped error.
	ClientViewError = 902
	// InvalidInputArgument is Notifications::InvalidInputArgument.
	InvalidInputArgument = 903
	// UnknownError is Notifications::UnknownError.
	UnknownError = 999

	// InvalidPluginPackage is Notifications::InvalidPluginPackage.
	InvalidPluginPackage = 703
	// UnsupportedPluginPackage is Notifications::UnsupportedPluginPackage.
	UnsupportedPluginPackage = 704
	// CantSavePluginSettings is Notifications::CantSavePluginSettings.
	CantSavePluginSettings = 705
)

// notificationNames maps every code onto the PHP constant name. It is the
// equivalent of the \array_search($iCode, $class->getConstants()) lookup of
// Notifications::GetNotificationsMessage().
var notificationNames = map[int]string{
	InvalidToken:              "InvalidToken",
	AuthError:                 "AuthError",
	ConnectionError:           "ConnectionError",
	DomainNotAllowed:          "DomainNotAllowed",
	AccountNotAllowed:         "AccountNotAllowed",
	CryptKeyError:             "CryptKeyError",
	ContactsSyncError:         "ContactsSyncError",
	CantGetMessageList:        "CantGetMessageList",
	CantGetMessage:            "CantGetMessage",
	CantDeleteMessage:         "CantDeleteMessage",
	CantMoveMessage:           "CantMoveMessage",
	CantCopyMessage:           "CantCopyMessage",
	CantSaveMessage:           "CantSaveMessage",
	CantSendMessage:           "CantSendMessage",
	InvalidRecipients:         "InvalidRecipients",
	CantSaveFilters:           "CantSaveFilters",
	CantGetFilters:            "CantGetFilters",
	CantActivateFiltersScript: "CantActivateFiltersScript",
	CantDeleteFiltersScript:   "CantDeleteFiltersScript",
	CantCreateFolder:          "CantCreateFolder",
	CantRenameFolder:          "CantRenameFolder",
	CantDeleteFolder:          "CantDeleteFolder",
	CantSubscribeFolder:       "CantSubscribeFolder",
	CantUnsubscribeFolder:     "CantUnsubscribeFolder",
	CantDeleteNonEmptyFolder:  "CantDeleteNonEmptyFolder",
	DomainAlreadyExists:       "DomainAlreadyExists",
	DemoSendMessageError:      "DemoSendMessageError",
	DemoAccountError:          "DemoAccountError",
	AccountAlreadyExists:      "AccountAlreadyExists",
	AccountDoesNotExist:       "AccountDoesNotExist",
	AccountSwitchFailed:       "AccountSwitchFailed",
	AccountFilterError:        "AccountFilterError",
	MailServerError:           "MailServerError",
	ClientViewError:           "ClientViewError",
	InvalidInputArgument:      "InvalidInputArgument",
	UnknownError:              "UnknownError",
	InvalidPluginPackage:      "InvalidPluginPackage",
	UnsupportedPluginPackage:  "UnsupportedPluginPackage",
	CantSavePluginSettings:    "CantSavePluginSettings",
}

// Name returns the PHP constant name of a notification code, or "" when the
// code is not part of the table.
func Name(code int) string { return notificationNames[code] }

// Message is Notifications::GetNotificationsMessage(): "Name[code]", or
// "UnknownNotification[code]" for a code outside the table.
//
// A ClientViewError carries the message of the wrapped error, exactly like the
// PHP special case (step 17 needs that for message view failures).
func Message(code int, previous error) string {
	if ClientViewError == code && nil != previous {
		return previous.Error()
	}

	name := Name(code)
	if "" == name {
		name = "UnknownNotification"
	}
	return name + "[" + strconv.Itoa(code) + "]"
}
