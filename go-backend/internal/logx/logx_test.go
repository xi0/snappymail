package logx

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// memDriver records the formatted lines so the prefix shape can be asserted
// without touching the filesystem.
type memDriver struct {
	baseDriver

	mu    sync.Mutex
	lines []string
}

func newMemDriver() *memDriver {
	return &memDriver{baseDriver: newBaseDriver()}
}

func (d *memDriver) Write(desc string, level Level, name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.lines = append(d.lines, d.format(desc, level, name))
	return true
}

func (d *memDriver) last(t *testing.T) string {
	t.Helper()

	d.mu.Lock()
	defer d.mu.Unlock()

	if 0 == len(d.lines) {
		t.Fatal("no log lines recorded")
	}

	return d.lines[len(d.lines)-1]
}

// fixedTime is used by the deterministic formatting tests.
var fixedTime = time.Date(2024, time.January, 2, 3, 4, 5, 6000000, time.UTC)

func fixedClock() func() time.Time {
	return func() time.Time { return fixedTime }
}

var lineShape = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\]\[[0-9a-f]{8}\] \[[A-Z]+\]: `)

func TestDriverLinePrefixShape(t *testing.T) {
	driver := newMemDriver()
	driver.nowFn = fixedClock()

	driver.Write("hello world", Warning, "")

	got := driver.last(t)
	want := "[2024-01-02 03:04:05.006][" + Guid() + "] [WARNING]: hello world"

	if got != want {
		t.Fatalf("line = %q, want %q", got, want)
	}

	if !lineShape.MatchString(got) {
		t.Fatalf("line %q does not match the PHP prefix shape", got)
	}
}

func TestDriverTimeZoneAffectsPrefix(t *testing.T) {
	driver := newMemDriver()
	driver.nowFn = func() time.Time { return fixedTime.In(time.UTC) }
	driver.SetTimeZone(time.FixedZone("CEST", 2*60*60))

	driver.Write("x", Info, "")

	if got := driver.last(t); !strings.HasPrefix(got, "[2024-01-02 05:04:05.006]") {
		t.Fatalf("line = %q, want the +02:00 timestamp", got)
	}
}

func TestDriverPrefixFlags(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*memDriver)
		want    string
	}{
		{
			name:    "all prefixes",
			prepare: func(*memDriver) {},
			want:    "[2024-01-02 03:04:05.006][" + Guid() + "] [WARNING]: msg",
		},
		{
			name:    "typed prefix only (auth log)",
			prepare: func(d *memDriver) { d.DisableTimePrefix(); d.DisableGuidPrefix() },
			want:    "[WARNING]: msg",
		},
		{
			name:    "no typed prefix",
			prepare: func(d *memDriver) { d.DisableTypedPrefix() },
			want:    "[2024-01-02 03:04:05.006][" + Guid() + "] msg",
		},
		{
			name: "driver name in typed prefix",
			prepare: func(d *memDriver) {
				d.SetName("IMAP")
			},
			want: "[2024-01-02 03:04:05.006][" + Guid() + "] IMAP[WARNING]: msg",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver := newMemDriver()
			driver.nowFn = fixedClock()
			test.prepare(driver)

			driver.Write("msg", Warning, "")

			if got := driver.last(t); got != test.want {
				t.Fatalf("line = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDriverWriteNameOverridesDriverName(t *testing.T) {
	driver := newMemDriver()
	driver.nowFn = fixedClock()
	driver.SetName("IMAP")

	driver.Write("msg", Warning, "SMTP")

	if got := driver.last(t); !strings.Contains(got, "SMTP[WARNING]: msg") {
		t.Fatalf("line = %q, want the SMTP name", got)
	}
}

func TestLevelFiltering(t *testing.T) {
	driver := newMemDriver()
	driver.nowFn = fixedClock()

	logger := New(Warning).Append(driver)

	if !logger.Debugf("debug message") {
		t.Fatal("filtered messages return true")
	}
	if 0 != len(driver.lines) {
		t.Fatalf("debug message was written at level Warning: %v", driver.lines)
	}
	if logger.Used() {
		t.Fatal("logger must not be marked used when everything was filtered")
	}

	logger.Warningf("warning message")
	logger.Errorf("error message")
	logger.Infof("info message")

	if 2 != len(driver.lines) {
		t.Fatalf("lines = %v, want warning and error only", driver.lines)
	}
	if !logger.Used() {
		t.Fatal("logger must be marked used after writing")
	}
	if !strings.Contains(driver.lines[0], "[WARNING]: warning message") {
		t.Fatalf("first line = %q", driver.lines[0])
	}
	if !strings.Contains(driver.lines[1], "[ERROR]: error message") {
		t.Fatalf("second line = %q", driver.lines[1])
	}
}

func TestLevelNumbersMatchPHP(t *testing.T) {
	// These numbers are the LOG_* constants used by logs.level.
	sizes := map[Level]int{
		Emergency: 0,
		Alert:     1,
		Critical:  2,
		Error:     3,
		Warning:   4,
		Notice:    5,
		Info:      6,
		Debug:     7,
	}

	for level, want := range sizes {
		if int(level) != want {
			t.Fatalf("level %s = %d, want %d", level.Name(), int(level), want)
		}
	}

	if "WARNING" != Warning.Name() || "DEBUG" != Debug.Name() {
		t.Fatalf("unexpected level names: %q, %q", Warning.Name(), Debug.Name())
	}
	if Level(8).Valid() {
		t.Fatal("level 8 must be invalid")
	}
}

func TestSecretMasking(t *testing.T) {
	driver := newMemDriver()
	driver.nowFn = fixedClock()

	logger := New(Debug).Append(driver)
	logger.AddSecret("hunter2")
	logger.AddSecret("hunter2")
	logger.AddSecret("hunter2extra")

	logger.Infof("login hunter2extra with hunter2")

	line := driver.last(t)
	if strings.Contains(line, "hunter2") {
		t.Fatalf("secret leaked: %q", line)
	}
	// Longer secrets are masked first, so the long one is not partially masked.
	if !strings.Contains(line, "login "+Mask+" with "+Mask) {
		t.Fatalf("line = %q, want both secrets masked", line)
	}

	logger.SetShowSecrets(true)
	logger.Infof("login hunter2")

	if line := driver.last(t); !strings.Contains(line, "login hunter2") {
		t.Fatalf("line = %q, want the unmasked secret", line)
	}
}

func TestAddSecretIgnoresEmptyAndDuplicates(t *testing.T) {
	logger := New(Debug)

	logger.AddSecret("   ")
	logger.AddSecret("")
	logger.AddSecret("token")
	logger.AddSecret("token")

	if 1 != len(logger.secrets) {
		t.Fatalf("secrets = %v, want a single entry", logger.secrets)
	}
}

func TestFileDriverWritesAndClears(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "sub", "log-2024-01-02.txt")

	driver, err := NewFileDriver(path)
	if nil != err {
		t.Fatalf("NewFileDriver: %v", err)
	}
	driver.nowFn = fixedClock()

	if !driver.Write("first", Info, "") {
		t.Fatal("Write returned false")
	}
	if !driver.Write("second", Error, "") {
		t.Fatal("Write returned false")
	}

	content, err := os.ReadFile(path)
	if nil != err {
		t.Fatalf("read log: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if 2 != len(lines) {
		t.Fatalf("lines = %v, want 2", lines)
	}
	if !strings.HasSuffix(lines[0], "[INFO]: first") {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "[ERROR]: second") {
		t.Fatalf("second line = %q", lines[1])
	}

	if !driver.Clear() {
		t.Fatal("Clear returned false")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("log file still exists: %v", err)
	}
}

func TestNewFileDriverRejectsEmptyPath(t *testing.T) {
	if _, err := NewFileDriver(""); nil == err {
		t.Fatal("expected an error for an empty path")
	}
}

func TestCompileFileName(t *testing.T) {
	params := Params{
		Location:        time.UTC,
		Now:             fixedTime,
		ClientIP:        "203.0.113.7",
		UserEmail:       "user@example.com",
		UserLogin:       "user",
		UserDomain:      "example.com",
		UserDomainClear: "example.com",
		UserUID:         "1abc23",
	}

	tests := []struct {
		in   string
		want string
	}{
		{"log-{date:Y-m-d}.txt", "log-2024-01-02.txt"},
		{"{date:Y-m-d}/{user:domain}/{user:email}_{user:uid}.log", "2024-01-02/example.com/user@example.com_1abc23.log"},
		{"syslog", "syslog"},
		{"stderr", "stderr"},
		{"log-{date:Y-m-d H:i:s}.txt", "log-2024-01-02030405.txt"},
		{"my log:file?.txt", "mylogfile.txt"},
		{"a//b..c.txt", "a/b.c.txt"},
		{"{user:ip}.txt", "203.0.113.7.txt"},
		{"", "snappymail-log.txt"},
		{"   ", "snappymail-log.txt"},
		// Unknown families are not replaced and the braces are stripped.
		{"{unknown:token}.txt", "unknowntoken.txt"},
	}

	for _, test := range tests {
		if got := CompileFileName(test.in, params); got != test.want {
			t.Errorf("CompileFileName(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestCompileFileNameFallsBackForUnusablePattern(t *testing.T) {
	// Everything is stripped, so PHP falls back to snappymail-log.txt.
	if got := CompileFileName("{date:Y-m-d}", Params{Location: time.UTC, Now: fixedTime}); got != "2024-01-02" {
		t.Fatalf("CompileFileName = %q", got)
	}

	if got := CompileFileName("|<>", Params{}); got != "snappymail-log.txt" {
		t.Fatalf("CompileFileName = %q, want the fallback", got)
	}
}

func TestCompileFileNameWithoutAccount(t *testing.T) {
	params := Params{Location: time.UTC, Now: fixedTime}

	// PHP replaces unknown families with the family name.
	if got := CompileFileName("{imap:login}.log", params); got != "imap.log" {
		t.Fatalf("CompileFileName = %q, want imap.log", got)
	}
	if got := CompileFileName("{smtp:host}.log", params); got != "smtp.log" {
		t.Fatalf("CompileFileName = %q, want smtp.log", got)
	}
	if got := CompileFileName("{user:email}.log", params); got != "unknown.log" {
		t.Fatalf("CompileFileName = %q, want unknown.log", got)
	}
	if got := CompileFileName("{request:domain}.log", params); got != "request.log" {
		t.Fatalf("CompileFileName = %q, want request.log", got)
	}
}

func TestCompileParamsAuthFormat(t *testing.T) {
	params := Params{
		Location:  time.UTC,
		Now:       fixedTime,
		ClientIP:  "203.0.113.7",
		ImapLogin: "user@example.com",
		ImapHost:  "mail.example.com",
		ImapPort:  993,
	}

	format := "[{date:Y-m-d H:i:s}] Auth failed: ip={request:ip} user={imap:login} host={imap:host} port={imap:port}"
	want := "[2024-01-02 03:04:05] Auth failed: ip=203.0.113.7 user=user@example.com host=mail.example.com port=993"

	if got := CompileParams(format, params); got != want {
		t.Fatalf("CompileParams =\n%q\nwant\n%q", got, want)
	}
}

func TestCompileParamsRequestAndUserFamilies(t *testing.T) {
	params := Params{
		Location:           time.UTC,
		Now:                fixedTime,
		ClientIP:           "203.0.113.7",
		RequestDomain:      "webmail.example.com",
		RequestDomainClear: "example.com",
		UserEmail:          "user@example.com",
		UserLogin:          "user",
		UserDomain:         "example.com",
		UserDomainClear:    "example.com",
		UserUID:            "1abc23",
	}

	got := CompileParams("{request:domain} {request:domain-clear} {user:email} {user:login} {user:domain} {user:domain-clear} {user:uid} {user:ip}", params)
	want := "webmail.example.com example.com user@example.com user example.com example.com 1abc23 203.0.113.7"

	if got != want {
		t.Fatalf("CompileParams = %q, want %q", got, want)
	}
}

func TestCompileParamsLabsRand(t *testing.T) {
	got := CompileParams("{labs:rand:4}", Params{})

	if matched, err := regexp.MatchString(`^\d{4}$`, got); nil != err || !matched {
		t.Fatalf("labs rand = %q, want four digits", got)
	}

	if got := CompileParams("{labs:anything}", Params{}); got != "labs" {
		t.Fatalf("labs fallback = %q, want labs", got)
	}
}

func TestCompileParamsAdditional(t *testing.T) {
	params := Params{Additional: map[string]string{
		"{imap:login}": "override@example.com",
		"{custom}":     "value",
	}}

	if got := CompileParams("{custom}", params); got != "value" {
		t.Fatalf("CompileParams = %q, want value", got)
	}
	// Additional params are applied after the family substitution.
	if got := CompileParams("{imap:login}", params); got != "imap" {
		t.Fatalf("CompileParams = %q, want imap", got)
	}
}

func TestFormatDate(t *testing.T) {
	// 2024-01-02 03:04:05.006 UTC is a Tuesday, day 2 of the year.
	stamp := time.Date(2024, time.January, 2, 3, 4, 5, 6*int(time.Millisecond), time.UTC)

	tests := []struct {
		format string
		want   string
	}{
		{"Y-m-d", "2024-01-02"},
		{"H:i:s", "03:04:05"},
		{"Y-m-d H:i:s.u", "2024-01-02 03:04:05.006000"},
		{"Y-m-d H:i:s.v", "2024-01-02 03:04:05.006"},
		{"D, j M Y", "Tue, 2 Jan 2024"},
		{"l jS F", "Tuesday 2nd January"},
		{"N w z", "2 2 1"},
		{"L", "1"},
		{"y n j g G", "24 1 2 3 3"},
		{"a A", "am AM"},
		{"O P T e", "+0000 +00:00 UTC UTC"},
		{"U", "1704164645"},
		{"\\Y", "Y"},
		{"YmdHis", "20240102030405"},
		{"o-W", "2024-01"},
	}

	for _, test := range tests {
		if got := FormatDate(test.format, stamp); got != test.want {
			t.Errorf("FormatDate(%q) = %q, want %q", test.format, got, test.want)
		}
	}
}

func TestFormatDateLeapYearAndMonthLength(t *testing.T) {
	february := time.Date(2023, time.February, 10, 0, 0, 0, 0, time.UTC)

	if got := FormatDate("t", february); got != "28" {
		t.Fatalf("days in February 2023 = %q, want 28", got)
	}
	if got := FormatDate("L", february); got != "0" {
		t.Fatalf("leap year flag = %q, want 0", got)
	}

	century := time.Date(1900, time.February, 10, 0, 0, 0, 0, time.UTC)
	if got := FormatDate("t", century); got != "28" {
		t.Fatalf("days in February 1900 = %q, want 28", got)
	}

	leap := time.Date(2000, time.February, 10, 0, 0, 0, 0, time.UTC)
	if got := FormatDate("t L", leap); got != "29 1" {
		t.Fatalf("February 2000 = %q, want 29 1", got)
	}
}

func TestGuidShape(t *testing.T) {
	guid := Guid()

	if 8 != len(guid) {
		t.Fatalf("Guid = %q, want 8 characters", guid)
	}
	if matched, err := regexp.MatchString(`^[0-9a-f]{8}$`, guid); nil != err || !matched {
		t.Fatalf("Guid = %q, want 8 hex characters", guid)
	}
	if group := Guid(); group != guid {
		t.Fatalf("Guid is not stable: %q != %q", group, guid)
	}
}
