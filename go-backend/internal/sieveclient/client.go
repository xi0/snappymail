// Package sieveclient ports the ManageSieve side of SnappyMail: the transport
// and command layer of MailSo\Sieve\SieveClient, the account helper
// RainLoop\Model\Account::SieveConnectAndLogin() and the provider of
// RainLoop\Providers\Filters (SieveStorage) that \RainLoop\Actions\Filters uses.
//
// ManageSieve (RFC 5804) is a small line based text protocol with literals and
// quoted strings, so the transport is hand-rolled: there is no maintained Go
// ManageSieve module (see SPEC.md). github.com/emersion/go-sasl is not used
// either, because only PLAIN and LOGIN are needed and their wire form is what
// the compatibility contract is about (the server sees the same AUTHENTICATE
// exchange PHP sends).
//
// The observable behaviour is ported as-is, including the odd parts:
//
//   - parseResponse() stops at a line that *starts* with OK or NO; a "BYE" does
//     not stop it, the loop just runs to EOF and then reports a negative
//     response, exactly like the PHP loop;
//   - a literal reply is inlined into the line that carries its "{N+}" marker,
//     which is what makes GetScript() return the script text without the
//     marker (a "{0+}" literal keeps its marker, and GetScript() drops that
//     first line instead);
//   - PutScript()/CheckScript() send the script as one raw line, so a CRLF is
//     appended to the literal octets, and an *empty* script is rejected because
//     sendRequest() refuses a blank request;
//   - the capability map is rebuilt on every capability response while the
//     SASL/SIEVE derived lists keep their previous value when a later response
//     does not advertise them again.
//
// Deviation (documented in the step notes): Login() skips the SCRAM-* entries
// of the SASL preference list. PHP selects one when the server advertises it
// and then throws LoginBadMethodException, because its SCRAM exchange is
// commented out; a server that advertises SASL SCRAM would make the whole
// filters page unusable, so those entries are skipped and the login falls
// through to PLAIN/LOGIN, which are the mechanisms this client can complete.
package sieveclient

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/logx"
)

// Connection and framing constants of MailSo\Net\NetClient.
const (
	// connectTimeout is NetClient::$iConnectTimeOut (10 seconds).
	connectTimeout = 10 * time.Second
	// minReadTimeout is the max(5, timeout) floor of stream_set_timeout().
	minReadTimeout = 5 * time.Second
	// logName is SieveClient::getLogName(): the log category and the "sieve."
	// prefix of the connect/login hooks.
	logName = "SIEVE"
)

// The framing failures of this package.
var (
	errNotConnected     = errors.New("sieveclient: not connected")
	errEmptyCredentials = errors.New("sieveclient: empty username or password")
)

// ErrorKind classifies a failure, because Providers\Filters::handleException()
// maps the MailSo exception classes onto different notification codes.
type ErrorKind int

// The MailSo exception classes handleException() distinguishes.
const (
	// KindNegative is MailSo\Sieve\Exceptions\NegativeResponseException (and
	// its Login* subclasses): the server answered NO/BYE or the exchange failed.
	KindNegative ErrorKind = iota
	// KindConnection is
	// MailSo\Net\Exceptions\SocketCanNotConnectToHostException.
	KindConnection
	// KindOther is any other failure (an invalid argument, a TLS problem).
	KindOther
)

// Error is the typed ManageSieve failure. Responses carries the protocol lines
// of a negative response, which is the additional message the frontend shows.
type Error struct {
	// Kind selects the notification code of Providers\Filters.
	Kind ErrorKind
	// Responses are the reply lines of a negative response.
	Responses []string
	// Err is the underlying transport/protocol failure.
	Err error
	// Message explains the failure when Err is nil.
	Message string
}

// Error implements error.
func (e *Error) Error() string {
	if nil == e {
		return "sieveclient: unknown error"
	}
	if nil != e.Err {
		return e.Err.Error()
	}
	if "" != e.Message {
		return e.Message
	}
	if 0 < len(e.Responses) {
		return "sieveclient: " + strings.Join(e.Responses, " | ")
	}
	return "sieveclient: unknown error"
}

// Unwrap exposes the underlying failure for errors.Is()/errors.As().
func (e *Error) Unwrap() error {
	if nil == e {
		return nil
	}
	return e.Err
}

// Client is MailSo\Sieve\SieveClient: one ManageSieve connection.
//
// A Client is scoped to one request and not safe for concurrent use.
type Client struct {
	// Settings is the effective MailSo\Sieve\Settings of the connection: the
	// per request copy of the domain configuration, so the connect/login hooks
	// can see and adjust host/port/type/timeout/sasl/authLiteral.
	Settings *domain.SieveSettings

	logger *logx.Logger

	conn   net.Conn
	reader *bufio.Reader

	readTimeout time.Duration
	encrypted   bool
	loggedIn    bool

	// capa is SieveClient::$aCapa: the upper cased capability tokens with their
	// value ("" when the capability has none).
	capa map[string]string
	// auth is SieveClient::$aAuth: the upper cased SASL mechanisms.
	auth []string
	// modules is SieveClient::$aModules: the lower cased Sieve extensions.
	modules []string
}

// New builds an unconnected client for settings. logger may be nil.
func New(settings *domain.SieveSettings, logger *logx.Logger) *Client {
	return &Client{
		Settings: settings,
		logger:   logger,
		capa:     map[string]string{},
	}
}

// Connect is SieveClient::Connect(): open the socket, read the greeting, then
// upgrade to TLS when the settings ask for it (or advertise STARTTLS while the
// type is AUTO_DETECT).
func (c *Client) Connect() error {
	if nil != c.conn {
		return nil
	}

	settings := c.Settings
	if nil == settings {
		return otherFailure(errors.New("sieveclient: missing settings"))
	}

	host := strings.TrimSpace(settings.Host)
	if "" == host || 0 > settings.Port || 65535 < settings.Port {
		return connectionFailure(errors.New("sieveclient: invalid host or port"))
	}

	implicitTLS := useSSL(settings.Port, settings.Type)
	if !implicitTLS && domain.SecuritySSL == settings.Type {
		return connectionFailure(errors.New("sieveclient: SSL is not supported"))
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

	lines, err := c.parseResponse()
	if nil != err {
		_ = c.Close()
		return err
	}
	c.parseStartupResponse(lines)

	if domain.SecuritySTARTTLS == settings.Type ||
		(domain.SecurityAutoDetect == settings.Type && c.HasCapability("STARTTLS")) {
		if err := c.startTLS(); nil != err {
			_ = c.Close()
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
	return connection.Handshake()
}

// startTLS is SieveClient::StartTLS(): require the capability, run STARTTLS,
// upgrade the socket and read the new capability list.
func (c *Client) startTLS() error {
	if !c.HasCapability("STARTTLS") {
		return connectionFailure(errors.New("sieveclient: STARTTLS is not supported"))
	}

	if err := c.sendRequestWithCheck("STARTTLS"); nil != err {
		return err
	}

	tlsConfig, err := tlsConfiguration(&c.Settings.SSL, strings.TrimSpace(c.Settings.Host))
	if nil != err {
		return connectionFailure(err)
	}

	secured := tls.Client(c.conn, tlsConfig)
	if err := c.handshake(secured); nil != err {
		return connectionFailure(err)
	}
	c.conn = secured
	c.reader = bufio.NewReader(secured)
	c.encrypted = true

	lines, err := c.parseResponse()
	if nil != err {
		return err
	}
	c.parseStartupResponse(lines)

	return nil
}

// Login is SieveClient::Login(): pick the first usable SASL mechanism the server
// advertises and run the exchange.
func (c *Client) Login(username, password string) error {
	if c.loggedIn {
		return nil
	}
	if "" == username || "" == password {
		return otherFailure(errEmptyCredentials)
	}

	mechanism := c.selectMechanism()
	if "" == mechanism {
		// RFC 5804 §2.2: a plaintext connection may retry over STARTTLS.
		if !c.encrypted && c.HasCapability("STARTTLS") {
			if err := c.startTLS(); nil != err {
				return err
			}
			return c.Login(username, password)
		}
		// PHP: writeLogException(new LoginException, LOG_ERR); a LoginException
		// is a NegativeResponseException without responses.
		return negativeFailure(nil, errors.New("sieveclient: no supported SASL mechanism"))
	}

	switch mechanism {
	case "PLAIN":
		return c.authPlain(username, password)
	case "LOGIN":
		return c.authLogin(username, password)
	}

	// OAUTHBEARER/XOAUTH2 need an access token the Go backend does not have.
	return negativeFailure(nil, fmt.Errorf("sieveclient: unsupported SASL mechanism %q", mechanism))
}

// selectMechanism is the SASL loop of SieveClient::Login(): the first configured
// mechanism the server advertises and this client can complete.
func (c *Client) selectMechanism() string {
	if nil == c.Settings || !c.HasCapability("SASL") {
		return ""
	}

	for _, candidate := range c.Settings.SASL {
		mechanism := strings.ToUpper(strings.TrimSpace(candidate))
		if !supports(mechanism) {
			continue
		}
		if c.SupportsAuthType(mechanism) {
			return mechanism
		}
	}

	return ""
}

// supports reports whether this client can complete the SASL mechanism. The
// SCRAM-* and OAuth mechanisms are deliberately excluded, see the package
// documentation.
func supports(mechanism string) bool {
	return "PLAIN" == mechanism || "LOGIN" == mechanism
}

// authPlain is the AUTHENTICATE "PLAIN" exchange, honouring authLiteral.
func (c *Client) authPlain(username, password string) error {
	payload := b64encode(saslPlain(username, password))

	if c.Settings.AuthLiteral {
		if err := c.sendRaw("AUTHENTICATE \"PLAIN\" {" + strconv.Itoa(len(payload)) + "+}"); nil != err {
			return err
		}
		if err := c.sendMasked(payload); nil != err {
			return err
		}
	} else {
		if err := c.sendRaw("AUTHENTICATE \"PLAIN\" \"" + payload + "\""); nil != err {
			return err
		}
	}

	lines, err := c.parseResponse()
	if nil != err {
		return loginFailure(err)
	}
	c.parseStartupResponse(lines)

	c.loggedIn = true
	return nil
}

// authLogin is the AUTHENTICATE "LOGIN" exchange. The server sends its
// challenges as literals; like PHP this client does not read them, it sends the
// two literals straight away.
func (c *Client) authLogin(username, password string) error {
	user := b64encode([]byte(username))
	pass := b64encode([]byte(password))

	for _, line := range []string{
		"AUTHENTICATE \"LOGIN\"",
		"{" + strconv.Itoa(len(user)) + "+}",
		user,
		"{" + strconv.Itoa(len(pass)) + "+}",
		pass,
	} {
		if err := c.sendMasked(line); nil != err {
			return err
		}
	}

	lines, err := c.parseResponse()
	if nil != err {
		return loginFailure(err)
	}
	c.parseStartupResponse(lines)

	c.loggedIn = true
	return nil
}

// Logout is SieveClient::Logout(): send LOGOUT and swallow a failing reply,
// which is what PHP does since issue #1455.
func (c *Client) Logout() {
	if !c.loggedIn {
		return
	}
	c.loggedIn = false

	if err := c.sendRequest("LOGOUT"); nil != err {
		c.logWarning("LOGOUT failed: %v", err)
		return
	}
	if _, err := c.parseResponse(); nil != err {
		c.logWarning("LOGOUT failed: %v", err)
	}
}

// Disconnect is NetClient::Disconnect(): LOGOUT when still logged in, then close
// the socket.
func (c *Client) Disconnect() {
	c.Logout()
	_ = c.Close()
}

// Close releases the socket without talking to the server.
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

// IsLoggedIn reports whether Login() succeeded.
func (c *Client) IsLoggedIn() bool { return nil != c.conn && c.loggedIn }

// Capability is SieveClient::Capability(): the capability map, fetched when it
// is empty or when force is set.
func (c *Client) Capability(force bool) (map[string]string, error) {
	if 0 == len(c.capa) || force {
		if err := c.sendRequest("CAPABILITY"); nil != err {
			return nil, err
		}
		lines, err := c.parseResponse()
		if nil != err {
			return nil, err
		}
		c.parseStartupResponse(lines)
	}
	return c.capa, nil
}

// HasCapability is SieveClient::hasCapability().
func (c *Client) HasCapability(name string) bool {
	_, ok := c.capa[strings.ToUpper(strings.TrimSpace(name))]
	return ok
}

// SupportsAuthType is SieveClient::supportsAuthType().
func (c *Client) SupportsAuthType(mechanism string) bool {
	needle := strings.ToUpper(strings.TrimSpace(mechanism))
	for _, advertised := range c.auth {
		if advertised == needle {
			return true
		}
	}
	return false
}

// Modules is SieveClient::Modules(): the Sieve extensions of the "SIEVE"
// capability, lower cased.
func (c *Client) Modules() []string {
	return append([]string(nil), c.modules...)
}

// ListScripts is SieveClient::ListScripts(): the script names with their active
// flag.
func (c *Client) ListScripts() (map[string]bool, error) {
	if err := c.sendRequest("LISTSCRIPTS"); nil != err {
		return nil, err
	}
	lines, err := c.parseResponse()
	if nil != err {
		return nil, err
	}

	result := make(map[string]bool, len(lines))
	for _, line := range lines {
		tokens := parseLine(line)
		if 0 == len(tokens) {
			continue
		}
		// PHP: 'ACTIVE' === substr($sLine, -6)
		result[tokens[0]] = strings.HasSuffix(line, "ACTIVE")
	}

	return result, nil
}

// GetScript is SieveClient::GetScript().
func (c *Client) GetScript(name string) (string, error) {
	if err := c.sendRequest("GETSCRIPT \"" + escapeName(name) + "\""); nil != err {
		return "", err
	}
	lines, err := c.parseResponse()
	if nil != err {
		return "", err
	}

	script := ""
	if 0 < len(lines) {
		// An empty literal keeps its "{0+}" marker on the line, so it is
		// dropped here (PHP: if ('{' === $aResponse[0][0]) array_shift()).
		if strings.HasPrefix(lines[0], "{") {
			lines = lines[1:]
		}

		if 0 < len(lines) {
			if last := lines[len(lines)-1]; strings.HasPrefix(last, "OK") || strings.HasPrefix(last, "NO") {
				lines = lines[:len(lines)-1]
			}
		}

		script = strings.Join(lines, "\r\n")
	}

	return script, nil
}

// PutScript is SieveClient::PutScript(): upload a script, replacing an existing
// one with the same name.
func (c *Client) PutScript(name, source string) error {
	source = normalizeCRLF(source)
	if err := c.sendRequest("PUTSCRIPT \"" + escapeName(name) + "\" {" + strconv.Itoa(len(source)) + "+}"); nil != err {
		return err
	}
	return c.sendRequestWithCheck(source)
}

// CheckScript is SieveClient::CheckScript().
func (c *Client) CheckScript(source string) error {
	source = normalizeCRLF(source)
	if err := c.sendRequest("CHECKSCRIPT {" + strconv.Itoa(len(source)) + "+}"); nil != err {
		return err
	}
	return c.sendRequestWithCheck(source)
}

// SetActiveScript is SieveClient::SetActiveScript(): an empty name disables the
// active script (RFC 5804 §2.7).
func (c *Client) SetActiveScript(name string) error {
	return c.sendRequestWithCheck("SETACTIVE \"" + escapeName(name) + "\"")
}

// DeleteScript is SieveClient::DeleteScript().
func (c *Client) DeleteScript(name string) error {
	return c.sendRequestWithCheck("DELETESCRIPT \"" + escapeName(name) + "\"")
}

// RenameScript is SieveClient::RenameScript().
func (c *Client) RenameScript(oldName, newName string) error {
	return c.sendRequestWithCheck(
		"RENAMESCRIPT \"" + escapeName(oldName) + "\" \"" + escapeName(newName) + "\"")
}

// Noop is SieveClient::Noop().
func (c *Client) Noop() error { return c.sendRequestWithCheck("NOOP") }

// sendRequest is SieveClient::sendRequest(): a blank request is an error.
func (c *Client) sendRequest(request string) error {
	if "" == strings.TrimSpace(request) {
		return otherFailure(errors.New("sieveclient: empty request"))
	}
	return c.sendRaw(request)
}

// sendRequestWithCheck is SieveClient::sendRequestWithCheck(): send and require
// a positive (OK) reply.
func (c *Client) sendRequestWithCheck(request string) error {
	if err := c.sendRequest(request); nil != err {
		return err
	}
	_, err := c.parseResponse()
	return err
}

// sendRaw is NetClient::sendRaw(): one CRLF terminated line.
func (c *Client) sendRaw(line string) error {
	if nil == c.conn {
		return connectionFailure(errNotConnected)
	}
	c.logDebug("> %s", line)
	c.setDeadline()
	if _, err := io.WriteString(c.conn, line+"\r\n"); nil != err {
		return connectionFailure(err)
	}
	return nil
}

// sendMasked is sendRaw() for a credential: the value is registered as a log
// secret first, so the log line only shows the mask
// (NetClient::logMask($sAuth) followed by sendRaw($sAuth) in PHP).
func (c *Client) sendMasked(secret string) error {
	if nil != c.logger && "" != secret {
		c.logger.AddSecret(secret)
	}
	return c.sendRaw(secret)
}

// parseResponse is SieveClient::parseResponse(): collect the reply lines up to
// the OK/NO status line, inlining literal payloads.
func (c *Client) parseResponse() ([]string, error) {
	var result []string

	for {
		raw, ok := c.readLine()
		if !ok {
			break
		}

		// PHP looks at the raw buffer, so a leading space would not count.
		end := strings.HasPrefix(raw, "OK") || strings.HasPrefix(raw, "NO")

		line := strings.TrimSpace(raw)
		if strings.HasSuffix(line, "}") {
			if position := strings.LastIndex(line, "{"); 0 <= position {
				if length := literalLength(line[position+1 : len(line)-1]); 0 < length {
					if literal, complete := c.readLiteral(length); complete {
						line = strings.TrimSpace(line[:position] + literal)
					}
				}
			}
		}

		result = append(result, line)

		if end {
			break
		}
	}

	c.logDebug("< %s", strings.Join(result, " | "))

	if 0 == len(result) || !strings.HasPrefix(result[len(result)-1], "OK") {
		return result, negativeFailure(result, nil)
	}

	return result, nil
}

// parseStartupResponse is SieveClient::parseStartupResponse().
//
// The capability map is rebuilt, the SASL/SIEVE derived lists are only replaced
// when the response advertises them again (PHP assigns them the same way).
func (c *Client) parseStartupResponse(lines []string) {
	c.capa = make(map[string]string, len(lines))

	for _, line := range lines {
		if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") {
			continue
		}

		tokens := parseLine(line)
		if 0 == len(tokens) || "" == tokens[0] {
			continue
		}

		token := strings.ToUpper(tokens[0])
		value := ""
		if 1 < len(tokens) {
			value = tokens[1]
		}
		c.capa[token] = value

		if 1 < len(tokens) {
			switch token {
			case "SASL":
				c.auth = strings.Split(strings.ToUpper(value), " ")
			case "SIEVE":
				c.modules = strings.Split(strings.ToLower(value), " ")
			}
		}
	}
}

// parseLine is SieveClient::parseLine(): the quoted strings of a capability or
// script line, or nil for a status line.
func parseLine(line string) []string {
	if strings.HasPrefix(line, "OK") || strings.HasPrefix(line, "NO") {
		return nil
	}
	values := quotedStrings(line)
	if 0 == len(values) {
		return nil
	}
	return values
}

// quotedStrings is preg_match_all('/(?:"((?:\\"|[^"])*)")/'): every double
// quoted string of the line, with the \" and \\ escapes resolved by
// stripcslashes().
func quotedStrings(line string) []string {
	var result []string

	for index := 0; index < len(line); index++ {
		if '"' != line[index] {
			continue
		}

		var builder strings.Builder
		closed := false

		position := index + 1
		for position < len(line) {
			character := line[position]
			if '\\' == character && position+1 < len(line) && '"' == line[position+1] {
				// The regex keeps the escaped quote inside the match.
				builder.WriteByte('\\')
				builder.WriteByte('"')
				position += 2
				continue
			}
			if '"' == character {
				closed = true
				position++
				break
			}
			builder.WriteByte(character)
			position++
		}

		if closed {
			result = append(result, stripCslashes(builder.String()))
			index = position - 1
		}
	}

	return result
}

// stripCslashes is PHP's stripcslashes() for the escapes that occur in a
// ManageSieve reply. Only \" and \\ are defined by RFC 5804, the C escapes are
// handled because PHP's stripcslashes() handles them.
func stripCslashes(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}

	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if '\\' != character || index+1 >= len(value) {
			builder.WriteByte(character)
			continue
		}

		index++
		switch value[index] {
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		case 'a':
			builder.WriteByte(7)
		case 'v':
			builder.WriteByte(11)
		case 'b':
			builder.WriteByte(8)
		case 'f':
			builder.WriteByte(12)
		default:
			// Like PHP, an unknown escape loses the backslash only.
			builder.WriteByte(value[index])
		}
	}

	return builder.String()
}

// literalLength is intval() of the "{30+}" length text.
func literalLength(text string) int {
	end := 0
	if 0 < len(text) && ('+' == text[0] || '-' == text[0]) {
		end = 1
	}
	start := end
	for end < len(text) && '0' <= text[end] && '9' >= text[end] {
		end++
	}
	if end == start {
		return 0
	}

	number, err := strconv.Atoi(text[:end])
	if nil != err {
		return 0
	}
	return number
}

// readLine is NetClient::getNextBuffer() for the line based reads: one CRLF
// terminated line. ok is false at EOF or on a socket error, exactly like fgets()
// returning false.
func (c *Client) readLine() (string, bool) {
	if nil == c.reader {
		return "", false
	}

	c.setDeadline()
	line, _ := c.reader.ReadString('\n')
	if "" == line {
		return "", false
	}

	// A partial line at EOF is still returned, like fgets() does.
	return line, true
}

// readLiteral reads exactly length bytes of a literal. complete is false when
// the peer closed the connection early, which leaves the "{N+}" marker in place
// like PHP's strlen() check does.
func (c *Client) readLiteral(length int) (string, bool) {
	if nil == c.reader {
		return "", false
	}

	buffer := make([]byte, length)
	c.setDeadline()
	read, err := io.ReadFull(c.reader, buffer)
	if nil != err {
		return string(buffer[:read]), false
	}
	return string(buffer[:read]), true
}

// setDeadline applies the max(5, timeout) read/write deadline of PHP's
// stream_set_timeout() to the next operation.
func (c *Client) setDeadline() {
	if 0 < c.readTimeout && nil != c.conn {
		_ = c.conn.SetDeadline(time.Now().Add(c.readTimeout))
	}
}

// readTimeout is stream_set_timeout(): max(5, timeout) seconds, or zero when no
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

// useSSL is ConnectionSecurityType::UseSSL(): implicit TLS for SSL and for
// AUTO_DETECT on the well known TLS ports.
func useSSL(port, securityType int) bool {
	result := securityType
	if domain.SecurityAutoDetect == securityType {
		switch port {
		case 993, 995, 465:
			result = domain.SecuritySSL
		}
	}
	return domain.SecuritySSL == result
}

// tlsConfiguration maps MailSo's SSLContext onto crypto/tls.
//
// Deviations documented for step 23: Go cannot "verify the chain but accept
// self signed leaves", so verify_peer/allow_self_signed disable verification,
// and a hashed capath directory is read as a list of PEM files.
func tlsConfiguration(settings *domain.SSL, host string) (*tls.Config, error) {
	config := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if nil == settings {
		return config, nil
	}

	if !settings.VerifyPeer || settings.AllowSelfSigned {
		config.InsecureSkipVerify = true
	}

	pool, err := rootPool(settings)
	if nil != err {
		return nil, err
	}
	if nil != pool {
		config.RootCAs = pool
	}

	if "" != strings.TrimSpace(settings.LocalCert) {
		certificate, loadErr := tls.LoadX509KeyPair(settings.LocalCert, settings.LocalCert)
		if nil != loadErr {
			return nil, loadErr
		}
		config.Certificates = []tls.Certificate{certificate}
	}

	return config, nil
}

// rootPool builds the CA pool from cafile and capath; nil keeps the system store.
func rootPool(settings *domain.SSL) (*x509.CertPool, error) {
	file := strings.TrimSpace(settings.CAFile)
	directory := strings.TrimSpace(settings.CAPath)
	if "" == file && "" == directory {
		return nil, nil
	}

	pool := x509.NewCertPool()

	if "" != file {
		if err := appendCertFile(pool, file); nil != err {
			return nil, err
		}
	}

	if "" != directory {
		entries, err := os.ReadDir(directory)
		if nil != err {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// A single unreadable/non-certificate file must not abort the whole
			// pool, mirroring OpenSSL's best effort directory hashing.
			_ = appendCertFile(pool, filepath.Join(directory, entry.Name()))
		}
	}

	return pool, nil
}

// appendCertFile adds the certificates of one PEM file to the pool.
func appendCertFile(pool *x509.CertPool, file string) error {
	content, err := os.ReadFile(file)
	if nil != err {
		return err
	}
	pool.AppendCertsFromPEM(content)
	return nil
}

// escapeName is addcslashes($name, '"\\').
func escapeName(value string) string {
	if !strings.ContainsAny(value, "\"\\") {
		return value
	}

	var builder strings.Builder
	for _, character := range value {
		if '"' == character || '\\' == character {
			builder.WriteByte('\\')
		}
		builder.WriteRune(character)
	}
	return builder.String()
}

// normalizeCRLF is preg_replace('/\r?\n/', "\r\n", $source): every LF becomes
// CRLF, a lone CR is left alone.
func normalizeCRLF(value string) string {
	var builder strings.Builder

	for index := 0; index < len(value); index++ {
		if '\r' == value[index] && index+1 < len(value) && '\n' == value[index+1] {
			builder.WriteString("\r\n")
			index++
			continue
		}
		if '\n' == value[index] {
			builder.WriteString("\r\n")
			continue
		}
		builder.WriteByte(value[index])
	}

	return builder.String()
}

// The error constructors.

func connectionFailure(err error) *Error {
	return &Error{Kind: KindConnection, Err: err}
}

func negativeFailure(responses []string, err error) *Error {
	return &Error{Kind: KindNegative, Responses: responses, Err: err}
}

func otherFailure(err error) *Error { return &Error{Kind: KindOther, Err: err} }

// loginFailure is the catch block of SieveClient::Login(): a negative response
// during authentication is a LoginBadCredentialsException, i.e. still a
// NegativeResponseException with the same responses.
func loginFailure(err error) error {
	var failure *Error
	if errors.As(err, &failure) && KindNegative == failure.Kind {
		return negativeFailure(failure.Responses, errors.New("sieveclient: login failed"))
	}
	return err
}

// logDebug writes a SnappyMail\Log debug line of the SIEVE category.
func (c *Client) logDebug(format string, args ...any) {
	if nil != c && nil != c.logger {
		c.logger.Writef(logx.Debug, logName, format, args...)
	}
}

// logWarning writes a SnappyMail\Log warning line of the SIEVE category.
func (c *Client) logWarning(format string, args ...any) {
	if nil != c && nil != c.logger {
		c.logger.Writef(logx.Warning, logName, format, args...)
	}
}
