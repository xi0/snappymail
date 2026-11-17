package logx

import (
	"fmt"
	"log/syslog"
	"os"
	"path/filepath"
	"sync"
)

// FileDriver appends to a log file, like \MailSo\Log\Drivers\File.
// Every write opens the file for appending, exactly as PHP's
// error_log($msg, 3, $file) does, so log rotation by moving/removing the file
// keeps working without restarting the process.
type FileDriver struct {
	baseDriver

	path string
	mu   sync.Mutex
}

// NewFileDriver returns a driver writing to path. Missing parent directories
// are created with mode 0755, matching the PHP constructor; the file itself is
// created with 0600 because it may contain credentials.
func NewFileDriver(path string) (*FileDriver, error) {
	if "" == path {
		return nil, fmt.Errorf("logx: empty log file path")
	}

	if dir := filepath.Dir(path); "" != dir && "." != dir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("logx: create log directory %s: %w", dir, err)
		}
	}

	return &FileDriver{baseDriver: newBaseDriver(), path: path}, nil
}

// Path returns the file the driver writes to.
func (d *FileDriver) Path() string {
	return d.path
}

// Write appends one formatted line.
func (d *FileDriver) Write(desc string, level Level, name string) bool {
	line := d.format(desc, level, name) + "\n"

	d.mu.Lock()
	defer d.mu.Unlock()

	file, err := os.OpenFile(d.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if nil != err {
		return false
	}
	defer file.Close()

	if _, err := file.WriteString(line); nil != err {
		return false
	}

	return true
}

// Clear removes the log file (PHP: Driver::Clear()).
func (d *FileDriver) Clear() bool {
	return nil == os.Remove(d.path)
}

// StderrDriver writes to stderr, like \MailSo\Log\Drivers\StderrStream.
type StderrDriver struct {
	baseDriver

	mu sync.Mutex
}

// NewStderrDriver returns a driver writing to os.Stderr.
func NewStderrDriver() *StderrDriver {
	return &StderrDriver{baseDriver: newBaseDriver()}
}

// Write appends one formatted line to stderr.
func (d *StderrDriver) Write(desc string, level Level, name string) bool {
	line := d.format(desc, level, name) + "\n"

	d.mu.Lock()
	defer d.mu.Unlock()

	_, err := os.Stderr.WriteString(line)
	return nil == err
}

// Clear is a no-op for streams (PHP: Driver::Clear() returns true).
func (d *StderrDriver) Clear() bool {
	return true
}

// SyslogDriver writes to syslog, like \MailSo\Log\Drivers\Syslog.
type SyslogDriver struct {
	baseDriver

	tag string
}

// NewSyslogDriver returns a driver logging with the "snappymail" tag, which is
// what the fail2ban rules ship with.
func NewSyslogDriver() *SyslogDriver {
	return &SyslogDriver{baseDriver: newBaseDriver(), tag: "snappymail"}
}

// SetTag overrides the syslog tag returned by NewSyslogDriver.
func (d *SyslogDriver) SetTag(tag string) {
	if "" != tag {
		d.tag = tag
	}
}

// Write sends one formatted line. Like the PHP driver it opens and closes the
// syslog connection per message, so the facility is never inherited from a
// long-lived handle.
func (d *SyslogDriver) Write(desc string, level Level, name string) bool {
	writer, err := syslog.New(syslog.LOG_USER|syslog.LOG_INFO, d.tag)
	if nil != err {
		return false
	}
	defer writer.Close()

	return nil == writer.Info(d.format(desc, level, name))
}

// Clear is a no-op (PHP: Driver::Clear() returns true).
func (d *SyslogDriver) Clear() bool {
	return true
}
