package mail

// Tests of Step 16: MessageList paging, search and the message JSON documents,
// against an in-process IMAP server.

import (
	"bufio"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
)

// listMessage is one message of the mock mailbox.
type listMessage struct {
	uid    int
	size   int
	flags  string
	header string
}

// listServer is a minimal IMAP server for MessageList().
type listServer struct {
	listener net.Listener
	port     int
	messages []listMessage

	mu       sync.Mutex
	received []string
}

// newListServer starts a server with the given messages.
func newListServer(t *testing.T, messages ...listMessage) *listServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &listServer{
		listener: listener,
		port:     listener.Addr().(*net.TCPAddr).Port,
		messages: messages,
	}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

// client connects an authenticated mail.Client to the server.
func (s *listServer) client(t *testing.T) *Client {
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

func (s *listServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

func (s *listServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *listServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	writeRaw := func(text string) { _, _ = connection.Write([]byte(text)) }

	const capabilities = "IMAP4rev1 AUTH=PLAIN SASL-IR SORT THREAD=REFERENCES PREVIEW LITERAL+"
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

		tag, rest := splitOnce(line, " ")
		if strings.HasPrefix(strings.ToUpper(rest), "UID ") {
			rest = rest[4:]
		}
		command, _ := splitOnce(rest, " ")
		command = strings.ToUpper(command)

		switch command {
		case "CAPABILITY":
			write("* CAPABILITY " + capabilities)
			write(tag + " OK CAPABILITY completed")
		case "AUTHENTICATE":
			write(tag + " OK authenticated")
		case "SELECT", "EXAMINE":
			write(fmt.Sprintf("* %d EXISTS", len(s.messages)))
			write("* OK [UIDVALIDITY 9] UIDs valid")
			write("* OK [UIDNEXT 100] Predicted next UID")
			write("* OK [PERMANENTFLAGS (\\Seen \\Deleted \\*)] Limited")
			write(tag + " OK [READ-WRITE] " + command + " completed")
		case "STATUS":
			write(fmt.Sprintf(`* STATUS "INBOX" (MESSAGES %d UNSEEN 1 UIDNEXT 100 UIDVALIDITY 9)`, len(s.messages)))
			write(tag + " OK STATUS completed")
		case "SORT":
			parts := make([]string, 0, len(s.messages))
			for index := len(s.messages) - 1; index >= 0; index-- {
				parts = append(parts, strconv.Itoa(s.messages[index].uid))
			}
			write("* SORT " + strings.Join(parts, " "))
			write(tag + " OK SORT completed")
		case "SEARCH":
			// A fixed match set: only the first message.
			if 0 < len(s.messages) {
				write("* SEARCH " + strconv.Itoa(s.messages[0].uid))
			} else {
				write("* SEARCH")
			}
			write(tag + " OK SEARCH completed")
		case "THREAD":
			write("* THREAD (1)(2 3)")
			write(tag + " OK THREAD completed")
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

func (s *listServer) writeFetch(write func(string), writeRaw func(string)) {
	for index, message := range s.messages {
		header := message.header
		flags := message.flags
		if "" == flags {
			flags = `\Seen`
		}
		write(fmt.Sprintf(`* %d FETCH (UID %d RFC822.SIZE %d FLAGS (%s) INTERNALDATE "01-Jan-2020 10:00:00 +0000" BODYSTRUCTURE ("text" "plain" ("charset" "utf-8") NIL NIL "7bit" 10 1) BODY[HEADER.FIELDS (SUBJECT FROM)] {%d}`,
			index+1, message.uid, message.size, flags, len(header)))
		writeRaw(header)
		writeRaw(")\r\n")
	}
}

// splitOnce splits value on the first separator.
func splitOnce(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// twoMessages builds the standard mailbox for the tests.
func twoMessages() []listMessage {
	return []listMessage{
		{uid: 1, size: 100, header: "Subject: First\r\nFrom: Alice <alice@example.com>\r\nDate: Thu, 10 Jun 2010 08:00:00 +0000\r\n\r\n"},
		{uid: 2, size: 200, flags: `\Seen \Flagged`, header: "Subject: Second\r\nFrom: Bob <bob@example.com>\r\nDate: Fri, 11 Jun 2010 09:00:00 +0000\r\n\r\n"},
	}
}

// TestMessageListPageOne checks page 1 of INBOX.
func TestMessageListPageOne(t *testing.T) {
	server := newListServer(t, twoMessages()...)
	client := server.client(t)

	params := NewMessageListParams()
	params.FolderName = "INBOX"
	params.Limit = 25

	collection, err := client.MessageList(params)
	if nil != err {
		t.Fatalf("MessageList: %v", err)
	}

	if 2 != collection.TotalEmails {
		t.Errorf("totalEmails = %d", collection.TotalEmails)
	}
	if "REVERSE DATE" != collection.Sort {
		t.Errorf("sort = %q", collection.Sort)
	}
	if nil == collection.FolderInfo || nil == collection.FolderInfo.Messages || 2 != *collection.FolderInfo.Messages {
		t.Errorf("folder info = %v", collection.FolderInfo)
	}

	messages := collection.Iterate()
	if 2 != len(messages) {
		t.Fatalf("messages = %d", len(messages))
	}
	// The server SORT answer is 2 1, so the newest message is first.
	if 2 != messages[0].UID() || "Second" != messages[0].subject {
		t.Errorf("first message = uid %d %q", messages[0].UID(), messages[0].subject)
	}
	if 200 != messages[0].size {
		t.Errorf("size = %d", messages[0].size)
	}
	if 0 == len(messages[0].flags) {
		t.Error("flags are missing")
	}
	if nil == messages[0].from || 1 != messages[0].from.Count() {
		t.Fatalf("from = %v", messages[0].from)
	}
	if "bob@example.com" != messages[0].from.Iterate()[0].GetEmail() {
		t.Errorf("from = %q", messages[0].from.Iterate()[0].GetEmail())
	}
	if "Bob" != messages[0].from.Iterate()[0].GetDisplayName() {
		t.Errorf("from name = %q", messages[0].from.Iterate()[0].GetDisplayName())
	}

	// The message document carries the searchable header collection.
	document, _ := messages[0].JSONSerialize().(map[string]any)
	if "Object/Message" != document["@Object"] {
		t.Errorf("document = %v", document["@Object"])
	}
	if "Second" != document["subject"] {
		t.Errorf("document subject = %v", document["subject"])
	}
	if nil == document["headers"] {
		t.Error("headers are missing from the document")
	}

	if !server.hasCommand("UID SORT (REVERSE DATE) UTF-8 UNDELETED") {
		t.Errorf("SORT command missing: %v", server.commands())
	}
}

// TestMessageListPaged checks a second page.
func TestMessageListPaged(t *testing.T) {
	server := newListServer(t, twoMessages()...)
	client := server.client(t)

	params := NewMessageListParams()
	params.FolderName = "INBOX"
	params.Offset = 1
	params.Limit = 10

	collection, err := client.MessageList(params)
	if nil != err {
		t.Fatalf("MessageList: %v", err)
	}

	// The whole sorted list is 2 1; offset 1 leaves only uid 1.
	if 2 != collection.TotalEmails {
		t.Errorf("totalEmails = %d", collection.TotalEmails)
	}
	messages := collection.Iterate()
	if 1 != len(messages) {
		t.Fatalf("messages = %d", len(messages))
	}
	if 1 != messages[0].UID() || "First" != messages[0].subject {
		t.Errorf("message = uid %d %q", messages[0].UID(), messages[0].subject)
	}
}

// TestMessageListSearch checks a search that keeps only one match.
func TestMessageListSearch(t *testing.T) {
	server := newListServer(t, twoMessages()...)
	client := server.client(t)

	params := NewMessageListParams()
	params.FolderName = "INBOX"
	params.Limit = 10
	params.Search = "from=alice@example.com"

	collection, err := client.MessageList(params)
	if nil != err {
		t.Fatalf("MessageList: %v", err)
	}

	if 1 != collection.TotalEmails {
		t.Errorf("totalEmails = %d", collection.TotalEmails)
	}
	messages := collection.Iterate()
	if 1 != len(messages) {
		t.Fatalf("messages = %d", len(messages))
	}
	if 1 != messages[0].UID() {
		t.Errorf("message uid = %d", messages[0].UID())
	}

	if !server.hasCommand("UID SEARCH") {
		t.Errorf("SEARCH command missing: %v", server.commands())
	}
}

// hasCommand reports whether any line contains needle (case insensitive).
func (s *listServer) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// countCommands counts the received lines that contain needle (case insensitive).
func (s *listServer) countCommands(needle string) int {
	needle = strings.ToUpper(needle)
	count := 0
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			count++
		}
	}
	return count
}

// memoryCacher is an in-memory mail.Cacher.
type memoryCacher struct {
	data map[string]string
}

// IsInited reports a working cache.
func (m *memoryCacher) IsInited() bool { return true }

// Get returns the cached value.
func (m *memoryCacher) Get(key string) string { return m.data[key] }

// Set stores a value.
func (m *memoryCacher) Set(key, value string) { m.data[key] = value }

// TestMessageListUsesTheUidCache checks the cache.server_uids UID cache: the
// second list request must reuse the cached SORT result.
func TestMessageListUsesTheUidCache(t *testing.T) {
	server := newListServer(t, twoMessages()...)
	client := server.client(t)
	cacher := &memoryCacher{data: map[string]string{}}

	first := NewMessageListParams()
	first.FolderName = "INBOX"
	first.Limit = 10
	first.Cacher = cacher
	if _, err := client.MessageList(first); nil != err {
		t.Fatalf("MessageList (first): %v", err)
	}
	sortsAfterFirst := server.countCommands("UID SORT")
	if 1 != sortsAfterFirst {
		t.Fatalf("SORT count after first request = %d", sortsAfterFirst)
	}

	second := NewMessageListParams()
	second.FolderName = "INBOX"
	second.Limit = 10
	second.Cacher = cacher
	if _, err := client.MessageList(second); nil != err {
		t.Fatalf("MessageList (second): %v", err)
	}
	if got := server.countCommands("UID SORT"); got != sortsAfterFirst {
		t.Errorf("SORT was re-issued despite the cache: %d", got)
	}
}
