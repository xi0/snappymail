package mail

// Tests of Step 17: MailClient::Message() and the body decoding of
// Message::fromFetchResponse(), against an in-process IMAP server.

import (
	"bufio"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// The MIME layout of the test message: multipart/mixed of a
// multipart/alternative (text/plain + text/html), an inline PNG and a PDF
// attachment.
//
// Part ids: 1.1 = text/plain, 1.2 = text/html, 2 = inline image, 3 = attachment.
const viewBodyStructure = `(` +
	`(("text" "plain" ("charset" "utf-8") NIL NIL "quoted-printable" 30 1 NIL NIL NIL NIL)` +
	` ("text" "html" ("charset" "iso-8859-1") NIL NIL "base64" 40 1 NIL NIL NIL NIL)` +
	` "alternative" ("boundary" "alt1") NIL NIL NIL)` +
	` ("image" "png" ("name" "pixel.png") "<pixel@example.com>" NIL "base64" 100 NIL ("inline" ("filename" "pixel.png")) NIL NIL)` +
	` ("application" "pdf" ("name" "report.pdf") NIL NIL "base64" 200 NIL ("attachment" ("filename" "report.pdf")) NIL NIL)` +
	` "mixed" ("boundary" "mix1") NIL NIL NIL` +
	`)`

// viewPlainBody is the quoted-printable encoded text/plain part.
const viewPlainBody = "Caf=C3=A9=20plain=20line=\r\n"

// viewHTMLBody is the latin-1 text/html part ("Café" with a raw 0xE9 byte).
const viewHTMLBody = "<p>Caf\xe9</p>"

// viewServer is a minimal IMAP server that serves one message.
type viewServer struct {
	listener net.Listener
	port     int

	header string

	mu       sync.Mutex
	received []string
}

// newViewServer starts the server.
func newViewServer(t *testing.T, header string) *viewServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &viewServer{
		listener: listener,
		port:     listener.Addr().(*net.TCPAddr).Port,
		header:   header,
	}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })

	return server
}

// viewHeader is the header block the server returns for the header request.
func viewHeader() string {
	return "Subject: View test\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"To: Bob <bob@example.com>\r\n" +
		"Date: Thu, 10 Jun 2010 08:00:00 +0000\r\n" +
		"Content-Type: multipart/mixed; boundary=\"mix1\"\r\n" +
		"Disposition-Notification-To: Alice <alice@example.com>\r\n" +
		"\r\n"
}

// client connects an authenticated mail.Client to the server.
func (s *viewServer) client(t *testing.T) *Client {
	t.Helper()

	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = s.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false

	imap := imapclient.New(&settings, nil)
	if err := imap.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	if err := imap.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	t.Cleanup(func() { _ = imap.Close() })

	return New(imap)
}

func (s *viewServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *viewServer) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

func (s *viewServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *viewServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	writeRaw := func(text string) { _, _ = connection.Write([]byte(text)) }

	const capabilities = "IMAP4rev1 AUTH=PLAIN SASL-IR"
	write("* OK [CAPABILITY " + capabilities + "] mock ready")

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.received = append(s.received, line)
		s.mu.Unlock()

		tag, rest := splitCommand(line, " ")
		if strings.HasPrefix(strings.ToUpper(rest), "UID ") {
			rest = rest[4:]
		}
		command, _ := splitCommand(rest, " ")

		switch strings.ToUpper(command) {
		case "CAPABILITY":
			write("* CAPABILITY " + capabilities)
			write(tag + " OK CAPABILITY completed")
		case "AUTHENTICATE":
			write(tag + " OK authenticated")
		case "SELECT", "EXAMINE":
			write("* 1 EXISTS")
			write("* OK [UIDVALIDITY 9] UIDs valid")
			write("* OK [UIDNEXT 43] Predicted next UID")
			write(tag + " OK [READ-ONLY] " + command + " completed")
		case "FETCH":
			s.writeFetch(write, writeRaw)
			write(tag + " OK FETCH completed")
		case "LOGOUT":
			write("* BYE logging out")
			write(tag + " OK LOGOUT completed")
			return
		case "NOOP":
			write(tag + " OK NOOP completed")
		default:
			write(tag + " BAD unknown command")
		}
	}
}

// writeFetch answers with the bodystructure, the header block and the two text
// parts, each as an IMAP literal.
func (s *viewServer) writeFetch(write func(string), writeRaw func(string)) {
	htmlBody := base64.StdEncoding.EncodeToString([]byte(viewHTMLBody))

	write(`* 1 FETCH (UID 42 RFC822.SIZE 1234 FLAGS (\Seen) INTERNALDATE "01-Jan-2020 10:00:00 +0000" BODYSTRUCTURE ` +
		viewBodyStructure +
		` BODY[HEADER.FIELDS (SUBJECT FROM)] {` + fmt.Sprint(len(s.header)) + `}`)
	writeRaw(s.header)
	write(` BODY[1.1] {` + fmt.Sprint(len(viewPlainBody)) + `}`)
	writeRaw(viewPlainBody)
	write(` BODY[1.2] {` + fmt.Sprint(len(htmlBody)) + `}`)
	writeRaw(htmlBody)
	write(`)`)
}

// splitCommand splits value on the first separator.
func splitCommand(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// TestMessageView checks the decoded body, the attachments and the ETag of a
// multipart message.
func TestMessageView(t *testing.T) {
	server := newViewServer(t, viewHeader())
	client := server.client(t)

	message, err := client.Message("INBOX", 42, true)
	if nil != err {
		t.Fatalf("Message: %v", err)
	}
	if nil == message {
		t.Fatal("Message returned nil")
	}

	if 42 != message.UID() {
		t.Errorf("uid = %d", message.UID())
	}
	if "View test" != message.subject {
		t.Errorf("subject = %q", message.subject)
	}
	if 1234 != message.size {
		t.Errorf("size = %d", message.size)
	}

	// text/plain: quoted-printable, so the soft line break is gone and the
	// "=C3=A9" turned into a UTF-8 é.
	if "Café plain line" != message.plain {
		t.Errorf("plain = %q", message.plain)
	}
	// text/html: base64 of a latin-1 part, converted to UTF-8.
	if "<p>Café</p>" != message.html {
		t.Errorf("html = %q", message.html)
	}

	// The inline image and the PDF attachment.
	if 2 != len(message.attachments) {
		t.Fatalf("attachments = %d", len(message.attachments))
	}
	first, _ := message.attachments[0].JSONSerialize().(map[string]any)
	if "2" != first["mimeIndex"] || "image/png" != first["mimeType"] {
		t.Errorf("inline attachment = %v", first)
	}
	if "<pixel@example.com>" != first["cId"] || true != first["isInline"] {
		t.Errorf("inline attachment flags = %v", first)
	}
	second, _ := message.attachments[1].JSONSerialize().(map[string]any)
	if "3" != second["mimeIndex"] || "application/pdf" != second["mimeType"] {
		t.Errorf("pdf attachment = %v", second)
	}
	if "report.pdf" != second["fileName"] {
		t.Errorf("pdf file name = %v", second["fileName"])
	}

	// "Disposition-Notification-To" becomes the readReceipt address.
	if "Alice <alice@example.com>" != message.readReceipt {
		t.Errorf("readReceipt = %q", message.readReceipt)
	}

	// The document carries the decoded bodies.
	document, _ := message.JSONSerialize().(map[string]any)
	if "<p>Café</p>" != document["html"] {
		t.Errorf("document html = %v", document["html"])
	}
	if "Café plain line" != document["plain"] {
		t.Errorf("document plain = %v", document["plain"])
	}

	// ETag: md5('MessageHash/' + folder/uid/flags/clientHash).
	sum := md5.Sum([]byte("MessageHash/" + "INBOX/42/\\seen/user@example.com"))
	if want := hex.EncodeToString(sum[:]); message.ETag("user@example.com") != want {
		t.Errorf("ETag = %q, want %q", message.ETag("user@example.com"), want)
	}

	// The BODYSTRUCTURE is fetched first, then the two text parts by id.
	if !server.hasCommand("BODYSTRUCTURE)") {
		t.Errorf("BODYSTRUCTURE fetch missing: %v", server.commands())
	}
	if !server.hasCommand("BODY.PEEK[1.1]") || !server.hasCommand("BODY.PEEK[1.2]") {
		t.Errorf("text part fetch missing: %v", server.commands())
	}
	if !server.hasCommand("EXAMINE") {
		t.Errorf("EXAMINE missing: %v", server.commands())
	}
}

// TestMessageViewRejectsAnInvalidIndex checks the ValueError of Message().
func TestMessageViewRejectsAnInvalidIndex(t *testing.T) {
	server := newViewServer(t, viewHeader())
	client := server.client(t)

	if _, err := client.Message("INBOX", 0, true); nil == err {
		t.Error("a uid of 0 must fail")
	}
}
