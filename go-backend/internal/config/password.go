package config

import "golang.org/x/crypto/bcrypt"

// The admin password of the web admin panel is stored in
// security.admin_password as a PHP password_hash(PASSWORD_DEFAULT) digest, i.e.
// bcrypt with the default cost. Go's bcrypt writes a "$2a$" hash where PHP
// writes "$2y$"; both password_verify()/bcrypt.CompareHashAndPassword accept
// either prefix, so a hash written by one backend authenticates in the other.

// ValidatePassword is Application::ValidatePassword():
//
//	return \strlen($oPassword) && \password_verify($oPassword,
//		$this->Get('security', 'admin_password', ''));
//
// An empty password or an empty/!bcrypt stored value always fails.
func (a *Application) ValidatePassword(password string) bool {
	if "" == password {
		return false
	}

	hash := a.GetString("security", "admin_password", "")
	if "" == hash {
		return false
	}

	return nil == bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// SetPassword is Application::SetPassword(): store
// password_hash($password, PASSWORD_DEFAULT) in security.admin_password.
//
// The hash is written through Set(), so an existing entry keeps the [security]
// section order. A bcrypt failure (an excessively long password) is ignored,
// matching PHP where password_hash() returning false stores an empty value.
func (a *Application) SetPassword(password string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if nil != err {
		a.Set("security", "admin_password", "")
		return
	}
	a.Set("security", "admin_password", string(hash))
}

// DumpExtended is AbstractConfig::jsonSerialize(): the configuration in the
// [value, comment] shape the admin panel edits.
//
// The admin settings form (ActionsAdmin::DoAdminSettingsGet) reads
// $config->jsonSerialize() and then overwrites the comment/option slots of a
// few keys (logs.time_zone, login.sign_me_auto, defaults.view_images). Each
// entry is a two-element list [value, comment]; the form appends a third element
// (the select options) on the client side. The [version] section is dropped by
// the caller.
func (a *Application) DumpExtended() map[string]map[string][]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	dump := make(map[string]map[string][]any, len(a.sections))
	for _, section := range a.sections {
		values := make(map[string][]any, len(section.keys))
		for _, key := range section.keys {
			option := section.values[key]
			// jsonSerialize() returns the raw defaultValues() entry: [value]
			// without a comment, [value, comment] with one.
			entry := []any{option.value}
			if "" != option.comment {
				entry = append(entry, option.comment)
			}
			values[key] = entry
		}
		dump[section.name] = values
	}

	return dump
}
