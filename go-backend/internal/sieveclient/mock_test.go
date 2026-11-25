package sieveclient

// Test helpers: a small in-process ManageSieve server (RFC 5804) that speaks
// just enough of the protocol (greeting, CAPABILITY, STARTTLS, AUTHENTICATE
// PLAIN/LOGIN with literals and quoted strings, LISTSCRIPTS, GETSCRIPT,
// PUTSCRIPT, SETACTIVE, DELETESCRIPT, LOGOUT) to exercise the client against a
// real socket.

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// mockModules is the "SIEVE" capability value of the mock, deliberately
// unsorted so the Load() sort can be observed.
const mockModules = "vacation fileinto reject imap4flags body regex"

// mockSieve is a minimal ManageSieve server for the tests.
type mockSieve struct {
	listener net.Listener
	port     int

	user string
	pass string

	tlsConfig *tls.Config
	// hideSASL hides the SASL capability until the connection is encrypted,
	// which is the usual "STARTTLS before AUTH" server configuration.
	hideSASL bool
	// refuse answers NO to the named command.
	refuse string

	mu          sync.Mutex
	received    []string
	tlsUpgraded bool
	scripts     map[string]string
	order       []string
	active      string
}

// newMockSieve starts a server with the credentials user@example.com / secret
// and the scripts "rainloop.user" (active) and "other".
func newMockSieve(t *testing.T) *mockSieve {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &mockSieve{
		listener: listener,
		port:     listener.Addr().(*net.TCPAddr).Port,
		user:     "user@example.com",
		pass:     "secret",
		scripts: map[string]string{
			"rainloop.user": "require [\"fileinto\"];\r\nkeep;\r\n",
			"other":         "discard;\r\n",
		},
		order:  []string{"other", "rainloop.user"},
		active: "rainloop.user",
	}

	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })

	return server
}

// account builds the account the provider uses, pointing at the mock. enabled
// mirrors the domain's SieveSettings.enabled flag.
func (m *mockSieve) account(enabled bool) *account.Account {
	config := domain.New("example.com", domain.Defaults{})
	config.Sieve.Host = "127.0.0.1"
	config.Sieve.Port = m.port
	config.Sieve.Type = domain.SecurityNone
	config.Sieve.Timeout = 5
	config.Sieve.Enabled = enabled
	config.Sieve.SSL.VerifyPeer = false

	main := account.NewMainAccount()
	main.SetCredentials(config, "user@example.com", "user@example.com", "secret", "", "")
	return &main.Account
}

// acceptLoop accepts connections until the listener is closed.
func (m *mockSieve) acceptLoop() {
	for {
		connection, err := m.listener.Accept()
		if nil != err {
			return
		}
		go m.handle(connection)
	}
}

// record stores one received client line.
func (m *mockSieve) record(line string) {
	m.mu.Lock()
	m.received = append(m.received, line)
	m.mu.Unlock()
}

// commands returns a copy of every client line the server received.
func (m *mockSieve) commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.received...)
}

// hasCommand reports whether a received line contains needle.
func (m *mockSieve) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range m.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// wasTLSUpgraded reports whether a STARTTLS upgrade happened.
func (m *mockSieve) wasTLSUpgraded() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tlsUpgraded
}

// script returns the stored body of a script.
func (m *mockSieve) script(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.scripts[name]
	return body, ok
}

// activeScript returns the name of the active script ("" for none).
func (m *mockSieve) activeScript() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// handle serves one connection.
func (m *mockSieve) handle(connection net.Conn) {
	defer connection.Close()

	current := connection
	reader := bufio.NewReader(current)

	writeLine := func(line string) {
		_, _ = io.WriteString(current, line+"\r\n")
	}
	writeLiteral := func(payload string) {
		writeLine("{" + strconv.Itoa(len(payload)) + "+}")
		_, _ = io.WriteString(current, payload)
		// RFC 5804: the literal octets are followed by CRLF.
		_, _ = io.WriteString(current, "\r\n")
	}
	sendCapabilities := func(encrypted bool) {
		writeLine(`"IMPLEMENTATION" "Mock ManageSieve"`)
		writeLine(`"SIEVE" "` + mockModules + `"`)
		if encrypted || !m.hideSASL {
			writeLine(`"SASL" "PLAIN LOGIN"`)
		}
		if !encrypted && nil != m.tlsConfig {
			writeLine(`"STARTTLS"`)
		}
		writeLine(`"VERSION" "1.0"`)
		writeLine(`OK "Mock ready."`)
	}

	sendCapabilities(false)

	encrypted := false
	authenticated := false

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		m.record(line)

		command := strings.ToUpper(line)
		if "" != m.refuse && strings.HasPrefix(command, strings.ToUpper(m.refuse)) {
			writeLine(`NO "` + m.refuse + ` refused by the test"`)
			continue
		}

		switch {
		case strings.HasPrefix(command, "CAPABILITY"):
			sendCapabilities(encrypted)

		case strings.HasPrefix(command, "NOOP"):
			writeLine(`OK "NOOP completed"`)

		case strings.HasPrefix(command, "STARTTLS"):
			if nil == m.tlsConfig {
				writeLine(`NO "TLS not configured"`)
				continue
			}
			writeLine(`OK "Begin TLS negotiation now"`)
			secured := tls.Server(current, m.tlsConfig)
			if err := secured.Handshake(); nil != err {
				return
			}
			m.mu.Lock()
			m.tlsUpgraded = true
			m.mu.Unlock()
			current = secured
			reader = bufio.NewReader(current)
			encrypted = true
			sendCapabilities(true)

		case strings.HasPrefix(command, "AUTHENTICATE"):
			authenticated = m.authenticate(reader, writeLine, writeLiteral, line)
			if authenticated {
				writeLine(`OK "Authentication successful"`)
			} else {
				writeLine(`NO "Authentication failed"`)
			}

		case strings.HasPrefix(command, "LISTSCRIPTS"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			m.mu.Lock()
			for _, name := range m.order {
				if name == m.active {
					writeLine(`"` + name + `" ACTIVE`)
					continue
				}
				writeLine(`"` + name + `"`)
			}
			m.mu.Unlock()
			writeLine(`OK "Listscripts completed"`)

		case strings.HasPrefix(command, "GETSCRIPT"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			if body, ok := m.script(firstQuoted(line)); ok {
				writeLiteral(body)
				writeLine(`OK "Getscript completed"`)
				continue
			}
			writeLine(`NO "Script does not exist"`)

		case strings.HasPrefix(command, "PUTSCRIPT"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			name := firstQuoted(line)
			length, convErr := strconv.Atoi(strings.TrimSuffix(lastBrace(line), "+"))
			if nil != convErr {
				writeLine(`NO "Malformed literal"`)
				continue
			}
			buffer := make([]byte, length)
			if _, err := io.ReadFull(reader, buffer); nil != err {
				return
			}
			m.record(string(buffer))
			if _, err := reader.ReadString('\n'); nil != err {
				return
			}
			m.mu.Lock()
			if _, exists := m.scripts[name]; !exists {
				m.order = append(m.order, name)
			}
			m.scripts[name] = string(buffer)
			m.mu.Unlock()
			writeLine(`OK "Putscript completed"`)

		case strings.HasPrefix(command, "SETACTIVE"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			activated := firstQuoted(line)
			if "" != activated {
				if _, ok := m.script(activated); !ok {
					writeLine(`NO "Script does not exist"`)
					continue
				}
			}
			m.mu.Lock()
			m.active = activated
			m.mu.Unlock()
			writeLine(`OK "Setactive completed"`)

		case strings.HasPrefix(command, "DELETESCRIPT"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			name := firstQuoted(line)
			m.mu.Lock()
			_, exists := m.scripts[name]
			delete(m.scripts, name)
			for index, script := range m.order {
				if script == name {
					m.order = append(m.order[:index], m.order[index+1:]...)
					break
				}
			}
			if m.active == name {
				m.active = ""
			}
			m.mu.Unlock()
			if !exists {
				writeLine(`NO "Script does not exist"`)
				continue
			}
			writeLine(`OK "Deletescript completed"`)

		case strings.HasPrefix(command, "CHECKSCRIPT"):
			if !authenticated {
				writeLine(`NO "Authenticate first"`)
				continue
			}
			length, convErr := strconv.Atoi(strings.TrimSuffix(lastBrace(line), "+"))
			if nil != convErr {
				writeLine(`NO "Malformed literal"`)
				continue
			}
			buffer := make([]byte, length)
			if _, err := io.ReadFull(reader, buffer); nil != err {
				return
			}
			m.record(string(buffer))
			if _, err := reader.ReadString('\n'); nil != err {
				return
			}
			writeLine(`OK "Checkscript completed"`)

		case strings.HasPrefix(command, "LOGOUT"):
			writeLine(`OK "Logout completed"`)
			return

		default:
			writeLine(`NO "Unknown command"`)
		}
	}
}

// authenticate handles the AUTHENTICATE command of one connection.
func (m *mockSieve) authenticate(
	reader *bufio.Reader,
	writeLine func(string),
	writeLiteral func(string),
	line string,
) bool {
	// One "<length line>" + octets + CRLF, as the client sends a literal.
	readLiteral := func() (string, bool) {
		lengthLine, err := reader.ReadString('\n')
		if nil != err {
			return "", false
		}
		m.record(strings.TrimRight(lengthLine, "\r\n"))

		length, convErr := strconv.Atoi(strings.TrimSuffix(lastBrace(lengthLine), "+"))
		if nil != convErr {
			return "", false
		}
		buffer := make([]byte, length)
		if _, err := io.ReadFull(reader, buffer); nil != err {
			return "", false
		}
		m.record(string(buffer))
		if _, err := reader.ReadString('\n'); nil != err {
			return "", false
		}
		return string(buffer), true
	}

	upper := strings.ToUpper(line)

	switch {
	case strings.HasPrefix(upper, `AUTHENTICATE "PLAIN"`):
		// The quoted form carries the payload on the same line.
		if index := strings.Index(line, `"PLAIN" "`); 0 <= index {
			payload := strings.TrimSuffix(line[index+len(`"PLAIN" "`):], `"`)
			return m.checkPlain(payload)
		}

		length, convErr := strconv.Atoi(strings.TrimSuffix(lastBrace(line), "+"))
		if nil != convErr {
			return false
		}
		buffer := make([]byte, length)
		if _, err := io.ReadFull(reader, buffer); nil != err {
			return false
		}
		m.record(string(buffer))
		if _, err := reader.ReadString('\n'); nil != err {
			return false
		}
		return m.checkPlain(string(buffer))

	case strings.HasPrefix(upper, `AUTHENTICATE "LOGIN"`):
		// The server asks for the user name with a literal challenge; the
		// client (like PHP) ignores it and sends both literals at once.
		writeLiteral("VXNlcm5hbWU6")
		username, okUser := readLiteral()
		if !okUser {
			return false
		}
		password, okPass := readLiteral()
		if !okPass {
			return false
		}
		return decode(username) == m.user && decode(password) == m.pass
	}

	// Any other mechanism is refused.
	writeLine(`NO "Unsupported mechanism"`)
	return false
}

// checkPlain validates a base64 PLAIN initial response.
func (m *mockSieve) checkPlain(payload string) bool {
	fields := strings.Split(decode(payload), "\x00")
	if 3 != len(fields) {
		return false
	}
	return fields[1] == m.user && fields[2] == m.pass
}

// decode is base64_decode() for the test server.
func decode(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if nil != err {
		return ""
	}
	return string(decoded)
}

// autoTLSConfig builds a self signed certificate for the STARTTLS test server.
func autoTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if nil != err {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if nil != err {
		t.Fatalf("create certificate: %v", err)
	}

	return &tls.Config{Certificates: []tls.Certificate{{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}}}
}

// firstQuoted returns the first quoted string of a command line.
func firstQuoted(line string) string {
	values := quotedStrings(line)
	if 0 == len(values) {
		return ""
	}
	return values[0]
}

// lastBrace returns the text between the last '{' and the closing '}'.
func lastBrace(line string) string {
	open := strings.LastIndex(line, "{")
	if 0 > open || !strings.HasSuffix(strings.TrimSpace(line), "}") {
		return ""
	}
	line = strings.TrimSpace(line)
	return line[open+1 : len(line)-1]
}
