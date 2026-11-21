package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/config"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// The install tree values of the tests.
const (
	testVersion = "0.0.0"
	testSalt    = "testsalt"
	testEmail   = "user@example.com"
)

// testTree is a temporary APP_VERSION_ROOT_PATH with the assets AppData reads.
type testTree struct {
	root      string
	indexRoot string
}

// newTestTree builds the localization, theme, sound and script files the
// document reports.
func newTestTree(t *testing.T) testTree {
	t.Helper()

	root := t.TempDir()
	files := []string{
		"app/localization/en/user.json",
		"app/localization/en/admin.json",
		"app/localization/en/static.json",
		"app/localization/da/user.json",
		"app/localization/da/admin.json",
		"themes/Gazellemail/styles.css",
		"themes/Other/styles.css",
		"static/sounds/new-mail.mp3",
		"static/sounds/second.mp3",
		"static/js/min/libs.min.js",
		"static/js/libs.js",
	}

	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o600); nil != err {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	return testTree{root: root, indexRoot: t.TempDir()}
}

// paths builds the install tree URL helpers of the test tree.
func (t testTree) paths() httpx.WebPaths {
	return httpx.WebPaths{Version: testVersion, VersionRoot: t.root}
}

// newConfig builds the real application.ini defaults, like an untouched
// installation.
func newConfig(t *testing.T) *config.Application {
	t.Helper()

	return config.NewApplication(filepath.Join(t.TempDir(), "application.ini"), testVersion, config.Defaults{})
}

// fakeSettings is Providers\Settings: the documents per account.
type fakeSettings struct {
	documents map[string]*storage.Settings
}

// newFakeSettings builds an empty provider.
func newFakeSettings() *fakeSettings {
	return &fakeSettings{documents: map[string]*storage.Settings{}}
}

// Load is Providers\Settings::Load().
func (f *fakeSettings) Load(resolved storage.Account) *storage.Settings {
	if document, ok := f.documents[resolved.Email()]; ok {
		return document
	}
	return storage.NewSettings()
}

// set stores a document built from the given values.
func (f *fakeSettings) set(email string, values map[string]any) *storage.Settings {
	document := storage.NewSettings()
	for key, value := range values {
		document.SetConf(key, value)
	}
	f.documents[email] = document
	return document
}

// fakeDomains is Providers\Domain::GetList().
type fakeDomains struct{ entries []domain.ListEntry }

// GetList is Providers\Domain::GetList().
func (f fakeDomains) GetList(bool) []domain.ListEntry { return f.entries }

// fakeAddressBook is Providers\AddressBook (step 22).
type fakeAddressBook struct {
	active bool
	sync   map[string]any
}

// IsActive is Providers\AddressBook::IsActive().
func (f *fakeAddressBook) IsActive(*account.Account) bool { return f.active }

// SyncData is Actions\Contacts::getContactsSyncData().
func (f *fakeAddressBook) SyncData(*account.Account) map[string]any { return f.sync }

// fakePlugins is Plugins\Manager (step 28).
type fakePlugins struct {
	js         bool
	hash       string
	hooks      []string
	initCalled bool
	panics     bool
}

// HaveJS is Plugins\Manager::HaveJs().
func (f *fakePlugins) HaveJS(bool) bool { return f.js }

// Hash is Plugins\Manager::Hash().
func (f *fakePlugins) Hash() string { return f.hash }

// RunHook is Plugins\Manager::RunHook().
func (f *fakePlugins) RunHook(name string, args ...any) {
	f.hooks = append(f.hooks, name)

	// A test plugin rewriting the language is the simplest way to exercise the
	// filter.language contract.
	if "filter.language" == name && 0 < len(args) {
		if language, ok := args[0].(*string); ok {
			*language = "da"
		}
	}
}

// InitAppData is Plugins\Manager::InitAppData().
func (f *fakePlugins) InitAppData(admin bool, appData map[string]any, resolved *account.Account) {
	if f.panics {
		panic(errors.New("plugin exploded"))
	}
	f.initCalled = true
	if admin {
		appData["pluginAdmin"] = true
	} else {
		appData["pluginUser"] = resolved != nil
	}
}

// fakeAdmin is Actions\Admin::IsAdminLoggined().
type fakeAdmin struct{ loggedIn bool }

// IsLoggedIn is Actions\Admin::IsAdminLoggined().
func (f fakeAdmin) IsLoggedIn(bool) (bool, error) { return f.loggedIn, nil }

// sessionCookieExpiry is the zero expiry of a session cookie, the last argument
// of cryptox.Jar.Set().
var sessionCookieExpiry = time.Time{}

// appDataFixture is one AppData installation under test.
type appDataFixture struct {
	t       *testing.T
	tree    testTree
	config  *config.Application
	options Options
	appData *AppData
}

// newFixture builds the AppData service of the test installation.
func newFixture(t *testing.T) *appDataFixture {
	t.Helper()

	tree := newTestTree(t)
	application := newConfig(t)

	fixture := &appDataFixture{t: t, tree: tree, config: application}
	fixture.options = Options{
		Config:               application,
		Version:              testVersion,
		Salt:                 testSalt,
		Paths:                tree.paths(),
		IndexRoot:            tree.indexRoot,
		PrivateData:          filepath.Join(tree.root, "private"),
		Settings:             newFakeSettings(),
		LocalSettings:        newFakeSettings(),
		Domains:              fakeDomains{entries: []domain.ListEntry{{Name: "*"}, {Name: "example.com"}}},
		AttachmentThumbnails: true,
	}
	fixture.appData = NewAppData(fixture.options)

	return fixture
}

// mainAccount builds the account of testEmail with the default domain config.
func mainAccount() *account.Account {
	resolved := account.NewAccount()
	resolved.SetCredentials(domain.New("example.com", domain.Defaults{}),
		testEmail, testEmail, "secret", "", "")
	return resolved
}

// document builds the JSON document the endpoint would send.
func (f *appDataFixture) document(admin bool, session *Session) map[string]any {
	f.t.Helper()

	encoded, err := encodeJSON(f.appData.Build(admin, session))
	if nil != err {
		f.t.Fatalf("encode AppData: %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(encoded, &document); nil != err {
		f.t.Fatalf("AppData is not JSON (%v): %s", err, encoded)
	}
	return document
}

// value returns a top level value of the document.
func value(t *testing.T, document map[string]any, key string) any {
	t.Helper()

	item, ok := document[key]
	if !ok {
		t.Fatalf("%q is missing from the document", key)
	}
	return item
}

// systemValue returns a "System" value of the document.
func systemValue(t *testing.T, document map[string]any, key string) any {
	t.Helper()

	system, ok := document["System"].(map[string]any)
	if !ok {
		t.Fatal("System is missing from the document")
	}
	item, ok := system[key]
	if !ok {
		t.Fatalf("System.%q is missing from the document", key)
	}
	return item
}

// capaValue returns a "Capa" value of the document.
func capaValue(t *testing.T, document map[string]any, key string) any {
	t.Helper()

	capa, ok := document["Capa"].(map[string]any)
	if !ok {
		t.Fatal("Capa is missing from the document")
	}
	item, ok := capa[key]
	if !ok {
		t.Fatalf("Capa.%q is missing from the document", key)
	}
	return item
}

// has reports whether the document holds key.
func has(document map[string]any, key string) bool {
	_, ok := document[key]
	return ok
}

func TestGuestAppData(t *testing.T) {
	fixture := newFixture(t)
	document := fixture.document(false, nil)

	if false != value(t, document, "Auth") {
		t.Error("Auth must be false without an account")
	}
	if "SnappyMail Webmail" != value(t, document, "title") {
		t.Errorf("title = %v", value(t, document, "title"))
	}
	if "SnappyMail" != value(t, document, "loadingDescription") {
		t.Errorf("loadingDescription = %v", value(t, document, "loadingDescription"))
	}
	// PHP: 'Plugins' => array() is an empty JSON *array*.
	plugins, ok := document["Plugins"].([]any)
	if !ok || 0 != len(plugins) {
		t.Errorf("Plugins = %#v, want an empty array", document["Plugins"])
	}
	if true != value(t, document, "allowLanguagesOnLogin") {
		t.Errorf("allowLanguagesOnLogin = %v", value(t, document, "allowLanguagesOnLogin"))
	}

	// The guest login form values.
	if "" != value(t, document, "DevEmail") || "" != value(t, document, "DevPassword") {
		t.Error("DevEmail/DevPassword must be empty outside a development version")
	}
	if 0.0 != value(t, document, "signMe") {
		t.Errorf("signMe = %v, want 0 (SignMeType::DefaultOff)", value(t, document, "signMe"))
	}

	// System: version, token, languages, paths.
	if testVersion != systemValue(t, document, "version") {
		t.Errorf("System.version = %v", systemValue(t, document, "version"))
	}
	if "" != systemValue(t, document, "token") {
		t.Errorf("System.token = %v, want empty without a session", systemValue(t, document, "token"))
	}
	if got := strings.Join(toStrings(t, systemValue(t, document, "languages")), ","); "da,en" != got {
		t.Errorf("System.languages = %q, want da,en", got)
	}
	if "/" != systemValue(t, document, "webPath") {
		t.Errorf("System.webPath = %v", systemValue(t, document, "webPath"))
	}
	if "/snappymail/v/0.0.0/" != systemValue(t, document, "webVersionPath") {
		t.Errorf("System.webVersionPath = %v", systemValue(t, document, "webVersionPath"))
	}
	// A guest has no themes (they are only reported for the app shell).
	if has(systemValueMap(t, document), "themes") {
		t.Error("System.themes must not be reported for a guest")
	}

	// No capabilities, no plugin link.
	if has(document, "Capa") || has(document, "attachmentLimit") || has(document, "phpUploadSizes") {
		t.Error("the authenticated keys must not be reported for a guest")
	}
	if "" != value(t, document, "PluginsLink") {
		t.Errorf("PluginsLink = %v, want empty without plugins", value(t, document, "PluginsLink"))
	}

	if "Gazellemail" != value(t, document, "Theme") {
		t.Errorf("Theme = %v", value(t, document, "Theme"))
	}
	if "en" != value(t, document, "language") {
		t.Errorf("language = %v", value(t, document, "language"))
	}
	if "en" != value(t, document, "clientLanguage") {
		t.Errorf("clientLanguage = %v, want the Accept-Language default en",
			value(t, document, "clientLanguage"))
	}

	// StaticLibsJs is the minified vendor bundle with the cache buster.
	libs, _ := value(t, document, "StaticLibsJs").(string)
	if !strings.HasPrefix(libs, "/snappymail/v/0.0.0/static/js/min/libs.min.js?v=") {
		t.Errorf("StaticLibsJs = %q", libs)
	}
}

func TestGuestAppDataDevAndSignMe(t *testing.T) {
	fixture := newFixture(t)
	fixture.options.Dev = true
	fixture.config.Set("labs", "dev_email", "dev@example.com")
	fixture.config.Set("labs", "dev_password", "devpass")
	fixture.config.Set("login", "sign_me_auto", "Unused")
	fixture.appData = NewAppData(fixture.options)

	document := fixture.document(false, nil)

	if "dev@example.com" != value(t, document, "DevEmail") {
		t.Errorf("DevEmail = %v", value(t, document, "DevEmail"))
	}
	if "devpass" != value(t, document, "DevPassword") {
		t.Errorf("DevPassword = %v", value(t, document, "DevPassword"))
	}
	if 2.0 != value(t, document, "signMe") {
		t.Errorf("signMe = %v, want 2 (SignMeType::Unused)", value(t, document, "signMe"))
	}

	// A value outside the SignMeType table is an undefined array key in PHP.
	fixture.config.Set("login", "sign_me_auto", "Bogus")
	document = fixture.document(false, nil)
	if nil != value(t, document, "signMe") {
		t.Errorf("signMe = %v, want null for an unknown value", value(t, document, "signMe"))
	}
}

func TestUserAppDataDefaults(t *testing.T) {
	fixture := newFixture(t)
	session := &Session{Request: httptest.NewRequest(http.MethodGet, "/", nil), Account: mainAccount()}

	document := fixture.document(false, session)

	if true != value(t, document, "Auth") {
		t.Fatal("Auth must be true with an account")
	}
	if testEmail != value(t, document, "Email") {
		t.Errorf("Email = %v", value(t, document, "Email"))
	}
	if testEmail != value(t, document, "mainEmail") {
		t.Errorf("mainEmail = %v", value(t, document, "mainEmail"))
	}
	wantHash := storage.SHA1(testEmail + testSalt + testEmail)
	if wantHash != value(t, document, "accountHash") {
		t.Errorf("accountHash = %v, want %s", value(t, document, "accountHash"), wantHash)
	}
	if got := strings.Join(toStrings(t, value(t, document, "localDomains")), ","); "*,example.com" != got {
		t.Errorf("localDomains = %q", got)
	}
	if false != value(t, document, "contactsAllowed") {
		t.Errorf("contactsAllowed = %v, want false without an address book",
			value(t, document, "contactsAllowed"))
	}

	// getAccountData() defaults.
	tests := []struct {
		key  string
		want any
	}{
		{"accountSignMe", false},
		{"HideUnsubscribed", false},
		{"defaultSort", ""},
		{"useThreads", false},
		{"threadAlgorithm", ""},
		{"ReplySameFolder", false},
		{"HideDeleted", true},
		{"ShowUnreadCount", false},
		{"UnhideKolabFolders", false},
		{"CheckMailInterval", 15.0},
		{"MessagesPerPage", 20.0},
		{"Layout", 1.0},
		{"ViewImages", "ask"},
		{"messageReadAuto", 1.0},
		{"MessageReadDelay", 5.0},
		{"MsgDefaultAction", 1.0},
		{"editorWysiwyg", "Squire"},
		{"EditorDefaultType", "Html"},
		{"AllowStyles", false},
		{"ListInlineAttachments", false},
		{"keyPassForget", 15.0},
		{"SoundNotification", true},
		{"NotificationSound", "new-mail"},
		{"DesktopNotifications", true},
		{"UseCheckboxesInList", true},
		{"AutoLogout", 30.0},
	}

	for _, test := range tests {
		if got := value(t, document, test.key); got != test.want {
			t.Errorf("%s = %#v, want %#v", test.key, got, test.want)
		}
	}

	// The user background is only reported with the capability.
	if has(document, "userBackgroundName") || has(document, "userBackgroundHash") {
		t.Error("userBackground* must be absent when webmail.allow_user_background is off")
	}

	// System additions.
	if false != systemValue(t, document, "allowAppendMessage") {
		t.Errorf("System.allowAppendMessage = %v", systemValue(t, document, "allowAppendMessage"))
	}
	if 50.0 != systemValue(t, document, "folderSpecLimit") {
		t.Errorf("System.folderSpecLimit = %v", systemValue(t, document, "folderSpecLimit"))
	}
	if false != systemValue(t, document, "listPermanentFiltered") {
		t.Errorf("System.listPermanentFiltered = %v", systemValue(t, document, "listPermanentFiltered"))
	}
	if "" != systemValue(t, document, "customLogoutLink") {
		t.Errorf("System.customLogoutLink = %v", systemValue(t, document, "customLogoutLink"))
	}
	if got := strings.Join(toStrings(t, systemValue(t, document, "attachmentsActions")), ","); "zip" != got {
		t.Errorf("System.attachmentsActions = %q, want zip", got)
	}
	if got := strings.Join(toStrings(t, systemValue(t, document, "themes")), ","); "Gazellemail,Other" != got {
		t.Errorf("System.themes = %q, want the shipped themes", got)
	}

	// newMailSounds lists the basenames of static/sounds/*.mp3.
	if got := strings.Join(toStrings(t, value(t, document, "newMailSounds")), ","); "new-mail,second" != got {
		t.Errorf("newMailSounds = %q", got)
	}

	// Capabilities.
	if true != capaValue(t, document, "AdditionalAccounts") {
		t.Error("Capa.AdditionalAccounts must follow webmail.allow_additional_accounts")
	}
	if true != capaValue(t, document, "AttachmentThumbnails") {
		t.Error("Capa.AttachmentThumbnails must follow the image support")
	}
	if true != capaValue(t, document, "AttachmentsActions") {
		t.Error("Capa.AttachmentsActions must follow capa.attachments_actions")
	}
	if false != capaValue(t, document, "Contacts") {
		t.Error("Capa.Contacts must follow contacts.enable")
	}
	if true != capaValue(t, document, "DangerousActions") {
		t.Error("Capa.DangerousActions must follow capa.dangerous_actions")
	}
	if false != capaValue(t, document, "GnuPG") {
		t.Error("Capa.GnuPG must be false without a gnupg binary")
	}
	if true != capaValue(t, document, "Identities") {
		t.Error("Capa.Identities must follow webmail.allow_additional_identities")
	}
	if true != capaValue(t, document, "OpenPGP") {
		t.Error("Capa.OpenPGP must follow security.openpgp")
	}
	if false != capaValue(t, document, "Sieve") {
		t.Error("Capa.Sieve must follow the domain Sieve settings")
	}
	if true != capaValue(t, document, "Themes") {
		t.Error("Capa.Themes must follow webmail.allow_themes")
	}
	if false != capaValue(t, document, "UserBackground") {
		t.Error("Capa.UserBackground must follow webmail.allow_user_background")
	}
	if false != capaValue(t, document, "Kolab") {
		t.Error("Capa.Kolab is always false (no Kolab plugin)")
	}

	// attachmentLimit is min(upload_max_filesize, attachment_size_limit).
	if 2*1024*1024.0 != value(t, document, "attachmentLimit") {
		t.Errorf("attachmentLimit = %v, want 2 MB", value(t, document, "attachmentLimit"))
	}
	uploads, ok := value(t, document, "phpUploadSizes").(map[string]any)
	if !ok {
		t.Fatalf("phpUploadSizes = %#v", value(t, document, "phpUploadSizes"))
	}
	if "2M" != uploads["upload_max_filesize"] || "8M" != uploads["post_max_size"] {
		t.Errorf("phpUploadSizes = %#v", uploads)
	}

	// The shared webmail keys.
	if true != value(t, document, "proxyExternalImages") {
		t.Errorf("proxyExternalImages = %v", value(t, document, "proxyExternalImages"))
	}
	if false != value(t, document, "autoVerifySignatures") {
		t.Errorf("autoVerifySignatures = %v", value(t, document, "autoVerifySignatures"))
	}
	if true != value(t, document, "allowLanguagesOnSettings") {
		t.Errorf("allowLanguagesOnSettings = %v", value(t, document, "allowLanguagesOnSettings"))
	}
	if 5.0 != value(t, document, "minRefreshInterval") {
		t.Errorf("minRefreshInterval = %v", value(t, document, "minRefreshInterval"))
	}

	// contacts.enable and contacts.allow_sync are both off by default.
	if has(document, "ContactsSync") {
		t.Error("ContactsSync must be absent while contacts sync is disabled")
	}
	if has(document, "mailToEmail") {
		t.Error("mailToEmail must be absent without a mailto cookie")
	}
}

func TestUserAppDataSettingsOverlay(t *testing.T) {
	fixture := newFixture(t)

	fixture.options.Settings.(*fakeSettings).set(testEmail, map[string]any{
		"MessagesPerPage":   4, // clamped to >= 10
		"ShowImages":        true,
		"ViewImages":        "match",
		"EditorDefaultType": "HtmlForced",
		"Layout":            2,
		"fontSansSerif":     "Arial",
		"MessageReadDelay":  9,
		"hourCycle":         "h23",
	})
	fixture.options.LocalSettings.(*fakeSettings).set(testEmail, map[string]any{
		"Theme":              "Other",
		"SentFolder":         "Sent",
		"DraftsFolder":       "Drafts",
		"JunkFolder":         "Junk",
		"TrashFolder":        "Trash",
		"ArchiveFolder":      "Archive",
		"HideUnsubscribed":   true,
		"defaultSort":        "date",
		"UseThreads":         true,
		"threadAlgorithm":    "REFERENCES",
		"ReplySameFolder":    true,
		"HideDeleted":        false,
		"ShowUnreadCount":    true,
		"UnhideKolabFolders": true,
		"CheckMailInterval":  3, // clamped to the minimum refresh interval
	})

	session := &Session{Request: httptest.NewRequest(http.MethodGet, "/", nil), Account: mainAccount()}
	document := fixture.document(false, session)

	tests := []struct {
		key  string
		want any
	}{
		{"MessagesPerPage", 10.0},
		{"ViewImages", "match"},
		{"EditorDefaultType", "Html"},
		{"Layout", 2.0},
		{"fontSansSerif", "Arial"},
		{"MessageReadDelay", 9.0},
		{"hourCycle", "h23"},
		{"Theme", "Other"},
		{"SentFolder", "Sent"},
		{"DraftsFolder", "Drafts"},
		{"JunkFolder", "Junk"},
		{"TrashFolder", "Trash"},
		{"ArchiveFolder", "Archive"},
		{"HideUnsubscribed", true},
		{"defaultSort", "date"},
		{"useThreads", true},
		{"threadAlgorithm", "REFERENCES"},
		{"ReplySameFolder", true},
		{"HideDeleted", false},
		{"ShowUnreadCount", true},
		{"UnhideKolabFolders", true},
		{"CheckMailInterval", 5.0},
	}

	for _, test := range tests {
		if got := value(t, document, test.key); got != test.want {
			t.Errorf("%s = %#v, want %#v", test.key, got, test.want)
		}
	}

	// ShowImages = true is only the fallback of ViewImages.
	fixture.options.Settings.(*fakeSettings).set(testEmail, map[string]any{"ShowImages": true})
	document = fixture.document(false, session)
	if "always" != value(t, document, "ViewImages") {
		t.Errorf("ViewImages = %v, want always from ShowImages", value(t, document, "ViewImages"))
	}

	// A stored MessagesPerPage of 0 falls back to the configuration default.
	fixture.options.Settings.(*fakeSettings).set(testEmail, map[string]any{"MessagesPerPage": 0})
	document = fixture.document(false, session)
	if 20.0 != value(t, document, "MessagesPerPage") {
		t.Errorf("MessagesPerPage = %v, want the configured 20", value(t, document, "MessagesPerPage"))
	}
}

func TestUserAppDataContactsSync(t *testing.T) {
	fixture := newFixture(t)
	fixture.config.Set("contacts", "enable", true)
	fixture.config.Set("contacts", "allow_sync", true)
	fixture.config.Set("contacts", "sync_interval", 1000)
	fixture.options.AddressBook = &fakeAddressBook{
		active: true,
		sync: map[string]any{
			"Mode":         2,
			"Url":          "https://dav.example.com",
			"User":         "dav",
			"Password":     "sealed",
			"PasswordHMAC": "mac",
		},
	}
	fixture.appData = NewAppData(fixture.options)

	session := &Session{Request: httptest.NewRequest(http.MethodGet, "/", nil), Account: mainAccount()}
	document := fixture.document(false, session)

	if true != value(t, document, "contactsAllowed") {
		t.Fatalf("contactsAllowed = %v", value(t, document, "contactsAllowed"))
	}

	sync, ok := value(t, document, "ContactsSync").(map[string]any)
	if !ok {
		t.Fatalf("ContactsSync = %#v", value(t, document, "ContactsSync"))
	}
	if 2.0 != sync["Mode"] || "https://dav.example.com" != sync["Url"] || "dav" != sync["User"] {
		t.Errorf("ContactsSync = %#v", sync)
	}
	// The stored password is replaced by the dummy and the HMAC is dropped.
	if dummy != sync["Password"] {
		t.Errorf("ContactsSync.Password = %v, want the dummy", sync["Password"])
	}
	if _, ok := sync["PasswordHMAC"]; ok {
		t.Error("ContactsSync.PasswordHMAC must not be sent to the client")
	}
	// sync_interval is clamped to 20..320.
	if 320.0 != sync["Interval"] {
		t.Errorf("ContactsSync.Interval = %v, want 320", sync["Interval"])
	}

	// Without stored sync data PHP sends the defaults.
	fixture.options.AddressBook.(*fakeAddressBook).sync = nil
	fixture.config.Set("contacts", "sync_interval", 20)
	document = fixture.document(false, session)
	sync, _ = value(t, document, "ContactsSync").(map[string]any)
	if 0.0 != sync["Mode"] || "" != sync["Url"] || "" != sync["User"] || "" != sync["Password"] {
		t.Errorf("ContactsSync defaults = %#v", sync)
	}
	if 20.0 != sync["Interval"] {
		t.Errorf("ContactsSync.Interval = %v, want 20", sync["Interval"])
	}
}

func TestUserAppDataMailTo(t *testing.T) {
	fixture := newFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)

	jar := cryptox.NewJar(request, cryptox.CookieOptions{})
	context := &account.Context{
		Salt:  testSalt,
		Crypt: cryptox.New(testSalt, testVersion, "aes-256-cbc"),
	}
	authenticator := auth.New(context, jar, recorder, nil)

	token, err := authenticator.EncodeKeyValuesQ(map[string]any{
		"Time":   1.0,
		"MailTo": "MailTo",
		"To":     "User@xn--bcher-kva.example",
	}, "")
	if nil != err {
		t.Fatalf("EncodeKeyValuesQ: %v", err)
	}
	jar.Set(recorder, auth.AuthMailtoTokenKey, token, sessionCookieExpiry, true)

	session := &Session{
		Request:       request,
		Response:      recorder,
		Authenticator: authenticator,
		Jar:           jar,
		Account:       mainAccount(),
	}

	document := fixture.document(false, session)

	// SnappyMail\IDN::emailToUtf8() of the domain part.
	if "User@bücher.example" != value(t, document, "mailToEmail") {
		t.Errorf("mailToEmail = %v", value(t, document, "mailToEmail"))
	}
	// The cookie is single-use: it is cleared in the same response (PHP drops
	// it from $_COOKIE as well).
	if !strings.Contains(strings.Join(recorder.Header().Values("Set-Cookie"), "\n"), auth.AuthMailtoTokenKey+"=;") {
		t.Error("AppData must clear the mailto cookie")
	}
	if jar.Has(auth.AuthMailtoTokenKey) {
		t.Error("the cleared mailto cookie must be gone from the jar")
	}

	// A second call has no mailto address.
	document = fixture.document(false, session)
	if has(document, "mailToEmail") {
		t.Error("mailToEmail must be single-use")
	}
}

func TestUserAppDataSignMeAndLanguages(t *testing.T) {
	fixture := newFixture(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept-Language", "da-DK,da;q=0.9,en;q=0.5")

	recorder := httptest.NewRecorder()
	jar := cryptox.NewJar(request, cryptox.CookieOptions{})
	jar.Set(recorder, auth.AuthSignMeTokenKey, "signme", sessionCookieExpiry, true)

	session := &Session{Request: request, Response: recorder, Jar: jar, Account: mainAccount()}
	document := fixture.document(false, session)

	if true != value(t, document, "accountSignMe") {
		t.Errorf("accountSignMe = %v, want true with a smremember cookie",
			value(t, document, "accountSignMe"))
	}
	// determine_user_language picks the first available Accept-Language entry.
	if "da" != value(t, document, "language") {
		t.Errorf("language = %v, want da", value(t, document, "language"))
	}
	if "da" != value(t, document, "clientLanguage") {
		t.Errorf("clientLanguage = %v, want da", value(t, document, "clientLanguage"))
	}

	// The stored setting wins over the detected language.
	fixture.options.Settings.(*fakeSettings).set(testEmail, map[string]any{"language": "en"})
	document = fixture.document(false, session)
	if "en" != value(t, document, "language") {
		t.Errorf("language = %v, want the stored en", value(t, document, "language"))
	}
	if "da" != value(t, document, "clientLanguage") {
		t.Errorf("clientLanguage = %v, want the detected da", value(t, document, "clientLanguage"))
	}

	// determine_user_language = false ignores the header.
	fixture.config.Set("login", "determine_user_language", false)
	document = fixture.document(false, session)
	if "en" != value(t, document, "language") {
		t.Errorf("language = %v, want en without detection", value(t, document, "language"))
	}
}

func TestAdminAppDataLoggedOut(t *testing.T) {
	fixture := newFixture(t)
	fixture.options.Admin = fakeAdmin{loggedIn: false}
	fixture.appData = NewAppData(fixture.options)

	document := fixture.document(true, nil)

	if false != value(t, document, "Auth") {
		t.Error("Auth must be false without an admin session")
	}

	admin, ok := value(t, document, "Admin").(map[string]any)
	if !ok {
		t.Fatalf("Admin = %#v", value(t, document, "Admin"))
	}
	if false != admin["host"] || "admin" != admin["path"] || true != admin["allowed"] {
		t.Errorf("Admin = %#v", admin)
	}

	// The admin settings are only reported after the login.
	for _, key := range []string{"adminLogin", "adminTOTP", "pluginsEnable", "supportedPdoDrivers",
		"contactsPdoType", "weakPassword", "gnupg"} {
		if has(document, key) {
			t.Errorf("%s must not be reported before the admin login", key)
		}
	}
	if has(admin, "language") {
		t.Error("Admin.language must not be reported before the admin login")
	}

	// The guest login values are webmail-only.
	if has(document, "signMe") || has(document, "DevEmail") {
		t.Error("the webmail guest keys must not be part of the admin document")
	}

	// The common keys are still there.
	if has(document, "Capa") || has(document, "attachmentLimit") {
		t.Error("the authenticated keys must not be reported before the admin login")
	}
	if "Gazellemail" != value(t, document, "Theme") {
		t.Errorf("Theme = %v", value(t, document, "Theme"))
	}
}

func TestAdminAppDataLoggedIn(t *testing.T) {
	fixture := newFixture(t)
	fixture.options.Admin = fakeAdmin{loggedIn: true}
	fixture.options.GnuPGVersion = "gpg (GnuPG) 2.2.27"
	fixture.options.PdoDrivers = []string{"sqlite"}
	fixture.appData = NewAppData(fixture.options)

	// A weak admin password file is reported.
	if err := os.MkdirAll(fixture.options.PrivateData, 0o700); nil != err {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.options.PrivateData, "admin_password.txt"),
		[]byte("weak\n"), 0o600); nil != err {
		t.Fatalf("write admin_password.txt: %v", err)
	}

	document := fixture.document(true, nil)

	if true != value(t, document, "Auth") {
		t.Fatal("Auth must be true with an admin session")
	}

	tests := []struct {
		key  string
		want any
	}{
		{"adminLogin", "admin"},
		{"adminTOTP", ""},
		{"pluginsEnable", false},
		{"loginDefaultDomain", ""},
		{"determineUserLanguage", true},
		{"determineUserDomain", false},
		{"contactsEnable", false},
		{"contactsSync", false},
		{"contactsPdoType", "sqlite"},
		{"contactsPdoDsn", "host=127.0.0.1;port=3306;dbname=snappymail"},
		{"contactsPdoUser", "root"},
		{"contactsPdoPassword", dummy},
		{"contactsMySQLSSLCA", ""},
		{"contactsMySQLSSLVerify", true},
		// The default value is empty; "HIGH" is only the comment of the key.
		{"contactsMySQLSSLCiphers", ""},
		{"contactsSQLiteGlobal", false},
		{"contactsSuggestionsLimit", 20.0},
		{"faviconUrl", ""},
		{"weakPassword", true},
		{"gnupg", "gpg (GnuPG) 2.2.27"},
	}

	for _, test := range tests {
		if got := value(t, document, test.key); got != test.want {
			t.Errorf("%s = %#v, want %#v", test.key, got, test.want)
		}
	}

	if got := strings.Join(toStrings(t, value(t, document, "supportedPdoDrivers")), ","); "sqlite" != got {
		t.Errorf("supportedPdoDrivers = %q", got)
	}

	admin, _ := value(t, document, "Admin").(map[string]any)
	if "en" != admin["language"] {
		t.Errorf("Admin.language = %v", admin["language"])
	}
	if got := strings.Join(toStrings(t, admin["languages"]), ","); "da,en" != got {
		t.Errorf("Admin.languages = %q", got)
	}
	if "en" != admin["clientLanguage"] {
		t.Errorf("Admin.clientLanguage = %v", admin["clientLanguage"])
	}

	// The logged in admin gets the authenticated capabilities, with Sieve
	// enabled for the admin area.
	if true != capaValue(t, document, "Sieve") {
		t.Error("Capa.Sieve must be true for the admin panel")
	}
	if true != capaValue(t, document, "GnuPG") {
		t.Error("Capa.GnuPG must follow the gnupg version")
	}
	if has(document, "ContactsSync") || has(document, "Email") {
		t.Error("the webmail account keys must not be part of the admin document")
	}
}

func TestPluginsLinkAndFilterLanguage(t *testing.T) {
	fixture := newFixture(t)
	plugins := &fakePlugins{js: true, hash: "abc"}
	fixture.options.Plugins = plugins
	fixture.appData = NewAppData(fixture.options)

	session := &Session{Request: httptest.NewRequest(http.MethodGet, "/", nil), Account: mainAccount()}
	document := fixture.document(false, session)

	// etag = md5(plugin hash + cache.index + APP_VERSION)
	wantLink := "Plugins/0/User/" + md5Hex("abc"+"v1"+testVersion) + "/"
	if wantLink != value(t, document, "PluginsLink") {
		t.Errorf("PluginsLink = %v, want %s", value(t, document, "PluginsLink"), wantLink)
	}
	if !plugins.initCalled {
		t.Error("InitAppData must run for every document")
	}
	if true != value(t, document, "pluginUser") {
		t.Error("InitAppData must receive the resolved account")
	}
	// The fake plugin rewrites the language through filter.language.
	if "da" != value(t, document, "language") {
		t.Errorf("language = %v, want the hooked da", value(t, document, "language"))
	}

	// The admin area uses its own link prefix.
	document = fixture.document(true, nil)
	if want := "Plugins/0/Admin/" + md5Hex("abc"+"v1"+testVersion) + "/"; want != value(t, document, "PluginsLink") {
		t.Errorf("PluginsLink = %v, want %s", value(t, document, "PluginsLink"), want)
	}
}

func TestServeAppDataWritesJSON(t *testing.T) {
	fixture := newFixture(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/AppData/0/123/", nil)

	if !fixture.appData.Serve(recorder, request, &web.Route{}) {
		t.Fatal("Serve must report the request as handled")
	}

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "application/json; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	// localAppData() calls ServerNoCache().
	if got := recorder.Header().Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); nil != err {
		t.Fatalf("body is not JSON (%v): %s", err, recorder.Body.String())
	}
	if false != document["Auth"] {
		t.Errorf("Auth = %v", document["Auth"])
	}
}

func TestServeAppDataReportsFailures(t *testing.T) {
	fixture := newFixture(t)
	fixture.options.Plugins = &fakePlugins{panics: true}
	fixture.appData = NewAppData(fixture.options)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/?/AdminAppData/0/123/", nil)

	fixture.appData.ServeAdmin(recorder, request, &web.Route{})

	if http.StatusInternalServerError != recorder.Code {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if "plugin exploded" != recorder.Body.String() {
		t.Errorf("body = %q, want the error message", recorder.Body.String())
	}
}

func TestRegisterAddsAppDataEndpoints(t *testing.T) {
	fixture := newFixture(t)

	router := web.NewRouter(web.DefaultConfig(), nil, fixture.tree.paths(), nil)
	Register(router, fixture.appData)

	for _, target := range []string{"/?/AppData/0/1/", "/?/AdminAppData/0/1/"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

		if http.StatusOK != recorder.Code {
			t.Fatalf("GET %s = %d, want 200", target, recorder.Code)
		}

		var document map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); nil != err {
			t.Fatalf("GET %s is not JSON (%v): %s", target, err, recorder.Body.String())
		}
		if _, ok := document["System"]; !ok {
			t.Errorf("GET %s has no System block", target)
		}
	}

	// A router without the service keeps working (Ping only).
	recorder := httptest.NewRecorder()
	plain := web.NewRouter(web.DefaultConfig(), nil, fixture.tree.paths(), nil)
	Register(plain, nil)
	plain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?/AppData/0/1/", nil))
	if http.StatusOK != recorder.Code || strings.Contains(recorder.Body.String(), "\"System\"") {
		t.Errorf("a router without AppData must fall back to the index page")
	}
}

// toStrings converts a decoded JSON array into strings.
func toStrings(t *testing.T, value any) []string {
	t.Helper()

	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%#v is not a JSON array", value)
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%#v is not a string", item)
		}
		result = append(result, text)
	}
	return result
}

// systemValueMap returns the decoded "System" block.
func systemValueMap(t *testing.T, document map[string]any) map[string]any {
	t.Helper()

	system, ok := document["System"].(map[string]any)
	if !ok {
		t.Fatal("System is missing from the document")
	}
	return system
}
