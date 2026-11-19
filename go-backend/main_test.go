package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/config"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/web"
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

func TestNewHandlerServesPingAndIndex(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	handler := newHandler(tree, nil, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?/Ping/0/", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("Ping status = %d, want 200", recorder.Code)
	}
	if "Pong" != recorder.Body.String() {
		t.Errorf("Ping body = %q, want Pong", recorder.Body.String())
	}

	// Everything else serves the index stub until step 11.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/whatever", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("index status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "SnappyMail") {
		t.Errorf("index body = %q", recorder.Body.String())
	}
}

func TestNewHandlerServesJSONDispatcher(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	handler := newHandler(tree, nil, nil)

	// The ?/Json entry point of step 9 reaches the dispatcher and answers with
	// the JSON envelope (Action + Result + epoch).
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?/Json/0/Version/", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("Json status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "application/json; charset=utf-8" != got {
		t.Errorf("Json Content-Type = %q", got)
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); nil != err {
		t.Fatalf("Json body is not JSON (%v): %s", err, recorder.Body.String())
	}
	if "Version" != response["Action"] {
		t.Errorf("Action = %v, want Version", response["Action"])
	}
	if _, ok := response["epoch"]; !ok {
		t.Error("the envelope must carry epoch")
	}
}

func TestNewHandlerServesRepoStaticAssets(t *testing.T) {
	// The step-2 acceptance check runs against the real install tree: the
	// minified bundle is served, the sources and private data are not.
	root, err := filepath.Abs("..")
	if nil != err {
		t.Fatalf("resolve repository root: %v", err)
	}

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	handler := newHandler(tree, nil, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/snappymail/v/0.0.0/static/js/min/app.min.js", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("app.min.js status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "application/javascript" != got {
		t.Errorf("app.min.js Content-Type = %q, want application/javascript", got)
	}
	if 0 == recorder.Body.Len() {
		t.Error("app.min.js body is empty")
	}

	unavailable := []string{
		"/snappymail/v/0.0.0/app/libraries/RainLoop/Api.php",
		"/snappymail/v/0.0.0/static/apache.htaccess",
		"/include.php",
		"/data/README.md",
		"/plugins/caldav/index.php",
	}

	for _, target := range unavailable {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))

		if http.StatusNotFound != recorder.Code {
			t.Errorf("GET %s = %d, want 404", target, recorder.Code)
		}
	}
}

func TestLoadApplicationCreatesAndAppliesConfiguration(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	logger, err := newLogger(options{
		logLevel:    int(logx.Warning),
		logFile:     "stderr",
		logTimeZone: "UTC",
	}, tree)
	if nil != err {
		t.Fatalf("newLogger: %v", err)
	}

	// The first start writes configs/application.ini like setup.php.
	application, err := loadApplication(tree, logger)
	if nil != err {
		t.Fatalf("loadApplication: %v", err)
	}

	configFile := filepath.Join(tree.privateData, "configs", "application.ini")
	content, err := os.ReadFile(configFile)
	if nil != err {
		t.Fatalf("read %s: %v", configFile, err)
	}
	if !strings.HasPrefix(string(content), "; SnappyMail configuration file\n") {
		t.Errorf("%s does not start with the PHP header:\n%s", configFile, content)
	}
	if got := application.GetString("security", "custom_server_signature", ""); "SnappyMail" != got {
		t.Errorf("custom_server_signature = %q, want SnappyMail", got)
	}

	// An operator edited file feeds the request pipeline.
	edited := "[webmail]\n" +
		"app_path = \"/mail\"\n" +
		"compress_output = On\n" +
		"\n" +
		"[security]\n" +
		"force_https = On\n" +
		"\n" +
		"[admin_panel]\n" +
		"key = \"manage\"\n"
	if err := os.WriteFile(configFile, []byte(edited), 0o600); nil != err {
		t.Fatalf("write %s: %v", configFile, err)
	}

	application, err = loadApplication(tree, logger)
	if nil != err {
		t.Fatalf("loadApplication: %v", err)
	}

	pipeline := pipelineConfig(application)
	if !pipeline.ForceHTTPS {
		t.Error("security.force_https = On must enable the https redirect")
	}
	if !pipeline.CompressOutput {
		t.Error("webmail.compress_output = On must enable output compression")
	}
	if "manage" != pipeline.AdminKey {
		t.Errorf("admin_panel.key = %q, want manage", pipeline.AdminKey)
	}

	router, ok := newHandler(tree, logger, application).(*web.Router)
	if !ok {
		t.Fatal("newHandler must return the request router")
	}
	if "/mail" != router.Paths().AppPath {
		t.Errorf("WebPaths.AppPath = %q, want the webmail.app_path value /mail", router.Paths().AppPath)
	}
}

func TestLoadApplicationReportsServerBusy(t *testing.T) {
	root := t.TempDir()

	tree, err := resolveInstallTree(options{root: root, version: "0.0.0"})
	if nil != err {
		t.Fatalf("resolveInstallTree: %v", err)
	}

	logger, err := newLogger(options{
		logLevel:    int(logx.Warning),
		logFile:     "stderr",
		logTimeZone: "UTC",
	}, tree)
	if nil != err {
		t.Fatalf("newLogger: %v", err)
	}

	// logConfigError must not panic on either error shape.
	logConfigError(logger, &config.ServerBusy{Load: 9.5, Message: "Mailserver too busy (9.5)."})
	logConfigError(logger, errors.New("no configuration"))
}

func TestEnvironmentSnapshot(t *testing.T) {
	t.Setenv("SNAPPYMAIL_TEST_VARIABLE", "value")

	environment := environment()
	if "value" != environment["SNAPPYMAIL_TEST_VARIABLE"] {
		t.Errorf("SNAPPYMAIL_TEST_VARIABLE = %q, want value", environment["SNAPPYMAIL_TEST_VARIABLE"])
	}
}

func TestIsFile(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file.txt")

	if err := os.WriteFile(file, []byte("x"), 0o600); nil != err {
		t.Fatalf("write: %v", err)
	}

	if !isFile(file) {
		t.Errorf("isFile(%q) = false, want true", file)
	}
	if isFile(directory) {
		t.Errorf("isFile(%q) = true for a directory", directory)
	}
	if isFile(filepath.Join(directory, "missing")) {
		t.Error("isFile() = true for a missing file")
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
		// PHP: FormatFileSize($size, 2) in the logger shutdown summary.
		if got := httpx.FormatFileSize(test.size, 2); got != test.want {
			t.Errorf("FormatFileSize(%d) = %q, want %q", test.size, got, test.want)
		}
	}
}
