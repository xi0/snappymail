package storage

import "encoding/json"

// The file names of RainLoop\Providers\Settings\DefaultSettings.
const (
	// SettingsFileName is DefaultSettings::FILE_NAME.
	SettingsFileName = "settings"
	// SettingsFileNameLocal is DefaultSettings::FILE_NAME_LOCAL.
	SettingsFileNameLocal = "settings_local"
)

// Settings is RainLoop\Settings: the per-account settings document stored in
// the "settings" or "settings_local" CONFIG entry.
//
// The key order is preserved from the loaded file, because the document is
// written back with json_encode() semantics and the PHP backend reads the same
// file. GetConf/SetConf mirror the PHP accessors, including their acceptance of
// any scalar or nested value.
type Settings struct {
	fields object
}

// NewSettings builds an empty settings document.
func NewSettings() *Settings {
	return &Settings{}
}

// ParseSettings is the json_decode() half of DefaultSettings::Load(): the parsed
// document, or nil when the content is not a JSON object.
func ParseSettings(content string) *Settings {
	if "" == content {
		return nil
	}

	value, err := decodeJSON([]byte(content))
	if nil != err {
		return nil
	}

	fields := objectFrom(value)
	if nil == fields {
		return nil
	}

	return newSettingsFrom(fields)
}

// newSettingsFrom applies the key normalisations of RainLoop\Settings::
// __construct().
//
// The PHP constructor *adds* JunkFolder/DraftsFolder below the existing
// SpamFolder/DraftFolder keys instead of renaming them, and moves a "Language"
// key to "language" at the end of the document; the order is part of the file
// layout, so it is reproduced exactly.
func newSettingsFrom(fields object) *Settings {
	if nil != lookupField(fields, "SpamFolder") && nil == lookupField(fields, "JunkFolder") {
		fields = append(fields, field{"JunkFolder", lookupField(fields, "SpamFolder")})
	}
	if nil != lookupField(fields, "DraftFolder") && nil == lookupField(fields, "DraftsFolder") {
		fields = append(fields, field{"DraftsFolder", lookupField(fields, "DraftFolder")})
	}

	if language := lookupField(fields, "Language"); nil != language && nil == lookupField(fields, "language") {
		// PHP appends "language" at the end and drops the old "Language" key.
		renamed := make(object, 0, len(fields))
		for _, entry := range fields {
			if "Language" != entry.key {
				renamed = append(renamed, entry)
			}
		}
		fields = append(renamed, field{"language", language})
	}

	return &Settings{fields: fields}
}

// GetConf is Settings::GetConf(): the value of name, or defaultValue when the
// key does not exist. A stored null counts as missing, like isset() does.
func (s *Settings) GetConf(name string, defaultValue any) any {
	if nil == s {
		return defaultValue
	}
	if value := lookupField(s.fields, name); nil != value {
		return value
	}
	return defaultValue
}

// SetConf is Settings::SetConf(): an existing key keeps its position, a new key
// is appended.
func (s *Settings) SetConf(name string, value any) {
	for index, entry := range s.fields {
		if entry.key == name {
			s.fields[index].value = value
			return
		}
	}
	s.fields = append(s.fields, field{name, value})
}

// Has reports whether the key exists in the document.
func (s *Settings) Has(name string) bool {
	return nil != s && hasField(s.fields, name)
}

// Keys returns the keys of the document in file order.
func (s *Settings) Keys() []string {
	if nil == s {
		return nil
	}
	keys := make([]string, 0, len(s.fields))
	for _, entry := range s.fields {
		keys = append(keys, entry.key)
	}
	return keys
}

// ToMap is Settings::toArray(): the document as a plain Go map, for the AppData
// payloads of step 10.
func (s *Settings) ToMap() map[string]any {
	if nil == s {
		return map[string]any{}
	}
	result := make(map[string]any, len(s.fields))
	for _, entry := range s.fields {
		result[entry.key] = plainValue(entry.value)
	}
	return result
}

// Encode is json_encode($oSettings): the exact bytes Save() writes.
func (s *Settings) Encode() ([]byte, error) {
	if nil == s {
		return encodeJSON(object{})
	}
	return encodeJSON(s.fields)
}

// String returns the document as the JSON the file holds.
func (s *Settings) String() string {
	encoded, err := s.Encode()
	if nil != err {
		return ""
	}
	return string(encoded)
}

// plainValue converts an ordered object or array into the equivalent Go map or
// slice, so callers can use the standard JSON tooling on it.
func plainValue(value any) any {
	switch typed := value.(type) {
	case object:
		result := make(map[string]any, len(typed))
		for _, entry := range typed {
			result[entry.key] = plainValue(entry.value)
		}
		return result
	case array:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, plainValue(item))
		}
		return result
	case json.Number:
		if number, err := typed.Int64(); nil == err {
			return number
		}
		if number, err := typed.Float64(); nil == err {
			return number
		}
		return typed.String()
	}
	return value
}

// SettingsStorage is RainLoop\Providers\Settings\DefaultSettings: the "settings"
// file (or "settings_local" for the local storage provider) of an account.
type SettingsStorage struct {
	storage *Provider
}

// NewSettingsStorage builds the provider on top of a storage provider, like
// DefaultSettings' constructor.
func NewSettingsStorage(storage *Provider) *SettingsStorage {
	return &SettingsStorage{storage: storage}
}

// FileName is the CONFIG key the provider reads and writes.
func (s *SettingsStorage) FileName() string {
	if nil != s.storage && s.storage.IsLocal() {
		return SettingsFileNameLocal
	}
	return SettingsFileName
}

// Load is DefaultSettings::Load(): the parsed document, or an empty one when the
// file is missing or does not hold a JSON object.
func (s *SettingsStorage) Load(account Account) *Settings {
	if nil == s.storage {
		return NewSettings()
	}
	content, ok := s.storage.Get(account, TypeConfig, s.FileName())
	if !ok {
		return NewSettings()
	}
	if settings := ParseSettings(content); nil != settings {
		return settings
	}
	return NewSettings()
}

// Save is DefaultSettings::Save().
func (s *SettingsStorage) Save(account Account, settings *Settings) error {
	if nil == s.storage {
		return nil
	}
	encoded, err := settings.Encode()
	if nil != err {
		return err
	}
	return s.storage.Put(account, TypeConfig, s.FileName(), string(encoded))
}

// Delete is DefaultSettings::Delete().
func (s *SettingsStorage) Delete(account Account) bool {
	if nil == s.storage {
		return false
	}
	return s.storage.Clear(account, TypeConfig, s.FileName())
}

// lookupField returns the value of key, or nil when the key does not exist.
func lookupField(fields object, key string) any {
	for _, entry := range fields {
		if entry.key == key {
			return entry.value
		}
	}
	return nil
}

// hasField reports whether the object holds key.
func hasField(fields object, key string) bool {
	for _, entry := range fields {
		if entry.key == key {
			return true
		}
	}
	return false
}
