package handler

// The remaining session scoped actions of \RainLoop\Actions\User (Step 21):
// DoSettingsUpdate, DoQuota, DoSuggestions, DoClearUserBackground and
// DoAppDelayStart.
//
// DoSettingsUpdate is the important one: it writes the "settings" and
// "settings_local" documents with exactly the coercions of the PHP
// setSettingsFromParams() helper, so the files stay interchangeable with the
// PHP backend.

import (
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The setSettingsFromParams() value types.
const (
	settingsTypeString = "string"
	settingsTypeInt    = "int"
	settingsTypeBool   = "bool"
)

// The DoAppDelayStart() timer constants ("$iOneDay1 = 3600 * 23").
const (
	day1Seconds = 3600 * 23
	day2Seconds = 3600 * 25
	// gcHours is the "GC(48)" age of the cache and file garbage collectors.
	gcHours = 48
)

// cacheTimersKey is the NOBODY storage key of the GC timer bookkeeping.
const cacheTimersKey = "Cache/Timers"

// backgroundKey is the CONFIG storage key of the user background blob.
const backgroundKey = "background"

// Suggestions is Providers\Suggestions::Process() (the suggestion provider
// built by internal/suggestions). A nil value yields an empty list.
type Suggestions interface {
	Process(account *account.Account, query string, limit int) []any
}

// cacherGC is the optional MailSo\Cache\CacheClient::GC() extension of the
// dispatcher Cacher; *storage.Cache satisfies it.
type cacherGC interface {
	GC(hours int) bool
}

// filesGC is the optional Providers\Files::GC() extension of the FilesProvider;
// *storage.Files satisfies it.
type filesGC interface {
	GC(hours int) bool
}

// connectionTokenUpdater is the optional Utils::UpdateConnectionToken() of the
// session; *auth.Authenticator satisfies it.
type connectionTokenUpdater interface {
	UpdateConnectionToken()
}

// RegisterUser registers the DoSettingsUpdate/DoQuota/DoSuggestions/
// DoClearUserBackground/DoAppDelayStart actions.
func RegisterUser(d *Dispatcher) {
	d.Register("SettingsUpdate", doSettingsUpdate)
	d.Register("Quota", doQuota)
	d.Register("Suggestions", doSuggestions)
	d.Register("ClearUserBackground", doClearUserBackground)
	d.Register("AppDelayStart", doAppDelayStart)
}

// doSettingsUpdate is Actions\User::DoSettingsUpdate().
func doSettingsUpdate(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	config := ctx.Config()

	settings := ctx.MainSettings(resolved)
	localSettings := ctx.LocalSettings(resolved)

	if config.GetBool("webmail", "allow_languages_on_settings", true) {
		ctx.setSettingsFromParams(settings, "language", settingsTypeString, func(value any) any {
			return ctx.validateLanguage(phpString(value))
		})
	}
	ctx.setSettingsFromParams(settings, "hourCycle", settingsTypeString, nil)

	if ctx.hasCapa("Themes") {
		ctx.setSettingsFromParams(localSettings, "Theme", settingsTypeString, func(value any) any {
			return ctx.validateTheme(phpString(value))
		})
		ctx.setSettingsFromParams(settings, "fontSansSerif", settingsTypeString, nil)
		ctx.setSettingsFromParams(settings, "fontSerif", settingsTypeString, nil)
		ctx.setSettingsFromParams(settings, "fontMono", settingsTypeString, nil)
	}

	ctx.setSettingsFromParams(settings, "MessagesPerPage", settingsTypeInt, clampCallback(10, 100))
	ctx.setSettingsFromParams(settings, "Layout", settingsTypeInt, layoutCallback)

	ctx.setSettingsFromParams(settings, "EditorDefaultType", settingsTypeString, nil)
	ctx.setSettingsFromParams(settings, "editorWysiwyg", settingsTypeString, nil)
	ctx.setSettingsFromParams(settings, "requestReadReceipt", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "requestDsn", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "requireTLS", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "pgpSign", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "pgpEncrypt", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "allowSpellcheck", settingsTypeBool, nil)

	ctx.setSettingsFromParams(settings, "ViewHTML", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "ViewImages", settingsTypeString, nil)
	ctx.setSettingsFromParams(settings, "ViewImagesWhitelist", settingsTypeString, nil)
	ctx.setSettingsFromParams(settings, "RemoveColors", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "AllowStyles", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "ListInlineAttachments", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "CollapseBlockquotes", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "MaxBlockquotesLevel", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "simpleAttachmentsList", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "listGrouped", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "ContactsAutosave", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "DesktopNotifications", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "SoundNotification", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "NotificationSound", settingsTypeString, nil)
	ctx.setSettingsFromParams(settings, "UseCheckboxesInList", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "AllowDraftAutosave", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "AutoLogout", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "keyPassForget", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "messageNewWindow", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "messageReadAuto", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "MessageReadDelay", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "MsgDefaultAction", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "showNextMessage", settingsTypeBool, nil)
	ctx.setSettingsFromParams(settings, "markdown", settingsTypeBool, nil)

	ctx.setSettingsFromParams(settings, "Resizer4Width", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "Resizer5Width", settingsTypeInt, nil)
	ctx.setSettingsFromParams(settings, "Resizer5Height", settingsTypeInt, nil)

	ctx.setSettingsFromParams(localSettings, "defaultSort", settingsTypeString, nil)
	ctx.setSettingsFromParams(localSettings, "UseThreads", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "threadAlgorithm", settingsTypeString, nil)
	ctx.setSettingsFromParams(localSettings, "ReplySameFolder", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "HideUnsubscribed", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "HideDeleted", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "UnhideKolabFolders", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "ShowUnreadCount", settingsTypeBool, nil)
	ctx.setSettingsFromParams(localSettings, "CheckMailInterval", settingsTypeInt, nil)

	// PHP: $oSettings->save() && $oSettingsLocal->save() (the && short-circuits).
	saved := false
	if err := ctx.SaveMainSettings(resolved, settings); nil == err {
		if err := ctx.SaveLocalSettings(resolved, localSettings); nil == err {
			saved = true
		}
	}

	return ctx.DefaultResponse(saved, nil), nil
}

// setSettingsFromParams is Actions\User::setSettingsFromParams().
//
// The int branch is a PHP quirk kept as-is: the callback result is assigned to
// $sValue but the raw (int) value is stored, so the MessagesPerPage 10..100 and
// Layout 0..2 clamps never take effect. To keep the settings files byte
// identical with PHP the raw value is stored here as well (the callback is
// still invoked for parity, its result discarded).
func (c *Context) setSettingsFromParams(
	settings *storage.Settings,
	name, kind string,
	callback func(any) any,
) {
	if !c.HasParam(name) {
		return
	}

	value := c.GetParam(name, "")

	switch kind {
	case settingsTypeInt:
		intValue := phpIntValue(value)
		if nil != callback {
			_ = callback(intValue)
		}
		settings.SetConf(name, intValue)

	case settingsTypeBool:
		// PHP: !empty($sValue) && 'false' !== $sValue.
		settings.SetConf(name, !phpEmpty(value) && "false" != scalarString(value))

	default: // settingsTypeString
		text := phpString(value)
		if nil != callback {
			// PHP: $sValue = $cCallback($sValue); SetConf(…, (string) $sValue).
			text = phpString(callback(text))
		}
		settings.SetConf(name, text)
	}
}

// clampCallback reproduces the MessagesPerPage "min(100, max(10, $iValue))"
// closure of DoSettingsUpdate().
func clampCallback(min, max int) func(any) any {
	return func(value any) any {
		number := phpIntValue(value)
		if number < min {
			number = min
		}
		if number > max {
			number = max
		}
		return number
	}
}

// layoutCallback reproduces the Layout closure: a value outside 0..2 falls back
// to SIDE_PREVIEW (1).
func layoutCallback(value any) any {
	number := phpIntValue(value)
	if 0 != number && 1 != number && 2 != number {
		return 1
	}
	return number
}

// hasCapa is Actions::GetCapa() for the capabilities Step 21 reads.
func (c *Context) hasCapa(name string) bool {
	config := c.Config()
	switch name {
	case "Themes":
		return config.GetBool("webmail", "allow_themes", false)
	case "UserBackground":
		return config.GetBool("webmail", "allow_user_background", false)
	case "Contacts":
		return config.GetBool("contacts", "enable", false)
	case "AdditionalAccounts":
		return config.GetBool("webmail", "allow_additional_accounts", false)
	case "Identities":
		return config.GetBool("webmail", "allow_additional_identities", false)
	}
	return false
}

// validateLanguage is Actions\Localization::ValidateLanguage().
func (c *Context) validateLanguage(language string) string {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.language {
		return language
	}
	return c.dispatcher.language(language)
}

// validateTheme is Actions\Themes::ValidateTheme().
func (c *Context) validateTheme(theme string) string {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.theme {
		return theme
	}
	return c.dispatcher.theme(theme)
}

// suggestions is Providers\Suggestions(), or nil when not wired.
func (c *Context) suggestions() Suggestions {
	if nil == c || nil == c.dispatcher {
		return nil
	}
	return c.dispatcher.suggestions
}

// doQuota is Actions\User::DoQuota().
func doQuota(ctx *Context) (any, error) {
	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}

	var quota []int
	if nil != client {
		quota = client.ImapClient().QuotaRoot("INBOX")
	}
	if nil == quota {
		// PHP: ImapClient()->QuotaRoot() ?: [0, 0, 0, 0].
		quota = []int{0, 0, 0, 0}
	}

	return ctx.DefaultResponse(quota, nil), nil
}

// doSuggestions is Actions\User::DoSuggestions().
func doSuggestions(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	query := strings.TrimSpace(ctx.stringParam("Query", ""))
	limit := ctx.Config().GetInt("contacts", "suggestions_limit", 20)

	// PHP: RunHook('json.suggestions-input-parameters',
	//   array(&$sQuery, &$iLimit, $oAccount)).
	ctx.dispatcher.runHook("json.suggestions-input-parameters", &query, &limit, resolved)

	var result []any
	if provider := ctx.suggestions(); nil != provider {
		result = provider.Process(resolved, query, limit)
	}
	if nil == result {
		result = []any{}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doClearUserBackground is Actions\User::DoClearUserBackground().
func doClearUserBackground(ctx *Context) (any, error) {
	if !ctx.hasCapa("UserBackground") {
		return ctx.FalseResponse(0, "", ""), nil
	}

	resolved, err := ctx.requireAccount()
	if err != nil {
		return nil, err
	}

	settings := ctx.MainSettings(resolved)

	if storageProvider := ctx.Storage(); nil != storageProvider {
		storageProvider.Clear(resolved, storage.TypeConfig, backgroundKey)
	}

	settings.SetConf("UserBackgroundName", "")
	settings.SetConf("UserBackgroundHash", "")

	saved := false
	if err := ctx.SaveMainSettings(resolved, settings); nil == err {
		saved = true
	}

	return ctx.DefaultResponse(saved, nil), nil
}

// doAppDelayStart is Actions\User::DoAppDelayStart(): the 23h/25h garbage
// collection timers of the cache, the storage and the uploaded files.
func doAppDelayStart(ctx *Context) (any, error) {
	ctx.updateConnectionToken()

	provider := ctx.Storage()

	timers := ""
	if nil != provider {
		timers, _ = provider.Get(nil, storage.TypeNobody, cacheTimersKey)
	}
	parts := strings.Split(timers, ",")

	mainCacheTime := timerValue(parts, 0)
	filesCacheTime := timerValue(parts, 1)

	now := time.Now().Unix()

	mainCache := false
	filesCache := false

	if 0 == mainCacheTime || mainCacheTime+day1Seconds < now {
		mainCache = true
		mainCacheTime = now
	}
	if 0 == filesCacheTime || filesCacheTime+day2Seconds < now {
		filesCache = true
		filesCacheTime = now
	}

	if mainCache || filesCache {
		encoded := strconv.FormatInt(mainCacheTime, 10) + "," + strconv.FormatInt(filesCacheTime, 10)
		if nil == provider || nil != provider.Put(nil, storage.TypeNobody, cacheTimersKey, encoded) {
			mainCache = false
			filesCache = false
		}
	}

	if mainCache {
		ctx.logWrite("Cacher GC: Begin", logx.Info, "")
		if cacher, ok := ctx.Cacher().(cacherGC); ok && nil != cacher {
			cacher.GC(gcHours)
		}
		ctx.logWrite("Cacher GC: End", logx.Info, "")

		ctx.logWrite("Storage GC: Begin", logx.Info, "")
		if nil != provider {
			provider.GC()
		}
		ctx.logWrite("Storage GC: End", logx.Info, "")
	} else if filesCache {
		ctx.logWrite("Files GC: Begin", logx.Info, "")
		if files, ok := ctx.dispatcher.files.(filesGC); ok && nil != files {
			files.GC(gcHours)
		}
		ctx.logWrite("Files GC: End", logx.Info, "")
	}

	return ctx.TrueResponse(nil), nil
}

// timerValue is the "!empty($aTimers[$index]) && is_numeric(...) ? (int) … : 0"
// lookup of the stored timers.
func timerValue(parts []string, index int) int64 {
	if index >= len(parts) {
		return 0
	}
	value := strings.TrimSpace(parts[index])
	if "" == value || "0" == value {
		return 0
	}
	number, err := strconv.ParseFloat(value, 64)
	if nil != err {
		return 0
	}
	return int64(number)
}

// updateConnectionToken is Utils::UpdateConnectionToken(): refresh the 30 day
// connection cookie when the session exposes it.
func (c *Context) updateConnectionToken() {
	if nil == c || nil == c.session {
		return
	}
	if updater, ok := c.session.(connectionTokenUpdater); ok && nil != updater {
		updater.UpdateConnectionToken()
	}
}

// logWrite is Actions::logWrite() for a single line of the GC bookkeeping.
func (c *Context) logWrite(message string, level logx.Level, category string) {
	if nil == c || nil == c.dispatcher {
		return
	}
	c.dispatcher.logWrite(message, level, category)
}
