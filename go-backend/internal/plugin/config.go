package plugin

// Port of RainLoop\Config\Plugin (over Config\AbstractConfig): the
// configs/plugin-<name>.json document that holds the "plugin" section of one
// plugin.
//
// The layout is a compatibility contract: PHP writes the file with
// json_encode($config, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) and reads
// the old plugin-<name>.ini first when no .json exists yet, so a migration
// between the two backends keeps the configured values. Save() therefore
// writes the .json pretty printed (4 spaces, no escaped slashes), removes the
// legacy .ini and keeps the values in declaration order.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config is Config\Plugin: the typed "plugin" section of one plugin.
//
// The type of every value comes from the Property default (bool, int, float or
// string), exactly like Config\Plugin::defaultValues() feeding
// AbstractConfig::Set().
type Config struct {
	// name is the plugin id; the file names derive from it.
	name string
	// dir is APP_PRIVATE_DATA . 'configs/'.
	dir string
	// keys keeps the declaration order of the settings.
	keys []string
	// values holds the typed values of the "plugin" section.
	values map[string]any
	// crypter seals and opens the encrypted properties.
	crypter Crypter
	// salt is APP_SALT, the key of Crypt::EncryptToJSON().
	salt string
	// loaded reports whether Load()/Save() already ran for this instance.
	loaded bool
}

// NewConfig is Config\Plugin::__construct(): the initialised defaults of the
// property map (a live plugin is required, PHP does the same).
func newConfig(name, dir string, properties []*Property, crypter Crypter, salt string) *Config {
	config := &Config{
		name:    name,
		dir:     dir,
		values:  map[string]any{},
		crypter: crypter,
		salt:    salt,
	}

	for _, property := range properties {
		if nil == property {
			continue
		}
		value := property.DefaultValue()
		if isListValue(value) {
			// Config\Plugin uses '' for an array default (the options are
			// stored as a string value).
			value = ""
		}
		config.keys = append(config.keys, property.Name())
		config.values[property.Name()] = value
	}

	return config
}

// isListValue reports whether value is a declaration array (options): PHP's
// Config\Plugin treats every array default as the empty string.
func isListValue(value any) bool {
	switch value.(type) {
	case []Option, []string, []any:
		return true
	}
	return false
}

// Name is the plugin id.
func (c *Config) Name() string {
	if nil == c {
		return ""
	}
	return c.name
}

// File is the absolute path of the .json configuration file.
func (c *Config) File() string {
	if nil == c || "" == c.dir {
		return ""
	}
	return filepath.Join(c.dir, "plugin-"+c.name+".json")
}

// legacyFile is the path of the pre-0.0.0 .ini configuration file.
func (c *Config) legacyFile() string {
	if nil == c || "" == c.dir {
		return ""
	}
	return filepath.Join(c.dir, "plugin-"+c.name+".ini")
}

// Get is AbstractConfig::Get(): the value of key, or defaultValue when the key
// is unknown or holds null (PHP's isset()).
func (c *Config) Get(key string, defaultValue any) any {
	if nil == c {
		return defaultValue
	}
	if value, ok := c.values[key]; ok && nil != value {
		return value
	}
	return defaultValue
}

// Set is AbstractConfig::Set(): the value keeps the type of the declared
// default. A key that is not declared is ignored.
func (c *Config) Set(key string, value any) {
	if nil == c {
		return
	}
	current, ok := c.values[key]
	if !ok {
		return
	}
	c.values[key] = castLike(current, value)
}

// GetDecrypted is AbstractConfig::getDecrypted(): the value decrypted with
// APP_SALT, or defaultValue when it is empty or broken.
func (c *Config) GetDecrypted(key string, defaultValue any) any {
	blob := phpString(c.Get(key, ""))
	if "" == blob || nil == c || nil == c.crypter {
		return defaultValue
	}

	value, err := c.crypter.DecryptFromJSON(blob, c.salt)
	if nil != err {
		return defaultValue
	}
	return value
}

// SetEncrypted is AbstractConfig::setEncrypted().
func (c *Config) SetEncrypted(key string, value any) {
	if nil == c || nil == c.crypter {
		return
	}

	blob, err := c.crypter.EncryptToJSON(value, c.salt)
	if nil != err {
		return
	}
	c.Set(key, blob)
}

// Load is AbstractConfig::Load(): read the .json file, else the legacy .ini
// file, and apply the stored "plugin" section onto the defaults. A missing or
// unreadable file is written back with the defaults, exactly like PHP.
func (c *Config) Load() bool {
	if nil == c || "" == c.dir {
		return true
	}

	file := c.File()
	if !isFile(file) {
		legacy := c.legacyFile()
		if !isFile(legacy) {
			return c.Save()
		}
		file = legacy
	}

	content, err := os.ReadFile(file)
	if nil != err {
		return false
	}

	section := map[string]any{}
	if strings.HasSuffix(file, ".json") {
		decoded := map[string]any{}
		if err := json.Unmarshal(content, &decoded); nil != err {
			return c.Save()
		}
		if pluginSection, ok := decoded["plugin"].(map[string]any); ok {
			section = pluginSection
		}
	} else {
		section = parseLegacyINI(string(content), "plugin")
	}

	for key, value := range section {
		c.Set(key, value)
	}
	c.loaded = true

	// PHP migrates the legacy file on the next Save(); so does an explicit
	// Save() of this instance.
	return true
}

// Save is AbstractConfig::Save() for a .json configuration: the pretty printed
// document (JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES) and the removal of the
// legacy .ini file.
func (c *Config) Save() bool {
	if nil == c || "" == c.dir {
		return false
	}

	if err := os.MkdirAll(c.dir, 0o700); nil != err {
		return false
	}

	encoded, err := encodePretty(map[string]any{"plugin": c.values})
	if nil != err {
		return false
	}

	if err := os.WriteFile(c.File(), encoded, 0o600); nil != err {
		return false
	}

	if legacy := c.legacyFile(); isFile(legacy) {
		_ = os.Remove(legacy)
	}

	c.loaded = true
	return true
}

// Loaded reports whether the document was read or written.
func (c *Config) Loaded() bool { return nil != c && c.loaded }

// Values is the "plugin" section as a plain map (the shape Config\Plugin::
// jsonSerialize() writes).
func (c *Config) Values() map[string]any {
	if nil == c {
		return map[string]any{}
	}
	result := make(map[string]any, len(c.values))
	for key, value := range c.values {
		result[key] = value
	}
	return result
}

// Keys is the declaration order of the settings.
func (c *Config) Keys() []string {
	if nil == c {
		return nil
	}
	return c.keys
}

// encodePretty is json_encode($value, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES):
// four space indentation, no escaped slashes and no escaped HTML. encoding/json
// sorts the keys of a map, which is the closest stable equivalent of PHP's
// insertion order and only affects the cosmetic ordering of the file.
func encodePretty(value any) ([]byte, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "    ")
	if err := encoder.Encode(value); nil != err {
		return nil, err
	}
	return []byte(buffer.String()), nil
}

// parseLegacyINI is a minimal parse_ini_file($file, true) for the plugin .ini
// files of older installations: "[section]" headers, "key = value" pairs,
// ";" and "#" comments, quoted strings and On/Off booleans.
//
// PHP's parser supports more (constants, heredocs, key suffixes), which no
// plugin configuration file uses; the unsupported syntax is ignored rather
// than throwing.
func parseLegacyINI(content, section string) map[string]any {
	result := map[string]any{}
	current := ""

	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if "" == line || ';' == line[0] || '#' == line[0] {
			continue
		}

		if '[' == line[0] {
			if end := strings.IndexByte(line, ']'); 0 < end {
				current = line[1:end]
			}
			continue
		}

		if current != section {
			continue
		}

		key, raw, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if "" == key {
			continue
		}
		result[key] = iniValue(raw)
	}

	return result
}

// iniValue is parse_ini_file()'s scalar handling for one value.
func iniValue(raw string) any {
	value := strings.TrimSpace(raw)

	// Drop a trailing comment that is not inside quotes.
	if '"' != firstRune(value) && '\'' != firstRune(value) {
		if index := strings.IndexAny(value, ";#"); 0 <= index {
			value = strings.TrimSpace(value[:index])
		}
	}

	if 2 <= len(value) && (('"' == value[0] && '"' == value[len(value)-1]) ||
		('\'' == value[0] && '\'' == value[len(value)-1])) {
		inner := value[1 : len(value)-1]
		if '"' == value[0] {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
		}
		return inner
	}

	switch strings.ToLower(value) {
	case "true", "on", "yes":
		return true
	case "false", "off", "no", "none", "null":
		return false
	}

	// Numeric values stay strings here: Config.Set() casts them like PHP does.
	return value
}

// firstRune is the (byte) first character of value, or 0.
func firstRune(value string) rune {
	if "" == value {
		return 0
	}
	return rune(value[0])
}
