# Go backend

This is the Go backend intende to replace the PHP backend.

## Goal

The Go backend should serve the same endpoints with identical responses as the traditional PHP backend.
It should also serve static assets, so that the entire application is available through this backend.
Responses do not need to be byte-identical if the semantics of the responses are identical.
Files not consumed by the frontend should not be available. External dependencies should be pulled in as
needed. Plugins are enabled at build time and for now, only the "caldav" plugin should be available and
enabled for the Go backend.

### Operating mode: drop-in replacement

The Go binary runs against an **existing SnappyMail installation tree** and is meant to be able to run
side by side with the PHP backend (different port) against the *same* files:

* configuration, domain configs, storage, cache and plugins are read/written in the **existing data folder
  layout** (`data/_data_/_default_/…`, see [Data compatibility](#data-compatibility)), so an operator can
  point either backend at the same folder and see the same accounts, settings and mail;
* static assets, themes, localization and view templates are served **from the on-disk tree**
  (`snappymail/v/<APP_VERSION>/…`), so the existing JS/CSS build pipeline (`gulp`/`rollup`) stays the single
  source of truth for frontend assets;
* encrypted blobs (cookies, stored passwords, SSO hashes) stay interchangeable with PHP (same
  `SnappyMail\Crypt` format), because both backends may serve the same browser session during a migration.

Consequences: no asset embedding, no database of our own, and every format PHP reads or writes is a
compatibility contract that must be implemented (and tested) rather than redesigned.

## Code organization

### Layout

```
go-backend/
├── main.go                     # flags, wiring, http.Server lifecycle, graceful shutdown
├── go.mod / go.sum
├── Makefile                    # build / build-release / test / diff-test (see Implementation steps)
├── SPEC.md
└── internal/
    ├── appctx/                 # App + per-request Action context (mirrors RainLoop\Api + RainLoop\Actions)
    ├── logx/                   # Logger with logs.* config (levels, filename patterns, auth log, syslog)
    ├── config/                 # application.ini: typed defaults, load/save, env replacement, migrations
    ├── domain/                 # domain model + provider (.json/.ini/.alias/disabled/_wildcard_)
    ├── account/                # Account/MainAccount/AdditionalAccount + token (de)serialization
    ├── auth/                   # login flow, SSO, sign-me, session/connection/CSRF tokens
    ├── cryptox/                # Crypt-compatible ciphertext, cookies, URL-safe base64, HMAC, TOTP
    ├── storage/                # file storage layout, settings provider, files provider, cache + GC
    ├── imapclient/             # IMAP transport, settings, SASL, capability negotiation
    ├── smtpclient/             # SMTP transport, settings, login, DATA
    ├── sieveclient/            # ManageSieve transport + script CRUD
    ├── mail/                   # MailClient: folders, list, message, flags, move/copy, save/send, quota
    ├── mime/                   # MIME parse/build, header codec, attachments, encodings, TNEF hook
    ├── message/                # Folder/Message/Identity JSON models with MailSo-identical field names
    ├── pgp/                    # OpenPGP (go-crypto) + GnuPG binary shim
    ├── smime/                  # S/MIME via PKCS#7
    ├── contacts/               # sqlite address book (contacts, groups, sync data), vCard/CSV
    ├── suggestions/            # address-book recipient suggestions (+ plugin drivers)
    ├── l10n/                   # langs.json + {user,admin,static}.json merge, plugin langs
    ├── themes/                 # theme discovery + CSS assembly (styles.css/less, @custom/@nextcloud)
    ├── plugin/                 # plugin API surface (hooks, assets, templates, langs, JSON actions) + registry
    ├── plugins/caldav/         # port of plugins/caldav (the only build-time plugin)
    ├── handler/                # Do* JSON actions, one file per PHP Actions trait
    ├── service/                # non-JSON endpoints (AppData, Lang, Css, Plugins, Raw, Upload*, Ping, …)
    ├── pages/                  # Index.html / Error.html rendering (templates + boot CSS/JS + language)
    ├── web/                    # router, middleware, static whitelist, response helpers
    └── httpx/                  # query-path parsing, client IP, content-disposition, ETag/expiry helpers
```

Rationale: one Go package per PHP namespace *file group*, so each implementation step has an obvious
"port this PHP file into that package" mapping and no package grows into a second monolith.

### Mapping from PHP

| PHP | Go package | Notes |
| --- | --- | --- |
| `RainLoop\Api` (`app/libraries/RainLoop/Api.php`) | `appctx` | singleton config/actions/logger/CSP |
| `RainLoop\Actions` + `RainLoop\Actions\*` traits | `handler`, `appctx` | one Go file per trait (`handler/folders.go`, `handler/messages.go`, …) |
| `RainLoop\Service` + `RainLoop\ServiceActions` | `web`, `service`, `pages` | routing, endpoints, index page |
| `RainLoop\Config\*` | `config` | `application.ini` only (plugin config → `plugin`) |
| `RainLoop\Model\*` | `account`, `domain`, `message` | JSON field names are part of the contract |
| `RainLoop\Providers\*` | `storage`, `contacts`, `suggestions`, `domain` | file/sqlite storage, identity-of-format |
| `RainLoop\Plugins\*` | `plugin` | `AbstractPlugin` → `plugin.Plugin` interface |
| `RainLoop\Utils`, `RainLoop\Notifications`, `RainLoop\Enumerations\*` | `httpx`, `appctx` | tokens, JSON envelope, notification codes, Capa/Layout/SignMeType constants |
| `MailSo\Base`, `MailSo\Net` | `httpx`, `cryptox`, `imapclient`, `smtpclient` | HTTP helpers, network login, SASL |
| `MailSo\Imap`, `MailSo\Mail`, `MailSo\Mime` | `imapclient`, `mail`, `mime`, `message` | command layer, mail client, MIME |
| `MailSo\Sieve` | `sieveclient` | |
| `SnappyMail\*` (`crypt`, `cookies`, `l10n`, `repo`, `image`, `mimetype`, `totp`, `qrcode`, `dns`, `idn`, `hibp`, `uuid`, `sasl`, `pgp`, `smime`, `dav` (CalDAV client), `http`, `file`) | `cryptox`, `l10n`, `mail`, `pgp`, `smime`, `plugins/caldav` | only what the enabled feature set needs |
| `Sabre\*`, `OAuth2\*`, `TNEFDecoder`, `lessphp` | — | see [External dependencies](#external-dependencies) for replacements |
| `plugins/caldav/index.php` (~3.0k lines) | `plugins/caldav` | split over four implementation steps |

### Routing and URL compatibility

The PHP router does **not** use real paths: everything is derived from the query string. `Service::RunResult()`
takes `QUERY_STRING` up to the first `&`, trims ` /`, then appends each element of `$_GET['q'][]` joined with
`/`, and splits the result on `/` into a path vector. A segment may carry an `@` suffix
(`Service<Name>@<extra>`), the first element selects the handler, and `index`/empty means "serve the app".

Go must reproduce that exactly (plus `PATH_INFO`-style `/index.php/...` requests, which Apache currently
rewrites into the query string):

| Request | PHP handler | Go package |
| --- | --- | --- |
| `GET /` or `GET /?/` or `GET /?/index/…` | index page | `pages` |
| `GET /?/AppData/0/<rand>/` | `ServiceAppData` | `service` |
| `GET /?/AdminAppData/0/<rand>/` | `ServiceAdminAppData` | `service` |
| `POST /?/Json/&q[]=/0/` (`Action` in the JSON body) or `GET /?/Json/&q[]=/0/<Action>/&q[]=/<RawKey>/` | `ServiceJson` | `handler` |
| `GET /?/Lang/0/{App,Admin}/<lang>/<ver>/` | `ServiceLang` | `service` |
| `GET /?/Css/0/{User,Admin}/-/<theme>/-/<ts>/Hash/-/Json/` | `ServiceCss` | `service` |
| `GET /?/Plugins/0/{User,Admin}/<etag>/` | `ServicePlugins` | `service` |
| `POST /?/Upload/&q[]=/0/` | `ServiceUpload` | `service` |
| `POST /?/UploadContacts/&q[]=/0/` | `ServiceUploadContacts` (5 MB cap) | `service` |
| `POST /?/UploadBackground/&q[]=/0/` | `ServiceUploadBackground` (1 MB cap) | `service` |
| `GET /?/Raw/&q[]=/0/<Action>/<RawKey>/` | `ServiceRaw` (`RawView`, `RawDownload`, `RawViewThumbnail`, `RawViewAsPlain`, `RawUserBackground`, `RawContactsVcf`, `RawContactsCsv`) | `service` |
| `GET /?/ProxyExternal/<urlsafe-b64>` | `ServiceProxyExternal` | `service` |
| `GET /?/Mailto?to=mailto:…` | `ServiceMailto` | `service` |
| `GET /?/Ping/…` | `ServicePing` (`text/plain` `Pong`) | `service` |
| `GET /?/Test/…` | `ServiceTest` | `service` |
| `GET /?/Sso?hash=…` | `ServiceSso` | `service` |
| `POST /?/CspReport/…` | `ServiceCspReport` | `service` |
| `GET /snappymail/v/<ver>/static/…`, `/themes/<theme>/…` | webserver static | `web` (whitelist) |

Admin area detection (mirrors `Service::RunResult`): if `admin_panel.host` is set, the request `Host` decides;
otherwise the first path segment must equal `admin_panel.key` (default `admin`) and is shifted off the vector.

### Request pipeline

Order matters and is part of the observable behaviour:

1. `Server` header (`security.custom_server_signature`), `Referrer-Policy: no-referrer`,
   `X-Content-Type-Options: nosniff`, CSP, `X-XSS-Protection`.
2. `security.force_https` redirect (301/302 to `https://` + host + URL).
3. Output compression (`webmail.compress_output`, `br` preferred, else gzip) unless disabled.
4. Path vector parsing (above) → plugin hook `filter.http-paths`.
5. `POST` ⇒ `no-store` cache headers.
6. Admin area + `security.allow_admin_panel` gate (403 + error template).
7. Sec-Fetch gate (`security.secfetch_allow`) for non-`mailto` paths (403 + error template).
8. Dispatch to `Service*` method, else plugin "additional part", else index page.
9. `BootEnd` (currently a no-op) and flushing.

### JSON response envelope

Every `Do*` action returns `map[string]any` that is at least:

```json
{ "Action": "<ActionName>", "Result": <json encode of the handler result>, "epoch": 1699999999 }
```

Failure additions: `code` (`RainLoop\Notifications` value), `message`, `messageAdditional`, `ExceptionCode`.
Guests/unknown actions must produce the same codes as PHP (`InvalidInputArgument` 903, `InvalidToken` 101,
`UnknownError` 999, …). `DefaultResponse`, `FalseResponse`, `TrueResponse`, `ExceptionResponse` and
`responseObject` (JSON-serializable model → `map`, plus the `checkable` flag added to folders) are ported
one-to-one. The notification code table lives in `appctx` and must never be renumbered.

Request parameter handling must also match: `Action` may come from the JSON body or from path segment 3 (GET),
`X-SM-Token` header or `XToken` body field must equal the connection token (skipped for `Logout`), admin
actions require an admin session (`AdminLogin`/`AdminLogout` exempt), JSON bodies that are not objects/arrays
are rejected, and parameters are normalised (depth ≤ 16, no objects/resources, type-mismatch falls back to the
declared default).

### Data compatibility

* **Install tree** — `APP_INDEX_ROOT_PATH` is the parent of the binary (overridable), `APP_VERSION_ROOT_PATH`
  is `APP_INDEX_ROOT_PATH/snappymail/v/<APP_VERSION>/` with `APP_VERSION` = `0.0.0` unless given by flag.
* **Private data** — `APP_DATA_FOLDER_PATH` defaults to `APP_INDEX_ROOT_PATH/data/`; the private folder is
  `data/_data_/<name>/` where `<name>` is `_default_` or (with `MULTIDOMAIN`) the sanitised request host.
  Subfolders: `configs/`, `domains/`, `plugins/`, `storage/`, `cache/`, `logs/`. `INSTALLED` holds the version
  and `SALT.php` holds the salt; `APP_SALT = md5(salt + privateDataName + salt)`.
* **Config** — `configs/application.ini` with PHP `parse_ini_file` semantics (`On`/`Off` for booleans, quoted
  strings, `;` comments) and the exact default section/key/type table from
  `RainLoop\Config\Application::defaultValues()`. Legacy key rewrites in `Application::Set()`
  (`webmail.language_admin` → `admin_panel.language`, `labs.imap_*`, `labs.cache_system_data`, …) must be
  honoured on load. `Save()` keeps the header comment and `version.current`/`version.saved`.
* **Domains** — `domains/<punycode-lowercase>.json` (canonical), `.ini` (legacy read), `.alias` (redirect),
  `disabled` (newline/comma separated) and `_wildcard_` file names. `default.json` and `<hostname>.json` are
  created on first install exactly as `setup.php` does.
* **Storage** — `storage/<domain>/<localpart>[/<subfolder>]/<securekey>`; subfolders `.sign_me/`, `.sessions/`,
  `.pgp/`; `__nobody__/<sha1>` for global keys; files `0600`, dirs `0700`. Settings are the JSON files
  `settings` and `settings_local`. Keys are sanitised with `MailSo\Base\Utils::SecureFileName` semantics.
* **Cookies** — `smaccount`, `smadditional`, `smremember` (sign-me), `smsession`, `smctoken`,
  `smmailtoauth`. Values are `urlsafe-base64(Crypt::EncryptToJSON(...))`; cookies are split into
  `name~1`, `name~2`, … when they exceed the 4 KB budget and reassembled by prefix on read.
* **Static assets** — served from disk with an explicit whitelist (`web/static.go`), because "files not
  consumed by the frontend should not be available":
  `static/js/{boot,libs,app,admin,sieve,openpgp,serviceworker}.js` and `static/js/min/*.js`,
  `static/css/{boot,app,admin}{,.min}.css`, `static/css/fonts/*`, `static/images/*`,
  `static/sounds/*`, `static/favicon.png`, `static/manifest.json`, `themes/<theme>/styles.css|less`,
  `themes/<theme>/images/*`. Everything else (`app/`, `app/localization/`, `plugins/`, `data/`, `tests`,
  `*.php`, `.htaccess`, `include.php`) must 404. Cache-busting `?v=<mtime>` is appended by
  `WebStaticPath`, so the same helper exists in Go.

### Plugin model

PHP plugins are pulled from a remote repository and stored in `data/_data_/<name>/plugins/<id>/`. The Go
backend instead registers plugins **at build time**:

* `plugin.Plugin` interface mirrors `RainLoop\Plugins\AbstractPlugin`: `Name`, `Version`, `Description`,
  `UseLangs`, `Init`, `ConfigMap`, `FilterAppDataPluginSection`, plus registration helpers `AddHook`,
  `AddJS`, `AddCSS`, `AddTemplate`, `AddJSONAction`, `AddPartHook`.
* A registry (`plugin.Registry`) is populated with explicit imports in `internal/plugins/registry.go`.
  Only `caldav` is registered; adding another plugin is a code change plus a build.
* The manager provides the parity surface used by the frontend: `Hash()` (plugin version + asset mtimes, feeds
  the `Plugins/<etag>/` URL and the index/`Lang`/`Css` cache keys), `CompileJS`, `CompileCSS`,
  `CompileTemplate`, `ReadLang`, `InitAppData` (plugin settings exposed through `AppData.Plugins[<name>]`),
  `RunHook`, `RunAdditionalPart`, `RunAdditionalJSON`.
* Hook names and signatures are the contract documented in `plugins/README.md`; only the hooks the ported
  feature set actually fires need to exist, but their names/argument order must match.
* Plugin settings live in `configs/plugin-<name>.ini`; per-user settings live under the `Plugins` key of
  `settings`/`settings_local` (same as `Manager::GetUserPluginSettings`).

## External dependencies

Dependency policy: pull in a module only where a PHP extension/library does real work that cannot be
reasonably hand-rolled, prefer pure Go (no cgo) so the binary stays portable, and keep the module set small
enough that a single `go.mod` review covers it. Anything that is a compatibility contract (INI, crypt,
storage, JSON shapes) is implemented in-repo rather than delegated to a library that might drift.

| Module | Replaces | Used by | Notes |
| --- | --- | --- | --- |
| `github.com/emersion/go-imap/v2` | `MailSo\Imap`, `MailSo\Net` | `imapclient`, `mail` | IMAP4rev1 client with SASL, IDLE, LIST-STATUS, MOVE, SORT/THREAD, ESEARCH/QRESYNC, METADATA. Check per-domain `disabled_capabilities` before use; fall back to client-side sorting when the server lacks SORT. |
| `github.com/emersion/go-sasl` | `MailSo\Net\*Sasl*` | `imapclient`, `smtpclient`, `sieveclient` | PLAIN, LOGIN, CRAM-MD5, ANONYMOUS. SCRAM-SHA-1/256/512 comes from `github.com/xdg-go/scram`. |
| `github.com/xdg-go/scram` | `SnappyMail\SASL\Scram*` | `imapclient`, `smtpclient` | Server list in domain config advertises SCRAM first. |
| `github.com/emersion/go-smtp` | `MailSo\Smtp` | `smtpclient`, `mail` | STARTTLS/implicit TLS, AUTH, ENVID/DSN via extension helpers as needed. |
| `github.com/emersion/go-message` | `MailSo\Mime\*Parser*` | `mime`, `mail` | Streaming MIME parser (header fold/unfold, charsets, multipart). Message *building* is in-repo so output matches PHP's byte layout. |
| `modernc.org/sqlite` | PDO SQLite (`Providers\AddressBook\PdoAddressBook`) | `contacts` | Pure Go, no cgo. Must open the existing `AddressBook.sqlite` schema unchanged. |
| `github.com/ProtonMail/go-crypto/openpgp` | `SnappyMail\PGP\OpenPGP` | `pgp` | Key parsing, sign/verify, encrypt/decrypt, armored output. |
| `github.com/smallstep/pkcs7` | `SnappyMail\SMIME` | `smime` | Sign/verify/encrypt/decrypt S/MIME and certificate parsing. |
| `github.com/pquerna/otp` | `SnappyMail\TOTP` | `auth` (admin 2FA) | TOTP/HOTP validation. |
| `github.com/skip2/go-qrcode` | `SnappyMail\QRCode` | `handler/admin.go` | `DoAdminQRCode` data URIs. |
| `golang.org/x/net/idna` | `idn_to_ascii`/`idn_to_utf8` | `domain`, `auth`, `account` | Domains are stored lowercase punycode. |
| `golang.org/x/text` | PHP `mbstring`/`iconv` usage | `mime`, `l10n`, `mail` | Charset conversion, `encoding` for RFC 2047/2231. |
| `golang.org/x/image` | GD/Imagick for thumbnails | `service` (`Raw*` thumbnails, background) | Resize/thumbnail; EXIF auto-rotate only if `labs.image_exif_auto_rotate` is on. |
| `golang.org/x/crypto` | `sodium_*`/OpenSSL usage | `cryptox` | `chacha20poly1305` (XChaCha20, the modern PHP path), `pbkdf2`, `bcrypt`/`argon2` for the admin password hash, `sha3` if needed. |
| `github.com/andybalholm/brotli` | `php-ext-brotli`/`ob_gzhandler` | `web` | `webmail.compress_output`; `compress/gzip` from stdlib for the gzip path. |
| `gopkg.in/ini.v1` | `parse_ini_file`/INI writer | `config`, `plugin` | Only used as a low-level reader; the typed default table, legacy rewrites and the writer stay in-repo to keep byte layout stable. |
| `github.com/microcosm-cc/bluemonday` | `MailSo\Base\HtmlUtils::ClearHtml` sanitising | `mime` | HTML e-mail sanitising (allow-list tuned to match current output). |
| `golang.org/x/oauth2` | `OAuth2\Client` | *not used yet* | Reserved: the `login-oauth2` plugin is not enabled for Go, so this stays out of `go.mod` until then. |

Standard library only, deliberately: `net/http` (+ `http.ServeContent` for range requests), `encoding/json`
(HTML-safe encoding off, `SetEscapeHTML(false)` to match `JSON_UNESCAPED_UNICODE|JSON_UNESCAPED_SLASHES`),
`encoding/xml` (CalDAV multistatus instead of DOMDocument/XPath), `archive/zip` (attachment zip actions),
`crypto/*`, `text/template` for view template assembly, `html` for escaping.

Deliberately **not** ported as dependencies:

* ManageSieve (RFC 5804) — there is no maintained Go ManageSieve client module (`go-managesieve` and `go-sieve`
  are not real modules; emersion's mail stack has no ManageSieve client), and the
  protocol is a small line-based text protocol with literals and quoted strings, so `internal/sieveclient`
  hand-rolls the transport: `CAPABILITY` (incl. `SASL`/`IMPLEMENTATION`/`VERSION`/`SIEVE` decoding), optional
  `STARTTLS`, SASL `PLAIN`/`LOGIN` via `github.com/emersion/go-sasl`, then
  `LISTSCRIPTS`/`GETSCRIPT`/`PUTSCRIPT`/`SETACTIVE`/`DELETESCRIPT`/`RENAMESCRIPT`/`CHECKSCRIPT` with the same
  literals/quoted-string rules `MailSo\Sieve` uses, plus the `NO`/`BYE` response mapping onto the client's error
  classes.
* `lessphp` — the shipped theme (`Gazellemail/styles.css`) and the only enabled plugin (`caldav/calendar.css`)
  are plain CSS. `themes` supports `.less` only by returning an error until a LESS compiler is actually
  needed (documented limitation; `@nextcloud`/`@custom` directory scanning stays).
* `Sabre\*` — only used by the CardDAV/DAV plugins, which are not enabled.
* `TNEFDecoder` — `winmail.dat` extraction is an edge case; `mime` exposes a hook so it can be added later
  without changing call sites.
* `OAuth2\Client` — see above.

Toolchain: the module declares `go 1.27`; CI must build with that toolchain and `CGO_ENABLED=0` for the
release binary. `go.mod` is tidied per implementation step (`go mod tidy` in the same commit that adds the
first import), not upfront.

## Implementation steps

Rules that keep every step inside one LLM context window:

* **One step = one package (or one handler file group) + its tests.** Do not start a step that needs two PHP
  files larger than ~1.5k lines combined; split it first (the split points are pre-declared below).
* **Read budget**: the PHP references listed for a step, plus library docs for at most one dependency.
* **Write budget**: ≲800 lines of Go per step; if the ported function set is bigger, extract a smaller slice
  and leave the remainder to the following step (mark it "partial" in the step notes).
* **Every step ends with the same verification shape**: `go build ./... && go vet ./... && go test ./...` plus
  the step's own acceptance check. Where a JSON/HTML shape is the contract, the check is a golden diff against
  the PHP backend (`testdata/<step>/…`), produced by `make diff-record` (see Step 33) and asserted by
  `make diff-test`.
* **Never "fix" a PHP quirk while porting.** Odd defaults, `On`/`Off` booleans, `Action`/`Result` casing,
  `epoch`, 4 KB cookie splitting, `.sign_me` paths — all ported as-is; deviations go into the step notes.

Each step below lists: **goal**, **references to read**, **deliverables**, **actions/endpoints**, **done when**.

---

### Phase 0 — Foundations

#### Step 1 — Module skeleton, flags, logging
* Goal: a binary that starts, logs like PHP and shuts down cleanly.
* Read: `go-backend/main.go`, `go-backend/Makefile`, `go-backend/go.mod`,
  `snappymail/v/0.0.0/app/libraries/RainLoop/Api.php` (`Config`, `Logger`, `Version`),
  `Actions.php` constructor + `compileLogParams`/`compileLogFileName`, `include.php` (constants).
* Deliver: `internal/logx` (levels 0–7, file driver, `stderr`/`syslog` targets, time zone, filename patterns
  `{date:…}`, `{user:…}`, `{request:ip}`, `{imap:…}`, `{smtp:…}`), flags
  `-port` (default 1443), `-root`, `-data`, `-version`, `-log-level`.
* Done when: `make` builds; `log-YYYY-MM-DD.txt` lines have the PHP prefix shape; `SIGINT` drains requests.

#### Step 2 — Router, middleware, static whitelist
* Goal: identical URL parsing, security headers and static asset exposure.
* Read: `RainLoop/Service.php` (`RunResult` first half), `MailSo/Base/Http.php`
  (`GetHost`/`GetUrl`/`IsSecure`/`IsPost`/`GetHeader`/`setContentDisposition`/`GetClientIp`),
  `RainLoop/Utils.php` (`WebPath`, `WebVersionPath`, `WebStaticPath`), `snappymail/v/0.0.0/static/apache.htaccess`,
  root `.htaccess`.
* Deliver: `internal/web/router.go` (query-string path vector incl. `q[]`, `@extra`, `PATH_INFO`), `middleware.go`
  (headers, HSTS, force_https, compression, POST no-cache), `static.go` (whitelist from
  [Data compatibility](#data-compatibility), `?v=mtime` handling, `Content-Type`, `Accept-Ranges`),
  `httpx.QueryPath`, `httpx.ClientIP`.
* Done when: `GET /snappymail/v/0.0.0/static/js/min/app.min.js` is 200 with `application/javascript`;
  `GET /snappymail/v/0.0.0/app/libraries/RainLoop/Api.php`, `/include.php`, `/data/…`, `/plugins/caldav/index.php`
  are 404; `GET /?/Ping/0/` reaches a stub and returns `Pong`; `GET /whatever` serves the index stub.

#### Step 3 — `application.ini`
* Goal: read and write the existing configuration with PHP typing semantics.
* Read: `RainLoop/Config/Application.php` (defaults + `Set` rewrites + `Load` extras), `Config/AbstractConfig.php`
  (`Load`/`Save`/`Get`/`Set`/`getDecrypted`), `include.php` (data paths, `APP_SALT`), `setup.php` (first-run).
* Deliver: `internal/config` with the typed default table (every section/key/type), a parser for the INI subset,
  a writer reproducing the header + `[section]` + `key = "value"` layout, `On`/`Off` booleans, the legacy key
  rewrites, `labs.replace_env_in_configuration` (`$NAME` substitution), `version.current/saved`, the
  `security.max_sys_getloadavg` 503 guard, and `security.encrypt_cipher` validation with fallback.
* Done when: loading the checked-in `data/_data_/_default_/configs/application.ini` yields the same dumped
  `map[section]map[key]value` as PHP (golden); a `Save()` round-trip produces an identical file.

#### Step 4 — Domain provider
* Goal: load/save/disable/alias/list domain configs, including wildcards.
* Read: `Providers/Domain/DefaultDomain.php`, `Model/Domain.php`, `Providers/Domain/DomainInterface.php`,
  `plugins/Helper.php` (`ValidateWildcardValues`), `app/domains/default.json`.
* Deliver: `internal/domain` (model + provider): `.json` canonical, `.ini` legacy, `.alias`, `disabled`,
  `default`/`_wildcard_` filenames, punycode lowercase names, `GetList`, and the IMAP/SMTP/Sieve settings
  structs with `fixUsername(shortLogin, lowerLogin)` behaviour, plus `SieveSettings.Enabled`.
* Done when: `default.json` loads with identical nesting; `_wildcard_*.json` matches PHP's matcher; `disabled`
  entries are refused; a written domain file is byte-identical to PHP's `JSON_PRETTY_PRINT` output.

#### Step 5 — Storage, settings and files providers
* Goal: the on-disk state layer everything else depends on.
* Read: `Providers/Storage/FileStorage.php`, `Providers/Storage.php`, `Providers/Settings/DefaultSettings.php`,
  `Providers/Files/FileStorage.php`, `Providers/Storage/Enumerations/StorageType.php`,
  `snappymail/upgrade.php` (`Upgrade::FileStorage`), `Utils::saveFile`.
* Deliver: `internal/storage`: config/session/sign-me/pgp/nobody paths, `SecureFileName`, atomic writes with
  `0600`/`0700`, `settings`+`settings_local` JSON load/save/delete, files provider (move-upload, get, clear, GC),
  cache client (get/set/delete/timer with account prefix and `cache.fast_cache_index`), and `GC()`.
* Done when: writes land at the same paths/perms as PHP; a PHP-written `settings` file loads and re-saves
  unchanged; cache keys match `KeyPathHelper` values for the same inputs.

#### Step 6 — Crypto and cookie compatibility
* Goal: encrypted blobs and cookies are interchangeable with PHP.
* Read: `snappymail/crypt.php`, `snappymail/cookies.php`, `RainLoop/Utils.php`
  (`GetSessionToken`, `GetConnectionToken`, `GetCsrfToken`, `EncodeKeyValuesQ`), `MailSo/Base/Xxtea.php`.
* Deliver: `internal/cryptox`: `Encrypt/Decrypt` with `sodium` (XChaCha20-Poly1305, AD = `APP_SALT`,
  key = `sha1(passphrase + APP_SALT)` zero-padded), `openssl` (`aes-256-cbc*`, key = first 32 bytes of the same
  digest, 16-byte IV) and `xxtea` **decrypt-only** fallback; `EncryptToJSON`/`DecryptFromJSON`,
  `EncryptUrlSafe`/`DecryptUrlSafe`; `internal/cryptox/cookies.go` with 4 KB splitting (`name~n`), `SameSite`,
  `Secure`, `HttpOnly`, configurable path.
* Done when: round-trip passes in both directions against PHP-produced fixtures for all three ciphers; a
  >4 KB cookie is split/reassembled exactly like PHP.

---

### Phase 1 — Session and service surface

#### Step 7 — Account model and tokens
* Goal: authenticate a request from cookies and expose the account to handlers.
* Read: `Model/Account.php`, `Model/MainAccount.php`, `Model/AdditionalAccount.php`,
  `Actions/UserAuth.php` (`getMainAccountFromToken`, `getAccountFromToken`, `SetAuthToken`,
  `SetAdditionalAuthToken`, `switchAccount`).
* Deliver: `internal/account` (models, `Hash()`, `CryptKey()`, token array (de)serialization incl. legacy numeric
  arrays) and `internal/auth` (`SessionToken`, `ConnectionToken`, `CSRFToken`, session-token existence check in
  `storage[SESSION]`, cookie clearing, `InvalidToken` (101) exceptions).
* Done when: a signature valid in PHP authenticates in Go; a stale `smsession` yields 101 and logs
  `SESSION_TOKEN value invalid`; `AdditionalAccount` selection mirrors PHP.

#### Step 8 — Login, logout, SSO, sign-me
* Goal: the full credential path.
* Read: `Actions/UserAuth.php` (`resolveLoginCredentials`, `LoginProcess`, `imapConnect`, sign-me helpers,
  `Logout`), `Actions/User.php` (`DoLogin`, `DoLogout`), `ServiceActions::ServiceSso`.
* Deliver: `auth.LoginProcess` with hooks `login.credentials.step-1`, `login.credentials.step-2`,
  `login.credentials`, `filter.account`, `login.success`; `default_domain`/`determine_user_domain` resolution;
  `login.fault_delay` sleep; `sign_me_auto`; language persisting on login; `DoLogin`/`DoLogout`/`DoSso`.
* Done when: `DoLogin` returns the same AppData payload as PHP for a valid account and 102 for a bad password
  (after the configured delay); sign-me cookie survives a restart and is refreshed.

#### Step 9 — JSON service dispatch
* Goal: the generic `?/Json` entry point with plugins and hooks.
* Read: `ServiceActions.php` (`ServiceJson`, `privateUpload`), `Actions.php`
  (`SetActionParams`, `normalizeActionParams`, `GetActionParam`, `HasActionParam`),
  `Actions/Response.php`, `Notifications.php`, `Exceptions/ClientException.php`.
* Deliver: `internal/handler` registry (`Register("Folders", …)`), `handler.Dispatcher` implementing the
  envelope, token/admin checks, `json.before-*`/`json.after-*` hooks, `DoPlugin*` lookup, `DefaultResponse`/
  `FalseResponse`/`TrueResponse`/`ExceptionResponse`, `responseObject`; plus the trivial `DoPing`, `DoVersion`.
* Done when: unknown action, bad body, missing token and `DoVersion` produce payloads identical to PHP;
  admin-only actions are refused without an admin session.

#### Step 10 — `AppData` (guest, user, admin)
* Goal: the single biggest contract for the frontend.
* Read: `Actions.php::AppData` (+ `Capa`, `etag`), `ActionsAdmin.php::AdminAppData`,
  `Enumerations/Capa.php`, `Enumerations/Layout.php`, `Enumerations/SignMeType.php`.
* Deliver: `service/appdata.go` building the exact key set for the three states (including `System.version`,
  `System.token`, `System.languages`, `webPath`, `webVersionPath`, `System.themes`,
  `StaticLibsJs`, `PluginsLink`, `Capa`, `attachmentLimit`, `phpUploadSizes`, `newMailSounds`, `signMe`,
  `DevEmail`/`DevPassword`, `clientLanguage`, `Theme`, `language`, `ContactsSync`, `mailToEmail`) and the user
  settings overlay read from `settings`/`settings_local`.
* Done when: sorted-key JSON diff against PHP is empty for all three states (with a fixed clock for `epoch`).

#### Step 11 — `Lang`, `Css`, `Plugins`, index page
* Goal: everything needed to render the app shell.
* Read: `ServiceActions.php` (`ServiceLang`, `ServiceCss`, `ServicePlugins`, `compileTemplates`),
  `Actions/Localization.php`, `Actions/Themes.php`, `Plugins/Manager.php`
  (`ReadLang`, `CompileCss`, `CompileJs`, `CompileTemplate`, `Hash`), `Utils::ClearHtmlOutput`,
  `Service.php` (index page half), `app/templates/Index.html`, `app/templates/Error.html`,
  `app/localization/langs.json`.
* Deliver: `internal/l10n` (language list, `validLanguage`, merge en→base→exact, plugin langs),
  `internal/themes` (discovery incl. `@custom`/`@nextcloud`, CSS assembly, `@{base}`/`url()` rewriting),
  `internal/pages` (Index/Error rendering with all `{{…}}` placeholders, CSP nonce injection, `sameSite`/`secure`
  substitution, template caching by the `TMPL:` key), and the three service endpoints
  (`?/Lang/0/{App,Admin}/<lang>/<ver>/`, `?/Css/…/Json/` → `[theme, css]`, `?/Plugins/0/{User,Admin}/<etag>/`).
* Done when: index HTML matches PHP apart from nonce and `?v=` mtimes; `Lang` JS text equals PHP's for `en` and
  `da`; `Css` JSON equals PHP's for `Gazellemail`.

#### Step 12 — Uploads and raw streaming
* Goal: file upload/download plumbing used by compose, contacts, background and message viewing.
* Read: `ServiceActions.php` (`privateUpload`, `ServiceUpload*`), `Actions.php::Upload`,
  `Actions/Themes.php::UploadBackground`, `Actions/Contacts.php::UploadContacts`,
  `Actions/Raw.php` (whole file), `snappymail/file/mimetype.php`.
* Deliver: `service/upload.go` (multipart `uploader` field, per-endpoint size limits from
  `webmail.attachment_size_limit`, `UploadError` codes, `filter.upload-response` hook), `service/raw.go`
  (`RawKey` encode/decode with account-hash validation, `RawView`, `RawDownload`, `RawViewThumbnail`,
  `RawViewAsPlain`, `RawUserBackground`, range requests, `Content-Disposition`, PDF MIME override,
  filename sanitising).
* Done when: an upload round-trips into the files provider with PHP-identical metadata; `RawDownload` returns
  206 for `Range: bytes=10-20` and identical headers.

#### Step 13 — Proxy, mailto, ping, test, SSO, CSP report
* Goal: the remaining small endpoints.
* Read: `ServiceActions.php` (`ServiceProxyExternal`, `ServiceMailto`, `ServicePing`, `ServiceTest`,
  `ServiceCspReport`), `snappymail/http/*` (`Request`, `CSP`, `SecFetch`), `Integrity::test`.
* Deliver: `service/misc.go`: external image proxy (only with `labs.use_local_proxy_for_external_images` and a
  logged-in account, max 2 redirects, `image/*` only, `X-Content-*` headers, cache by key), mailto cookie,
  `text/plain` ping, test report, SSO hash consumption (cache key, decrypt, login, redirect `./`), CSP report log.
* Done when: proxy 404s when disabled or when the upstream is not an image; `?/Sso?hash=…` logs in and the hash
  is single-use.

---

### Phase 2 — Mail core

#### Step 14 — IMAP client
* Goal: connect, negotiate and authenticate like `MailSo\Imap`.
* Read: `MailSo/Imap/ImapClient.php` (connect/login/capability parts), `MailSo/Imap/Settings.php`,
  `MailSo/Net/NetClient.php`, `MailSo/Net/LoginProcess.php`, `snappymail/sasl*`,
  `Account::ImapConnectAndLogin`.
* Deliver: `internal/imapclient`: settings (host/port/type/timeout/ssl/sasl list/shortLogin/lowerLogin/
  `disabled_capabilities`), TLS and STARTTLS with `ssl.*` config, capability filtering, SASL selection order,
  hooks `imap.before-connect`/`after-connect`/`before-login`/`after-login`, connection reuse per request.
* Done when: against the test server the negotiated capability set and the login result match PHP; a wrong
  password returns the same error class (`AuthError` 102).

#### Step 15 — Folders
* Goal: the whole folder surface.
* Read: `Actions/Folders.php`, `MailSo/Imap/Commands/Folders.php`, `MailSo/Imap/Folder.php`,
  `MailSo/Imap/Enumerations/MetadataKeys.php`, `Providers/Storage` (`CheckableFolder` setting).
* Deliver: `handler/folders.go` and `mail/folders.go` for `DoFolders`, `DoFolderCreate`, `DoFolderRename`,
  `DoFolderDelete`, `DoFolderClear`, `DoFolderInformation`, `DoFolderInformationMultiply`,
  `DoFolderSubscribe`, `DoFolderSetMetadata`, `DoFolderSettings`, `DoFolderCheckable`, `DoFolderACL`,
  `DoFolderDeleteACL`, `DoFolderSetACL`, `DoSystemFoldersUpdate`, `DoFolderAppend`.
* Done when: `DoFolders` JSON matches PHP for the test account (including `checkable`, special-use flags,
  unread/total counts) and renamed/deleted folders behave identically.

#### Step 16 — Message list
* Goal: paging, searching, sorting, threading, unread counts.
* Read: `Actions/Messages.php::DoMessageList`, `MailSo/Mail/MailClient.php` (list/search/sort parts),
  `MailSo/Imap/Commands/*` used for FETCH/SEARCH/SORT/THREAD, `MailSo/Base/Enumerations` (sort/search enums),
  `cache.server_uids` handling.
* Deliver: `mail/list.go` + `handler/messages_list.go` with `messages_per_page`, page/offset, search filters,
  `UseThreads`/`threadAlgorithm`, `listGrouped`, permanent filter, `folderSpecLimit`, UID cache.
* Done when: page 1 of INBOX, a paged request, and one search produce golden-equal JSON.

#### Step 17 — Message view and MIME
* Goal: one message, all parts, HTML/plain rendering.
* Read: `Actions/Messages.php::DoMessage`, `MailSo/Mail/Message.php`, `MailSo/Mime/*` (Message, Part, Header,
  Email, Parameter), `MailSo/Base/HtmlUtils.php`, `Actions/Response.php` (`responseObject` read-receipt branch).
* Deliver: `mail/message.go`, `handler/messages_view.go`, `mime/*` parser wrappers and plain/HTML conversion,
  inline-image resolution, `readReceipt` suppression via the receipt cache key, hooks `filter.result-message`,
  `filter.message-html`, `filter.message-plain`.
* Done when: JSON for a multipart HTML message with inline images and attachments is golden-equal, including
  `parts`, `attachments`, `flags`, `hasHtml`-style fields and the sanitised HTML body.

#### Step 18 — Flags, seen, delete, move, copy
* Goal: all message state mutations.
* Read: `Actions/Messages.php::DoMessageSetSeen`, `DoMessageSetSeenToAll`, `DoMessageSetFlagged`,
  `DoMessageSetDeleted`, `DoMessageSetKeyword`, `DoMessageDelete`, `DoMessageMove`, `DoMessageCopy`.
* Deliver: `handler/messages_actions.go` + `mail/flags.go`, honouring `capa.dangerous_actions`,
  `imap.use_expunge_all_on_delete`, COPYUID/MOVE presence and `use_force_selection`.
* Done when: sequences of (seen → flagged → move → delete) leave PHP-identical JSON and identical server state.

#### Step 19 — Save and send
* Goal: drafts and outgoing mail.
* Read: `Actions/Messages.php::DoSaveMessage`, `DoSendMessage`, `DoSendReadReceiptMessage`,
  `Actions/Accounts.php` (identity selection), `MailSo/Mime/Message.php` builder side, `smtpclient` contract,
  hooks `filter.build-message`, `filter.save-message`, `filter.send-message`, `filter.send-message-stream`,
  `filter.smtp-from`, `filter.smtp-hidden-rcpt`, `filter.message-rcpt`, `filter.build-read-receipt-message`.
* Deliver: `mime/build.go` (byte-compatible header order/folding, boundary prefix from `labs.boundary_prefix`,
  `X-Mailer` hiding via `security.hide_x_mailer_header`), `mail/send.go`, `handler/messages_send.go`
  (recipients validation → 303, save-to-Sent, draft deletion, MDN, DSN).
* Done when: the raw stream handed to SMTP is byte-identical to PHP's for the same compose payload, and
  `DoSendMessage` returns the same JSON.

#### Step 20 — Attachments and attachment actions
* Goal: compose attachments and the attachment toolbar.
* Read: `Actions/Messages.php::DoMessageUploadAttachments`, `Actions/Attachments.php`,
  `snappymail/attachmentsaction.php`, `capa.attachments_actions`.
* Deliver: `handler/attachments.go` (`DoAttachmentsActions` incl. `zip`), attachment upload into the files
  provider, `RawDownload` integration for uploaded file hashes.
* Done when: a zip of two attachments is byte-comparable-ish (entry names + contents) and the JSON matches.

#### Step 21 — Settings update, quota, suggestions, housekeeping
* Goal: the remaining session-scoped actions.
* Read: `Actions/User.php` (`DoSettingsUpdate`, `DoQuota`, `DoSuggestions`, `DoClearUserBackground`,
  `DoAppDelayStart`, `setSettingsFromParams`), `Providers/Suggestions.php`,
  `Providers/Suggestions/ISuggestions.php` (address-book hits; plugins may add drivers).
* Deliver: `handler/user.go` with the exact `setSettingsFromParams` type coercions/clamps (MessagesPerPage
  10..100, Layout 0..2, …), quota passthrough, suggestions (address book, deduplicated by lowercase e-mail,
  `min 5`/`max contacts.suggestions_limit`), background clearing, and the
  23h/25h GC timers using `NOBODY` storage key `Cache/Timers`.
* Done when: `DoSettingsUpdate` on a full parameter set writes the same `settings`/`settings_local` JSON as PHP;
  `DoQuota` returns `[used, total, …]` shape or `[0,0,0,0]`.

---

### Phase 3 — Features

#### Step 22 — Contacts
* Read: `Actions/Contacts.php`, `Providers/AddressBook/PdoAddressBook.php`, `Providers/AddressBook.php`,
  `contacts.*` config keys, `RawContactsVcf`/`RawContactsCsv`, `DoContactsSync`, `DoSaveContactsSyncData`,
  `DoTestContactsSyncData`.
* Deliver: `internal/contacts` (sqlite schema identical to the PHP one, CRUD, groups, search, sync data) and
  `handler/contacts.go` (`DoContacts`, `DoContactSave`, `DoContactsDelete`, `DoContactsSync`,
  `DoSaveContactsSyncData`, `DoTestContactsSyncData`), plus the two raw exports.
* Done when: pointing Go at a PHP-created `AddressBook.sqlite` lists/saves/deletes identically; CSV export
  matches PHP's column order.

#### Step 23 — Filters (Sieve)
* Read: `Actions/Filters.php`, `Providers/Filters/SieveStorage.php`, `MailSo/Sieve/*`,
  `Capabilities` (Sieve requires `SieveSettings.enabled`), RFC 5804 (ManageSieve protocol).
* Note: no external ManageSieve module is used — see [External dependencies](#external-dependencies);
  `sieveclient` implements the protocol itself and takes SASL `PLAIN`/`LOGIN` from `github.com/emersion/go-sasl`.
* Deliver: `internal/sieveclient` (connection + TLS/STARTTLS, capability negotiation, literal/quoted-string
  codec, SASL login, script CRUD) + `handler/filters.go` (`DoFilters`, `DoFiltersScriptSave`,
  `DoFiltersScriptActivate`, `DoFiltersScriptDelete`) and `Capa.Sieve` gating.
* Done when: script list/save/activate/delete against the test Sieve server matches PHP, and Sieve is reported
  unavailable when disabled in the domain config.

#### Step 24 — Identities and additional accounts
* Read: `Actions/Accounts.php` (whole file), `Providers/Identities/FileIdentities.php`,
  `Model/AdditionalAccount.php`, `Capa.ADDITIONAL_ACCOUNTS`/`IDENTITIES`.
* Deliver: `handler/accounts.go` (`DoAccountSetup`, `DoAccountUnread`, `DoAccountImport`, `DoAccountDelete`,
  `DoAccountSwitch`, `DoIdentityUpdate`, `DoIdentityDelete`, `DoAccountsAndIdentitiesSortOrder`,
  `DoAccountsAndIdentities`), identity JSON storage under `CONFIG` key `identities`, additional-account
  encryption using the main account's crypt key.
* Done when: two accounts + two identities survive a restart and `DoAccountSwitch` reproduces PHP's JSON
  (including the caldav plugin's `additionalaccounts` storage key write/read).

#### Step 25 — PGP
* Read: `Actions/Pgp.php`, `snappymail/pgp/*`, `cryptox` (key escrow), `security.gnupg`/`security.openpgp`.
* Deliver: `internal/pgp` + `handler/pgp.go` (`DoGetPGPKeys`, `DoPgpSearchKey`, `DoGnupg*`, `DoPgpImportKey`,
  `DoGetStoredPGPKeys`, `DoPgpStoreKeyPair`, `DoStorePGPKey`, `DoPgpVerifyMessage`), capabilities `GnuPG`/
  `OpenPGP`, and the compose-time hooks used by `DoSendMessage`.
* Done when: stored key pairs decrypt after a restart; sign+encrypt round-trip succeeds and `DoGetPGPKeys`
  matches PHP's shape.

#### Step 26 — S/MIME
* Read: `Actions/SMime.php`, `snappymail/smime/*`.
* Deliver: `internal/smime` + `handler/smime.go` (`DoGetSMimeCertificate`, `DoSMimeGetCertificates`,
  `DoSMimeCreateCertificate`, `DoSMimeExportPrivateKey`, `DoSMimeDecryptMessage`, `DoSMimeVerifyMessage`,
  `DoSMimeImportCertificate`, `DoSMimeImportCertificatesFromMessage`).
* Done when: certificate listing/import/export round-trips and decrypt/verify of a PHP-produced message works.

---

### Phase 4 — Admin panel

#### Step 27 — Admin core and domains
* Read: `ActionsAdmin.php` (whole file), `Actions/AdminDomains.php`, `Actions/AdminExtensions.php`,
  `snappymail/repository.php` (only the parts the admin UI needs; the remote repository can be stubbed),
  `Providers/Domain` (save/delete/disable).
* Deliver: `handler/admin.go` (`AdminAppData`, `DoAdminLogin`, `DoAdminLogout`, `DoAdminSettingsGet`,
  `DoAdminSettingsSet`, `DoAdminSettingsUpdate`, `DoAdminContactsTest`, `DoAdminPasswordUpdate`,
  `DoAdminClearCache`, `DoAdminInfo`, `DoAdminQRCode`), `handler/admin_domains.go` (domain CRUD,
  `DoAdminDomainTest`, `DoAdminDomainAutoconfig`, `DoAdminDomainMatch`), `handler/admin_extensions.go`
  (list installed plugins; enable/disable/install rejected as "build-time only" with a clear message),
  plus the admin session/token handling (`IsAdminLoggined`, TOTP).
* Done when: admin login/settings round-trip, a domain can be created/edited/deleted with PHP-identical files,
  and `AdminAppData` matches PHP with a sorted-key diff.

---

### Phase 5 — Plugin, hardening, verification

#### Step 28 — Plugin manager API
* Read: `Plugins/Manager.php`, `Plugins/AbstractPlugin.php`, `Plugins/Property.php`, `Config/Plugin.php`,
  `plugins/README.md` (hooks), `Actions.php::AppData` (`InitAppData`, `PluginsLink`).
* Deliver: `internal/plugin` (interface, registry, hook runner, asset/template/lang collectors, JSON-action
  registry, `Hash()`, per-plugin `configs/plugin-<name>.ini`, per-user `Plugins` settings),
  `internal/plugins/registry.go` importing `plugins/caldav` in the same way a build tag would.
* Done when: a no-op test plugin can add a JS/CSS/template/lang/JSON action and it appears in `PluginsLink`,
  `?/Plugins/…`, `?/Css/…`, the compiled templates and `AppData.Plugins`; `Hash()` changes when an asset mtime
  changes.

#### Step 29 — CalDAV plugin (part 1): configuration, discovery, HTTP client
* Read: `plugins/caldav/index.php` lines 1–545 (class header, `Init`, `configMapping`, `AutoConfigureCalDAV`,
  `getAdditionalAccounts`, `getCalendarConfig`/`readCalendarConfig`/`getDecryptedPassword`, `calendarUrl`,
  `resolveEventUrl`, `DoGetCalendars`, `parseCalendarsResponse`) plus lines 740–930 (`resolveCollectionUrl`,
  `isSameDavHost`, `normalizeCalendarColor`, `xmlText`, `slugifyCalendarName`, `uniqueCalendarId`,
  `calDAVMultistatusHasErrors`, `makeCalDAVRequest`), and `plugins/caldav/langs/*.json`.
* Deliver: `plugins/caldav/plugin.go` (name `Mailbux CalDAV Auto`, version 1.19, `UseLangs`, hooks
  `login.success`/`json.after-AccountSwitch`, the four `addJs` (`meeting.js`, `openstreetmap.js`,
  `calendar-dialog.js`, `message.js`) + `addCss('calendar.css')`, `configMapping` with `caldav_server`,
  `jmap_server`, `default_protocol`, `auto_sync`, `sync_interval`, `allow_invites`),
  `plugins/caldav/config.go` (the plugin's own `calendar_sync` storage blob, main-account crypt key,
  additional-account password conversion), `plugins/caldav/client.go` (Basic-auth HTTP client, explicit header
  list so non-PROPFIND requests do not inherit `Depth: 1`), `plugins/caldav/calendars.go` (discovery + URL
  helpers + `DoPluginGetCalendars` incl. the read-only `default` fallback).
* Done when: after login the `calendar_sync` blob exists with PHP-identical JSON; `GetCalendars` returns the
  same list against the mock CalDAV server; the fallback calendar appears on discovery failure.

#### Step 30 — CalDAV plugin (part 2): collections and event read/create
* Read: `plugins/caldav/index.php` lines 545–740 (`DoCreateCalendar`, `DoUpdateCalendar`, `DoDeleteCalendar`)
  and 930–1330 (`parseICalendar`, `parseICalDate`, `tzidToUtcBasic`, `DoGetCalendarEvents`,
  `parseCalDAVResponse` at 1974–2010, `DoCreateCalendarEvent`).
* Deliver: `plugins/caldav/ical.go` (unfold, TEXT escape/unescape, `parseICalDate` + TZID→UTC, ATTENDEE and
  ORGANIZER parsing), `plugins/caldav/events.go` (`REPORT` calendar-query, multistatus parsing, the event JSON
  shape consumed by `calendar-dialog.js`: `uid summary dtstart dtend description location allDay href etag rrule
  rdate exdate recurrenceId sequence status organizer organizerName attendees`), `plugins/caldav/calendars.go`
  additions for MKCALENDAR (`CreateCalendar`) / PROPPATCH (`UpdateCalendar`) / DELETE (`DeleteCalendar`).
* Done when: a fixture VCALENDAR set lists identically to PHP; creating an all-day and a timed event yields the
  same ICS bytes; a PROPPATCH multistatus containing a 4xx is reported as failure; a calendar can be created
  with a unique slug, renamed/recoloured and deleted.

#### Step 31 — CalDAV plugin (part 3): event update/delete and recurrence
* Read: `plugins/caldav/index.php` lines 1330–1974 (`DoUpdateCalendarEvent`, `DoDeleteCalendarEvent`,
  `escapeICS`, `buildNewEventIcs`, `buildVEventDetailLines`, `buildDatePropertyLines*`, `formatIcsDateValue`,
  `updateSeriesIcs`, `buildOccurrenceOverrideIcs`, `addExdateToSeries`, `getVEventBlocks`, `veventBlockKey`,
  `mergeRecurrenceOverride`).
* Deliver: `plugins/caldav/recurrence.go` (series rewrite that keeps `RRULE`/`EXDATE`/overrides, `RECURRENCE-ID`
  override upsert, `EXDATE` insertion, VEVENT block indexing/merging), `plugins/caldav/events.go` additions
  (`UpdateCalendarEvent` with `Mode=series|occurrence`, `DeleteCalendarEvent`, fetching the current resource
  first so unknown properties survive).
* Done when: editing a whole series preserves existing exclusions and overrides; editing/removing one
  occurrence writes a `RECURRENCE-ID` override / adds an `EXDATE`; the resulting resources round-trip through
  the mock server unchanged.

#### Step 32 — CalDAV plugin (part 4): invites, attendees, iTIP mail
* Read: `plugins/caldav/index.php` lines 2010–2500 (iCalendar import helpers: `unfoldIcs`, `parseInvite`,
  `inviteProp`, `inviteMailAddress`, `escapeICSText`/`unescapeICSText`, `convertTzidLine`, `prepareImportIcs`,
  `removeVtimezoneBlocks`, `parseAttendeeLines`, `parseOrganizerLine`, `parseAttendeeInput`,
  `escapeICSParamValue`, `buildAttendeeLines`, `readAttendeesParam`, `inviteAttendees`, `buildMethodIcs`) and
  2500–3050 (`DoGetContacts`, `DoSendInvitations`, `DoProcessEventReply`, `buildReplyIcs`,
  `DoImportCalendarEvent`, `DoRespondToEvent`, `DoRemoveCalendarEvent`, `sendInviteReply`, `sendCalendarMail`,
  `updateEventPartstat`), plus `plugins/caldav/message.js` for the expected JSON.
  (Tight on context: split at line 2500 into "import helpers" + "actions" if needed.)
* Deliver: `plugins/caldav/invites.go` (`GetContacts` via the address-book provider, `ImportCalendarEvent` with
  `METHOD` stripping + UTC conversion + override merge, `RespondToEvent` building `METHOD:REPLY`,
  `ProcessEventReply`, `RemoveCalendarEvent` series vs occurrence, PARTSTAT rewrite) and
  `plugins/caldav/mail.go` (multipart e-mail with a `text/calendar; method=…` attachment sent through
  `smtpclient`; `SMTP.usePhpMail` is reported as unsupported since PHP `mail()` has no Go equivalent).
* Done when: `SendInvitations` mails every non-organizer attendee with the server's current ICS copy;
  `RespondToEvent` sends a valid `METHOD:REPLY` and updates the stored PARTSTAT; a `METHOD:CANCEL` invite
  removes the event or the single occurrence; the invite box JSON matches what `message.js` expects.

#### Step 33 — Hardening, CSP, caching and the differential harness
* Read: `Service.php` (headers, CSP/`SnappyMail\HTTP\CSP`, `SecFetch`), `Actions::cacheByKey`/`verifyCacheByKey`/
  `etag`, `MailSo/Base/Http::ServerUseCache`/`ServerNoCache`/`checkETag`, `snappymail/http/*`.
* Deliver: full CSP builder with plugin hook `main.content-security-policy` and nonce injection, Sec-Fetch rule
  matcher (`security.secfetch_allow` default `site=same-origin;site=none`), HSTS, ETag/`Expires`/
  `Cache-Control` helpers used by `Css`/`Lang`/`Plugins`/`Raw`/`ProxyExternal`, HTTP→HTTPS redirect,
  graceful `503` for `max_sys_getloadavg`.
* Also deliver the harness: `make diff-record` (runs the PHP backend via `php -S` and the Go binary against
  copies of the same data folder, stores normalised responses under `go-backend/testdata/<step>/`),
  `make diff-test` (Go test that diffs normalised output: `epoch`, nonces, `Date`, mtimes and random ids
  masked), and CI wiring so both commands run on PRs.
* Done when: `make diff-test` is green for every endpoint exercised by Steps 9–13 and 27–32; a CSP report POST
  is logged; a second request with `If-None-Match` yields `304` on `Css`/`Lang`/`Plugins`.

---

### Deferred / explicitly out of scope

* Remote plugin installation and the SnappyMail repository client (`DoAdminPackageInstall`,
  `DoAdminPackageDelete`) — plugins are build-time only; the admin UI must report that clearly.
* `DoAdminUpgradeCore` and `DoAdminBackup` — core upgrades and backups stay a PHP/ops task.
* LESS compilation (`.less` themes/plugins) — plain CSS only for `Gazellemail` + `caldav`.
* CardDAV/DAV, OAuth2/SSO-provider and the remaining bundled plugins — not enabled for the Go backend.
* Byte-identical output is not required; where PHP output is non-deterministic (ordering of map keys, random
  ids, timestamps) the diff harness normalises it.
