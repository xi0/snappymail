package imapclient

// Tests of Step 16: the FETCH/SEARCH/SORT/THREAD command layer and the
// BODYSTRUCTURE/FetchResponse parsing, against an in-process IMAP server.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// mockMessage is one message of the mock server.
type mockMessage struct {
	uid    int
	size   int
	flags  string
	header string
	body   string
}

// messageServer is a minimal IMAP server for the message commands.
type messageServer struct {
	listener     net.Listener
	port         int
	capabilities string
	messages     []mockMessage

	mu       sync.Mutex
	received []string
}

// newMessageServer starts a server with the given messages.
func newMessageServer(t *testing.T, messages ...mockMessage) *messageServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &messageServer{
		listener:     listener,
		port:         listener.Addr().(*net.TCPAddr).Port,
		capabilities: "IMAP4rev1 AUTH=PLAIN SASL-IR SORT THREAD=REFERENCES PREVIEW",
		messages:     messages,
	}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

// settings points an unencrypted connection at the mock.
func (s *messageServer) settings() *domain.IMAPSettings {
	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = s.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false
	return &settings
}

// connect opens and authenticates a client.
func (s *messageServer) connect(t *testing.T) *Client {
	t.Helper()

	client := New(s.settings(), nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// commands returns a copy of the received client lines.
func (s *messageServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *messageServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *messageServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	writeRaw := func(text string) { _, _ = connection.Write([]byte(text)) }

	write("* OK [CAPABILITY " + s.capabilities + "] mock ready")

	for {
		line, err := reader.ReadString('\n')
		if nil != err {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		s.mu.Lock()
		s.received = append(s.received, line)
		s.mu.Unlock()

		tag, rest := splitFirst(line, " ")
		if strings.HasPrefix(strings.ToUpper(rest), "UID ") {
			rest = rest[4:]
		}
		command, _ := splitFirst(rest, " ")
		command = strings.ToUpper(command)

		switch command {
		case "CAPABILITY":
			write("* CAPABILITY " + s.capabilities)
			write(tag + " OK CAPABILITY completed")
		case "AUTHENTICATE":
			write(tag + " OK authenticated")
		case "LOGIN":
			write(tag + " OK logged in")
		case "SELECT", "EXAMINE":
			write(fmt.Sprintf("* %d EXISTS", len(s.messages)))
			write("* OK [UIDVALIDITY 9] UIDs valid")
			write("* OK [UIDNEXT 100] Predicted next UID")
			write("* OK [PERMANENTFLAGS (\\Seen \\Deleted \\*)] Limited")
			write(tag + " OK [READ-WRITE] " + command + " completed")
		case "STATUS":
			write(fmt.Sprintf(`* STATUS "INBOX" (MESSAGES %d UNSEEN 1 UIDNEXT 100 UIDVALIDITY 9)`, len(s.messages)))
			write(tag + " OK STATUS completed")
		case "SEARCH":
			s.writeSearch(write, tag, rest)
		case "SORT":
			s.writeSort(write, tag, rest)
		case "THREAD":
			write("* THREAD (1)(2 3)")
			write(tag + " OK THREAD completed")
		case "FETCH":
			s.writeFetch(write, writeRaw, tag, rest)
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

func (s *messageServer) writeSearch(write func(string), tag, rest string) {
	// A UID SEARCH: echo the message UIDs in ascending order.
	var parts []string
	for _, message := range s.messages {
		parts = append(parts, fmt.Sprint(message.uid))
	}
	write("* SEARCH " + strings.Join(parts, " "))
	write(tag + " OK SEARCH completed")
}

func (s *messageServer) writeSort(write func(string), tag, rest string) {
	var parts []string
	for index := len(s.messages) - 1; index >= 0; index-- {
		parts = append(parts, fmt.Sprint(s.messages[index].uid))
	}
	write("* SORT " + strings.Join(parts, " "))
	write(tag + " OK SORT completed")
}

func (s *messageServer) writeFetch(write func(string), writeRaw func(string), tag, rest string) {
	for index, message := range s.messages {
		header := message.header
		if "" == header {
			header = "Subject: message " + fmt.Sprint(message.uid) + "\r\n"
		}
		flags := message.flags
		if "" == flags {
			flags = `\Seen`
		}
		write(fmt.Sprintf(`* %d FETCH (UID %d RFC822.SIZE %d FLAGS (%s) INTERNALDATE "01-Jan-2020 10:00:00 +0000" BODYSTRUCTURE %s BODY[HEADER.FIELDS (SUBJECT FROM)] {%d}`,
			index+1, message.uid, message.size, flags, messageBodyStructure(message), len(header)))
		writeRaw(header)
		writeRaw(")\r\n")
	}
	write(tag + " OK FETCH completed")
}

// messageBodyStructure renders the BODYSTRUCTURE of one mock message.
func messageBodyStructure(message mockMessage) string {
	if "" != message.body {
		return message.body
	}
	return `("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 10 1)`
}

// TestMessageSearch checks the SEARCH parsing and reversal.
func TestMessageSearch(t *testing.T) {
	server := newMessageServer(t,
		mockMessage{uid: 1}, mockMessage{uid: 2}, mockMessage{uid: 3},
	)
	client := server.connect(t)

	uids, err := client.MessageSearch("ALL", true)
	if nil != err {
		t.Fatalf("MessageSearch: %v", err)
	}
	// The server answers 1 2 3; PHP reverses so the newest is first.
	if 3 != len(uids) || 3 != uids[0] || 2 != uids[1] || 1 != uids[2] {
		t.Errorf("uids = %v, want [3 2 1]", uids)
	}
}

// TestMessageSort checks the SORT parsing.
func TestMessageSort(t *testing.T) {
	server := newMessageServer(t,
		mockMessage{uid: 1}, mockMessage{uid: 2}, mockMessage{uid: 3},
	)
	client := server.connect(t)

	uids, err := client.MessageSort([]string{"REVERSE DATE"}, "ALL", true)
	if nil != err {
		t.Fatalf("MessageSort: %v", err)
	}
	if 3 != len(uids) || 3 != uids[0] {
		t.Errorf("uids = %v", uids)
	}
	if !server.hasCommand("UID SORT (REVERSE DATE) UTF-8 ALL") {
		t.Errorf("SORT command missing: %v", server.commands())
	}
}

// TestMessageThread checks the THREAD parsing.
func TestMessageThread(t *testing.T) {
	server := newMessageServer(t, mockMessage{uid: 1}, mockMessage{uid: 2}, mockMessage{uid: 3})
	client := server.connect(t)

	threads, err := client.MessageThread("ALL", "REFERENCES", true)
	if nil != err {
		t.Fatalf("MessageThread: %v", err)
	}
	if 2 != len(threads) {
		t.Fatalf("threads = %v", threads)
	}
	if first, ok := threads[0].(int); !ok || 1 != first {
		t.Errorf("thread[0] = %v", threads[0])
	}
	if second, ok := threads[1].([]any); !ok || 2 != len(second) {
		t.Errorf("thread[1] = %v", threads[1])
	}
}

// TestFetchParsesTheHeaderAndFlags checks the FETCH literal parsing.
func TestFetchParsesTheHeaderAndFlags(t *testing.T) {
	server := newMessageServer(t, mockMessage{
		uid:    7,
		size:   512,
		flags:  `\Seen \Flagged`,
		header: "Subject: Hello\r\nFrom: Alice <alice@example.com>\r\n\r\n",
	})
	client := server.connect(t)

	responses, err := client.Fetch([]string{
		FetchUID, FetchRFC822Size, FetchInternalDate, FetchFlags, FetchBodyStructure,
		BuildBodyCustomHeaderRequest([]string{"SUBJECT", "FROM"}, true),
	}, "7", true)
	if nil != err {
		t.Fatalf("Fetch: %v", err)
	}
	if 1 != len(responses) {
		t.Fatalf("responses = %d", len(responses))
	}

	response := responses[0]
	if 7 != response.FetchInt(FetchUID) {
		t.Errorf("UID = %d", response.FetchInt(FetchUID))
	}
	if 512 != response.FetchInt(FetchRFC822Size) {
		t.Errorf("SIZE = %d", response.FetchInt(FetchRFC822Size))
	}
	flags := response.FetchValueStrings(FetchFlags)
	if 2 != len(flags) || `\Seen` != flags[0] || `\Flagged` != flags[1] {
		t.Errorf("flags = %v", flags)
	}
	headers := response.GetHeaderFieldsValue()
	if !strings.Contains(headers, "Subject: Hello") {
		t.Errorf("headers = %q", headers)
	}

	structure := response.GetFetchBodyStructure()
	if nil == structure {
		t.Fatal("BODYSTRUCTURE was not parsed")
	}
	if "text/plain" != structure.ContentType() || "1" != structure.PartID() {
		t.Errorf("structure = %q / %q", structure.ContentType(), structure.PartID())
	}
	if !server.hasCommand("UID FETCH 7 (UID RFC822.SIZE") {
		t.Errorf("FETCH command missing: %v", server.commands())
	}
}

// hasCommand reports whether any line contains needle (upper case comparison).
func (s *messageServer) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// TestBodyStructureMultipartParts checks the attachment/text part search.
func TestBodyStructureMultipartParts(t *testing.T) {
	server := newMessageServer(t, mockMessage{
		uid:  1,
		body: `(("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 10 1)("application" "pdf" ("name" "report.pdf") NIL NIL "base64" 4000) "mixed" ("boundary" "b1"))`,
	})
	client := server.connect(t)

	responses, err := client.Fetch([]string{FetchUID, FetchBodyStructure}, "1", true)
	if nil != err {
		t.Fatalf("Fetch: %v", err)
	}
	structure := responses[0].GetFetchBodyStructure()
	if nil == structure {
		t.Fatal("BODYSTRUCTURE missing")
	}
	if "multipart/mixed" != structure.ContentType() {
		t.Errorf("content type = %q", structure.ContentType())
	}
	if parts := structure.GetHtmlAndPlainParts(); 1 != len(parts) || "1" != parts[0].PartID() {
		t.Errorf("text parts = %v", parts)
	}
	attachments := structure.SearchAttachmentsParts()
	if 1 != len(attachments) || "2" != attachments[0].PartID() {
		t.Fatalf("attachments = %v", attachments)
	}
	if "report.pdf" != attachments[0].FileName(false) {
		t.Errorf("filename = %q", attachments[0].FileName(false))
	}
	if "application/pdf" != attachments[0].ContentType() {
		t.Errorf("attachment type = %q", attachments[0].ContentType())
	}
}
