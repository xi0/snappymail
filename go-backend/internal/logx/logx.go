// Package logx implements the SnappyMail logging contract: RFC 5424 severity
// levels, PHP-identical line prefixes, secret masking and the file, stderr and
// syslog sinks used by \MailSo\Log.
//
// The PHP originals are MailSo/Log/Logger.php and MailSo/Log/Driver.php. Their
// observable output is a compatibility contract (fail2ban parses the auth log,
// operators grep the file log), so the prefix layout, the level numbers and the
// masking behaviour are ported as-is instead of being "improved".
package logx

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Level is an RFC 5424 severity. The numbering matches PHP's LOG_* constants,
// which MailSo and RainLoop\Config\Application::defaultValues() rely on:
// 0 = highest (Emergency) .. 7 = lowest (Debug).
type Level int

// Severity levels, numbered like PHP's LOG_* constants.
const (
	Emergency Level = 0 // LOG_EMERG
	Alert     Level = 1 // LOG_ALERT
	Critical  Level = 2 // LOG_CRIT
	Error     Level = 3 // LOG_ERR
	Warning   Level = 4 // LOG_WARNING
	Notice    Level = 5 // LOG_NOTICE
	Info      Level = 6 // LOG_INFO
	Debug     Level = 7 // LOG_DEBUG
)

// Valid reports whether l is a known severity (0..7).
func (l Level) Valid() bool {
	return l >= Emergency && l <= Debug
}

// Name returns the MailSo name of the level, e.g. "WARNING".
func (l Level) Name() string {
	if p, ok := prefixes[l]; ok {
		return strings.Trim(p, "[]")
	}
	return ""
}

// Mask is the replacement used by AddSecret, exactly like \MailSo\Log\Logger.
const Mask = "*******"

// Logger dispatches messages to a set of drivers, mirroring
// \MailSo\Log\Logger. The zero value is not usable, call New.
type Logger struct {
	mu          sync.Mutex
	drivers     []Driver
	level       Level
	secrets     []string
	showSecrets bool
	used        bool
}

// New returns a logger that drops everything below level, like
// (new \MailSo\Log\Logger())->SetLevel(level).
func New(level Level) *Logger {
	return &Logger{level: level}
}

// SetLevel sets the threshold; messages below it are dropped.
func (l *Logger) SetLevel(level Level) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
	return l
}

// Level returns the current threshold.
func (l *Logger) Level() Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetShowSecrets disables secret masking (PHP: SetShowSecrets).
func (l *Logger) SetShowSecrets(show bool) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.showSecrets = show
	return l
}

// ShowSecrets reports whether secrets are written unmasked.
func (l *Logger) ShowSecrets() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.showSecrets
}

// Append adds a driver. PHP's Logger only ever keeps the last appended driver,
// appending here is additive because a single message may need to reach both a
// file and syslog.
func (l *Logger) Append(d Driver) *Logger {
	if d == nil {
		return l
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.drivers = append(l.drivers, d)
	return l
}

// Enabled reports whether any driver is attached (PHP: IsEnabled).
func (l *Logger) Enabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.drivers) > 0
}

// Used reports whether at least one message passed the level filter. It feeds
// the shutdown summary, like \MailSo\Log\Logger::$bUsed.
func (l *Logger) Used() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

// AddSecret registers a word that is replaced by Mask in every message.
// Longer words are replaced first so overlapping secrets are fully masked,
// matching \MailSo\Log\Logger::AddSecret().
func (l *Logger) AddSecret(word string) {
	word = strings.TrimSpace(word)
	if "" == word {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	for _, known := range l.secrets {
		if known == word {
			return
		}
	}

	l.secrets = append(l.secrets, word)
	sort.SliceStable(l.secrets, func(i, j int) bool {
		return len(l.secrets[i]) > len(l.secrets[j])
	})
}

// Write emits desc at level. It returns true when the message was filtered out
// or handed to the drivers, mirroring \MailSo\Log\Logger::Write().
func (l *Logger) Write(desc string, level Level, name string) bool {
	if l.Level() < level {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.showSecrets {
		for _, secret := range l.secrets {
			desc = strings.ReplaceAll(desc, secret, Mask)
		}
	}

	l.used = true

	result := true
	for _, driver := range l.drivers {
		result = driver.Write(desc, level, name)
	}

	return result
}

// Writef is Write with printf-style formatting.
func (l *Logger) Writef(level Level, name, format string, args ...any) bool {
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	return l.Write(format, level, name)
}

// Emergencyf logs at level 0.
func (l *Logger) Emergencyf(format string, args ...any) bool {
	return l.Writef(Emergency, "", format, args...)
}

// Alertf logs at level 1.
func (l *Logger) Alertf(format string, args ...any) bool {
	return l.Writef(Alert, "", format, args...)
}

// Criticalf logs at level 2.
func (l *Logger) Criticalf(format string, args ...any) bool {
	return l.Writef(Critical, "", format, args...)
}

// Errorf logs at level 3.
func (l *Logger) Errorf(format string, args ...any) bool {
	return l.Writef(Error, "", format, args...)
}

// Warningf logs at level 4.
func (l *Logger) Warningf(format string, args ...any) bool {
	return l.Writef(Warning, "", format, args...)
}

// Noticef logs at level 5.
func (l *Logger) Noticef(format string, args ...any) bool {
	return l.Writef(Notice, "", format, args...)
}

// Infof logs at level 6.
func (l *Logger) Infof(format string, args ...any) bool {
	return l.Writef(Info, "", format, args...)
}

// Debugf logs at level 7.
func (l *Logger) Debugf(format string, args ...any) bool {
	return l.Writef(Debug, "", format, args...)
}

// Error logs a plain message at level 3.
func (l *Logger) Error(msg string) bool { return l.Write(msg, Error, "") }

// Warning logs a plain message at level 4.
func (l *Logger) Warning(msg string) bool { return l.Write(msg, Warning, "") }

// Notice logs a plain message at level 5.
func (l *Logger) Notice(msg string) bool { return l.Write(msg, Notice, "") }

// Info logs a plain message at level 6.
func (l *Logger) Info(msg string) bool { return l.Write(msg, Info, "") }

// DebugMessage logs a plain message at level 7.
func (l *Logger) DebugMessage(msg string) bool { return l.Write(msg, Debug, "") }

// WriteException logs an error, mirroring \MailSo\Log\Logger::WriteException().
func (l *Logger) WriteException(err error, level Level) bool {
	if err == nil {
		return true
	}
	return l.Write(err.Error(), level, "")
}

var (
	guidOnce  sync.Once
	guidValue string
)

// Guid returns the 8 character per-process request id that PHP puts in front of
// every log line (Logger::Guid(), the tail of a sha1).
func Guid() string {
	guidOnce.Do(func() {
		var seed [16]byte
		if _, err := rand.Read(seed[:]); err == nil {
			sum := sha1.Sum(seed[:])
			hexed := hex.EncodeToString(sum[:])
			guidValue = hexed[len(hexed)-8:]
		}
		if "" == guidValue {
			guidValue = "00000000"
		}
	})

	return guidValue
}
