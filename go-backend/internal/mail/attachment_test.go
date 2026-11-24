package mail

// Tests of Step 20: MailClient::MessageMimeStream() against an in-process IMAP
// server that serves one MIME part (a base64 PDF attachment) and the whole
// message header/body.

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// partMimeHeader is the MIME header block of part 2 (the PDF attachment).
const partMimeHeader = "Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n"

// messageHeader is the top level header block of the message.
const messageHeader = "Subject: Full message\r\n" +
	"From: Alice <alice@example.com>\r\n" +
	"\r\n"

// partServer serves one message with part 2 as a base64 PDF attachment.
type partServer struct {
	listener net.Listener
	port     int
	body     string // base64 of the part content

	mu       sync.Mutex
	received []string
}

// newPartServer starts the server.
func newPartServer(t *testing.T) *partServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &partServer{
		listener: listener,
		port:     listener.Addr().(*net.TCPAddr).Port,
		body:     base64.StdEncoding.EncodeToString([]byte("PDFDATA")),
	}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *partServer) client(t *testing.T) *Client {
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

func (s *partServer) hasCommand(needle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	needle = strings.ToUpper(needle)
	for _, line := range s.received {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

func (s *partServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *partServer) handle(connection net.Conn) {
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

		tag, rest := splitPart(line, " ")
		if strings.HasPrefix(strings.ToUpper(rest), "UID ") {
			rest = rest[4:]
		}
		command, args := splitPart(rest, " ")

		switch strings.ToUpper(command) {
		case "CAPABILITY":
			write("* CAPABILITY " + capabilities)
			write(tag + " OK CAPABILITY completed")
		case "AUTHENTICATE", "LOGIN":
			write(tag + " OK authenticated")
		case "EXAMINE", "SELECT":
			write("* 1 EXISTS")
			write(tag + " OK [READ-ONLY] " + command + " completed")
		case "FETCH":
			s.writeFetch(write, writeRaw, args)
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

// writeFetch answers the FETCH requested by its BODY.PEEK[...] item.
func (s *partServer) writeFetch(write func(string), writeRaw func(string), rest string) {
	item := bodyPeekItem(rest) // "[2]", "[2.MIME]", "[HEADER]" or "[]"
	key := "BODY" + item

	content := s.body
	switch {
	case strings.HasSuffix(item, ".MIME]"):
		content = partMimeHeader
	case "[HEADER]" == item:
		content = messageHeader
	}

	write(`* 1 FETCH (UID 42 RFC822.SIZE 500 ` + key + ` {` + strconv.Itoa(len(content)) + `}`)
	writeRaw(content)
	write(`)`)
}

// bodyPeekItem extracts the "[...]" of the first BODY.PEEK item of a FETCH.
func bodyPeekItem(rest string) string {
	index := strings.Index(rest, "BODY.PEEK[")
	if 0 > index {
		return ""
	}
	remainder := rest[index+len("BODY.PEEK"):]
	end := strings.Index(remainder, "]")
	if 0 > end {
		return ""
	}
	return remainder[:end+1]
}

// splitPart splits value on the first separator.
func splitPart(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// TestMessageMimeStreamDecodesAPart checks the decoded bytes, the media type,
// the file name and the part id of a base64 attachment.
func TestMessageMimeStreamDecodesAPart(t *testing.T) {
	server := newPartServer(t)
	client := server.client(t)

	var (
		gotContent     string
		gotContentType string
		gotFileName    string
		gotIndex       string
	)
	found := client.MessageMimeStream(func(reader io.ReadSeeker, contentType, fileName, mimeIndex string) {
		data, _ := io.ReadAll(reader)
		gotContent = string(data)
		gotContentType = contentType
		gotFileName = fileName
		gotIndex = mimeIndex
	}, "INBOX", 42, "2")

	if !found {
		t.Fatal("MessageMimeStream did not find the part")
	}
	if "PDFDATA" != gotContent {
		t.Errorf("content = %q, want the base64 decoded PDFDATA", gotContent)
	}
	if "application/pdf" != gotContentType {
		t.Errorf("contentType = %q", gotContentType)
	}
	if "report.pdf" != gotFileName {
		t.Errorf("fileName = %q", gotFileName)
	}
	if "2" != gotIndex {
		t.Errorf("mimeIndex = %q", gotIndex)
	}

	if !server.hasCommand("BODY.PEEK[2.MIME]") {
		t.Errorf("the MIME header fetch is missing: %v", server.received)
	}
	if !server.hasCommand("BODY.PEEK[2]") {
		t.Errorf("the part body fetch is missing: %v", server.received)
	}
}

// TestMessageMimeStreamWholeMessage checks the empty part id branch: the whole
// message is streamed as message/rfc822 with a "<subject>.eml" file name.
func TestMessageMimeStreamWholeMessage(t *testing.T) {
	server := newPartServer(t)
	client := server.client(t)

	var gotContentType, gotFileName string
	found := client.MessageMimeStream(func(reader io.ReadSeeker, contentType, fileName, mimeIndex string) {
		_, _ = io.ReadAll(reader)
		gotContentType = contentType
		gotFileName = fileName
		if "" != mimeIndex {
			t.Errorf("mimeIndex = %q, want empty", mimeIndex)
		}
	}, "INBOX", 42, "")

	if !found {
		t.Fatal("MessageMimeStream did not stream the whole message")
	}
	if "message/rfc822" != gotContentType {
		t.Errorf("contentType = %q", gotContentType)
	}
	if "Full message.eml" != gotFileName {
		t.Errorf("fileName = %q", gotFileName)
	}
	if !server.hasCommand("BODY.PEEK[HEADER]") {
		t.Errorf("the whole-message header fetch is missing: %v", server.received)
	}
	if !server.hasCommand("BODY.PEEK[]") {
		t.Errorf("the whole-message body fetch is missing: %v", server.received)
	}
}

// TestMessageMimeStreamRejectsAnInvalidPartId checks the FetchType guard.
func TestMessageMimeStreamRejectsAnInvalidPartId(t *testing.T) {
	server := newPartServer(t)
	client := server.client(t)

	if client.MessageMimeStream(func(io.ReadSeeker, string, string, string) {}, "INBOX", 42, "../evil") {
		t.Error("an invalid part id must be refused")
	}
}
