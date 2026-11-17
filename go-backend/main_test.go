package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// phpLineShape is the prefix layout of \MailSo\Log\Driver::Write():
// [2024-01-02 03:04:05.006][deadbeef] [INFO]: message
var phpLineShape = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\]\[[0-9a-f]{8}\] \[INFO\]: `)

func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil)
	if nil != err {
		t.Fatalf("parseFlags: %v", err)
	}

	if defaultPort != opts.port {
		t.Errorf("port = %d, want %d", opts.port, defaultPort)
	}
	if "0.0.0" != opts.version {
		t.Errorf("version = %q, want 0.0.0", opts.version)
	}
	if "log-{date:Y-m-d}.txt" != opts.logFile {
		t.Errorf("log file = %q, want the PHP default pattern", opts.logFile)
	}
	if int(logx.Warning) != opts.logLevel {
		t.Errorf("log level = %d, want %d (the logs.level default)", opts.logLevel, int(logx.Warning))
	}
	if "UTC" != opts.logTimeZone {
		t.Errorf("log time zone = %q, want UTC", opts.logTimeZone)
	}
}

func TestParseFlagsValues(t *testing.T) {
	opts, err := parseFlags([]string{
		"-port", "8080",
		"-root", "/srv/snappymail",
		"-data", "/srv/data",
		"-version", "2.8.2",
		"-log-level", "7",
		"-log-file", "stderr",
		"-log-time-zone", "Europe/Amsterdam",
	})
	if nil != err {
		t.Fatalf("parseFlags: %v", err)
	}

	if 8080 != opts.port || "/srv/snappymail" != opts.root || "/srv/data" != opts.data {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if "2.8.2" != opts.version || 7 != opts.logLevel || "stderr" != opts.logFile {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if "Europe/Amsterdam" != opts.logTimeZone {
		t.Fatalf("log time zone = %q", opts.logTimeZone)
	}
}

func TestParseFlagsRejects(t *testing.T) {
	invalid := [][]string{
		{"-port", "0"},
		{"-port", "65536"},
		{"-log-level", "8"},
		{"-log-level", "-1"},
		{"-version", "   "},
		{"unexpected-positional"},
	}

	for _, args := range invalid {
		if _, err := parseFlags(args); nil == err {
			t.Errorf("parseFlags(%v) accepted an invalid value", args)
		}
	}
}

func TestResolveInstallTree(t *testing.T) {
	root := t.TempDir()
	separator := string(filepath.Separator)

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	if got, want := tree.indexRoot, root+separator; got != want {
		t.Errorf("indexRoot = %q, want %q", got, want)
	}
	if got, want := tree.versionRoot, filepath.Join(root, "snappymail", "v", "0.0.0")+separator; got != want {
		t.Errorf("versionRoot = %q, want %q", got, want)
	}
	if got, want := tree.dataFolder, filepath.Join(root, "data")+separator; got != want {
		t.Errorf("dataFolder = %q, want %q", got, want)
	}
	if got, want := tree.privateData, filepath.Join(root, "data", "_data_", "_default_")+separator; got != want {
		t.Errorf("privateData = %q, want %q", got, want)
	}
	if "_default_" != tree.privateName {
		t.Errorf("privateName = %q, want _default_", tree.privateName)
	}
	if "0.0.0" != tree.version {
		t.Errorf("version = %q, want 0.0.0", tree.version)
	}
}

func TestResolveInstallTreeCustomDataFolder(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "elsewhere")
	separator := string(filepath.Separator)

	tree, err := resolveInstallTree(options{root: root, data: data, version: "1.2.3"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	if got, want := tree.dataFolder, data+separator; got != want {
		t.Errorf("dataFolder = %q, want %q", got, want)
	}
	if got, want := tree.versionRoot,
		filepath.Join(root, "snappymail", "v", "1.2.3")+separator; got != want {
		t.Errorf("versionRoot = %q, want %q", got, want)
	}
}

func TestNewLoggerWritesPHPShapeLine(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	opts := options{
		logLevel:    int(logx.Info),
		logFile:     "log-{date:Y-m-d}.txt",
		logTimeZone: "UTC",
	}

	logger, err := newLogger(opts, tree)
	if nil != err {
		t.Fatalf("newLogger: %v", err)
	}

	logger.Infof("hello %s", "world")

	path := filepath.Join(
		root, "data", "_data_", "_default_", "logs",
		"log-"+logx.FormatDate("Y-m-d", time.Now().UTC())+".txt",
	)

	content, err := os.ReadFile(path)
	if nil != err {
		t.Fatalf("read log file %s: %v", path, err)
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if 1 != len(lines) {
		t.Fatalf("lines = %v, want one line", lines)
	}
	if !phpLineShape.MatchString(lines[0]) {
		t.Fatalf("line %q does not have the PHP prefix shape", lines[0])
	}
	if !strings.HasSuffix(lines[0], "[INFO]: hello world") {
		t.Fatalf("line = %q", lines[0])
	}
}

func TestNewLoggerHonoursLogLevel(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	logger, err := newLogger(options{
		logLevel:    int(logx.Warning),
		logFile:     "log-{date:Y-m-d}.txt",
		logTimeZone: "UTC",
	}, tree)
	if nil != err {
		t.Fatalf("newLogger: %v", err)
	}

	logger.Debugf("filtered")
	if logger.Used() {
		t.Fatal("debug message must be filtered out at level Warning")
	}
}

func TestNewLoggerSupportsStderrAndSyslogTargets(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	for _, target := range []string{"stderr", "syslog"} {
		logger, err := newLogger(options{
			logLevel:    int(logx.Info),
			logFile:     target,
			logTimeZone: "UTC",
		}, tree)
		if nil != err {
			t.Fatalf("newLogger(%q): %v", target, err)
		}
		if !logger.Enabled() {
			t.Fatalf("newLogger(%q) has no driver", target)
		}
	}
}

func TestNewLoggerRejectsBadTimeZone(t *testing.T) {
	tree, err := resolveInstallTree(options{root: t.TempDir(), version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	if _, err := newLogger(options{logLevel: int(logx.Info), logFile: "stderr", logTimeZone: "Mars/Olympus"}, tree); nil == err {
		t.Fatal("expected an error for an unknown time zone")
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1024B"},
		{1536, "1.5KB"},
		{1342177, "1.28MB"},
	}

	for _, test := range tests {
		if got := formatFileSize(test.size); got != test.want {
			t.Errorf("formatFileSize(%d) = %q, want %q", test.size, got, test.want)
		}
	}
}
