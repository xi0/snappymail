package handler

// Tests of Step 20: DoMessageUploadAttachments and DoAttachmentsActions against
// an in-process IMAP server and a real files provider.

import (
	"archive/zip"
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/domain"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mail"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// attachmentsConfig enables capa.attachments_actions and reports the defaults
// otherwise.
type attachmentsConfig struct{}

func (attachmentsConfig) GetString(_, _, defaultValue string) string { return defaultValue }

func (attachmentsConfig) GetBool(section, key string, defaultValue bool) bool {
	if "capa" == section && "attachments_actions" == key {
		return true
	}
	return defaultValue
}

func (attachmentsConfig) GetInt(_, _ string, defaultValue int) int { return defaultValue }

// attachServer serves one message with a base64 PDF part 2.
type attachServer struct {
	listener net.Listener
	port     int

	mu       sync.Mutex
	received []string
}

// The MIME header and body of part 2 (a base64 PDF attachment).
const attachPartHeader = "Content-Type: application/pdf; name=\"report.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n"

// attachTextHeader is the MIME header of part 3 (a base64 text attachment).
const attachTextHeader = "Content-Type: text/plain; name=\"note.txt\"\r\n" +
	"Content-Disposition: attachment; filename=\"note.txt\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n"

// newAttachServer starts the mock.
func newAttachServer(t *testing.T) *attachServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if nil != err {
		t.Fatalf("listen: %v", err)
	}

	server := &attachServer{listener: listener, port: listener.Addr().(*net.TCPAddr).Port}
	go server.acceptLoop()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

// connect opens and authenticates a mail client on the mock.
func (s *attachServer) connect(t *testing.T) *mail.Client {
	t.Helper()

	settings := domain.New("example.com", domain.Defaults{}).IMAP
	settings.Host = "127.0.0.1"
	settings.Port = s.port
	settings.Type = domain.SecurityNone
	settings.Timeout = 5
	settings.SSL.VerifyPeer = false

	client := imapclient.New(&settings, nil)
	if err := client.Connect(); nil != err {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Login("user@example.com", "secret"); nil != err {
		t.Fatalf("Login: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mail.New(client)
}

func (s *attachServer) acceptLoop() {
	for {
		connection, err := s.listener.Accept()
		if nil != err {
			return
		}
		go s.handle(connection)
	}
}

func (s *attachServer) handle(connection net.Conn) {
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

		tag, rest := splitOnce(line, " ")
		if strings.HasPrefix(strings.ToUpper(rest), "UID ") {
			rest = rest[4:]
		}
		command, args := splitOnce(rest, " ")

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
			item := bodyPeekItemArg(args)
			content := base64.StdEncoding.EncodeToString([]byte("PDFDATA"))
			switch {
			case "[3.MIME]" == item:
				content = attachTextHeader
			case strings.HasSuffix(item, ".MIME]"):
				content = attachPartHeader
			case "[3]" == item:
				content = base64.StdEncoding.EncodeToString([]byte("notedata"))
			}
			write(`* 1 FETCH (UID 42 RFC822.SIZE 500 BODY` + item + ` {` + strconv.Itoa(len(content)) + `}`)
			writeRaw(content)
			write(`)`)
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

// bodyPeekItemArg extracts the "[...]" of the first BODY.PEEK item.
func bodyPeekItemArg(rest string) string {
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

// splitOnce splits value on the first separator.
func splitOnce(value, separator string) (string, string) {
	if index := strings.Index(value, separator); 0 <= index {
		return value[:index], value[index+len(separator):]
	}
	return value, ""
}

// rawKey builds a RawKey with the given fields and the current account hash.
func rawKey(t *testing.T, resolved *account.Account, salt string, fields map[string]any) string {
	t.Helper()

	document := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		document[key] = value
	}
	document["accountHash"] = resolved.Hash(salt)

	encoded, err := json.Marshal(document)
	if nil != err {
		t.Fatalf("marshal raw key: %v", err)
	}
	return cryptox.URLSafeBase64Encode(string(encoded))
}

// attachmentsFixture builds a dispatcher wired to a real files provider.
func attachmentsFixture(t *testing.T, resolved *account.Account, client *mail.Client) (*Dispatcher, FilesProvider) {
	t.Helper()

	files := storage.NewFiles(t.TempDir())
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Salt:    "salt",
		Config:  attachmentsConfig{},
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Files:   files,
		Mail: func(http.ResponseWriter, *http.Request) Mail {
			return fakeMail{resolved: resolved, client: client}
		},
	})
	return dispatcher, files
}

// TestAttachmentActionsAreRegistered checks the registry of step 20.
func TestAttachmentActionsAreRegistered(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})
	for _, action := range []string{"MessageUploadAttachments", "AttachmentsActions"} {
		if !dispatcher.registry.Has(action) {
			t.Errorf("%s is not registered", action)
		}
	}
}

// TestAttachmentsActionsDisabled checks the capa.attachments_actions gate: with
// the capability off the action reports false.
func TestAttachmentsActionsDisabled(t *testing.T) {
	resolved := folderAccount()
	files := storage.NewFiles(t.TempDir())
	session := &folderSession{account: resolved}
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Salt:    "salt",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		Files:   files,
	})

	ctx := folderContext(dispatcher, session, map[string]any{"target": "zip"})
	response := mustResponse(doAttachmentsActions(ctx))
	if false != response["Result"] {
		t.Errorf("Result = %v, want false without the capability", response["Result"])
	}
}

// TestAttachmentsActionsZipOfData checks the zip branch end to end: two
// data-backed RawKeys become an archive with the expected entry names and
// contents, stored in the files provider under the returned fileHash.
func TestAttachmentsActionsZipOfData(t *testing.T) {
	resolved := folderAccount()
	dispatcher, files := attachmentsFixture(t, resolved, nil)
	session := &folderSession{account: resolved}

	ctx := folderContext(dispatcher, session, map[string]any{
		"target": "zip",
		"hashes": []any{
			rawKey(t, resolved, "salt", map[string]any{
				"fileName": "a.txt",
				"data":     cryptox.URLSafeBase64Encode("hello"),
			}),
			rawKey(t, resolved, "salt", map[string]any{
				"fileName": "b.txt",
				"data":     cryptox.URLSafeBase64Encode("world"),
			}),
		},
	})

	response := mustResponse(doAttachmentsActions(ctx))
	rawZipKey, ok := response["Result"].(string)
	if !ok || "" == rawZipKey {
		t.Fatalf("Result = %v, want a zip RawKey", response["Result"])
	}

	// Decode the returned RawKey: {fileName, mimeType, fileHash}.
	decoded, err := cryptox.URLSafeBase64Decode(rawZipKey)
	if nil != err {
		t.Fatalf("decode zip RawKey: %v", err)
	}
	var zipKey map[string]any
	if err := json.Unmarshal(decoded, &zipKey); nil != err {
		t.Fatalf("unmarshal zip RawKey: %v", err)
	}
	if "application/zip" != zipKey["mimeType"] {
		t.Errorf("mimeType = %v", zipKey["mimeType"])
	}
	if name, _ := zipKey["fileName"].(string); !strings.HasSuffix(name, ".zip") {
		t.Errorf("fileName = %q, want a .zip name", name)
	}

	zipHash, _ := zipKey["fileHash"].(string)
	handle, err := files.Open(resolved, zipHash)
	if nil != err {
		t.Fatalf("open zip: %v", err)
	}
	defer handle.Close()

	size, _ := files.FileSize(resolved, zipHash)
	archive, err := zip.NewReader(handle, size)
	if nil != err {
		t.Fatalf("read zip: %v", err)
	}

	contents := map[string]string{}
	for _, entry := range archive.File {
		reader, openErr := entry.Open()
		if nil != openErr {
			t.Fatalf("open entry: %v", openErr)
		}
		data, _ := io.ReadAll(reader)
		_ = reader.Close()
		contents[entry.Name] = string(data)
	}

	if "hello" != contents["a.txt"] {
		t.Errorf("a.txt = %q, want hello", contents["a.txt"])
	}
	if "world" != contents["b.txt"] {
		t.Errorf("b.txt = %q, want world", contents["b.txt"])
	}
}

// TestAttachmentsActionsEmptyHashes checks the false result of an empty hash
// list.
func TestAttachmentsActionsEmptyHashes(t *testing.T) {
	resolved := folderAccount()
	dispatcher, _ := attachmentsFixture(t, resolved, nil)
	session := &folderSession{account: resolved}

	response := mustResponse(doAttachmentsActions(folderContext(dispatcher, session, map[string]any{
		"target": "zip",
		"hashes": []any{},
	})))
	if false != response["Result"] {
		t.Errorf("Result = %v, want false without hashes", response["Result"])
	}
}

// TestAttachmentsActionsNeedsAnAccount checks the InvalidToken failure (101) of
// a request without a session and without a mail layer.
func TestAttachmentsActionsNeedsAnAccount(t *testing.T) {
	files := storage.NewFiles(t.TempDir())
	dispatcher := NewDispatcher(Options{
		Version: "0.0.0",
		Salt:    "salt",
		Config:  attachmentsConfig{},
		Files:   files,
	})

	_, err := doAttachmentsActions(folderContext(dispatcher, nil, map[string]any{
		"target": "zip",
		"hashes": []any{"x"},
	}))
	if appctx.InvalidToken != clientCodeOf(err) {
		t.Errorf("code = %d, want %d", clientCodeOf(err), appctx.InvalidToken)
	}
}

// TestMessageUploadAttachmentsStoresThePart checks that a message part is
// fetched, stored under sha1(rawKey)+extension and reported with its MIME type.
func TestMessageUploadAttachmentsStoresThePart(t *testing.T) {
	resolved := folderAccount()
	server := newAttachServer(t)
	client := server.connect(t)

	dispatcher, files := attachmentsFixture(t, resolved, client)
	session := &folderSession{account: resolved}

	key := rawKey(t, resolved, "salt", map[string]any{
		"folder":    "INBOX",
		"uid":       42,
		"mimeIndex": "2",
	})

	uploadResult, uploadErr := doMessageUploadAttachments(folderContext(dispatcher, session, map[string]any{
		"attachments": []any{key},
	}))
	if nil != uploadErr {
		t.Fatalf("doMessageUploadAttachments: %v", uploadErr)
	}
	response := mustResponse(uploadResult, uploadErr)

	list, ok := response["Result"].([]any)
	if !ok || 1 != len(list) {
		t.Fatalf("Result = %v, want a one element list", response["Result"])
	}
	stored, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("slot = %v, want a {tempName, mimeType} map", list[0])
	}

	wantTempName := storage.SHA1(key) + ".pdf"
	if wantTempName != stored["tempName"] {
		t.Errorf("tempName = %v, want %q", stored["tempName"], wantTempName)
	}
	if "application/pdf" != stored["mimeType"] {
		t.Errorf("mimeType = %v, want application/pdf", stored["mimeType"])
	}
	if !files.FileExists(resolved, wantTempName) {
		t.Errorf("the part was not stored as %q", wantTempName)
	}
}

// TestMessageUploadAttachmentsRejectsANonArray checks the empty result of a
// non-array attachments parameter.
func TestMessageUploadAttachmentsRejectsANonArray(t *testing.T) {
	resolved := folderAccount()
	dispatcher, _ := attachmentsFixture(t, resolved, nil)
	session := &folderSession{account: resolved}

	uploadResult, uploadErr := doMessageUploadAttachments(folderContext(dispatcher, session, map[string]any{
		"attachments": "not-an-array",
	}))
	if nil != uploadErr {
		t.Fatalf("doMessageUploadAttachments: %v", uploadErr)
	}
	response := mustResponse(uploadResult, uploadErr)
	list, ok := response["Result"].([]any)
	if !ok || 0 != len(list) {
		t.Errorf("Result = %v, want an empty list", response["Result"])
	}
}

// TestAttachmentsActionsZipOfMessageParts checks the message-part branch of
// getMimeFileByHash() and the "uid-" entry prefix of a single-uid zip: two parts
// of the same message are fetched, packed and cleaned up.
func TestAttachmentsActionsZipOfMessageParts(t *testing.T) {
	resolved := folderAccount()
	server := newAttachServer(t)
	client := server.connect(t)

	dispatcher, files := attachmentsFixture(t, resolved, client)
	session := &folderSession{account: resolved}

	pdfKey := rawKey(t, resolved, "salt", map[string]any{
		"fileName":  "report.pdf",
		"folder":    "INBOX",
		"uid":       42,
		"mimeIndex": "2",
	})
	textKey := rawKey(t, resolved, "salt", map[string]any{
		"fileName":  "note.txt",
		"folder":    "INBOX",
		"uid":       42,
		"mimeIndex": "3",
	})

	response := mustResponse(doAttachmentsActions(folderContext(dispatcher, session, map[string]any{
		"target": "zip",
		"folder": "INBOX",
		"hashes": []any{pdfKey, textKey},
	})))
	rawZipKey, ok := response["Result"].(string)
	if !ok || "" == rawZipKey {
		t.Fatalf("Result = %v, want a zip RawKey", response["Result"])
	}

	decoded, err := cryptox.URLSafeBase64Decode(rawZipKey)
	if nil != err {
		t.Fatalf("decode zip RawKey: %v", err)
	}
	var zipKey map[string]any
	if err := json.Unmarshal(decoded, &zipKey); nil != err {
		t.Fatalf("unmarshal zip RawKey: %v", err)
	}

	zipHash, _ := zipKey["fileHash"].(string)
	handle, err := files.Open(resolved, zipHash)
	if nil != err {
		t.Fatalf("open zip: %v", err)
	}
	defer handle.Close()

	size, _ := files.FileSize(resolved, zipHash)
	archive, err := zip.NewReader(handle, size)
	if nil != err {
		t.Fatalf("read zip: %v", err)
	}

	contents := map[string]string{}
	for _, entry := range archive.File {
		reader, openErr := entry.Open()
		if nil != openErr {
			t.Fatalf("open entry: %v", openErr)
		}
		data, _ := io.ReadAll(reader)
		_ = reader.Close()
		contents[entry.Name] = string(data)
	}

	// One uid and a folder: the "uid-" prefix, with the decoded part contents.
	if "PDFDATA" != contents["42-report.pdf"] {
		t.Errorf("42-report.pdf = %q, want PDFDATA", contents["42-report.pdf"])
	}
	if "notedata" != contents["42-note.txt"] {
		t.Errorf("42-note.txt = %q, want notedata", contents["42-note.txt"])
	}
}
