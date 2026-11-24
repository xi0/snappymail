package handler

// Tests of the Step 21 user actions: the settings documents DoSettingsUpdate
// writes (including the PHP int quirk), the DoQuota fallback, the DoSuggestions
// wiring, DoClearUserBackground and the DoAppDelayStart GC timers.

import (
	"net/http"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// userConfig is an application.ini stand-in with a keyed value table.
type userConfig struct {
	bools map[string]bool
	ints  map[string]int
	strs  map[string]string
}

func configKey(section, key string) string { return section + "." + key }

// GetString returns the configured string or the default.
func (c userConfig) GetString(section, key, defaultValue string) string {
	if value, ok := c.strs[configKey(section, key)]; ok {
		return value
	}
	return defaultValue
}

// GetBool returns the configured bool or the default.
func (c userConfig) GetBool(section, key string, defaultValue bool) bool {
	if value, ok := c.bools[configKey(section, key)]; ok {
		return value
	}
	return defaultValue
}

// GetInt returns the configured int or the default.
func (c userConfig) GetInt(section, key string, defaultValue int) int {
	if value, ok := c.ints[configKey(section, key)]; ok {
		return value
	}
	return defaultValue
}

// fakeStorageProvider is an in-memory StorageProvider.
type fakeStorageProvider struct {
	nobody  map[string]string
	cleared []string
	gcCalls int
}

func newFakeStorageProvider() *fakeStorageProvider {
	return &fakeStorageProvider{nobody: map[string]string{}}
}

// Get returns a NOBODY entry.
func (f *fakeStorageProvider) Get(_ any, storageType storage.StorageType, key string) (string, bool) {
	if storage.TypeNobody == storageType {
		value, ok := f.nobody[key]
		return value, ok
	}
	return "", false
}

// Put stores a NOBODY entry.
func (f *fakeStorageProvider) Put(_ any, storageType storage.StorageType, key, value string) error {
	if storage.TypeNobody == storageType {
		f.nobody[key] = value
	}
	return nil
}

// Clear records the cleared key.
func (f *fakeStorageProvider) Clear(_ any, _ storage.StorageType, key string) bool {
	f.cleared = append(f.cleared, key)
	return true
}

// GC counts the calls.
func (f *fakeStorageProvider) GC() { f.gcCalls++ }

// fakeCacher is the per-account cache with the GC extension.
type fakeCacher struct {
	gcCalls int
}

// GetDefault returns the default; no receipt is cached in these tests.
func (f *fakeCacher) GetDefault(_, defaultValue string) string { return defaultValue }

// GC counts the calls.
func (f *fakeCacher) GC(int) bool {
	f.gcCalls++
	return true
}

// fakeUserMail is a Mail layer that returns a fixed account and client.
type fakeUserMail struct {
	account *account.Account
	client  *mail.Client
}

// Connect returns the fixed values.
func (f *fakeUserMail) Connect() (*account.Account, *mail.Client, error) {
	return f.account, f.client, nil
}

// fakeUserSuggestions is a handler.Suggestions that records its arguments.
type fakeUserSuggestions struct {
	query  string
	limit  int
	result []any
}

// Process records the arguments and returns the configured result.
func (f *fakeUserSuggestions) Process(_ *account.Account, query string, limit int) []any {
	f.query = query
	f.limit = limit
	return f.result
}

// userSettings builds an empty in-memory providers\Settings document store.
func newUserSettings() *memorySettings { return newMemorySettings() }

// userContext builds a Context of a user action.
func userContext(dispatcher *Dispatcher, session Session, params map[string]any) *Context {
	return &Context{dispatcher: dispatcher, session: session, params: params}
}

// TestSettingsUpdateIntQuirk checks that DoSettingsUpdate stores the raw int,
// not the clamp result (a PHP quirk kept as-is), for MessagesPerPage and Layout.
func TestSettingsUpdateIntQuirk(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	main := newUserSettings()
	local := newUserSettings()

	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main,
		LocalSettings: local,
	})

	response := mustResponse(doSettingsUpdate(userContext(dispatcher, session, map[string]any{
		"MessagesPerPage": 5,
		"Layout":          9,
	})))
	if nil == response {
		t.Fatal("doSettingsUpdate failed")
	}
	if true != response["Result"] {
		t.Errorf("Result = %v, want true", response["Result"])
	}

	document := main.docs[testEmail]
	if nil == document {
		t.Fatal("the settings document was not saved")
	}
	// PHP stores $iValue (raw), the clamp callback result is discarded.
	if value, ok := document.GetConf("MessagesPerPage", nil).(int); !ok || 5 != value {
		t.Errorf("MessagesPerPage = %v, want the raw 5", document.GetConf("MessagesPerPage", nil))
	}
	if value, ok := document.GetConf("Layout", nil).(int); !ok || 9 != value {
		t.Errorf("Layout = %v, want the raw 9", document.GetConf("Layout", nil))
	}
}

// TestSettingsUpdateTypeCoercion checks the string/int/bool coercions and that
// absent parameters are left untouched.
func TestSettingsUpdateTypeCoercion(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	main := newUserSettings()
	local := newUserSettings()

	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main,
		LocalSettings: local,
	})

	response := mustResponse(doSettingsUpdate(userContext(dispatcher, session, map[string]any{
		"hourCycle":          "H23",
		"ViewHTML":           "false",
		"RemoveColors":       1,
		"AllowStyles":        "",
		"NotificationSound":  "new-mail",
		"CheckMailInterval":  "15",
		"HideUnsubscribed":   true,
		"ShowUnreadCount":    "1",
		// A key that is not part of the parameter set must not be written.
		"NotARealSetting": "x",
	})))
	if nil == response {
		t.Fatal("doSettingsUpdate failed")
	}

	mainDoc := main.docs[testEmail]
	if "H23" != phpString(mainDoc.GetConf("hourCycle", nil)) {
		t.Errorf("hourCycle = %v", mainDoc.GetConf("hourCycle", nil))
	}
	if false != mainDoc.GetConf("ViewHTML", nil) {
		t.Errorf("ViewHTML = %v, want false (\"false\" literal)", mainDoc.GetConf("ViewHTML", nil))
	}
	if true != mainDoc.GetConf("RemoveColors", nil) {
		t.Errorf("RemoveColors = %v, want true", mainDoc.GetConf("RemoveColors", nil))
	}
	if false != mainDoc.GetConf("AllowStyles", nil) {
		t.Errorf("AllowStyles = %v, want false (empty)", mainDoc.GetConf("AllowStyles", nil))
	}
	if "new-mail" != phpString(mainDoc.GetConf("NotificationSound", nil)) {
		t.Errorf("NotificationSound = %v", mainDoc.GetConf("NotificationSound", nil))
	}
	if mainDoc.Has("NotARealSetting") {
		t.Error("NotARealSetting must not be written")
	}

	localDoc := local.docs[testEmail]
	if 15 != phpIntValue(localDoc.GetConf("CheckMailInterval", nil)) {
		t.Errorf("CheckMailInterval = %v", localDoc.GetConf("CheckMailInterval", nil))
	}
	if true != localDoc.GetConf("HideUnsubscribed", nil) {
		t.Errorf("HideUnsubscribed = %v", localDoc.GetConf("HideUnsubscribed", nil))
	}
	if true != localDoc.GetConf("ShowUnreadCount", nil) {
		t.Errorf("ShowUnreadCount = %v", localDoc.GetConf("ShowUnreadCount", nil))
	}
}

// TestSettingsUpdateLanguageGate checks the allow_languages_on_settings gate and
// the ValidateLanguage callback.
func TestSettingsUpdateLanguageGate(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}

	// Enabled: the validated language is stored.
	main := newUserSettings()
	local := newUserSettings()
	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main,
		LocalSettings: local,
		Config:        userConfig{},
		Language:      func(language string) string { if "xx" == language { return "en" }; return language },
	})
	doSettingsUpdate(userContext(dispatcher, session, map[string]any{"language": "xx"}))
	if "en" != phpString(main.docs[testEmail].GetConf("language", nil)) {
		t.Errorf("language = %v, want the validated en", main.docs[testEmail].GetConf("language", nil))
	}

	// Disabled: the language is not written.
	main2 := newUserSettings()
	dispatcher2 := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main2,
		LocalSettings: newUserSettings(),
		Config:        userConfig{bools: map[string]bool{"webmail.allow_languages_on_settings": false}},
	})
	doSettingsUpdate(userContext(dispatcher2, session, map[string]any{"language": "xx"}))
	if doc := main2.docs[testEmail]; nil != doc && doc.Has("language") {
		t.Errorf("language must not be written when disabled: %v", doc.GetConf("language", nil))
	}
}

// TestSettingsUpdateThemesGate checks the Themes capability gate: Theme and the
// font settings are only written when webmail.allow_themes is on.
func TestSettingsUpdateThemesGate(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}

	// Off (default): nothing is written.
	main := newUserSettings()
	local := newUserSettings()
	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main,
		LocalSettings: local,
		Config:        userConfig{},
		Theme:         func(string) string { return "Gazellemail" },
	})
	doSettingsUpdate(userContext(dispatcher, session, map[string]any{
		"Theme": "bogus", "fontSansSerif": "sans",
	}))
	if doc := local.docs[testEmail]; nil != doc && doc.Has("Theme") {
		t.Errorf("Theme must not be written when Themes is off")
	}
	if doc := main.docs[testEmail]; nil != doc && doc.Has("fontSansSerif") {
		t.Errorf("fontSansSerif must not be written when Themes is off")
	}

	// On: Theme is validated and the font settings are stored.
	main2 := newUserSettings()
	local2 := newUserSettings()
	dispatcher2 := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Settings:      main2,
		LocalSettings: local2,
		Config:        userConfig{bools: map[string]bool{"webmail.allow_themes": true}},
		Theme:         func(string) string { return "Gazellemail" },
	})
	doSettingsUpdate(userContext(dispatcher2, session, map[string]any{
		"Theme": "bogus", "fontSansSerif": "sans", "fontSerif": "serif", "fontMono": "mono",
	}))
	if "Gazellemail" != phpString(local2.docs[testEmail].GetConf("Theme", nil)) {
		t.Errorf("Theme = %v, want the validated Gazellemail", local2.docs[testEmail].GetConf("Theme", nil))
	}
	for _, key := range []string{"fontSansSerif", "fontSerif", "fontMono"} {
		if "" == phpString(main2.docs[testEmail].GetConf(key, nil)) {
			t.Errorf("%s not written when Themes is on", key)
		}
	}
}

// TestQuotaFallback checks the [0,0,0,0] fallback when there is no quota.
func TestQuotaFallback(t *testing.T) {
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Mail: func(http.ResponseWriter, *http.Request) Mail {
			return &fakeUserMail{}
		},
	})
	ctx := &Context{dispatcher: dispatcher, params: map[string]any{}}

	response := mustResponse(doQuota(ctx))
	if nil == response {
		t.Fatal("doQuota failed")
	}
	quota, ok := response["Result"].([]int)
	if !ok {
		t.Fatalf("Result = %v, want []int", response["Result"])
	}
	if 4 != len(quota) || 0 != quota[0] || 0 != quota[1] || 0 != quota[2] || 0 != quota[3] {
		t.Errorf("quota = %v, want [0,0,0,0]", quota)
	}
}

// TestSuggestionsPassesQueryAndLimit checks that DoSuggestions trims the query,
// reads contacts.suggestions_limit and returns the provider output.
func TestSuggestionsPassesQueryAndLimit(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}

	provider := &fakeUserSuggestions{result: []any{[]any{"a@example.com", "A"}}}
	dispatcher := NewDispatcher(Options{
		Version:       "0.0.0",
		Session:       func(http.ResponseWriter, *http.Request) Session { return session },
		Config:        userConfig{ints: map[string]int{"contacts.suggestions_limit": 7}},
		Suggestions:   provider,
	})

	response := mustResponse(doSuggestions(userContext(dispatcher, session, map[string]any{
		"Query": "  bob  ",
	})))
	if nil == response {
		t.Fatal("doSuggestions failed")
	}
	if "bob" != provider.query {
		t.Errorf("query = %q, want the trimmed bob", provider.query)
	}
	if 7 != provider.limit {
		t.Errorf("limit = %d, want 7", provider.limit)
	}
	result, _ := response["Result"].([]any)
	if 1 != len(result) {
		t.Errorf("Result = %v", response["Result"])
	}
}

// TestSuggestionsWithoutProvider checks the empty list of an unwired provider.
func TestSuggestionsWithoutProvider(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
	})

	response := mustResponse(doSuggestions(userContext(dispatcher, session, map[string]any{
		"Query": "bob",
	})))
	if nil == response {
		t.Fatal("doSuggestions failed")
	}
	result, ok := response["Result"].([]any)
	if !ok || 0 != len(result) {
		t.Errorf("Result = %v, want an empty list", response["Result"])
	}
}

// TestClearUserBackground checks the UserBackground gate, the storage clear and
// the blanked settings keys.
func TestClearUserBackground(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}

	// Gate off (default): FalseResponse.
	main := newUserSettings()
	document := storage.NewSettings()
	document.SetConf("UserBackgroundName", "bg.png")
	main.docs[testEmail] = document
	provider := newFakeStorageProvider()

	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Settings: main,
		Storage:  provider,
		Config:   userConfig{},
	})
	response := mustResponse(doClearUserBackground(userContext(dispatcher, session, map[string]any{})))
	if false != response["Result"] {
		t.Errorf("Result = %v, want false when the capability is off", response["Result"])
	}
	if 0 != len(provider.cleared) {
		t.Errorf("storage must not be cleared when the capability is off")
	}

	// Gate on: the background is cleared and the keys are blanked.
	mainOn := newUserSettings()
	documentOn := storage.NewSettings()
	documentOn.SetConf("UserBackgroundName", "bg.png")
	documentOn.SetConf("UserBackgroundHash", "hash")
	mainOn.docs[testEmail] = documentOn
	providerOn := newFakeStorageProvider()

	dispatcherOn := NewDispatcher(Options{
		Version:  "0.0.0",
		Session:  func(http.ResponseWriter, *http.Request) Session { return session },
		Settings: mainOn,
		Storage:  providerOn,
		Config:   userConfig{bools: map[string]bool{"webmail.allow_user_background": true}},
	})
	responseOn := mustResponse(doClearUserBackground(userContext(dispatcherOn, session, map[string]any{})))
	if true != responseOn["Result"] {
		t.Errorf("Result = %v, want true", responseOn["Result"])
	}
	if 1 != len(providerOn.cleared) || "background" != providerOn.cleared[0] {
		t.Errorf("cleared = %v, want [background]", providerOn.cleared)
	}
	if "" != phpString(mainOn.docs[testEmail].GetConf("UserBackgroundName", "x")) {
		t.Errorf("UserBackgroundName not blanked")
	}
	if "" != phpString(mainOn.docs[testEmail].GetConf("UserBackgroundHash", "x")) {
		t.Errorf("UserBackgroundHash not blanked")
	}
}

// TestAppDelayStartTimers checks the 23h/25h NOBODY "Cache/Timers" bookkeeping
// and that the first call runs the garbage collectors.
func TestAppDelayStartTimers(t *testing.T) {
	resolved := folderAccount()
	session := &folderSession{account: resolved}
	provider := newFakeStorageProvider()
	cacher := &fakeCacher{}

	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Storage: provider,
		Cacher:  func(*account.Account) Cacher { return cacher },
	})
	ctx := userContext(dispatcher, session, map[string]any{})

	if _, err := doAppDelayStart(ctx); nil != err {
		t.Fatalf("doAppDelayStart: %v", err)
	}
	if "" == provider.nobody["Cache/Timers"] {
		t.Fatal("the Cache/Timers entry was not written")
	}
	if 1 != cacher.gcCalls {
		t.Errorf("cache GC calls = %d, want 1", cacher.gcCalls)
	}
	if 1 != provider.gcCalls {
		t.Errorf("storage GC calls = %d, want 1", provider.gcCalls)
	}

	// A second call within the 23h/25h windows must not run the collectors again.
	ctx2 := userContext(dispatcher, session, map[string]any{})
	if _, err := doAppDelayStart(ctx2); nil != err {
		t.Fatalf("doAppDelayStart (second): %v", err)
	}
	if 1 != cacher.gcCalls {
		t.Errorf("cache GC calls after the second call = %d, want 1", cacher.gcCalls)
	}
	if 1 != provider.gcCalls {
		t.Errorf("storage GC calls after the second call = %d, want 1", provider.gcCalls)
	}
}
