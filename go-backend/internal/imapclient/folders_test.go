package imapclient

// Tests of Step 15: the folder command layer and the folder models, against an
// in-process IMAP server that speaks the LIST/STATUS/SELECT/CREATE/RENAME/
// DELETE/SUBSCRIBE/NAMESPACE/QUOTA/METADATA/ACL subset of the protocol.

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/domain"
)

// mockFolder is one mailbox of the mock server.
type mockFolder struct {
	name        string
	attributes  []string
	messages    int
	unseen      int
	uidNext     int
	uidValidity int
	modSeq      int
	subscribed  bool
}

// folderServer is a minimal IMAP server for the folder commands.
type folderServer struct {
	listener     net.Listener
	port         int
	greeting     string
	capabilities string

	mu       sync.Mutex
	folders  []*mockFolder
	received []string
}

// newFolderServer starts a server with the given mailboxes.
func newFolderServer(t *testing.T, capabilities, greeting string, folders ...*mockFolder) *folderServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &folderServer{
		listener:     listener,
		port:         listener.Addr().(*net.TCPAddr).Port,
		greeting:     greeting,
		capabilities: capabilities,
		folders:      folders,
	}

	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })

	return server
}

// defaultFolderServer advertises the capabilities the folder commands use.
func defaultFolderServer(t *testing.T, folders ...*mockFolder) *folderServer {
	const capabilities = "IMAP4rev1 AUTH=PLAIN SASL-IR LIST-EXTENDED SPECIAL-USE " +
		"LIST-STATUS NAMESPACE QUOTA METADATA ACL UNSELECT"
	return newFolderServer(t, capabilities,
		"* OK [CAPABILITY "+capabilities+"] mock ready", folders...)
}

// settings points an unencrypted connection at the mock.
func (s *folderServer) settings() *domain.IMAPSettings {
	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = s.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false
	return &settings
}

// connect opens and authenticates a client on the mock.
func (s *folderServer) connect(t *testing.T) *Client {
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

// commands returns a copy of every client line the server received.
func (s *folderServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

// hasCommand reports whether any received line contains needle (case
// insensitive).
func (s *folderServer) hasCommand(needle string) bool {
	needle = strings.ToUpper(needle)
	for _, line := range s.commands() {
		if strings.Contains(strings.ToUpper(line), needle) {
			return true
		}
	}
	return false
}

// find returns the mailbox with the given name.
func (s *folderServer) find(name string) *mockFolder {
	for _, folder := range s.folders {
		if folder.name == name {
			return folder
		}
	}
	return nil
}

func (s *folderServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *folderServer) handle(connection net.Conn) {
	defer connection.Close()

	reader := bufio.NewReader(connection)
	write := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }

	write(s.greeting)

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
		command, _ := splitFirst(rest, " ")
		quoted := quotedArgs(rest)

		switch strings.ToUpper(command) {
		case "AUTHENTICATE":
			write(tag + " OK authenticated")

		case "CAPABILITY":
			write("* CAPABILITY " + s.capabilities)
			write(tag + " OK CAPABILITY completed")

		case "LIST", "LSUB":
			s.writeList(write, strings.ToUpper(command), quoted)
			write(tag + " OK " + strings.ToUpper(command) + " completed")

		case "STATUS":
			if folder := s.find(firstOrEmpty(quoted)); nil != folder {
				write(fmt.Sprintf(`* STATUS "%s" (MESSAGES %d UNSEEN %d UIDNEXT %d UIDVALIDITY %d HIGHESTMODSEQ %d)`,
					folder.name, folder.messages, folder.unseen, folder.uidNext,
					folder.uidValidity, folder.modSeq))
			}
			write(tag + " OK STATUS completed")

		case "SELECT", "EXAMINE":
			folder := s.find(firstOrEmpty(quoted))
			write(`* FLAGS (\Answered \Flagged \Deleted \Seen \Draft)`)
			if nil != folder {
				write(fmt.Sprintf("* %d EXISTS", folder.messages))
				write(fmt.Sprintf("* %d UIDNEXT", folder.uidNext))
				write(fmt.Sprintf("* %d UIDVALIDITY", folder.uidValidity))
			}
			write(`* OK [PERMANENTFLAGS (\Deleted \Seen \*)] Limited`)
			write(tag + " OK [READ-WRITE] " + strings.ToUpper(command) + " completed")

		case "CREATE":
			if 0 < len(quoted) {
				s.mu.Lock()
				if nil == s.find(quoted[0]) {
					s.folders = append(s.folders, &mockFolder{name: quoted[0], uidValidity: 1})
				}
				s.mu.Unlock()
			}
			write(tag + " OK CREATE completed")

		case "DELETE":
			if 0 < len(quoted) {
				s.mu.Lock()
				for index, folder := range s.folders {
					if folder.name == quoted[0] {
						s.folders = append(s.folders[:index], s.folders[index+1:]...)
						break
					}
				}
				s.mu.Unlock()
			}
			write(tag + " OK DELETE completed")

		case "RENAME":
			if 2 <= len(quoted) {
				s.mu.Lock()
				if folder := s.find(quoted[0]); nil != folder {
					folder.name = quoted[1]
				}
				s.mu.Unlock()
			}
			write(tag + " OK RENAME completed")

		case "SUBSCRIBE", "UNSUBSCRIBE":
			if 0 < len(quoted) {
				s.mu.Lock()
				if folder := s.find(quoted[0]); nil != folder {
					folder.subscribed = "SUBSCRIBE" == strings.ToUpper(command)
				}
				s.mu.Unlock()
			}
			write(tag + " OK " + strings.ToUpper(command) + " completed")

		case "UNSELECT":
			write(tag + " OK UNSELECT completed")

		case "NAMESPACE":
			write(`* NAMESPACE (("INBOX/" "/")) (("Other/" "/")) NIL`)
			write(tag + " OK NAMESPACE completed")

		case "GETQUOTAROOT", "GETQUOTA":
			write(`* QUOTA "User quota" (STORAGE 128 2097152 MESSAGE 3 100)`)
			write(tag + " OK QUOTA completed")

		case "GETMETADATA":
			name := firstOrEmpty(quoted)
			if "" == name || "*" == name {
				name = "INBOX"
			}
			write(fmt.Sprintf(`* METADATA "%s" (/private/comment "hello")`, name))
			write(tag + " OK GETMETADATA completed")

		case "SETMETADATA":
			write(tag + " OK SETMETADATA completed")

		case "MYRIGHTS":
			write(`* MYRIGHTS "INBOX" "lrswipkxtea"`)
			write(tag + " OK MYRIGHTS completed")

		case "GETACL":
			write(`* ACL "INBOX" "user@example.com" "lrswipkxtea" "other@example.com" "lr"`)
			write(tag + " OK GETACL completed")

		case "SETACL", "DELETEACL":
			write(tag + " OK " + strings.ToUpper(command) + " completed")

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

// writeList answers a LIST/LSUB command. quoted holds the parent and the
// pattern, so an empty pattern asks for the hierarchy delimiter only.
func (s *folderServer) writeList(write func(string), command string, quoted []string) {
	pattern := "*"
	if 1 < len(quoted) {
		pattern = quoted[1]
	}

	if "" == pattern {
		write(`* LIST (\Noselect) "/" ""`)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, folder := range s.folders {
		if "LSUB" == command && !folder.subscribed {
			continue
		}
		if "*" != pattern && pattern != folder.name {
			continue
		}
		attributes := append([]string(nil), folder.attributes...)
		if folder.subscribed && !containsExact(attributes, `\Subscribed`) {
			attributes = append(attributes, `\Subscribed`)
		}
		write(fmt.Sprintf(`* %s (%s) "/" "%s"`, command, strings.Join(attributes, " "), folder.name))

		// RFC 5819 LIST-STATUS: only the mailboxes with counters report them.
		if "LIST" == command && 0 < folder.uidNext {
			write(fmt.Sprintf(`* STATUS "%s" (MESSAGES %d UNSEEN %d UIDNEXT %d UIDVALIDITY %d)`,
				folder.name, folder.messages, folder.unseen, folder.uidNext, folder.uidValidity))
		}
	}
}

// firstOrEmpty returns the first quoted argument, or "".
func firstOrEmpty(values []string) string {
	if 0 == len(values) {
		return ""
	}
	return values[0]
}

// quotedArgs returns the quoted strings of a command line.
func quotedArgs(line string) []string {
	var result []string
	for index := 0; index < len(line); index++ {
		if '"' != line[index] {
			continue
		}
		var builder strings.Builder
		index++
		for ; index < len(line) && '"' != line[index]; index++ {
			builder.WriteByte(line[index])
		}
		result = append(result, builder.String())
	}
	return result
}

// TestFolderListParsesTheResponse checks LIST + LIST-STATUS parsing, the
// attributes, the metadata and the JSON shape.
func TestFolderListParsesTheResponse(t *testing.T) {
	server := defaultFolderServer(t,
		&mockFolder{name: "INBOX", messages: 3, unseen: 1, uidNext: 4, uidValidity: 9, modSeq: 7, subscribed: true},
		&mockFolder{name: "Archive", attributes: []string{`\HasNoChildren`, `\Archive`}},
		&mockFolder{name: "Archive/2024", attributes: []string{`\HasNoChildren`}},
	)
	client := server.connect(t)

	collection, err := client.FolderStatusList("", "*")
	if nil != err {
		t.Fatalf("FolderStatusList: %v", err)
	}
	if 3 != collection.Count() {
		t.Fatalf("Count = %d, want 3", collection.Count())
	}

	inbox, ok := collection.Get("INBOX")
	if !ok {
		t.Fatal("INBOX is missing")
	}
	if "INBOX" != inbox.Name() || "/" != inbox.Delimiter() {
		t.Errorf("name/delimiter = %q/%q", inbox.Name(), inbox.Delimiter())
	}
	if !inbox.IsSubscribed() {
		t.Error("INBOX must be subscribed")
	}

	nested, ok := collection.Get("Archive/2024")
	if !ok || "2024" != nested.Name() {
		t.Errorf("Name() must be the last hierarchy element, got %v", nested)
	}

	archive, ok := collection.Get("Archive")
	if !ok || "archive" != archive.Role() {
		t.Errorf("Role() = %v, want archive", archive)
	}
	if "" != archive.ETag {
		t.Error("a folder without STATUS counters needs no ETag")
	}
	if "" == inbox.ETag {
		t.Error("LIST-STATUS counters must produce an ETag")
	}

	// RFC 5464: the METADATA of the server is attached to the listed folder.
	if value, ok := inbox.GetMetadata("/private/comment"); !ok || "hello" != value {
		t.Errorf("metadata = %q, %v", value, ok)
	}

	serialized, ok := inbox.JSONSerialize().(map[string]any)
	if !ok {
		t.Fatalf("JSONSerialize returned %T", inbox.JSONSerialize())
	}
	if "Object/Folder" != serialized["@Object"] || "INBOX" != serialized["fullName"] {
		t.Errorf("serialized = %v", serialized)
	}
	if _, ok := serialized["etag"]; !ok {
		t.Error("the etag must be part of the folder document")
	}
	if nil == serialized["totalEmails"] {
		t.Errorf("totalEmails = %v", serialized["totalEmails"])
	}

	// The client asked for the LIST-EXTENDED extras.
	if !server.hasCommand("RETURN") || !server.hasCommand("SUBSCRIBED") {
		t.Errorf("LIST-EXTENDED RETURN was not sent: %v", server.commands())
	}
}

// TestFolderListInjectsInbox checks that a LIST without INBOX adds it.
func TestFolderListInjectsInbox(t *testing.T) {
	server := defaultFolderServer(t,
		&mockFolder{name: "Projects", messages: 2, uidNext: 3, uidValidity: 1},
	)
	client := server.connect(t)

	collection, err := client.FolderStatusList("", "*")
	if nil != err {
		t.Fatalf("FolderStatusList: %v", err)
	}
	if _, ok := collection.Get("INBOX"); !ok {
		t.Fatalf("INBOX was not injected: %v", collection.SortedNames())
	}

	projects, ok := collection.Get("Projects")
	if !ok {
		t.Fatal("Projects is missing")
	}
	if "" == projects.ETag {
		t.Error("STATUS counters must produce an ETag")
	}
	if nil == projects.Messages || 2 != *projects.Messages {
		t.Errorf("Messages = %v, want 2", projects.Messages)
	}

	// The injected INBOX has no STATUS, so it carries no ETag.
	inbox, _ := collection.Get("INBOX")
	if "" != inbox.ETag {
		t.Error("the injected INBOX must not have an ETag")
	}
}

// TestFolderStatusAndSelect checks the STATUS + SELECT merge of FolderStatus.
func TestFolderStatusAndSelect(t *testing.T) {
	server := defaultFolderServer(t,
		&mockFolder{name: "INBOX", messages: 3, unseen: 2, uidNext: 4, uidValidity: 9},
	)
	client := server.connect(t)

	info, err := client.FolderStatusAndSelect("INBOX")
	if nil != err {
		t.Fatalf("FolderStatusAndSelect: %v", err)
	}
	if 3 != pointerValue(info.Messages) || 2 != pointerValue(info.Unseen) {
		t.Errorf("messages/unseen = %v/%v", info.Messages, info.Unseen)
	}
	if 4 != pointerValue(info.UIDNext) || 9 != pointerValue(info.UIDValidity) {
		t.Errorf("uidNext/uidValidity = %v/%v", info.UIDNext, info.UIDValidity)
	}
	if !info.IsWritable {
		t.Error("SELECT must report a writable folder")
	}
	if !containsExact(info.PermanentFlags, `\*`) {
		t.Errorf("permanentFlags = %v", info.PermanentFlags)
	}
	if !client.IsSelected() {
		t.Error("the folder must be selected")
	}

	serialized, ok := info.JSONSerialize().(map[string]any)
	if !ok {
		t.Fatalf("JSONSerialize returned %T", info.JSONSerialize())
	}
	if "INBOX" != serialized["name"] {
		t.Errorf("name = %v", serialized["name"])
	}
	for _, key := range []string{"id", "uidNext", "uidValidity", "totalEmails", "unreadEmails", "etag", "permanentFlags"} {
		if _, ok := serialized[key]; !ok {
			t.Errorf("serialized is missing %q: %v", key, serialized)
		}
	}
}

// TestFolderCreateRenameDelete exercises the mutating commands.
func TestFolderCreateRenameDelete(t *testing.T) {
	server := defaultFolderServer(t)
	client := server.connect(t)

	delimiter, ok := client.FolderHierarchyDelimiter("")
	if !ok || "/" != delimiter {
		t.Fatalf("delimiter = %q, %v", delimiter, ok)
	}

	if err := client.FolderCreate("Projects", true); nil != err {
		t.Fatalf("FolderCreate: %v", err)
	}
	if !server.hasCommand(`CREATE "Projects"`) {
		t.Errorf("CREATE was not sent: %v", server.commands())
	}
	if !server.hasCommand(`SUBSCRIBE "Projects"`) {
		t.Errorf("SUBSCRIBE was not sent: %v", server.commands())
	}

	folders, err := client.FolderStatusList("Projects", "*")
	if nil != err {
		t.Fatalf("FolderStatusList: %v", err)
	}
	if _, ok := folders.Get("Projects"); !ok {
		t.Fatalf("the created folder is missing: %v", folders.SortedNames())
	}

	if err := client.FolderRename("Projects", "Work"); nil != err {
		t.Fatalf("FolderRename: %v", err)
	}
	if nil == server.find("Work") {
		t.Errorf("RENAME did not reach the server: %v", server.commands())
	}

	if err := client.FolderUnsubscribe("Work"); nil != err {
		t.Fatalf("FolderUnsubscribe: %v", err)
	}
	if folder := server.find("Work"); nil != folder && folder.subscribed {
		t.Error("UNSUBSCRIBE was not applied")
	}

	if err := client.FolderDelete("Work"); nil != err {
		t.Fatalf("FolderDelete: %v", err)
	}
	if nil != server.find("Work") {
		t.Errorf("DELETE did not reach the server: %v", server.commands())
	}
}

// TestFolderDeleteNonEmpty checks the NonEmptyFolder error.
func TestFolderDeleteNonEmpty(t *testing.T) {
	server := defaultFolderServer(t,
		&mockFolder{name: "Full", messages: 1, uidNext: 2, uidValidity: 1},
	)
	client := server.connect(t)

	err := client.FolderDelete("Full")
	if nil == err {
		t.Fatal("deleting a non-empty folder must fail")
	}
	if ErrNonEmptyFolder != err {
		t.Errorf("err = %v, want ErrNonEmptyFolder", err)
	}
}

// TestNamespacesAndQuota checks NAMESPACE and the quota tuple.
func TestNamespacesAndQuota(t *testing.T) {
	server := defaultFolderServer(t)
	client := server.connect(t)

	namespaces := client.GetNamespaces()
	if nil == namespaces {
		t.Fatal("NAMESPACE was not parsed")
	}
	if 1 != len(namespaces.Personal) || "INBOX/" != namespaces.Personal[0].Prefix {
		t.Fatalf("personal = %v", namespaces.Personal)
	}
	if "INBOX/" != namespaces.GetPersonalPrefix() {
		t.Errorf("GetPersonalPrefix = %q", namespaces.GetPersonalPrefix())
	}
	if 1 != len(namespaces.OtherUsers) || "Other/" != namespaces.OtherUsers[0].Prefix {
		t.Errorf("users = %v", namespaces.OtherUsers)
	}

	serialized, ok := namespaces.JSONSerialize().(map[string]any)
	if !ok || "Object/Namespaces" != serialized["@Object"] {
		t.Errorf("serialized = %v", namespaces.JSONSerialize())
	}

	quota := client.QuotaRoot("INBOX")
	if 4 != len(quota) || 128 != quota[0] || 2097152 != quota[1] || 3 != quota[2] || 100 != quota[3] {
		t.Errorf("quota = %v", quota)
	}
}

// TestFolderACL checks the MYRIGHTS + GETACL parsing.
func TestFolderACL(t *testing.T) {
	server := defaultFolderServer(t)
	client := server.connect(t)

	acls := client.FolderGetACL("INBOX")
	if 2 != len(acls) {
		t.Fatalf("acls = %v", acls)
	}
	if !acls[0].Mine || "user@example.com" != acls[0].Identifier {
		t.Errorf("own ACL = %+v", acls[0])
	}
	if !acls[0].HasRight("ADMINISTER") {
		t.Error("the rights must report ADMINISTER")
	}
	if "other@example.com" != acls[1].Identifier || "lr" != acls[1].Rights {
		t.Errorf("other ACL = %+v", acls[1])
	}
}

// TestMUTF7RoundTrip checks the modified UTF-7 codec of the folder names.
func TestMUTF7RoundTrip(t *testing.T) {
	for _, name := range []string{"INBOX", "Archive/2024", "Entw&APw-rfe", "日本語", "a&b"} {
		encoded := UTF8ToMUTF7(name)
		if decoded := MUTF7ToUTF8(encoded); decoded != name {
			t.Errorf("MUTF7ToUTF8(UTF8ToMUTF7(%q)) = %q", name, decoded)
		}
	}

	if "&-" != UTF8ToMUTF7("&") {
		t.Errorf("& must encode as &-, got %q", UTF8ToMUTF7("&"))
	}
	if "&" != MUTF7ToUTF8("&-") {
		t.Error("&- must decode to &")
	}
	if !strings.Contains(UTF8ToMUTF7("Entwürfe"), "&") {
		t.Errorf("non-ASCII must be encoded: %q", UTF8ToMUTF7("Entwürfe"))
	}
}

// TestSequenceSetString checks the compressed sequence-set rendering.
func TestSequenceSetString(t *testing.T) {
	set := NewSequenceSet([]any{"1", "2", "3", "5", "*"}, true)
	if "1:3,5,*" != set.String() {
		t.Errorf("String() = %q", set.String())
	}
	if 5 != set.Count() {
		t.Errorf("Count = %d", set.Count())
	}

	// Values that are not a valid sequence-set item are dropped.
	injected := NewSequenceSet([]any{"1:*", "2,3", "4"}, false)
	if "1:*,4" != injected.String() {
		t.Errorf("String() = %q", injected.String())
	}
	if NewSequenceSet(nil, true).String() != "" {
		t.Error("an empty set renders as the empty string")
	}
}
