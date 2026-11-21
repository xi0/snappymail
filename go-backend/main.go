// Command go-backend is the Go implementation of the SnappyMail backend.
//
// Phase 0 step 1 provided a binary that starts, logs like the PHP backend and
// shuts down cleanly. Step 2 adds the request pipeline: the query-string path
// vector of RainLoop\Service::RunResult(), the security headers, the
// force_https redirect, output compression, the admin panel and Sec-Fetch gates
// and the static asset whitelist (internal/web). Step 3 loads
// configs/application.ini (internal/config) and maps it onto that pipeline.
// Step 10 wires the ?/AppData and ?/AdminAppData endpoints (internal/service)
// with the salt, the storage/settings/domain providers and the per-request
// session. Routing to the remaining Service* endpoints and the JSON actions is
// filled in by steps 11–13.
//
// The pieces below are the ones the later steps need to stay compatible with:
//
//   - the install tree layout (APP_INDEX_ROOT_PATH, APP_VERSION_ROOT_PATH,
//     APP_DATA_FOLDER_PATH, APP_PRIVATE_DATA), see include.php;
//   - APP_SALT = md5(salt . privateDataName . salt) with the salt from
//     data/SALT.php;
//   - the log line format and the log filename patterns of
//     RainLoop\Actions::compileLogParams()/compileLogFileName() and
//     \MailSo\Log\Driver;
//   - the application.ini file layout and typing rules, see internal/config.
package main

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
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

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/config"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/handler"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/logx"
	"github.com/xi0/snappymail/go-backend/internal/service"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
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

	// configs/application.ini; the Application keeps the defaults when the file
	// cannot be loaded, so it is always usable.
	application, err := loadApplication(tree, logger)
	if nil != err {
		logConfigError(logger, err)
	}

	server := &http.Server{
		Handler:           newHandler(tree, logger, application),
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

	// PHP: \MailSo\Base\Utils::FormatFileSize(\memory_get_peak_usage(true), 2)
	logger.Infof("Memory peak usage: %s", httpx.FormatFileSize(int64(stats.Sys), 2))
	logger.Infof("Time delta: %f", time.Since(started).Seconds())
}

// newHandler builds the request pipeline of step 2.
//
// Step 3 maps configs/application.ini onto web.Config, step 10 adds the
// AppData endpoints, step 11 replaces web.DefaultIndex with the pages package
// and steps 11–13 register the remaining service endpoints next to the Ping
// stub. A nil application falls back to the pipeline defaults.
func newHandler(tree installTree, logger *logx.Logger, application *config.Application) http.Handler {
	pipeline := web.DefaultConfig()
	appPath := ""

	if nil != application {
		pipeline = pipelineConfig(application)
		appPath = application.GetString("webmail", "app_path", "")
	}

	paths := httpx.WebPaths{
		AppPath:     appPath,
		Version:     tree.version,
		VersionRoot: tree.versionRoot,
	}

	router := web.NewRouter(pipeline, logger, paths, nil)

	appData := newAppData(tree, logger, application, paths)
	service.Register(router, appData)

	// The ?/Json action dispatcher of step 9, on the same session stack as the
	// AppData endpoints, so the connection/CSRF token checks and the account
	// resolution behave like PHP. The admin session (step 27) and the plugin
	// manager (step 28) are still unwired.
	options := handler.Options{Version: tree.version}
	if nil != logger {
		// A typed-nil *logx.Logger would be a non-nil handler.Logger interface
		// and panic on the first log line, so it is only set when usable.
		options.Logger = logger
	}
	options.Session = func(w http.ResponseWriter, r *http.Request) handler.Session {
		session := appData.NewSession(w, r)
		if nil == session || nil == session.Authenticator {
			return nil
		}
		return session.Authenticator
	}
	router.Register("Json", handler.NewDispatcher(options).Serve)

	return router
}

// newAppData builds the AppData/AdminAppData endpoints (step 10) on top of the
// on-disk state of the install tree: the salt, the storage/settings/domain
// providers and the per-request session (cookies + account).
//
// A nil application uses the application.ini defaults, so the pipeline (and its
// tests) also work before the configuration file exists.
func newAppData(
	tree installTree,
	logger *logx.Logger,
	application *config.Application,
	paths httpx.WebPaths,
) *service.AppData {
	cfg := application
	if nil == cfg {
		cfg = config.NewApplication(
			filepath.Join(tree.privateData, "configs", "application.ini"),
			tree.version,
			config.Defaults{},
		)
	}

	salt := loadSalt(tree, logger)
	crypter := cryptox.New(salt, tree.version, cfg.GetString("security", "encrypt_cipher", config.DefaultCipher))

	// Providers\Storage and Providers\Settings: the main folder and the
	// "storage-local" variant that keeps additional accounts in a subfolder.
	storageDir := filepath.Join(tree.privateData, "storage")
	storageProvider := storage.NewProvider(storage.NewFileStorage(storageDir, false))
	localStorageProvider := storage.NewProvider(storage.NewFileStorage(storageDir, true))

	domains := domain.NewProvider(filepath.Join(tree.privateData, "domains"), nil, domain.Defaults{})
	// Providers\Domain adds the filter.domain hook and the account checks the
	// account models rely on; the plain provider is the GetList() source.
	domainProvider := account.NewDomains(domains)

	cookies := cryptox.CookieOptions{
		Path:     cfg.GetString("labs", "cookie_default_path", ""),
		SameSite: cfg.GetString("security", "cookie_samesite", "Strict"),
		Secure:   cfg.GetBool("labs", "cookie_default_secure", false),
	}

	// A typed-nil *logx.Logger would be a non-nil auth.Logger and panic on the
	// first log line, so it is only passed when usable.
	var authLogger auth.Logger
	if nil != logger {
		authLogger = logger
	}

	newSession := func(w http.ResponseWriter, r *http.Request) *service.Session {
		options := cookies
		options.RequestIsSecure = httpx.IsSecure(r)

		jar := cryptox.NewJar(r, options)
		context := &account.Context{
			Salt:             salt,
			Storage:          storageProvider,
			Domains:          domainProvider,
			Crypt:            crypter,
			InsecureCryptKey: cfg.GetBool("security", "insecure_cryptkey", false),
		}

		return &service.Session{
			Request:       r,
			Response:      w,
			Authenticator: auth.New(context, jar, w, authLogger),
			Jar:           jar,
		}
	}

	return service.NewAppData(service.Options{
		Config:        cfg,
		Version:       tree.version,
		Salt:          salt,
		Paths:         paths,
		IndexRoot:     tree.indexRoot,
		PrivateData:   tree.privateData,
		Dev:           isDevVersion(tree.version),
		Settings:      storage.NewSettingsStorage(storageProvider),
		LocalSettings: storage.NewSettingsStorage(localStorageProvider),
		Domains:       domains,
		// Go resizes thumbnails itself (golang.org/x/image), so the equivalent
		// of PHP's gd/imagick check is always true.
		AttachmentThumbnails: true,
		NewSession:           newSession,
	})
}

// isDevVersion is SNAPPYMAIL_DEV: '0.0.0' === APP_VERSION.
func isDevVersion(version string) bool { return "0.0.0" == version }

// loadSalt is APP_SALT of include.php: md5(salt . APP_PRIVATE_DATA_NAME . salt),
// where the salt is the content of data/SALT.php.
//
// The first run of include.php/setup.php writes that file; the Go backend runs
// against an existing installation tree (see the drop-in mode of SPEC.md), so a
// missing file only warns and falls back to a per-process salt instead of
// modifying the tree.
func loadSalt(tree installTree, logger *logx.Logger) string {
	salt := ""

	if content, err := os.ReadFile(filepath.Join(tree.dataFolder, "SALT.php")); nil == err {
		salt = strings.TrimSpace(string(content))
	}

	if "" == salt {
		salt = "<?php //" + randomHex(48)
		if nil != logger {
			logger.Warningf("data/SALT.php is missing, using a per-process salt: " +
				"sessions and encrypted cookies will not survive a restart")
		}
	}

	return md5Hex(salt + tree.privateName + salt)
}

// randomHex is bin2hex(random_bytes(size)).
func randomHex(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); nil != err {
		// crypto/rand does not fail in practice; an empty salt would be worse.
		return ""
	}
	return hex.EncodeToString(buffer)
}

// md5Hex is PHP's md5().
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// loadApplication reads configs/application.ini like
// RainLoop\Config\Application does.
//
// The returned Application is usable even when err is not nil: it then still
// holds the defaultValues() defaults, which is also what PHP falls back to when
// the file cannot be written.
func loadApplication(tree installTree, logger *logx.Logger) (*config.Application, error) {
	application := config.NewApplication(
		filepath.Join(tree.privateData, "configs", "application.ini"),
		tree.version,
		config.Defaults{
			// Go has no php.ini, so the zero value falls back to PHP's built-in
			// upload_max_filesize of 2M for webmail.attachment_size_limit.
			AddressBookExists: isFile(filepath.Join(tree.privateData, "AddressBook.sqlite")),
		},
	)

	application.Env = environment()
	application.LoadAverage = config.SystemLoadAverage
	application.CipherSupported = cryptox.CipherSupported
	application.Logf = func(category, message string) {
		logger.Warningf("%s: %s", category, message)
	}

	return application, application.Load()
}

// logConfigError reports a configuration problem without stopping the server.
func logConfigError(logger *logx.Logger, err error) {
	// The security.max_sys_getloadavg guard becomes an HTTP 503 in step 33;
	// until then the request is served so an operator can reach the admin panel.
	var busy *config.ServerBusy
	if errors.As(err, &busy) {
		logger.Warningf("%s (Retry-After: %d)", busy.Message, config.RetryAfter)
		return
	}

	logger.Warningf("configuration not loaded, using the defaults: %v", err)
}

// pipelineConfig maps application.ini onto the request pipeline settings. It
// lives here so internal/web stays independent of internal/config.
func pipelineConfig(application *config.Application) web.Config {
	return web.Config{
		ServerSignature:          application.GetString("security", "custom_server_signature", ""),
		XSSProtectionHeader:      application.GetString("security", "x_xss_protection_header", ""),
		ForceHTTPS:               application.GetBool("security", "force_https", false),
		AllowAdminPanel:          application.GetBool("security", "allow_admin_panel", true),
		AdminHost:                application.GetString("admin_panel", "host", ""),
		AdminKey:                 application.GetString("admin_panel", "key", "admin"),
		CompressOutput:           application.GetBool("webmail", "compress_output", false),
		ContentSecurityPolicy:    application.GetString("security", "content_security_policy", ""),
		CSPReport:                application.GetBool("security", "csp_report", false),
		SecFetchAllow:            application.GetString("security", "secfetch_allow", ""),
		TrustProxyClientIP:       application.GetBool("labs", "http_client_ip_check_proxy", false),
		FaviconURL:               application.GetString("webmail", "favicon_url", ""),
		LocalProxyExternalImages: application.GetBool("labs", "use_local_proxy_for_external_images", true),
		Debug:                    application.GetBool("debug", "enable", false),
	}
}

// environment snapshots the process environment, the Go equivalent of the $_ENV
// array labs.replace_env_in_configuration substitutes from.
func environment() map[string]string {
	variables := os.Environ()

	result := make(map[string]string, len(variables))
	for _, variable := range variables {
		if name, value, found := strings.Cut(variable, "="); found {
			result[name] = value
		}
	}

	return result
}

// isFile mirrors PHP's is_file().
func isFile(name string) bool {
	info, err := os.Stat(name)
	return nil == err && info.Mode().IsRegular()
}

// withTrailingSeparator returns path ending with the OS separator, like the
// PHP path constants that are concatenated with relative names.
func withTrailingSeparator(path string) string {
	separator := string(filepath.Separator)
	return strings.TrimRight(path, "/\\") + separator
}
