package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// fakeStreamer is a MessageStreamer returning fixed bytes for one part.
type fakeStreamer struct {
	data        []byte
	contentType string
	fileName    string
	found       bool
	called      bool
}

// MessageMimeStream invokes the handler with an in-memory stream.
func (f *fakeStreamer) MessageMimeStream(
	handler func(reader io.ReadSeeker, contentType, fileName, mimeIndex string),
	folder string, uid int, mimeIndex string,
) bool {
	f.called = true
	if !f.found {
		return false
	}
	handler(bytes.NewReader(f.data), f.contentType, f.fileName, mimeIndex)
	return true
}

func TestRawDownloadRangeReturnsPartialContent(t *testing.T) {
	fixture := newTransferFixture(t)
	data := []byte("0123456789ABCDEFGHIJKLMNOP")
	fixture.appData.options.MessageStreamer = &fakeStreamer{
		data:        data,
		contentType: "application/octet-stream",
		fileName:    "test.bin",
		found:       true,
	}

	rawKey := fixture.appData.encodeRawKey(fixture.account, map[string]any{
		"folder": "INBOX", "uid": 1, "mimeIndex": "2",
	})

	request := httptest.NewRequest(http.MethodGet, "/?/Raw/", nil)
	request.Header.Set("Range", "bytes=10-20")
	recorder := httptest.NewRecorder()

	fixture.appData.Raw(recorder, request, &web.Route{Paths: []string{"Raw", "0", "Download", rawKey}})

	if http.StatusPartialContent != recorder.Code {
		t.Fatalf("status = %d, want 206", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Range"); "bytes 10-20/26" != got {
		t.Errorf("Content-Range = %q, want bytes 10-20/26", got)
	}
	if got := recorder.Header().Get("Content-Length"); "10" != got {
		t.Errorf("Content-Length = %q, want 10", got)
	}
	if got := recorder.Body.String(); got != string(data[10:20]) {
		t.Errorf("body = %q, want %q", got, string(data[10:20]))
	}
	if got := recorder.Header().Get("Content-Type"); "application/octet-stream" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Accept-Ranges"); "bytes" != got {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") ||
		!strings.Contains(got, `filename="test.bin"`) {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := recorder.Header().Get("Content-Transfer-Encoding"); "binary" != got {
		t.Errorf("Content-Transfer-Encoding = %q, want binary", got)
	}
}

func TestRawDownloadServesTheWholeMessage(t *testing.T) {
	fixture := newTransferFixture(t)
	data := []byte("the whole message part")
	fixture.appData.options.MessageStreamer = &fakeStreamer{
		data:        data,
		contentType: "text/plain",
		fileName:    "body.txt",
		found:       true,
	}

	rawKey := fixture.appData.encodeRawKey(fixture.account, map[string]any{
		"folder": "INBOX", "uid": 5,
	})

	request := httptest.NewRequest(http.MethodGet, "/?/Raw/", nil)
	recorder := httptest.NewRecorder()
	fixture.appData.Raw(recorder, request, &web.Route{Paths: []string{"Raw", "0", "Download", rawKey}})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != string(data) {
		t.Errorf("body = %q, want %q", got, string(data))
	}
	if got := recorder.Header().Get("Content-Type"); "text/plain" != got {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
}

func TestRawDownloadOfAnUploadedFile(t *testing.T) {
	fixture := newTransferFixture(t)
	content := []byte("hello attachment")

	// Upload the file first so it lands in the files provider.
	upload := httptest.NewRecorder()
	fixture.appData.Upload(upload, uploadRequest(t, "hello.txt", content),
		&web.Route{Paths: []string{"Upload", "0"}})

	document := decodeResponse(t, upload.Body.Bytes())
	result, _ := document["Result"].(map[string]any)
	attachment, _ := result["Attachment"].(map[string]any)
	tempName, _ := attachment["tempName"].(string)
	if "" == tempName {
		t.Fatalf("the upload returned no tempName: %s", upload.Body.String())
	}

	rawKey := fixture.appData.encodeRawKey(fixture.account, map[string]any{
		"fileHash": tempName,
		"fileName": "hello.txt",
		"mimeType": "text/plain",
	})

	request := httptest.NewRequest(http.MethodGet, "/?/Raw/", nil)
	recorder := httptest.NewRecorder()
	fixture.appData.Raw(recorder, request, &web.Route{Paths: []string{"Raw", "0", "Download", rawKey}})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != string(content) {
		t.Errorf("body = %q, want %q", got, string(content))
	}
	if got := recorder.Header().Get("Content-Type"); "text/plain" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Accept-Ranges"); "none" != got {
		t.Errorf("Accept-Ranges = %q, want none", got)
	}
	if got := recorder.Header().Get("Content-Transfer-Encoding"); "binary" != got {
		t.Errorf("Content-Transfer-Encoding = %q, want binary", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="hello.txt"`) {
		t.Errorf("Content-Disposition = %q", got)
	}

	// A PDF name is always served as application/pdf (issue 144).
	rawKey = fixture.appData.encodeRawKey(fixture.account, map[string]any{
		"fileHash": tempName,
		"fileName": "report.pdf",
		"mimeType": "application/octet-stream",
	})
	recorder = httptest.NewRecorder()
	fixture.appData.Raw(recorder, httptest.NewRequest(http.MethodGet, "/?/Raw/", nil),
		&web.Route{Paths: []string{"Raw", "0", "Download", rawKey}})
	if got := recorder.Header().Get("Content-Type"); "application/pdf" != got {
		t.Errorf("PDF Content-Type = %q, want application/pdf", got)
	}
}

func TestRawUserBackground(t *testing.T) {
	fixture := newTransferFixture(t)

	payload := []byte("background-bytes")
	blob, err := json.Marshal(map[string]any{
		"ContentType": "image/png",
		"Raw":         base64.StdEncoding.EncodeToString(payload),
	})
	if nil != err {
		t.Fatalf("marshal blob: %v", err)
	}
	if err := fixture.storage.Put(fixture.account, storage.TypeConfig, "background", string(blob)); nil != err {
		t.Fatalf("store background: %v", err)
	}

	rawKey := fixture.appData.encodeRawKey(fixture.account, map[string]any{})
	recorder := httptest.NewRecorder()
	fixture.appData.Raw(recorder, httptest.NewRequest(http.MethodGet, "/?/Raw/", nil),
		&web.Route{Paths: []string{"Raw", "0", "UserBackground", rawKey}})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != string(payload) {
		t.Errorf("body = %q, want %q", got, string(payload))
	}
	if got := recorder.Header().Get("Content-Type"); "image/png" != got {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestDecodeRawKeyValidatesTheAccount(t *testing.T) {
	fixture := newTransferFixture(t)

	rawKey := fixture.appData.encodeRawKey(fixture.account, map[string]any{
		"folder": "INBOX", "uid": 1,
	})
	if nil == fixture.appData.decodeRawKey(rawKey, fixture.account) {
		t.Error("a RawKey for the current account must decode")
	}

	other := mainAccount()
	other.SetImapUser("someone-else@example.com")
	if nil != fixture.appData.decodeRawKey(rawKey, other) {
		t.Error("a RawKey carrying another account hash must be rejected")
	}

	if nil != fixture.appData.decodeRawKey("!!!notbase64!!!", fixture.account) {
		t.Error("invalid base64 must be rejected")
	}

	// A guest (no account) may decode but an indexed array exposes accountHash[3].
	indexed := fixture.appData.encodeRawKey(nil, map[string]any{"0": "INBOX", "1": 1})
	if nil == fixture.appData.decodeRawKey(indexed, nil) {
		t.Error("a guest RawKey must decode")
	}
}

func TestRawUnknownActionIsEmpty(t *testing.T) {
	fixture := newTransferFixture(t)

	recorder := httptest.NewRecorder()
	fixture.appData.Raw(recorder, httptest.NewRequest(http.MethodGet, "/?/Raw/", nil),
		&web.Route{Paths: []string{"Raw", "0", "Bogus", ""}})

	if "" != recorder.Body.String() {
		t.Errorf("body = %q, want empty", recorder.Body.String())
	}
	if "" != recorder.Header().Get("X-Raw-Action") {
		t.Errorf("X-Raw-Action = %q, want empty for an unknown action", recorder.Header().Get("X-Raw-Action"))
	}
}

func TestClearFileName(t *testing.T) {
	tests := []struct {
		fileName    string
		contentType string
		mimeIndex   string
		want        string
	}{
		{"report 2024.pdf", "", "", "report 2024.pdf"},
		{"a//b\\c.txt", "", "", "a--b-c.txt"},
		{"", "image/png", "", "image.png"},
		{"", "image/png", "2", "2.image.png"},
		{"../../etc/passwd", "", "", ".-.-etc-passwd"},
	}

	for _, test := range tests {
		if got := clearFileName(test.fileName, test.contentType, test.mimeIndex, 250); got != test.want {
			t.Errorf("clearFileName(%q, %q, %q) = %q, want %q",
				test.fileName, test.contentType, test.mimeIndex, got, test.want)
		}
	}
}
