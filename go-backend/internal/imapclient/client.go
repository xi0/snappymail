// Package imapclient ports the connection layer of MailSo\Imap\ImapClient:
// Connect()/StartTLS()/Login() on top of MailSo\Net\NetClient, the capability
// negotiation and filtering of ImapClient::setCapabilities()/hasCapability(),
// the SASL mechanism selection of ImapClient::Login(), and the account helper
// RainLoop\Model\Account::ImapConnectAndLogin() including its plugin hooks.
//
// Step 14 is deliberately limited to connect + capability + authentication; the
// command layer (folders, list, fetch, …) is built on the returned *Client in
// the following steps. A *Client is scoped to one request and meant to be reused
// for the commands of that request ("connection reuse per request"); it is not
// safe for concurrent use.
//
// The observable behaviour is ported as-is instead of being "improved":
//
//   - the STARTTLS upgrade only happens for type STARTTLS or for AUTO_DETECT when
//     the greeting advertises it (Connect()), and the capabilities are re-read
//     afterwards;
//   - a plain LOGIN command is used whenever the selected mechanism needs no SASL
//     exchange (the AUTH=LOGIN case of PHP degrades to LOGIN, not AUTHENTICATE);
//   - the disabled_capabilities removal is case sensitive, because PHP's
//     array_diff() is, and "SORT" is not rewritten to also drop "ESORT" during
//     negotiation (that only happens in the settings JSON);
//   - after a successful LOGIN the capability set of the login response replaces
//     the negotiated one, which usually clears it and re-fetches it lazily on the
//     next hasCapability(), exactly like PHP.
package imapclient

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// Connection and framing constants of MailSo\Net\NetClient.
const (
	// connectTimeout is NetClient::$iConnectTimeOut (10 seconds).
	connectTimeout = 10 * time.Second
	// minReadTimeout is the max(5, timeout) floor of stream_set_timeout().
	minReadTimeout = 5 * time.Second
	// defaultTagPrefix is ImapClient::$TAG_PREFIX.
	defaultTagPrefix = "TAG"
	// logName is ImapClient::getLogName() ("IMAP"); it is also the category of
	// the log lines and the "imap." prefix of the accounting hooks.
	logName = "IMAP"
)

// The framing errors of this package.
var (
	errNotConnected           = errors.New("imapclient: not connected")
	errEmptyResponse          = errors.New("imapclient: empty response")
	errUnexpectedContinuation = errors.New("imapclient: unexpected continuation")
	errEmptyCredentials       = errors.New("imapclient: empty username or password")
)

// responseKind classifies one protocol line.
type responseKind int

const (
	// respStatus is a status response (OK/NO/BAD/PREAUTH/BYE), tagged or not.
	respStatus responseKind = iota
	// respContinuation is a "+" continuation request (a SASL challenge).
	respContinuation
	// respData is any other untagged response ("* CAPABILITY …", "* LIST …").
	respData
)

// response is one parsed protocol line. Only the fields Step 14 needs are kept:
// the tag, the status/name, the (response-coded) arguments and the human
// readable text.
type response struct {
	tag      string
	kind     responseKind
	status   string   // OK/NO/BAD/PREAUTH/BYE when kind == respStatus
	name     string   // upper case atom for respData ("CAPABILITY", …)
	text     string   // human readable remainder / continuation payload
	args     []string // data arguments (e.g. capability tokens)
	code     string   // optional response code atom, upper case ("CAPABILITY", "ALERT")
	codeArgs []string // response code arguments
	// values holds the data tokens of a respData line. values[i] is what the
	// PHP code reads as ResponseList[i+1]: values[0] is the name ("LIST",
	// "STATUS", …) and the rest are the arguments, lists included.
	values []token
}

// statusError reports a status response with a failure status.
type statusError struct{ resp response }

// Error implements error.
func (e statusError) Error() string {
	return "imapclient: server responded " + e.resp.status + " " + strings.TrimSpace(e.resp.text)
}

// Client is one open IMAP connection: the Go counterpart of MailSo\Imap\ImapClient.
type Client struct {
	// Settings is the effective Imap\Settings of the connection. It is the
	// (per request) copy the account helper mixed application.ini into, so plugin
	// hooks can see and adjust host/port/type/timeout/sasl/disabled_capabilities.
	Settings *domain.IMAPSettings

	logger *logx.Logger

	conn   net.Conn
	reader *bufio.Reader

	tagPrefix string
	tagCount  int

	// capabilities: capaRaw is the negotiated set (nil when not fetched yet,
	// which is the "lazy CAPABILITY" trigger of PHP), capa the set after the
	// disabled_capabilities filter.
	capaRaw  []string
	capa     []string
	fetching bool

	readTimeout time.Duration
	encrypted   bool
	loggedIn    bool
	utf8        bool
	// username is Settings->username of the last Login(); Hash() uses it.
	username string
	// current is ImapClient::$oCurrentFolderInfo: the selected mailbox.
	current *FolderInformation
	// allMetadataFetched caches getAllMetadata() for the request.
	allMetadataFetched map[string]map[string]string
}

// New builds an unconnected client for settings. logger may be nil.
func New(settings *domain.IMAPSettings, logger *logx.Logger) *Client {
	return &Client{
		Settings:  settings,
		logger:    logger,
		tagPrefix: defaultTagPrefix,
	}
}

// Connect is ImapClient::Connect() + NetClient::Connect(): open the socket,
// read the greeting, seed the capabilities and, when configured, upgrade to TLS.
func (c *Client) Connect() error {
	if nil != c.conn {
		return nil
	}

	settings := c.Settings
	if nil == settings {
		return protocolFailure(errors.New("imapclient: missing settings"))
	}

	host := strings.TrimSpace(settings.Host)
	if "" == host || 0 > settings.Port || 65535 < settings.Port {
		return protocolFailure(errors.New("imapclient: invalid host or port"))
	}

	implicitTLS := useSSL(settings.Port, settings.Type)
	if !implicitTLS && domain.SecuritySSL == settings.Type {
		return connectionFailure(errors.New("imapclient: SSL is not supported"))
	}

	c.readTimeout = readTimeout(settings.Timeout)
	address := net.JoinHostPort(host, strconv.Itoa(settings.Port))
	dialer := &net.Dialer{Timeout: connectTimeout}

	if implicitTLS {
		tlsConfig, err := tlsConfiguration(&settings.SSL, host)
		if nil != err {
			return connectionFailure(err)
		}
		raw, err := dialer.Dial("tcp", address)
		if nil != err {
			return connectionFailure(err)
		}
		secured := tls.Client(raw, tlsConfig)
		c.logDebug("Start connection to \"%s\" (TLS)", address)
		if err := c.handshake(secured); nil != err {
			_ = raw.Close()
			return connectionFailure(err)
		}
		c.conn = secured
		c.encrypted = true
	} else {
		raw, err := dialer.Dial("tcp", address)
		if nil != err {
			return connectionFailure(err)
		}
		c.logDebug("Start connection to \"%s\"", address)
		c.conn = raw
	}

	c.reader = bufio.NewReader(c.conn)

	// Greeting: a single "* OK/PREAUTH/BYE …" line.
	segments, err := c.readLogicalLine()
	if nil != err {
		c.Close()
		return protocolFailure(err)
	}
	greeting := parseResponseLine(segments)
	if respStatus == greeting.kind {
		switch greeting.status {
		case "BYE":
			c.Close()
			return connectionFailure(fmt.Errorf("imapclient: server closed connection: %s", greeting.text))
		case "PREAUTH":
			// Already authenticated: skip Login() like an already logged in
			// connection (deviation: PHP would still run Login()).
			c.loggedIn = true
		}
	}

	c.applyCapabilities([]response{greeting})

	if domain.SecuritySTARTTLS == settings.Type ||
		(domain.SecurityAutoDetect == settings.Type && c.HasCapability("STARTTLS")) {
		if err := c.startTLS(); nil != err {
			c.Close()
			return err
		}
	}

	return nil
}

// handshake performs the TLS handshake under the read timeout.
func (c *Client) handshake(connection *tls.Conn) error {
	if 0 < c.readTimeout {
		_ = connection.SetDeadline(time.Now().Add(c.readTimeout))
	}
	if err := connection.Handshake(); nil != err {
		return err
	}
	return nil
}

// startTLS is ImapClient::StartTLS(): require the capability, run STARTTLS, then
// upgrade the socket and drop the now stale capabilities.
func (c *Client) startTLS() error {
	if !c.HasCapability("STARTTLS") {
		return connectionFailure(errors.New("imapclient: STARTTLS is not supported"))
	}

	replies, err := c.command("STARTTLS")
	if nil != err {
		return protocolFailure(err)
	}
	if _, err := expectOK(replies); nil != err {
		return err
	}

	settings := c.Settings
	tlsConfig, err := tlsConfiguration(&settings.SSL, strings.TrimSpace(settings.Host))
	if nil != err {
		return connectionFailure(err)
	}

	secured := tls.Client(c.conn, tlsConfig)
	if err := c.handshake(secured); nil != err {
		return protocolFailure(err)
	}
	c.conn = secured
	c.reader = bufio.NewReader(secured)
	c.encrypted = true

	// The capabilities advertised before STARTTLS must not be trusted.
	c.capaRaw = nil
	c.capa = nil

	return nil
}

// Login is ImapClient::Login(): select a SASL mechanism, authenticate and record
// the resulting capabilities.
func (c *Client) Login(username, password string) error {
	if c.loggedIn {
		return nil
	}
	if "" == username || "" == password {
		return protocolFailure(errEmptyCredentials)
	}

	mechanism := c.selectMechanism()
	if "" == mechanism {
		// RFC 3501 §6.2.3: a plaintext connection may retry over STARTTLS.
		if !c.encrypted && c.HasCapability("STARTTLS") {
			if err := c.startTLS(); nil != err {
				return err
			}
			return c.Login(username, password)
		}
		return protocolFailure(fmt.Errorf(
			"imapclient: no supported SASL mechanism found, remote server wants: %s",
			strings.Join(c.authMechanisms(), ", ")))
	}

	var (
		replies []response
		err     error
	)
	switch {
	case "CRAM-MD5" == mechanism:
		replies, err = c.authCramMD5(username, password)
	case "PLAIN" == mechanism || strings.HasPrefix(mechanism, "SCRAM-"):
		replies, err = c.authPlainScram(mechanism, username, password)
	default:
		// The AUTH=LOGIN mechanism (and the RFC 3501 LOGIN fallback) send the
		// LOGIN command, exactly like PHP's final else branch.
		replies, err = c.authLoginCommand(username, password)
	}
	if nil != err {
		return err
	}

	c.applyCapabilities(replies)

	// RFC 6855 / RFC 5738.
	if c.HasCapability("UTF8=ONLY") || c.HasCapability("UTF8=ACCEPT") {
		c.utf8 = true
		// PHP rethrows a failing ENABLE as bad credentials; a spurious ENABLE
		// failure must not turn an accepted login into an error, so it is only
		// logged here (documented deviation).
		if enableErr := c.enableUTF8(); nil != enableErr {
			c.logWarning("ENABLE UTF8=ACCEPT failed: %v", enableErr)
		}
	}

	c.loggedIn = true
	c.username = username
	return nil
}

// selectMechanism is the SASL loop of ImapClient::Login(): the first configured
// mechanism the server advertises and this client supports, then the LOGIN
// fallback, else "".
func (c *Client) selectMechanism() string {
	if nil == c.Settings {
		return ""
	}

	for _, mechanism := range c.Settings.SASL {
		if c.HasCapability("AUTH=" + mechanism) && saslSupported(mechanism) {
			return mechanism
		}
	}

	if containsExact(c.Settings.SASL, "LOGIN") && !c.HasCapability("LOGINDISABLED") {
		return "LOGIN"
	}

	return ""
}

// authMechanisms lists the AUTH= mechanisms the server offers, for the "no
// supported mechanism" error message.
func (c *Client) authMechanisms() []string {
	var result []string
	for _, capability := range c.Capability() {
		if name, value, found := strings.Cut(capability, "="); found && "AUTH" == name {
			result = append(result, value)
		}
	}
	return result
}

// authPlainScram authenticates with PLAIN (one shot) or a SCRAM-* mechanism
// (challenge/response), honouring SASL-IR.
func (c *Client) authPlainScram(mechanism, username, password string) ([]response, error) {
	var scram *scramClient
	var initial string

	if strings.HasPrefix(mechanism, "SCRAM-") {
		scram = newScram(mechanism, username, password)
		if nil == scram {
			return nil, protocolFailure(fmt.Errorf("imapclient: unsupported SASL mechanism %q", mechanism))
		}
		initial = b64encode(scram.clientFirst())
	} else {
		initial = b64encode(saslPlain(username, password))
	}

	hasSASLIR := c.HasCapability("SASL-IR")

	tag := c.nextTag()
	if hasSASLIR {
		if err := c.writeLine(tag + " AUTHENTICATE " + mechanism + " " + initial); nil != err {
			return nil, protocolFailure(err)
		}
	} else {
		if err := c.writeLine(tag + " AUTHENTICATE " + mechanism); nil != err {
			return nil, protocolFailure(err)
		}
		prompt, err := c.readReply(tag)
		if nil != err {
			return nil, protocolFailure(err)
		}
		if last := prompt[len(prompt)-1]; respContinuation != last.kind {
			return prompt, finalizeAuth(prompt)
		}
		if err := c.writeLine(initial); nil != err {
			return nil, protocolFailure(err)
		}
	}

	replies, err := c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}

	if nil == scram {
		return replies, finalizeAuth(replies)
	}

	last := replies[len(replies)-1]
	if respContinuation != last.kind {
		return replies, finalizeAuth(replies)
	}

	clientFinal, err := scram.clientFinal(last.text)
	if nil != err {
		return replies, protocolFailure(err)
	}
	if err := c.writeLine(clientFinal); nil != err {
		return nil, protocolFailure(err)
	}

	replies, err = c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}
	last = replies[len(replies)-1]
	if respContinuation != last.kind {
		return replies, finalizeAuth(replies)
	}

	if err := scram.verify(last.text); nil != err {
		return replies, protocolFailure(err)
	}
	if err := c.writeLine(""); nil != err {
		return nil, protocolFailure(err)
	}

	replies, err = c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}
	return replies, finalizeAuth(replies)
}

// authCramMD5 authenticates with the CRAM-MD5 challenge/response exchange.
func (c *Client) authCramMD5(username, password string) ([]response, error) {
	tag := c.nextTag()
	if err := c.writeLine(tag + " AUTHENTICATE CRAM-MD5"); nil != err {
		return nil, protocolFailure(err)
	}

	challenge, err := c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}
	last := challenge[len(challenge)-1]
	if respContinuation != last.kind {
		return challenge, finalizeAuth(challenge)
	}

	if err := c.writeLine(string(saslCramMD5(username, password, last.text))); nil != err {
		return nil, protocolFailure(err)
	}

	replies, err := c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}
	return replies, finalizeAuth(replies)
}

// authLoginCommand sends the plain IMAP LOGIN command.
func (c *Client) authLoginCommand(username, password string) ([]response, error) {
	tag := c.nextTag()
	command := tag + " LOGIN " + quoteString(username) + " " + quoteString(latin1(password))
	if err := c.writeLine(command); nil != err {
		return nil, protocolFailure(err)
	}

	replies, err := c.readReply(tag)
	if nil != err {
		return nil, protocolFailure(err)
	}
	return replies, finalizeAuth(replies)
}

// enableUTF8 is ImapClient::Enable("UTF8=ACCEPT").
func (c *Client) enableUTF8() error {
	replies, err := c.command("ENABLE", "UTF8=ACCEPT")
	if nil != err {
		return err
	}
	_, err = expectOK(replies)
	return err
}

// Capability is ImapClient::Capability(): the active capability set (after the
// disabled_capabilities filter), fetched lazily like PHP.
func (c *Client) Capability() []string {
	c.ensureCapabilities()
	return c.capa
}

// Capabilities is ImapClient::Capabilities(): the negotiated raw set (after the
// "unused capability" stripping but before disabled_capabilities).
func (c *Client) Capabilities() []string {
	c.ensureCapabilities()
	return c.capaRaw
}

// HasCapability is ImapClient::hasCapability().
func (c *Client) HasCapability(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if "" == name {
		return false
	}
	return containsExact(c.Capability(), name)
}

// CapabilityValue is ImapClient::CapabilityValue(): the value of an
// "NAME=value" capability, or ("", false).
func (c *Client) CapabilityValue(name string) (string, bool) {
	prefix := strings.ToUpper(strings.TrimSpace(name)) + "="
	for _, capability := range c.Capability() {
		if strings.HasPrefix(capability, prefix) {
			return capability[len(prefix):], true
		}
	}
	return "", false
}

// IsLoggedIn is ImapClient::IsLoggined().
func (c *Client) IsLoggedIn() bool { return nil != c.conn && c.loggedIn }

// Logout is ImapClient::Logout().
func (c *Client) Logout() error {
	if nil == c.conn || !c.loggedIn {
		return nil
	}
	c.loggedIn = false
	if _, err := c.command("LOGOUT"); nil != err {
		return protocolFailure(err)
	}
	return nil
}

// Close releases the socket.
func (c *Client) Close() error {
	if nil == c.conn {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.reader = nil
	c.loggedIn = false
	return err
}

// ensureCapabilities fetches CAPABILITY once when no set was negotiated yet, the
// lazy behaviour of ImapClient::hasCapability().
func (c *Client) ensureCapabilities() {
	if nil != c.capaRaw || c.fetching || nil == c.conn {
		return
	}
	c.fetching = true
	defer func() { c.fetching = false }()

	replies, err := c.command("CAPABILITY")
	if nil != err {
		// hasCapability() throws in PHP; reporting "not supported" and letting
		// the caller surface the real failure is enough here.
		c.logDebug("CAPABILITY failed: %v", err)
		return
	}
	c.applyCapabilities(replies)
}

// applyCapabilities is ImapClient::setCapabilities(): store the raw set, strip
// the unused capabilities, then apply disabled_capabilities.
func (c *Client) applyCapabilities(replies []response) {
	list := capabilityResult(replies)
	if 0 == len(list) {
		c.capaRaw = nil
		c.capa = nil
		return
	}

	list = removeValues(list, "PREVIEW=FUZZY", "SNIPPET=FUZZY", "SORT=DISPLAY")
	c.capaRaw = list

	active := list
	if nil != c.Settings && 0 < len(c.Settings.DisabledCapabilities) {
		active = removeValues(active, c.Settings.DisabledCapabilities...)
		if containsExact(c.Settings.DisabledCapabilities, "THREAD") {
			active = removePrefix(active, "THREAD=")
		}
	}
	c.capa = active
}

// capabilityResult is ResponseCollection::getCapabilityResult(): the tokens of
// the first "* CAPABILITY …" or "* … [CAPABILITY …]" response, upper cased.
func capabilityResult(replies []response) []string {
	for _, reply := range replies {
		if respData == reply.kind && "CAPABILITY" == reply.name && 0 < len(reply.args) {
			return upperAll(reply.args)
		}
		if respStatus == reply.kind && "CAPABILITY" == reply.code && 0 < len(reply.codeArgs) {
			return upperAll(reply.codeArgs)
		}
	}
	return nil
}

// nextTag is ImapClient::getNewTag(): TAG1, TAG2, …
func (c *Client) nextTag() string {
	c.tagCount++
	return c.tagPrefix + strconv.Itoa(c.tagCount)
}

// command sends "<tag> <name> <args…>" and reads the reply up to the tag.
func (c *Client) command(name string, args ...string) ([]response, error) {
	if nil == c.conn {
		return nil, errNotConnected
	}

	tag := c.nextTag()
	parts := make([]string, 0, 2+len(args))
	parts = append(parts, tag, name)
	parts = append(parts, args...)

	if err := c.writeLine(strings.Join(parts, " ")); nil != err {
		return nil, err
	}
	return c.readReply(tag)
}

// readReply reads lines until the tagged response of tag or a continuation.
func (c *Client) readReply(tag string) ([]response, error) {
	replies := make([]response, 0, 4)
	for {
		segments, err := c.readLogicalLine()
		if nil != err {
			return replies, err
		}
		reply := parseResponseLine(segments)
		replies = append(replies, reply)
		if "+" == reply.tag || tag == reply.tag {
			return replies, nil
		}
	}
}

// writeLine sends one line terminated with CRLF under the read timeout.
func (c *Client) writeLine(line string) error {
	if nil == c.conn {
		return errNotConnected
	}
	c.setDeadline()
	if _, err := io.WriteString(c.conn, line+"\r\n"); nil != err {
		return err
	}
	return nil
}

// readLine reads one CRLF terminated line without the terminator.
func (c *Client) readLine() (string, error) {
	if nil == c.reader {
		return "", errNotConnected
	}
	c.setDeadline()
	line, err := c.reader.ReadString('\n')
	if nil != err {
		return line, err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// setDeadline applies the max(5, timeout) read/write deadline of PHP's
// stream_set_timeout() to the next operation.
func (c *Client) setDeadline() {
	if 0 < c.readTimeout && nil != c.conn {
		_ = c.conn.SetDeadline(time.Now().Add(c.readTimeout))
	}
}

// isStatusWord reports whether word is one of the RFC 3501 status responses.
func isStatusWord(word string) bool {
	switch word {
	case "OK", "NO", "BAD", "PREAUTH", "BYE":
		return true
	}
	return false
}

// finalizeAuth turns the final reply of an authentication command into an error:
// a failure status is a bad-credentials failure (LoginBadCredentialsException),
// with the [ALERT] text as the additional message.
func finalizeAuth(replies []response) error {
	last, err := expectOK(replies)
	if nil == err {
		return nil
	}
	var protocol *auth.MailError
	if errors.As(err, &protocol) && auth.MailErrorOther == protocol.Kind {
		// A status failure during authentication is a credentials failure.
		return credentialFailure(alertText(last), statusError{last})
	}
	return err
}

// expectOK validates a reply collection: the last response must be a status
// response with status OK.
func expectOK(replies []response) (response, error) {
	if 0 == len(replies) {
		return response{}, protocolFailure(errEmptyResponse)
	}
	last := replies[len(replies)-1]
	if respContinuation == last.kind {
		return last, protocolFailure(errUnexpectedContinuation)
	}
	if "OK" != last.status {
		return last, protocolFailure(statusError{last})
	}
	return last, nil
}

// alertText is NegativeResponseException::getAlertFromStatus(): the human
// readable text of a status response that carries an [ALERT] response code.
func alertText(reply response) string {
	if "ALERT" == reply.code {
		return strings.TrimSpace(reply.text)
	}
	return ""
}

// The error constructors map onto the MailSo exception classes imapConnect()
// distinguishes: ConnectionException → ConnectionError (104), everything else →
// AuthError (102), and LoginBadCredentialsException → AuthError with an alert.
func connectionFailure(err error) *auth.MailError {
	return &auth.MailError{Kind: auth.MailErrorConnection, Err: err}
}

func credentialFailure(alert string, err error) *auth.MailError {
	return &auth.MailError{Kind: auth.MailErrorBadCredentials, Alert: alert, Err: err}
}

func protocolFailure(err error) *auth.MailError {
	return &auth.MailError{Kind: auth.MailErrorOther, Err: err}
}

// readTimeout is stream_set_timeout(): max(5, timeout) seconds, but zero when no
// timeout is configured (tests).
func readTimeout(timeout int) time.Duration {
	if 0 >= timeout {
		return 0
	}
	base := time.Duration(timeout) * time.Second
	if minReadTimeout > base {
		return minReadTimeout
	}
	return base
}

// quoteString is ImapClient::EscapeString(): a quoted string with '\' and '"'
// escaped.
func quoteString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, character := range value {
		if '\\' == character || '"' == character {
			builder.WriteByte('\\')
		}
		builder.WriteRune(character)
	}
	builder.WriteByte('"')
	return builder.String()
}

// latin1 is mb_convert_encoding(value, 'ISO-8859-1', 'UTF-8') for the LOGIN
// command: runes above U+00FF become '?'.
func latin1(value string) string {
	needsConversion := false
	for _, character := range value {
		if 0xFF < character {
			needsConversion = true
			break
		}
	}
	if !needsConversion {
		return value
	}

	var builder strings.Builder
	for _, character := range value {
		if 0xFF < character {
			builder.WriteByte('?')
			continue
		}
		builder.WriteByte(byte(character))
	}
	return builder.String()
}

// containsExact reports whether values contains needle (case sensitive, like
// PHP's in_array with the default loose=false on strings).
func containsExact(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// removeValues is array_diff(): every value that is not in removes, order kept.
func removeValues(values []string, removes ...string) []string {
	drop := make(map[string]struct{}, len(removes))
	for _, remove := range removes {
		drop[remove] = struct{}{}
	}

	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := drop[value]; ok {
			continue
		}
		result = append(result, value)
	}
	return result
}

// removePrefix is array_filter(str_starts_with()): drop the values that start
// with prefix.
func removePrefix(values []string, prefix string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			continue
		}
		result = append(result, value)
	}
	return result
}

// upperAll is array_map('strtoupper', …).
func upperAll(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToUpper(value)
	}
	return result
}

// logDebug writes a SnappyMail\Log debug line of the IMAP category.
func (c *Client) logDebug(format string, args ...any) {
	if nil != c.logger {
		c.logger.Writef(logx.Debug, logName, format, args...)
	}
}

// logWarning writes a SnappyMail\Log warning line of the IMAP category.
func (c *Client) logWarning(format string, args ...any) {
	if nil != c.logger {
		c.logger.Writef(logx.Warning, logName, format, args...)
	}
}
