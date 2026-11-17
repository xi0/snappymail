package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RetryAfter is the Retry-After header value PHP sends with the 503 of the
// security.max_sys_getloadavg guard.
const RetryAfter = 120

// ServerBusy is the security.max_sys_getloadavg guard of
// RainLoop\Config\Application::Load(): PHP answers "503 Service Unavailable"
// with "Retry-After: 120" and this message, then exits.
type ServerBusy struct {
	// Load is the 1 minute load average that tripped the guard.
	Load float64
	// Message is the exit text ("Mailserver too busy (1.5). Please try again later.").
	Message string
}

// Error implements error.
func (e *ServerBusy) Error() string { return e.Message }

// Load is RainLoop\Config\Application::Load(): parse the file, run the load
// average guard, prepare the environment replacement, validate the encryption
// cipher and the log time zone.
//
// A missing, unreadable, empty or unparsable file is replaced by the defaults,
// exactly like AbstractConfig::Load() falling through to Save().
func (a *Application) Load() error {
	if err := a.loadFile(); nil != err {
		return err
	}

	if err := a.checkLoadAverage(); nil != err {
		return err
	}

	a.prepareReplaceEnv()
	a.checkCipher()
	a.checkTimeZone()

	return nil
}

// loadFile parses the file and applies every entry through Set(), which runs the
// legacy key rewrites and the type casts.
//
// Two AbstractConfig::Load() branches are deliberately absent:
//
//   - the ".json instead of .ini" variant, which only the domain and plugin
//     providers use;
//   - the additional configuration file (APP_CONFIGURATION_NAME), which is
//     unreachable in PHP because the constructor tests file_exists() on the
//     still empty property instead of the candidate name.
func (a *Application) loadFile() error {
	content, err := os.ReadFile(a.file)
	if nil != err {
		// AbstractConfig::Load(): !file_exists || !is_readable → Save().
		return a.Save()
	}

	document, err := parseINI(string(content))
	if nil != err {
		// parse_ini_file() returns false on a syntax error and Load() falls
		// through to Save(), overwriting the broken file with the defaults.
		a.logf("INI", fmt.Sprintf("%s: %v", a.file, err))
		return a.Save()
	}

	if 0 == document.topLevel {
		// An empty or comment only file is falsy in PHP as well.
		return a.Save()
	}

	for _, entry := range document.entries {
		a.Set(entry.section, entry.key, entry.value)
	}

	return nil
}

// checkLoadAverage ports the security.max_sys_getloadavg guard.
func (a *Application) checkLoadAverage() error {
	maximum := a.GetFloat("security", "max_sys_getloadavg", 0)
	if 0 == maximum || nil == a.LoadAverage {
		return nil
	}

	load, ok := a.LoadAverage()
	if !ok || load <= maximum {
		return nil
	}

	return &ServerBusy{
		Load: load,
		// PHP: "Mailserver too busy ({$load[0]}). Please try again later."
		Message: fmt.Sprintf("Mailserver too busy (%s). Please try again later.",
			strconv.FormatFloat(load, 'f', -1, 64)),
	}
}

// prepareReplaceEnv collects the lowercased "section.key" names of
// labs.replace_env_in_configuration. It requires a non empty environment, like
// the isset($_ENV)/isset($_SERVER) check in PHP.
func (a *Application) prepareReplaceEnv() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.replaceEnv = nil

	if 0 == len(a.Env) {
		return
	}

	names, ok := a.lookup("labs", "replace_env_in_configuration")
	if !ok {
		return
	}

	list, ok := names.(string)
	if !ok || "" == list {
		return
	}

	for _, name := range strings.Split(list, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); "" != name {
			a.replaceEnv = append(a.replaceEnv, name)
		}
	}
}

// checkCipher ports the security.encrypt_cipher validation: an unsupported
// cipher is replaced by DefaultCipher and the file is rewritten.
//
// PHP picks a random cipher from Crypt::listCiphers() here; Go has a single
// supported default, so the fallback is deterministic (noted in the step
// notes).
func (a *Application) checkCipher() {
	cipher := a.GetString("security", "encrypt_cipher", "")
	if "" != cipher && a.cipherSupported(cipher) {
		return
	}

	if "" != cipher {
		a.logf("Crypt", "OpenSSL no support for cipher '"+cipher+"'")
	}

	if cipher == DefaultCipher {
		return
	}

	a.Set("security", "encrypt_cipher", DefaultCipher)

	if err := a.Save(); nil != err {
		a.logf("Config", err.Error())
	}
}

// checkTimeZone resets an unknown logs.time_zone to UTC. Like PHP, the fallback
// is not saved: it is the default value anyway.
func (a *Application) checkTimeZone() {
	zone := a.GetString("logs", "time_zone", "")
	if _, err := time.LoadLocation(zone); nil != err {
		a.Set("logs", "time_zone", "UTC")
	}
}

// SystemLoadAverage reports the 1 minute load average, the Go equivalent of
// PHP's sys_getloadavg()[0]. Only Linux exposes it as a file; elsewhere the
// second result is false and the guard is skipped, exactly like PHP without the
// sys_getloadavg() function.
func SystemLoadAverage() (float64, bool) {
	if "linux" != runtime.GOOS {
		return 0, false
	}

	content, err := os.ReadFile("/proc/loadavg")
	if nil != err {
		return 0, false
	}

	fields := strings.Fields(string(content))
	if 0 == len(fields) {
		return 0, false
	}

	load, err := strconv.ParseFloat(fields[0], 64)
	if nil != err {
		return 0, false
	}

	return load, true
}
