// Package plugin is the port of RainLoop\Plugins: the plugin API surface
// (AbstractPlugin, Property and Helper), the per-plugin configuration
// (Config\Plugin) and the Manager that compiles the assets, templates,
// languages and JSON actions of the plugins registered at build time.
//
// PHP discovers plugins in data/_data_/<name>/plugins/<id>/ and loads their
// index.php at runtime. The Go backend registers them at build time instead
// (see the "Plugin model" section of SPEC.md): a plugin is a Go type that
// implements Plugin and is listed in internal/plugins/registry.go. Adding a
// plugin is therefore a code change plus a build, which is why the admin
// package/plugin actions of step 27 report "build-time only".
//
// The manager is built once at start-up, its Init() calls register hooks and
// assets, and it is read-only afterwards, so one manager serves all requests.
package plugin

import (
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// PropertyType is Enumerations\PluginPropertyType. The numbers are part of the
// admin JSON ("type" of Object/PluginProperty) and must never change.
type PropertyType int

// The RainLoop\Enumerations\PluginPropertyType values, in PHP's numbering.
const (
	// StringValue is PluginPropertyType::STRING.
	StringValue PropertyType = 0
	// IntValue is PluginPropertyType::INT.
	IntValue PropertyType = 1
	// StringTextValue is PluginPropertyType::STRING_TEXT (a textarea).
	StringTextValue PropertyType = 2
	// PasswordValue is PluginPropertyType::PASSWORD.
	PasswordValue PropertyType = 3
	// SelectionValue is PluginPropertyType::SELECTION (a checkbox list).
	SelectionValue PropertyType = 4
	// BoolValue is PluginPropertyType::BOOL.
	BoolValue PropertyType = 5
	// URLValue is PluginPropertyType::URL.
	URLValue PropertyType = 6
	// GroupValue is PluginPropertyType::GROUP (a visual group, no value).
	GroupValue PropertyType = 7
	// SelectValue is PluginPropertyType::SELECT (a dropdown).
	SelectValue PropertyType = 8
)

// Option is one entry of a SELECT property (PHP's array('id' => …, 'name' => …)).
type Option struct {
	// ID is the value stored in the configuration.
	ID string
	// Name is the label shown in the admin panel.
	Name string
}

// Property is RainLoop\Plugins\Property: one plugin setting, declared by
// Plugin.ConfigMap() and stored in the "plugin" section of the plugin
// configuration file.
type Property struct {
	name        string
	typ         PropertyType
	label       string
	desc        string
	placeholder string
	allowedInJS bool
	encrypted   bool
	value       any
	defaultVal  any
	options     []Option
	selection   []string
}

// NewProperty is Property::NewInstance(). The default value starts as the empty
// string, like AbstractPlugin's $mDefaultValue (so an options-only property
// reports '' and not null).
func NewProperty(name string) *Property {
	return &Property{name: name, defaultVal: ""}
}

// SetType is Property::SetType().
func (p *Property) SetType(value PropertyType) *Property {
	p.typ = value
	return p
}

// SetLabel is Property::SetLabel().
func (p *Property) SetLabel(label string) *Property {
	p.label = label
	return p
}

// SetDescription is Property::SetDescription().
func (p *Property) SetDescription(desc string) *Property {
	p.desc = desc
	return p
}

// SetPlaceholder is Property::SetPlaceholder().
func (p *Property) SetPlaceholder(placeholder string) *Property {
	p.placeholder = placeholder
	return p
}

// SetAllowedInJs is Property::SetAllowedInJs(): the value is exposed to the
// frontend through AppData.Plugins[<name>].
func (p *Property) SetAllowedInJs(allowed bool) *Property {
	p.allowedInJS = allowed
	return p
}

// SetEncrypted is Property::SetEncrypted(): the stored value is a
// Crypt::EncryptToJSON() blob with APP_SALT as key.
func (p *Property) SetEncrypted(encrypted bool) *Property {
	p.encrypted = encrypted
	return p
}

// SetDefaultValue is Property::SetDefaultValue(): an array switches between the
// SELECT options and the SELECTION values, anything else is the default value.
func (p *Property) SetDefaultValue(value any) *Property {
	switch typed := value.(type) {
	case []Option:
		p.options = typed
	case []string:
		p.selection = typed
	default:
		p.defaultVal = value
	}
	return p
}

// SetOptions is Property::SetOptions() for a SELECT property.
func (p *Property) SetOptions(options []Option) *Property {
	p.options = options
	return p
}

// SetSelection is Property::SetOptions() for a SELECTION property.
func (p *Property) SetSelection(values []string) *Property {
	p.selection = values
	return p
}

// SetValue is Property::SetValue(): the value is cast to the property type and
// silently dropped (nil) when it is not one of the declared options.
func (p *Property) SetValue(value any) {
	p.value = nil

	switch p.typ {
	case IntValue:
		p.value = phpInt(value)
	case BoolValue:
		p.value = !phpEmpty(value)
	case SelectValue:
		for _, option := range p.options {
			if phpString(value) == option.ID {
				p.value = phpString(value)
			}
		}
	case SelectionValue:
		for _, item := range p.selection {
			if phpString(value) == item {
				p.value = phpString(value)
			}
		}
	case PasswordValue, StringValue, StringTextValue, URLValue:
		p.value = phpString(value)
	}
}

// Name is Property::Name().
func (p *Property) Name() string {
	if nil == p {
		return ""
	}
	return p.name
}

// Type is Property::Type().
func (p *Property) Type() PropertyType {
	if nil == p {
		return StringValue
	}
	return p.typ
}

// Label is Property::Label().
func (p *Property) Label() string {
	if nil == p {
		return ""
	}
	return p.label
}

// Description is Property::Description().
func (p *Property) Description() string {
	if nil == p {
		return ""
	}
	return p.desc
}

// Placeholder is Property::Placeholder().
func (p *Property) Placeholder() string {
	if nil == p {
		return ""
	}
	return p.placeholder
}

// AllowedInJs is Property::AllowedInJs().
func (p *Property) AllowedInJs() bool {
	return nil != p && p.allowedInJS
}

// Encrypted is Property::encrypted.
func (p *Property) Encrypted() bool {
	return nil != p && p.encrypted
}

// DefaultValue is Property::DefaultValue().
func (p *Property) DefaultValue() any {
	if nil == p {
		return nil
	}
	return p.defaultVal
}

// Options is Property::Options().
func (p *Property) Options() []Option {
	if nil == p {
		return nil
	}
	return p.options
}

// Value is Property::Value().
func (p *Property) Value() any {
	if nil == p {
		return nil
	}
	return p.value
}

// JSONSerialize is Property::jsonSerialize(): the Object/PluginProperty the
// admin panel edits.
//
// Unlike PHP it does not decrypt an encrypted value: the current admin port
// (step 27) does not expose plugin configuration yet, and a later step calls
// SetValue() with the decrypted value (or Actions::APP_DUMMY) before
// serializing, exactly like DoAdminPluginLoad()/DoAdminPluginSettingsUpdate().
func (p *Property) JSONSerialize() map[string]any {
	return map[string]any{
		"@Object":     "Object/PluginProperty",
		"value":       p.Value(),
		"placeholder": p.Placeholder(),
		"name":        p.Name(),
		"type":        int(p.Type()),
		"label":       p.Label(),
		"default":     p.DefaultValue(),
		"options":     optionsJSON(p.Options()),
		"desc":        p.Description(),
	}
}

// optionsJSON renders the SELECT options like PHP's json_encode() of the
// array-of-arrays property declaration.
func optionsJSON(options []Option) []any {
	result := make([]any, 0, len(options))
	for _, option := range options {
		result = append(result, map[string]any{"id": option.ID, "name": option.Name})
	}
	return result
}

// Plugin is RainLoop\Plugins\AbstractPlugin: a build-time plugin.
//
// Name() is the plugin id and doubles as the folder name of its assets and
// language files (data/_data_/<name>/plugins/<id>/).
type Plugin interface {
	// Name is AbstractPlugin::Name(), e.g. "caldav".
	Name() string
	// Version is AbstractPlugin::VERSION.
	Version() string
	// Description is AbstractPlugin::Description().
	Description() string
	// UseLangs is AbstractPlugin::UseLangs(): read <path>/langs/*.json.
	UseLangs() bool
	// Init is AbstractPlugin::Init(): register hooks, assets, templates and
	// JSON actions through env.
	Init(env Env)
	// ConfigMap is AbstractPlugin::configMapping().
	ConfigMap() []*Property
	// FilterAppDataPluginSection is
	// AbstractPlugin::FilterAppDataPluginSection(): the last chance to change
	// (or drop) the AppData.Plugins[<name>] section.
	FilterAppDataPluginSection(admin, auth bool, config map[string]any)
}

// Factory builds a plugin instance. It is called once at start-up, before
// Init(), so a factory must not depend on request state.
type Factory func() Plugin

// Hook is one plugin hook callback. PHP calls it with the hook arguments
// spread (`$mCallback(...$aArg)`); arguments that the hook may rewrite are
// passed as pointers, e.g. `&params`.
//
// A hook whose PHP implementation reaches the request through
// `$this->Manager()->Actions()` receives the request environment as a trailing
// argument, because the Go manager is process wide: "login.success" gets the
// *account.Context of the request (storage, crypt, APP_SALT) and "json.after-*"
// the request context (account resolution plus the same environment). The
// documented argument order of plugins/README.md is kept for the arguments that
// precede it.
type Hook func(args ...any)

// Part is Plugins\Manager::AddAdditionalPartAction() callback. PHP spreads the
// path vector into the arguments (`$mCallbak(...$aParts)`).
type Part func(args ...string) bool

// ActionContext is the Actions surface a plugin JSON action gets:
// Actions::GetActionParam()/HasActionParam() and Actions\Response::
// DefaultResponse().
//
// *handler.Context satisfies it structurally, so this package stays
// independent of internal/handler.
type ActionContext interface {
	// GetParam is Actions::GetActionParam().
	GetParam(key string, defaultValue any) any
	// HasParam is Actions::HasActionParam().
	HasParam(key string) bool
	// DefaultResponse is Actions\Response::DefaultResponse().
	DefaultResponse(result any, additional map[string]any) map[string]any
}

// JSONAction is a plugin JSON action (DoPlugin<Action>). PHP calls it without
// arguments and expects the result of jsonResponse() (a DefaultResponse map);
// the context is passed here so a plugin can read its parameters.
type JSONAction func(ctx ActionContext) (any, error)

// Env is the AbstractPlugin registration surface a plugin receives in Init().
type Env interface {
	// AddHook is AbstractPlugin::addHook() (the hook name is lowercased).
	AddHook(name string, fn Hook)
	// AddJS is AbstractPlugin::addJs().
	AddJS(file string, admin bool)
	// AddCSS is AbstractPlugin::addCss().
	AddCSS(file string, admin bool)
	// AddTemplate is AbstractPlugin::addTemplate() (the compiled view
	// templates of the index page).
	AddTemplate(file string, admin bool)
	// AddPartHook is AbstractPlugin::addPartHook(): a non-JSON endpoint.
	AddPartHook(action string, fn Part)
	// AddJSONAction is AbstractPlugin::addJsonHook(): DoPlugin<action>.
	AddJSONAction(action string, fn JSONAction)
	// Path is AbstractPlugin::Path(): the plugin folder, or "" for a plugin
	// that is not installed on disk (Go plugins may be compiled in without
	// their assets being copied to the data folder).
	Path() string
	// Config is AbstractPlugin::Config(): the plugin-<name>.json document.
	Config() *Config
	// Account is Actions::getAccountFromToken(false) of the request that runs
	// Init(); it is nil at start-up (Init runs once per process).
	Account() *account.Account
}

// ValidateWildcardValues is Plugins\Helper::ValidateWildcardValues(): the
// wildcard matcher the change-password and ldap plugins use for their
// "<name>_allowed_emails" settings.
//
// foundValue is PHP's by-reference $sFoundValue (the wildcard entry that
// matched).
func ValidateWildcardValues(value, wildcardValues string) (matched bool, foundValue string) {
	return domain.ValidateWildcardValues(value, wildcardValues)
}

// The PHP casts the plugin code relies on. They are deliberately tiny and
// private: everything that must be shared lives in its own package.

// phpString is PHP's (string) cast for the scalar types a decoded JSON
// document or an admin form holds.
func phpString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "1"
		}
		return ""
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case nil:
		return ""
	}
	return ""
}

// phpInt is PHP's (int) cast for the same types.
func phpInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		return leadingInt(typed)
	}
	return 0
}

// phpEmpty ports PHP's empty() for the values a plugin sees.
func phpEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return "" == typed || "0" == typed
	case bool:
		return !typed
	case int:
		return 0 == typed
	case int64:
		return 0 == typed
	case float64:
		return 0 == typed
	case []any:
		return 0 == len(typed)
	case map[string]any:
		return 0 == len(typed)
	}
	return false
}

// leadingInt is PHP's intval() for a string: the leading decimal number.
func leadingInt(value string) int {
	value = strings.TrimSpace(value)

	end := 0
	if 0 < len(value) && ('+' == value[0] || '-' == value[0]) {
		end = 1
	}
	start := end
	for end < len(value) && '0' <= value[end] && '9' >= value[end] {
		end++
	}
	if end == start {
		return 0
	}

	number, err := strconv.Atoi(value[:end])
	if nil != err {
		return 0
	}
	return number
}
