package logx

import (
	"fmt"
	"strings"
	"time"
)

// Driver is a log sink, mirroring \MailSo\Log\Driver.
type Driver interface {
	// Write formats and stores one message. name overrides the driver's own
	// name in the typed prefix (PHP: the $sName argument of Write()).
	Write(desc string, level Level, name string) bool
}

// prefixes is \MailSo\Log\Driver::PREFIXES, including the brackets.
var prefixes = map[Level]string{
	Emergency: "[EMERGENCY]",
	Alert:     "[ALERT]",
	Critical:  "[CRITICAL]",
	Error:     "[ERROR]",
	Warning:   "[WARNING]",
	Notice:    "[NOTICE]",
	Info:      "[INFO]",
	Debug:     "[DEBUG]",
}

// ltrimCutset is PHP's default ltrim() character list.
const ltrimCutset = " \t\r\n\x00\x0B"

// baseDriver carries the prefix configuration shared by all drivers.
type baseDriver struct {
	location    *time.Location
	name        string
	timePrefix  bool
	guidPrefix  bool
	typedPrefix bool
	// nowFn overrides the clock in tests.
	nowFn func() time.Time
}

// newBaseDriver returns the defaults of \MailSo\Log\Driver: UTC, time prefix,
// guid prefix and typed prefix all enabled.
func newBaseDriver() baseDriver {
	return baseDriver{
		location:    time.UTC,
		timePrefix:  true,
		guidPrefix:  true,
		typedPrefix: true,
	}
}

// SetTimeZone sets the zone used for the time prefix (logs.time_zone).
func (d *baseDriver) SetTimeZone(loc *time.Location) {
	if nil != loc {
		d.location = loc
	}
}

// Location returns the zone used for the time prefix.
func (d *baseDriver) Location() *time.Location {
	return d.location
}

// SetName sets the name that precedes the typed prefix.
func (d *baseDriver) SetName(name string) {
	d.name = name
}

// DisableTimePrefix drops the leading "[Y-m-d H:i:s.mmm]" (auth log).
func (d *baseDriver) DisableTimePrefix() {
	d.timePrefix = false
}

// DisableGuidPrefix drops the "[guid]" part (auth log).
func (d *baseDriver) DisableGuidPrefix() {
	d.guidPrefix = false
}

// DisableTypedPrefix drops the "[WARNING]: " part (auth log).
func (d *baseDriver) DisableTypedPrefix() {
	d.typedPrefix = false
}

// now returns the current time in the driver's zone.
func (d *baseDriver) now() time.Time {
	if nil != d.nowFn {
		return d.nowFn().In(d.location)
	}
	return time.Now().In(d.location)
}

// format assembles the line like \MailSo\Log\Driver::Write():
//
//	[2024-01-02 03:04:05.006][deadbeef] [WARNING]: message
//
// The typed prefix carries its own leading space; when it is dropped (the auth
// log) the remaining prefixes still keep a single space before the message.
func (d *baseDriver) format(desc string, level Level, name string) string {
	var prefix strings.Builder

	if d.timePrefix {
		prefix.WriteString("[" + timeWithMilliseconds(d.now()) + "]")
	}
	if d.guidPrefix {
		prefix.WriteString("[" + Guid() + "]")
	}
	if d.typedPrefix {
		prefix.WriteString(" " + typedPrefix(level, name, d.name))
	} else if 0 < prefix.Len() {
		prefix.WriteString(" ")
	}

	return strings.TrimLeft(prefix.String(), ltrimCutset) + desc
}

// typedPrefix is \MailSo\Log\Driver::getTypedPrefix().
func typedPrefix(level Level, name, driverName string) string {
	if "" == name {
		name = driverName
	}

	prefix, ok := prefixes[level]
	if !ok {
		return ""
	}

	return name + prefix + ": "
}

// timeWithMilliseconds is \MailSo\Log\Driver::getTimeWithMicroSec(), which
// formats 'Y-m-d H:i:s.u' and then cuts the last three digits, leaving
// milliseconds.
func timeWithMilliseconds(t time.Time) string {
	return t.Format("2006-01-02 15:04:05") + fmt.Sprintf(".%03d", t.Nanosecond()/1_000_000)
}
