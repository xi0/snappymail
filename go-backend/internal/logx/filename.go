package logx

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Params holds the values substituted into log filenames and formats.
//
// It is the Go counterpart of the $aAdditionalParams array plus the values
// Actions::compileLogParams() looks up itself (request IP, account details).
// Empty fields behave like "no value available": the placeholder is then
// replaced by the generic word PHP leaves behind ("unknown", "imap", ...).
type Params struct {
	// Location is the zone used by {date:...}. Defaults to UTC, the value of
	// logs.time_zone in application.ini.
	Location *time.Location
	// Now overrides the current time, used by tests.
	Now time.Time

	// ClientIP is {request:ip} / {user:ip}.
	ClientIP string

	// RequestDomain is {request:domain}, RequestDomainClear is
	// {request:domain-clear} (the domain without a webmail./mail. prefix).
	RequestDomain      string
	RequestDomainClear string

	// Logged in account, used by {user:...}.
	UserEmail       string
	UserLogin       string
	UserDomain      string
	UserDomainClear string
	UserUID         string

	// IMAP/SMTP account details, used by {imap:...} and {smtp:...}.
	ImapLogin string
	ImapHost  string
	ImapPort  int
	SmtpLogin string
	SmtpHost  string
	SmtpPort  int

	// Additional holds extra "placeholder" -> "value" pairs, applied last.
	Additional map[string]string
}

var (
	dateParamRe       = regexp.MustCompile(`\{date:([^}]+)\}`)
	dateClearRe       = regexp.MustCompile(`\{date:([^}]*)\}`)
	imapClearRe       = regexp.MustCompile(`(?i)\{imap:([^}]*)\}`)
	smtpClearRe       = regexp.MustCompile(`(?i)\{smtp:([^}]*)\}`)
	requestClearRe    = regexp.MustCompile(`(?i)\{request:([^}]*)\}`)
	userClearRe       = regexp.MustCompile(`(?i)\{user:([^}]*)\}`)
	labsClearRe       = regexp.MustCompile(`\{labs:([^}]*)\}`)
	labsRandRe        = regexp.MustCompile(`\{labs:rand:([1-9])\}`)
	slashesRe         = regexp.MustCompile(`/+`)
	dotsRe            = regexp.MustCompile(`\.+`)
	fileNameAllowedRe = regexp.MustCompile(`[^a-zA-Z0-9@_+=\-./!()\[\]]`)
)

// CompileParams replaces the placeholders of line, mirroring
// RainLoop\Actions::compileLogParams() including the order in which families
// are substituted and then cleared.
func CompileParams(line string, p Params) string {
	location := p.Location
	if nil == location {
		location = time.UTC
	}

	now := p.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(location)

	if strings.Contains(line, "{date:") {
		line = dateParamRe.ReplaceAllStringFunc(line, func(match string) string {
			// match is "{date:<format>}"
			return FormatDate(match[len("{date:"):len(match)-1], now)
		})
		line = dateClearRe.ReplaceAllString(line, "date")
	}

	if strings.Contains(line, "{imap:") || strings.Contains(line, "{smtp:") {
		if "" != p.ImapLogin || "" != p.ImapHost || "" != p.SmtpLogin || "" != p.SmtpHost {
			line = strings.ReplaceAll(line, "{imap:login}", p.ImapLogin)
			line = strings.ReplaceAll(line, "{smtp:login}", p.SmtpLogin)

			if "" != p.ImapHost {
				line = strings.ReplaceAll(line, "{imap:host}", p.ImapHost)
				line = strings.ReplaceAll(line, "{imap:port}", strconv.Itoa(p.ImapPort))
			}
			if "" != p.SmtpHost {
				line = strings.ReplaceAll(line, "{smtp:host}", p.SmtpHost)
				line = strings.ReplaceAll(line, "{smtp:port}", strconv.Itoa(p.SmtpPort))
			}
		}

		line = imapClearRe.ReplaceAllString(line, "imap")
		line = smtpClearRe.ReplaceAllString(line, "smtp")
	}

	if strings.Contains(line, "{request:") {
		line = strings.ReplaceAll(line, "{request:ip}", p.ClientIP)

		if "" != p.RequestDomain {
			line = strings.ReplaceAll(line, "{request:domain}", p.RequestDomain)
			line = strings.ReplaceAll(line, "{request:domain-clear}", p.RequestDomainClear)
		}

		line = requestClearRe.ReplaceAllString(line, "request")
	}

	if strings.Contains(line, "{user:") {
		line = strings.ReplaceAll(line, "{user:uid}", p.UserUID)
		line = strings.ReplaceAll(line, "{user:ip}", p.ClientIP)

		if "" != p.UserEmail {
			line = strings.ReplaceAll(line, "{user:email}", p.UserEmail)
			line = strings.ReplaceAll(line, "{user:login}", p.UserLogin)
			line = strings.ReplaceAll(line, "{user:domain}", p.UserDomain)
			line = strings.ReplaceAll(line, "{user:domain-clear}", p.UserDomainClear)
		}

		line = userClearRe.ReplaceAllString(line, "unknown")
	}

	if strings.Contains(line, "{labs:") {
		line = labsRandRe.ReplaceAllStringFunc(line, func(match string) string {
			// match is "{labs:rand:<n>}", take <n> from the capture group
			// instead of the trailing byte, which is the closing brace.
			groups := labsRandRe.FindStringSubmatch(match)
			if 2 > len(groups) {
				return match
			}

			digits, err := strconv.Atoi(groups[1])
			if nil != err {
				return match
			}

			return randDigits(digits)
		})
		line = labsClearRe.ReplaceAllString(line, "labs")
	}

	// PHP iterates $aAdditionalParams in insertion order; use a stable order so
	// overlapping placeholders behave deterministically.
	if len(p.Additional) > 0 {
		keys := make([]string, 0, len(p.Additional))
		for key := range p.Additional {
			keys = append(keys, key)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))

		for _, key := range keys {
			line = strings.ReplaceAll(line, key, p.Additional[key])
		}
	}

	return line
}

// CompileFileName sanitises a log filename pattern, mirroring
// RainLoop\Actions::compileLogFileName().
//
// The pattern may contain sub-directories; every character outside
// [a-zA-Z0-9@_+=-./!()[]] is dropped so a client controlled value can never
// escape the log directory. An empty result falls back to
// "snappymail-log.txt", like PHP.
func CompileFileName(name string, p Params) string {
	fileName := strings.TrimSpace(name)

	if len(fileName) > 0 {
		fileName = CompileParams(fileName, p)
		fileName = dotsRe.ReplaceAllString(fileName, ".")
		fileName = slashesRe.ReplaceAllString(fileName, "/")
		fileName = fileNameAllowedRe.ReplaceAllString(fileName, "")
	}

	if len(fileName) == 0 {
		fileName = "snappymail-log.txt"
	}

	return fileName
}

// randDigits returns a random number with exactly digits digits, like
// rand(pow(10, n - 1), pow(10, n) - 1) in PHP.
func randDigits(digits int) string {
	// Keep the range representable: rand.Intn panics on a non-positive span.
	if digits < 1 {
		digits = 1
	} else if digits > 18 {
		digits = 18
	}

	low := 1
	for i := 1; i < digits; i++ {
		low *= 10
	}

	high := low * 10

	return strconv.Itoa(low + rand.Intn(high-low))
}

// FormatDate renders t using PHP's date() format characters.
//
// Only the characters that make sense for log filenames and log lines are
// implemented; unknown characters are copied verbatim and a backslash escapes
// the following character, as in PHP.
func FormatDate(format string, t time.Time) string {
	var out strings.Builder

	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		if '\\' == runes[i] {
			if i+1 < len(runes) {
				i++
				out.WriteRune(runes[i])
			}
			continue
		}

		out.WriteString(dateToken(runes[i], t))
	}

	return out.String()
}

// dateToken renders a single PHP date() character.
func dateToken(char rune, t time.Time) string {
	switch char {
	case 'd':
		return twoDigits(t.Day())
	case 'D':
		return t.Format("Mon")
	case 'j':
		return strconv.Itoa(t.Day())
	case 'l':
		return t.Format("Monday")
	case 'N':
		return strconv.Itoa((int(t.Weekday())+6)%7 + 1)
	case 'S':
		return ordinalSuffix(t.Day())
	case 'w':
		return strconv.Itoa(int(t.Weekday()))
	case 'z':
		return strconv.Itoa(t.YearDay() - 1)
	case 'W':
		_, week := t.ISOWeek()
		return twoDigits(week)
	case 'F':
		return t.Format("January")
	case 'm':
		return twoDigits(int(t.Month()))
	case 'M':
		return t.Format("Jan")
	case 'n':
		return strconv.Itoa(int(t.Month()))
	case 't':
		return strconv.Itoa(daysInMonth(t))
	case 'L':
		if isLeapYear(t.Year()) {
			return "1"
		}
		return "0"
	case 'o':
		year, _ := t.ISOWeek()
		return strconv.Itoa(year)
	case 'Y':
		return strconv.Itoa(t.Year())
	case 'y':
		return twoDigits(t.Year() % 100)
	case 'a':
		return t.Format("pm")
	case 'A':
		return t.Format("PM")
	case 'g':
		return strconv.Itoa(hour12(t))
	case 'G':
		return strconv.Itoa(t.Hour())
	case 'h':
		return twoDigits(hour12(t))
	case 'H':
		return twoDigits(t.Hour())
	case 'i':
		return twoDigits(t.Minute())
	case 's':
		return twoDigits(t.Second())
	case 'u':
		return fmt.Sprintf("%06d", t.Nanosecond()/1_000)
	case 'v':
		return fmt.Sprintf("%03d", t.Nanosecond()/1_000_000)
	case 'e':
		return t.Location().String()
	case 'T':
		return t.Format("MST")
	case 'O':
		return t.Format("-0700")
	case 'P':
		return t.Format("-07:00")
	case 'Z':
		_, offset := t.Zone()
		return strconv.Itoa(offset)
	case 'U':
		return strconv.FormatInt(t.Unix(), 10)
	case 'c':
		return t.Format("2006-01-02T15:04:05-07:00")
	case 'r':
		return t.Format("Mon, 02 Jan 2006 15:04:05 -0700")
	}

	return string(char)
}

// twoDigits is PHP's zero padded numeric output.
func twoDigits(value int) string {
	return fmt.Sprintf("%02d", value)
}

// hour12 converts a 24 hour clock value to PHP's "g" value (1..12).
func hour12(t time.Time) int {
	hour := t.Hour() % 12
	if 0 == hour {
		return 12
	}
	return hour
}

// ordinalSuffix is PHP's "S" (st, nd, rd, th).
func ordinalSuffix(day int) string {
	if day%100 >= 11 && day%100 <= 13 {
		return "th"
	}

	switch day % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}

	return "th"
}

// daysInMonth is PHP's "t".
func daysInMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return first.AddDate(0, 1, -1).Day()
}

// isLeapYear is PHP's "L".
func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}
