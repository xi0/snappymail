// Package caldav is the port of the bundled "caldav" plugin
// (plugins/caldav/index.php), the only plugin the Go backend enables (see the
// "Plugin model" section of SPEC.md).
//
// The PHP plugin is ~3k lines, so the port is split over four steps:
//
//	step 29 (this file set)  configuration, discovery and the HTTP client
//	step 30                  calendar collections and event read/create
//	step 31                  event update/delete and recurrence
//	step 32                  invites, attendees and iTIP mail
//
// The plugin keeps its own per-account configuration in the CONFIG entry
// "calendar_sync" of the account storage instead of the CardDAV shared
// settings, so an account configured by the PHP backend is found again by the
// Go backend and vice versa. The sealed password inside that document is a
// SnappyMail\Crypt blob keyed with the *main* account's crypt key, which is why
// every action resolves the main account even when an additional account is
// selected.
//
// Deliberate deviations from the PHP plugin (kept small and documented):
//
//   - Name() is the plugin id ("caldav"), because PHP's
//     Manager::CreatePluginByName() overwrites AbstractPlugin::NAME with the
//     folder name; the display name of the PHP class is available through
//     DisplayName();
//   - the process-wide plugin instance cannot keep PHP's per-request
//     $lastConfiguredEmail cache, so AutoConfigureCalDAV() simply rewrites the
//     blob (the stored document is identical, only the sealed password is
//     re-encrypted with a fresh nonce);
//   - the hooks receive the request environment as a trailing argument
//     (Actions for "json.after-*", the account Context for "login.success"),
//     because the Go plugin manager is built once at start-up and has no
//     $this->Manager()->Actions().
package caldav

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/plugin"
)

// The metadata of plugins/caldav/index.php. The class constants are private in
// PHP (they are read through AbstractPlugin's accessors and the plugin list), so
// the Go plugin exposes the ones the backend can observe as methods.
const (
	// name is the plugin id: AbstractPlugin::NAME is overwritten by the folder
	// name in PHP, so the storage folder and plugins.enabled_list entry are
	// "caldav".
	name = "caldav"
	// displayName is CaldavPlugin::NAME.
	displayName = "Mailbux CalDAV Auto"
	// version is CaldavPlugin::VERSION.
	version = "1.19"
	// release is CaldavPlugin::RELEASE.
	release = "2026-02-01"
	// category is CaldavPlugin::CATEGORY.
	category = "Calendar"
	// description is CaldavPlugin::DESCRIPTION.
	description = "Auto-configures CalDAV calendar sync with JMAP support - switches per account"
	// required is CaldavPlugin::REQUIRED (the SnappyMail version it targets).
	required = "2.0.0"
)

// New is the plugin.Factory of the caldav plugin (internal/plugins registers it).
func New() plugin.Plugin { return &Plugin{} }

// Plugin is CaldavPlugin.
//
// The instance is created once at start-up and shared by all requests, so it
// holds no request state. The Init() closures reference it, which is also how a
// test injects its own HTTP client.
type Plugin struct {
	// HTTP is the client of the CalDAV requests. A nil value uses defaultClient,
	// which is the value a production binary runs with; the tests point it at
	// an httptest.Server.
	HTTP *http.Client
}

// Name is AbstractPlugin::Name(): the plugin id, which is also the folder name
// of the plugin assets and of its "calendar_sync" configuration.
func (p *Plugin) Name() string { return name }

// DisplayName is CaldavPlugin::NAME (the human readable name the admin panel
// shows).
func (p *Plugin) DisplayName() string { return displayName }

// Version is AbstractPlugin::VERSION.
func (p *Plugin) Version() string { return version }

// Release is CaldavPlugin::RELEASE.
func (p *Plugin) Release() string { return release }

// Category is CaldavPlugin::CATEGORY.
func (p *Plugin) Category() string { return category }

// Required is CaldavPlugin::REQUIRED.
func (p *Plugin) Required() string { return required }

// Description is AbstractPlugin::Description() without the README file lookup
// (the ported plugin ships no README).
func (p *Plugin) Description() string { return description }

// UseLangs is AbstractPlugin::UseLangs(): read langs/*.json.
func (p *Plugin) UseLangs() bool { return true }

// ConfigMap is CaldavPlugin::configMapping().
func (p *Plugin) ConfigMap() []*plugin.Property {
	return []*plugin.Property{
		plugin.NewProperty("caldav_server").
			SetLabel("CalDAV Server URL").
			SetType(plugin.StringValue).
			SetDescription("CalDAV server URL (e.g., https://my.mailbux.com/dav/cal)").
			SetDefaultValue(defaultCalDAVServer),

		plugin.NewProperty("jmap_server").
			SetLabel("JMAP Server URL").
			SetType(plugin.StringValue).
			SetDescription("JMAP Server URL (e.g., https://my.mailbux.com/jmap)").
			SetDefaultValue(defaultJMAPServer),

		// configMapping() declares the SELECTION values with SetOptions(); the Go
		// Property keeps them as the accepted SELECTION values (SetSelection), so
		// the stored value is validated the same way.
		plugin.NewProperty("default_protocol").
			SetLabel("Default Protocol").
			SetType(plugin.SelectionValue).
			SetDescription("Default protocol to use for calendar sync").
			SetSelection([]string{"caldav", "jmap"}).
			SetDefaultValue("caldav"),

		plugin.NewProperty("auto_sync").
			SetLabel("Auto Sync").
			SetType(plugin.BoolValue).
			SetDescription("Automatically sync calendar on login and account switch").
			SetDefaultValue(true),

		plugin.NewProperty("sync_interval").
			SetLabel("Sync Interval (minutes)").
			SetType(plugin.IntValue).
			SetDescription("Auto-sync interval in minutes (0 to disable)").
			SetDefaultValue(5),

		plugin.NewProperty("allow_invites").
			SetLabel("Calendar invites").
			SetType(plugin.BoolValue).
			SetDescription(`Show "Add to calendar" and response options when a received mail contains an .ics calendar invite`).
			SetDefaultValue(true),
	}
}

// FilterAppDataPluginSection is AbstractPlugin::FilterAppDataPluginSection():
// the PHP plugin does not override it, so the AppData.Plugins["caldav"] section
// stays exactly the AllowedInJs configuration (which is empty for this plugin:
// every setting is server side).
func (p *Plugin) FilterAppDataPluginSection(bool, bool, map[string]any) {}

// Init is CaldavPlugin::Init().
func (p *Plugin) Init(env plugin.Env) {
	// Self-configure CalDAV sync per account (no CardDAV dependency).
	env.AddHook("login.success", func(args ...any) {
		request := requestFromHook(args)
		if nil == request {
			return
		}
		request.autoConfigure(env)
	})

	// Called after the AccountSwitch action completes.
	env.AddHook("json.after-AccountSwitch", func(args ...any) {
		if 1 > len(args) {
			return
		}
		// PHP: if (!empty($aResponse['Result'])).
		response, ok := args[0].(*map[string]any)
		if !ok || nil == response {
			return
		}
		if value, present := (*response)["Result"]; !present || phpEmpty(value) {
			return
		}

		request := requestFromHook(args)
		if nil == request {
			return
		}
		request.autoConfigure(env)
	})

	// The JSON actions. Step 29 provides the discovery; the event and invite
	// actions are added by steps 30–32.
	env.AddJSONAction("GetCalendars", func(ctx plugin.ActionContext) (any, error) {
		return p.doPluginGetCalendars(env, ctx)
	})

	// meeting.js and openstreetmap.js must load first: they expose
	// window.MailbuxCalDavMeeting / window.MailbuxCalDavOsm used by both the
	// calendar dialog and the invite box.
	env.AddJS("meeting.js", false)
	env.AddJS("openstreetmap.js", false)
	env.AddJS("calendar-dialog.js", false)
	env.AddJS("message.js", false)

	env.AddCSS("calendar.css", false)
}

// envConfig is plugin.Env.Config() as a nil-safe lookup with the PHP default of
// Config\Plugin::Get('plugin', $sKey, $mDefault).
func envConfig(env plugin.Env, key string, defaultValue any) any {
	if nil == env || nil == env.Config() {
		return defaultValue
	}
	return env.Config().Get(key, defaultValue)
}

// envBool is envConfig for a boolean setting.
func envBool(env plugin.Env, key string, defaultValue bool) bool {
	value := envConfig(env, key, defaultValue)
	flag, ok := value.(bool)
	if ok {
		return flag
	}
	return !phpEmpty(value)
}

// envString is envConfig for a string setting.
func envString(env plugin.Env, key, defaultValue string) string {
	return phpString(envConfig(env, key, defaultValue))
}

// msg is CaldavPlugin::msg(): a string of the plugin's own langs/ folder with
// the %NAME% placeholders replaced.
//
// English is always the base and the language of the request (only "da" has a
// dedicated translation) is merged over it. Without a translation the value of
// fallback is used, and an empty fallback falls back to the key itself, exactly
// like PHP's "$sDefault ?: $sKey".
func msg(env plugin.Env, request *request, key string, replace map[string]string, fallback string) string {
	language := "en"
	if nil != request {
		language = request.language()
	}

	values := map[string]string{}
	if nil != env && "" != env.Path() {
		values = translations(env.Path(), language)
	}

	text, ok := values[key]
	if !ok || "" == text {
		if "" != fallback {
			text = fallback
		} else {
			text = key
		}
	}

	for name, value := range replace {
		text = strings.ReplaceAll(text, "%"+name+"%", value)
	}

	return text
}

// translations merges langs/en.json and langs/<language>.json of the plugin
// folder into the flat "CALDAV" key/value map the messages are looked up in.
func translations(path, language string) map[string]string {
	values := map[string]string{}

	// English is the base of every language; PHP loops over ['en', $sLang] and
	// replaces the English values with the native ones.
	codes := []string{"en"}
	if "en" != language {
		codes = append(codes, language)
	}

	for _, code := range codes {
		for key, value := range readLangSection(filepath.Join(path, "langs", code+".json")) {
			values[key] = value
		}
	}

	return values
}

// readLangSection reads the "CALDAV" section of one plugin language file. A
// missing or unreadable file yields an empty map, like the is_file() guard of
// msg().
func readLangSection(name string) map[string]string {
	result := map[string]string{}

	content, err := os.ReadFile(name)
	if nil != err {
		return result
	}

	document := map[string]any{}
	if err := json.Unmarshal(content, &document); nil != err {
		return result
	}

	section, ok := document["CALDAV"].(map[string]any)
	if !ok {
		return result
	}

	for key, value := range section {
		result[key] = phpString(value)
	}

	return result
}
