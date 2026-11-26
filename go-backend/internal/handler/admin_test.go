package handler

// Tests of the Step 27 admin core actions: login/logout (through a fake admin
// session), the settings get/set/update round-trip, password update, cache
// clearing, the info report and the QR code.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
)

// adminPanelFake is a handler.AdminPanel with canned results.
type adminPanelFake struct {
	loggedIn bool
	loginErr error

	loginCalls int
	loginValue string
	passValue  string
	totpValue  string

	logoutCalls int
}

// IsLoggedIn is Actions\Admin::IsAdminLoggined().
func (a *adminPanelFake) IsLoggedIn(throw bool) (bool, error) {
	if a.loggedIn {
		return true, nil
	}
	if throw {
		return false, appctx.NewClientError(appctx.AuthError, nil, "")
	}
	return false, nil
}

// Login records the credentials and returns the canned error.
func (a *adminPanelFake) Login(login, password, passcode string) error {
	a.loginCalls++
	a.loginValue, a.passValue, a.totpValue = login, password, passcode
	return a.loginErr
}

// Logout records the call.
func (a *adminPanelFake) Logout() error {
	a.logoutCalls++
	return nil
}

// ValidatePassword accepts the canned password.
func (a *adminPanelFake) ValidatePassword(password string) bool { return "good" == password }

// adminConfigFake is a handler.AdminConfig backed by maps.
type adminConfigFake struct {
	values   map[string]map[string]any
	dump     map[string]map[string][]any
	password string
	saved    bool
}

// newAdminConfigFake builds an empty configuration double.
func newAdminConfigFake() *adminConfigFake {
	return &adminConfigFake{values: map[string]map[string]any{}, dump: map[string]map[string][]any{}}
}

// GetString returns the stored string value or the default.
func (f *adminConfigFake) GetString(section, key, defaultValue string) string {
	if items, ok := f.values[section]; ok {
		if value, ok := items[key]; ok {
			return phpString(value)
		}
	}
	return defaultValue
}

// GetBool returns the stored bool value or the default.
func (f *adminConfigFake) GetBool(section, key string, defaultValue bool) bool {
	if items, ok := f.values[section]; ok {
		if value, ok := items[key]; ok {
			return phpTruthy(value)
		}
	}
	return defaultValue
}

// GetInt returns the stored int value or the default.
func (f *adminConfigFake) GetInt(section, key string, defaultValue int) int {
	if items, ok := f.values[section]; ok {
		if value, ok := items[key]; ok {
			return phpIntValue(value)
		}
	}
	return defaultValue
}

// Set stores a value.
func (f *adminConfigFake) Set(section, key string, value any) {
	if nil == f.values[section] {
		f.values[section] = map[string]any{}
	}
	f.values[section][key] = value
}

// Save records the save.
func (f *adminConfigFake) Save() error {
	f.saved = true
	return nil
}

// DumpExtended returns the canned [value, comment] document.
func (f *adminConfigFake) DumpExtended() map[string]map[string][]any {
	result := map[string]map[string][]any{}
	for section, keys := range f.dump {
		result[section] = map[string][]any{}
		for key, values := range keys {
			copied := make([]any, len(values))
			copy(copied, values)
			result[section][key] = copied
		}
	}
	return result
}

// ValidatePassword checks the canned password.
func (f *adminConfigFake) ValidatePassword(password string) bool { return password == f.password }

// SetPassword stores a new password.
func (f *adminConfigFake) SetPassword(password string) { f.password = password }

// adminDispatcher builds a dispatcher wired to the fakes.
func adminDispatcher(panel AdminPanel, config AdminConfig, appData map[string]any) *Dispatcher {
	return NewDispatcher(Options{
		Version:    "0.0.0",
		AdminPanel: func(Session) AdminPanel { return panel },
		Config:     config,
		AppData: func(http.ResponseWriter, *http.Request, bool) map[string]any {
			return appData
		},
	})
}

// adminContext builds the per-request context of one admin action.
func adminContext(dispatcher *Dispatcher, params map[string]any) *Context {
	return &Context{dispatcher: dispatcher, params: params}
}

// TestAdminLogin checks DoAdminLogin: it forwards the credentials and returns
// the AppData document.
func TestAdminLogin(t *testing.T) {
	panel := &adminPanelFake{}
	appData := map[string]any{"Auth": true, "Admin": map[string]any{"path": "admin"}}
	ctx := adminContext(adminDispatcher(panel, newAdminConfigFake(), appData), map[string]any{
		"Login":    "admin",
		"Password": "secret",
		"TOTP":     "123456",
	})

	result, err := doAdminLogin(ctx)
	if nil != err {
		t.Fatalf("doAdminLogin: %v", err)
	}
	response := mustResponse(result, err)
	result2, _ := response["Result"].(map[string]any)
	if nil == result2 || true != result2["Auth"] {
		t.Fatalf("Result = %v", response["Result"])
	}
	if 1 != panel.loginCalls || "admin" != panel.loginValue || "secret" != panel.passValue || "123456" != panel.totpValue {
		t.Errorf("login credentials = %q/%q/%q", panel.loginValue, panel.passValue, panel.totpValue)
	}

	// A failed login surfaces the AuthError.
	panel.loginErr = appctx.NewClientError(appctx.AuthError, nil, "")
	if _, err := doAdminLogin(ctx); appctx.AuthError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.AuthError)
	}

	// Without an admin panel the login cannot succeed.
	ctx = adminContext(NewDispatcher(Options{Version: "0.0.0"}), map[string]any{"Login": "x"})
	if _, err := doAdminLogin(ctx); appctx.AuthError != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.AuthError)
	}
}

// TestAdminLogout checks DoAdminLogout.
func TestAdminLogout(t *testing.T) {
	panel := &adminPanelFake{}
	ctx := adminContext(adminDispatcher(panel, newAdminConfigFake(), nil), map[string]any{})

	response := mustResponse(doAdminLogout(ctx))
	if nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}
	if 1 != panel.logoutCalls {
		t.Errorf("logout calls = %d, want 1", panel.logoutCalls)
	}
}

// TestAdminSettingsGet checks DoAdminSettingsGet: it drops [version] and appends
// the select options.
func TestAdminSettingsGet(t *testing.T) {
	config := newAdminConfigFake()
	config.dump = map[string]map[string][]any{
		"version": {"current": {""}, "saved": {""}},
		"logs":    {"time_zone": {"UTC", "comment"}},
		"login":   {"sign_me_auto": {"DefaultOff", "comment"}},
		"defaults": {"view_images": {"ask", "comment"}},
		"webmail": {"title": {"SnappyMail", "Text"}},
	}
	ctx := adminContext(adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil), map[string]any{})

	response := mustResponse(doAdminSettingsGet(ctx))
	if nil == response {
		t.Fatal("doAdminSettingsGet failed")
	}
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if _, ok := result["version"]; ok {
		t.Error("the [version] section must be dropped")
	}

	logs, _ := result["logs"].(map[string]any)
	timeZone, _ := logs["time_zone"].([]any)
	if 3 != len(timeZone) {
		t.Fatalf("logs.time_zone = %#v, want three elements", logs["time_zone"])
	}
	if "" != timeZone[1] {
		t.Errorf("time_zone comment = %v, want empty", timeZone[1])
	}
	if _, ok := timeZone[2].([]string); !ok {
		t.Errorf("time_zone options = %#v, want a string list", timeZone[2])
	}

	login, _ := result["login"].(map[string]any)
	signMe, _ := login["sign_me_auto"].([]any)
	if options, _ := signMe[2].([]string); 3 != len(options) || "Unused" != options[2] {
		t.Errorf("sign_me_auto options = %#v", signMe[2])
	}
}

// TestAdminSettingsUpdate checks DoAdminSettingsUpdate's coercions.
func TestAdminSettingsUpdate(t *testing.T) {
	config := newAdminConfigFake()
	panel := &adminPanelFake{loggedIn: true}
	dispatcher := adminDispatcher(panel, config, nil)
	dispatcher.adminLanguage = func(string) string { return "da" }

	ctx := adminContext(dispatcher, map[string]any{
		"attachmentLimit":   42, // int
		"capaOpenPGP":       false,
		"capaGnuPG":         "false", // string "false" is false
		"CapaThemes":        true,
		"contactsPdoType":   "mysql", // validPdoType falls back to sqlite
		"languageAdmin":     "xx",
		"contactsPdoPassword": appDummy, // unchanged dummy is skipped
	})

	response := mustResponse(doAdminSettingsUpdate(ctx))
	if nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}
	if !config.saved {
		t.Error("the configuration was not saved")
	}

	if 42 != config.values["webmail"]["attachment_size_limit"] {
		t.Errorf("attachment_size_limit = %v", config.values["webmail"]["attachment_size_limit"])
	}
	if false != config.values["security"]["openpgp"] {
		t.Errorf("security.openpgp = %v", config.values["security"]["openpgp"])
	}
	if false != config.values["security"]["gnupg"] {
		t.Errorf("security.gnupg = %v (\"false\" must be false)", config.values["security"]["gnupg"])
	}
	if true != config.values["webmail"]["allow_themes"] {
		t.Errorf("allow_themes = %v", config.values["webmail"]["allow_themes"])
	}
	if "sqlite" != config.values["contacts"]["type"] {
		t.Errorf("contacts.type = %v, want the validPdoType fallback", config.values["contacts"]["type"])
	}
	if "da" != config.values["admin_panel"]["language"] {
		t.Errorf("admin_panel.language = %v, want the validated da", config.values["admin_panel"]["language"])
	}
	if _, ok := config.values["contacts"]["pdo_password"]; ok {
		t.Error("an unchanged dummy password must not be written")
	}
}

// TestAdminSettingsSet checks DoAdminSettingsSet validation.
func TestAdminSettingsSet(t *testing.T) {
	config := newAdminConfigFake()
	ctx := adminContext(adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil), map[string]any{
		"config": map[string]any{
			"webmail": map[string]any{"title": "New title", "allow_themes": true},
		},
	})

	response := mustResponse(doAdminSettingsSet(ctx))
	if nil == response || true != response["Result"] {
		t.Fatalf("Result = %v", response["Result"])
	}
	if "New title" != config.values["webmail"]["title"] {
		t.Errorf("title = %v", config.values["webmail"]["title"])
	}

	// The "custom" section is rejected.
	ctx = adminContext(adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil), map[string]any{
		"config": map[string]any{"custom": map[string]any{"x": "y"}},
	})
	if _, err := doAdminSettingsSet(ctx); appctx.InvalidInputArgument != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidInputArgument)
	}

	// A non-scalar value is rejected.
	ctx = adminContext(adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil), map[string]any{
		"config": map[string]any{"webmail": map[string]any{"title": map[string]any{}}},
	})
	if _, err := doAdminSettingsSet(ctx); appctx.InvalidInputArgument != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidInputArgument)
	}
}

// TestAdminPasswordUpdate checks DoAdminPasswordUpdate.
func TestAdminPasswordUpdate(t *testing.T) {
	dir := t.TempDir()
	passfile := filepath.Join(dir, "admin_password.txt")
	if err := os.WriteFile(passfile, []byte("weak\n"), 0o600); nil != err {
		t.Fatalf("write weak file: %v", err)
	}

	config := newAdminConfigFake()
	config.password = "old"
	dispatcher := adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil)
	dispatcher.privateData = dir

	// A wrong current password changes nothing.
	ctx := adminContext(dispatcher, map[string]any{"Password": "wrong"})
	if response := mustResponse(doAdminPasswordUpdate(ctx)); false != response["Result"] {
		t.Errorf("Result = %v, want false for a wrong password", response["Result"])
	}
	if !isRegularFile(passfile) {
		t.Error("the weak password file must survive a failed update")
	}

	// A correct password updates the login/TOTP and removes the weak file.
	ctx = adminContext(dispatcher, map[string]any{
		"Password":    "old",
		"newPassword": "brandnew",
		"Login":       "root",
		"TOTP":        "JBSWY3DPEHPK3PXP",
	})
	response := mustResponse(doAdminPasswordUpdate(ctx))
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v, want the Weak document", response["Result"])
	}
	if false != result["Weak"] {
		t.Errorf("Weak = %v, want false after the weak file was removed", result["Weak"])
	}
	if "root" != config.values["security"]["admin_login"] {
		t.Errorf("admin_login = %v", config.values["security"]["admin_login"])
	}
	if "brandnew" != config.password {
		t.Errorf("password = %v", config.password)
	}
	if isRegularFile(passfile) {
		t.Error("the weak password file must be removed after a password change")
	}
}

// TestAdminClearCache checks that DoAdminClearCache removes the cache folder.
func TestAdminClearCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(filepath.Join(cache, "__"), 0o700); nil != err {
		t.Fatalf("mkdir cache: %v", err)
	}

	dispatcher := adminDispatcher(&adminPanelFake{loggedIn: true}, newAdminConfigFake(), nil)
	dispatcher.privateData = dir

	if response := mustResponse(doAdminClearCache(adminContext(dispatcher, map[string]any{}))); nil == response {
		t.Fatal("doAdminClearCache failed")
	}
	if _, err := os.Stat(cache); nil == err {
		t.Error("the cache folder must be removed")
	}
}

// TestAdminQRCode checks that DoAdminQRCode renders a non-empty block QR code.
func TestAdminQRCode(t *testing.T) {
	config := newAdminConfigFake()
	config.values["webmail"] = map[string]any{"title": "SnappyMail"}
	ctx := adminContext(adminDispatcher(&adminPanelFake{loggedIn: true}, config, nil), map[string]any{
		"username": "user@example.com",
		"TOTP":     "JBSWY3DPEHPK3PXP",
	})

	response := mustResponse(doAdminQRCode(ctx))
	if nil == response {
		t.Fatal("doAdminQRCode failed")
	}
	rendered, _ := response["Result"].(string)
	if !strings.Contains(rendered, "█") || !strings.Contains(rendered, "▀") {
		t.Errorf("the QR code does not use the block characters: %q", rendered)
	}
}

// TestAdminInfo checks the DoAdminInfo shape.
func TestAdminInfo(t *testing.T) {
	dispatcher := adminDispatcher(&adminPanelFake{loggedIn: true}, newAdminConfigFake(), nil)
	dispatcher.version = "0.0.0"

	response := mustResponse(doAdminInfo(adminContext(dispatcher, map[string]any{})))
	if nil == response {
		t.Fatal("doAdminInfo failed")
	}
	result, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if _, ok := result["system"]; !ok {
		t.Error("system block is missing")
	}
	core, _ := result["core"].(map[string]any)
	if _, ok := core["warnings"]; !ok {
		t.Error("core.warnings is missing")
	}
	if list, _ := result["php"].([]any); 0 == len(list) {
		t.Error("the php checklist must not be empty")
	}
}
