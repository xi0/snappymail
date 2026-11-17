package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// stringEscaper is \addcslashes($value, '\\"'), the escaping
// AbstractConfig::Save() applies to string values.
var stringEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// Save is RainLoop\Config\Application::Save(): it stamps [version] and writes
// the file with the header, the "; comment" blocks and the values in the
// defaultValues() order.
//
// The output is byte identical to PHP's, including the missing trailing
// newline, so the PHP backend can keep reading the file.
func (a *Application) Save() error {
	a.Set("version", "current", a.version)
	a.Set("version", "saved", a.now().UTC().Format(saveDateFormat))

	a.mu.Lock()
	content := a.render()
	a.mu.Unlock()

	return writeFile(a.file, content)
}

// render builds the file content.
func (a *Application) render() string {
	lines := make([]string, 0, 512)

	for _, section := range a.sections {
		if !isValidIniKey(section.name) {
			continue
		}

		lines = append(lines, "", "["+section.name+"]")
		first := true

		for _, key := range section.keys {
			option, ok := section.lookup(key)
			if !ok || !isValidIniKey(key) {
				continue
			}

			// A comment block is separated from the previous value by a blank
			// line, except at the top of a section.
			if "" != option.comment {
				if !first {
					lines = append(lines, "")
				}
				for _, line := range splitLines(option.comment) {
					lines = append(lines, "; "+line)
				}
			}

			first = false
			lines = append(lines, key+" = "+renderValue(option.value))
		}
	}

	header := ""
	if "" != a.header {
		header = a.header
	}

	return header + "\n" + strings.Join(lines, "\n")
}

// renderValue renders one value the way AbstractConfig::Save() does: On/Off for
// booleans, a bare number for int and float, and a quoted, escaped string.
func renderValue(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "On"
		}
		return "Off"
	case int:
		return strconv.Itoa(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return `"` + stringEscaper.Replace(typed) + `"`
	}
	return `""`
}

// writeFile ports \RainLoop\Utils::saveFile(): it creates the directory with
// 0700, writes the file and restricts it to 0600, because configs/ holds the
// admin password hash and the encrypted account passwords.
func writeFile(name, content string) error {
	directory := filepath.Dir(name)
	if info, err := os.Stat(directory); nil != err || !info.IsDir() {
		if err := os.MkdirAll(directory, 0o700); nil != err {
			return fmt.Errorf("failed to create directory %q: %w", directory, err)
		}
	}

	if err := os.WriteFile(name, []byte(content), 0o600); nil != err {
		return fmt.Errorf("failed to save file %q: %w", name, err)
	}

	// os.WriteFile keeps the mode of an existing file, PHP always chmods.
	if err := os.Chmod(name, 0o600); nil != err {
		return fmt.Errorf("failed to restrict file %q: %w", name, err)
	}

	return nil
}
