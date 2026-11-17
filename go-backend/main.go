// Command go-backend is the Go implementation of the SnappyMail backend.
//
// Phase 0 step 1: a binary that starts, logs like the PHP backend and shuts
// down cleanly. Routing (step 2), application.ini (step 3) and everything else
// is wired in by the following steps; the pieces below are the ones the later
// steps need to stay compatible with:
//
//   - the install tree layout (APP_INDEX_ROOT_PATH, APP_VERSION_ROOT_PATH,
//     APP_DATA_FOLDER_PATH, APP_PRIVATE_DATA), see include.php;
//   - the log line format and the log filename patterns of
//     RainLoop\Actions::compileLogParams()/compileLogFileName() and
//     \MailSo\Log\Driver.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/logx"
)

const (
	// defaultPort is the port the PHP development setup uses for the Go
	// backend, so both can run side by side against the same data folder.
	defaultPort = 8080
	// defaultVersion is the version directory used while the backend is
	// developed, matching SNAPPYMAIL_DEV in include.php.
	defaultVersion = "0.0.0"
	// defaultLogFileName is the logs.filename default of
	// RainLoop\Config\Application::defaultValues().
	defaultLogFileName = "log-{date:Y-m-d}.txt"
	// defaultLogFileNameSyslog/Stderr are the magic logs.filename values.
	defaultLogFileNameSyslog = "syslog"
	defaultLogFileNameStderr = "stderr"
	// defaultLogTimeZone is the logs.time_zone default.
	defaultLogTimeZone = "UTC"
	// defaultLogLevel is the logs.level default: 4 = Warning.
	defaultLogLevel = logx.Warning
	// shutdownTimeout is how long in-flight requests may drain after SIGINT.
	shutdownTimeout = 15 * time.Second
	// serverName replaces $_SERVER['SERVER_SOFTWARE'] in the boot log line.
	serverName = "snappymail-go"
)

// options holds the parsed command line.
type options struct {
	port        int
	root        string
	data        string
	version     string
	logLevel    int
	logFile     string
	logTimeZone string
}

// installTree mirrors the path constants defined by include.php.
//
// All paths keep a trailing separator, like the PHP constants, so callers can
// concatenate relative names the same way the PHP code does.
type installTree struct {
	// indexRoot is APP_INDEX_ROOT_PATH: the parent of the binary.
	indexRoot string
	// versionRoot is APP_VERSION_ROOT_PATH: <root>/snappymail/v/<version>/.
	versionRoot string
	// dataFolder is APP_DATA_FOLDER_PATH.
	dataFolder string
	// privateName is APP_PRIVATE_DATA_NAME ("_default_" without MULTIDOMAIN).
	privateName string
	// privateData is APP_PRIVATE_DATA: <data>/_data_/<name>/.
	privateData string
	// version is APP_VERSION.
	version string
}

func main() {
	started := time.Now()

	if err := run(os.Args[1:], started); nil != err {
		// No logger may exist yet (bad flags, unusable log target), so report
		// like the PHP fatal path: plain text on stderr and a non-zero exit.
		fmt.Fprintln(os.Stderr, "go-backend:", err)
		os.Exit(1)
	}
}

// run starts the HTTP server and returns when it has shut down.
func run(args []string, started time.Time) error {
	opts, err := parseFlags(args)
	if nil != err {
		// "go-backend -h" is not a failure.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	tree, err := resolveInstallTree(opts)
	if nil != err {
		return err
	}

	logger, err := newLogger(opts, tree)
	if nil != err {
		return err
	}

	logBootLine(logger, tree, opts)

	server := &http.Server{
		Handler:           newPlaceholderHandler(logger),
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(opts.port)))
	if nil != err {
		return fmt.Errorf("listen on port %d: %w", opts.port, err)
	}

	logger.Infof("listening on %s", listener.Addr().String())

	// SIGINT/SIGTERM: stop accepting, drain in-flight requests, then exit.
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		if nil != err && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-signalContext.Done():
		logger.Infof("shutdown signal received, draining requests")

		shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := server.Shutdown(shutdownContext); nil != err {
			logger.Errorf("graceful shutdown failed: %v", err)
			_ = server.Close()
			return err
		}

		<-serveErrors
	}

	logShutdownLine(logger, started)

	return nil
}

// parseFlags parses and validates the command line.
func parseFlags(args []string) (options, error) {
	opts := options{}

	flags := flag.NewFlagSet("go-backend", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.IntVar(&opts.port, "port", defaultPort, "TCP port to listen on")
	flags.StringVar(&opts.root, "root", "",
		"install tree root (APP_INDEX_ROOT_PATH), defaults to the directory of the binary")
	flags.StringVar(&opts.data, "data", "",
		"data folder (APP_DATA_FOLDER_PATH), defaults to <root>/data")
	flags.StringVar(&opts.version, "version", defaultVersion,
		"application version (APP_VERSION), selects <root>/snappymail/v/<version>/")
	flags.IntVar(&opts.logLevel, "log-level", int(defaultLogLevel),
		"log severity threshold 0..7 (0 = emergency, 7 = debug)")
	flags.StringVar(&opts.logFile, "log-file", defaultLogFileName,
		"log filename pattern, or \"stderr\" / \"syslog\"")
	flags.StringVar(&opts.logTimeZone, "log-time-zone", defaultLogTimeZone,
		"time zone used for log timestamps and {date:...} patterns")

	if err := flags.Parse(args); nil != err {
		return opts, err
	}

	if flags.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	if opts.port < 1 || opts.port > 65535 {
		return opts, fmt.Errorf("invalid port %d", opts.port)
	}

	if !logx.Level(opts.logLevel).Valid() {
		return opts, fmt.Errorf("invalid log level %d, expected 0..7", opts.logLevel)
	}

	if "" == strings.TrimSpace(opts.version) {
		return opts, errors.New("invalid version, must not be empty")
	}

	return opts, nil
}

// resolveInstallTree reproduces the path constants of include.php.
func resolveInstallTree(opts options) (installTree, error) {
	root := strings.TrimSpace(opts.root)
	if "" == root {
		// APP_INDEX_ROOT_PATH is the parent of the binary.
		executable, err := os.Executable()
		if nil != err {
			return installTree{}, fmt.Errorf("resolve install root: %w", err)
		}
		root = filepath.Dir(executable)
	}

	var err error
	if root, err = filepath.Abs(root); nil != err {
		return installTree{}, fmt.Errorf("resolve install root: %w", err)
	}

	dataFolder := strings.TrimSpace(opts.data)
	if "" == dataFolder {
		dataFolder = filepath.Join(root, "data")
	}
	if dataFolder, err = filepath.Abs(dataFolder); nil != err {
		return installTree{}, fmt.Errorf("resolve data folder: %w", err)
	}

	// MULTIDOMAIN is not ported yet, so the private folder is always
	// "_default_" (APP_PRIVATE_DATA_NAME in include.php).
	privateName := "_default_"

	tree := installTree{
		version:     opts.version,
		privateName: privateName,
	}

	tree.indexRoot = withTrailingSeparator(root)
	tree.versionRoot = withTrailingSeparator(filepath.Join(root, "snappymail", "v", opts.version))
	tree.dataFolder = withTrailingSeparator(dataFolder)
	tree.privateData = withTrailingSeparator(filepath.Join(dataFolder, "_data_", privateName))

	return tree, nil
}

// newLogger builds the logger from the command line, mirroring the driver
// selection in the RainLoop\Actions constructor.
func newLogger(opts options, tree installTree) (*logx.Logger, error) {
	location, err := time.LoadLocation(strings.TrimSpace(opts.logTimeZone))
	if nil != err {
		return nil, fmt.Errorf("invalid log time zone %q: %w", opts.logTimeZone, err)
	}

	// logs.hide_passwords defaults to true, so secrets stay masked.
	logger := logx.New(logx.Level(opts.logLevel)).SetShowSecrets(false)

	fileName := strings.TrimSpace(opts.logFile)

	switch strings.ToLower(fileName) {
	case defaultLogFileNameSyslog:
		driver := logx.NewSyslogDriver()
		driver.SetTimeZone(location)
		logger.Append(driver)

	case defaultLogFileNameStderr:
		driver := logx.NewStderrDriver()
		driver.SetTimeZone(location)
		logger.Append(driver)

	default:
		params := logx.Params{Location: location}

		// logs.path defaults to APP_PRIVATE_DATA . 'logs'. The directory holds
		// credentials, so it is created with 0700 (PHP: mkdir(..., 0700, true)).
		logDirectory := filepath.Join(tree.privateData, "logs")
		if err := os.MkdirAll(logDirectory, 0o700); nil != err {
			return nil, fmt.Errorf("create log directory %s: %w", logDirectory, err)
		}

		path := filepath.Join(logDirectory, filepath.FromSlash(logx.CompileFileName(fileName, params)))

		driver, err := logx.NewFileDriver(path)
		if nil != err {
			return nil, err
		}
		driver.SetTimeZone(location)

		logger.Append(driver)
	}

	if !logger.Enabled() {
		return nil, errors.New("no log driver configured")
	}

	return logger, nil
}

// logBootLine writes the equivalent of the boot line the PHP Actions
// constructor logs for every request.
func logBootLine(logger *logx.Logger, tree installTree, opts options) {
	// PHP: [SM:<version>][IP:<client>][PID:<pid>][<SERVER_SOFTWARE>][<PHP_SAPI>]
	//      [Streams:<transports>][<METHOD> <scheme>://<host><REQUEST_URI>]
	// There is no client IP or request URI at boot in Go, so those fields are
	// logged per request by the middleware added in step 2.
	logger.Infof("[SM:%s][PID:%d][%s][%s/%s][Streams:TCP][listen :%d][root:%s][data:%s]",
		tree.version,
		os.Getpid(),
		serverName,
		runtime.GOOS,
		runtime.GOARCH,
		opts.port,
		tree.indexRoot,
		tree.dataFolder,
	)

	// A wrong -root/-version is a common operator mistake; the static assets
	// (and later the templates) are read from here.
	if info, err := os.Stat(tree.versionRoot); nil != err || !info.IsDir() {
		logger.Warningf("version root %s is not a directory, static assets and templates will not be found",
			tree.versionRoot)
	}
}

// logShutdownLine mirrors \MailSo\Log\Logger::__loggerShutDown(), which only
// reports memory and duration when at least one message was written.
func logShutdownLine(logger *logx.Logger, started time.Time) {
	if !logger.Used() {
		return
	}

	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)

	logger.Infof("Memory peak usage: %s", formatFileSize(int64(stats.Sys)))
	logger.Infof("Time delta: %f", time.Since(started).Seconds())
}

// newPlaceholderHandler is the temporary handler for step 1.
//
// Step 2 replaces it with web.Router (query-string path vector, security
// headers, static whitelist, ...). For now every request is answered so the
// graceful shutdown path has something to drain.
func newPlaceholderHandler(logger *logx.Logger) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		logger.Infof("%s %s %s from %s",
			request.Method, request.Host, request.URL.RequestURI(), request.RemoteAddr)

		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("SnappyMail Go backend (Phase 0 step 1 skeleton)\n"))
	})
}

// formatFileSize is \MailSo\Base\Utils::FormatFileSize() with the PHP rounding.
// It moves to httpx once that package exists (step 2).
func formatFileSize(size int64) string {
	units := []string{"B", "KB", "MB"}
	index := 0

	value := float64(size)
	for value > 1024 && index+1 < len(units) {
		value /= 1024
		index++
	}

	return strconv.FormatFloat(roundHalfUp(value, 2), 'f', -1, 64) + units[index]
}

// roundHalfUp rounds like PHP's round($value, $precision).
func roundHalfUp(value float64, precision int) float64 {
	factor := 1.0
	for i := 0; i < precision; i++ {
		factor *= 10
	}

	return float64(int64(value*factor+0.5)) / factor
}

// withTrailingSeparator returns path ending with the OS separator, like the
// PHP path constants that are concatenated with relative names.
func withTrailingSeparator(path string) string {
	separator := string(filepath.Separator)
	return strings.TrimRight(path, "/\\") + separator
}
