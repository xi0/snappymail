package service

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/config"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// transferFixture is an AppData wired to a real files/storage provider so the
// upload and raw endpoints round-trip through disk.
type transferFixture struct {
	t       *testing.T
	tree    testTree
	config  *config.Application
	appData *AppData
	account *account.Account
	files   *storage.Files
	storage *storage.Provider
}

// newTransferFixture builds the fixture on the default application.ini values.
func newTransferFixture(t *testing.T) *transferFixture {
	t.Helper()

	tree := newTestTree(t)
	application := newConfig(t)
	storageDir := filepath.Join(tree.root, "storage")

	files := storage.NewFiles(storageDir)
	provider := storage.NewProvider(storage.NewFileStorage(storageDir, false))
	resolved := mainAccount()

	options := Options{
		Config:        application,
		Version:       testVersion,
		Salt:          testSalt,
		Paths:         tree.paths(),
		IndexRoot:     tree.indexRoot,
		PrivateData:   filepath.Join(tree.root, "private"),
		Settings:      storage.NewSettingsStorage(provider),
		LocalSettings: storage.NewSettingsStorage(provider),
		Files:         files,
		Storage:       provider,
		NewSession: func(w http.ResponseWriter, r *http.Request) *Session {
			return &Session{Request: r, Response: w, Account: resolved}
		},
	}

	return &transferFixture{
		t:       t,
		tree:    tree,
		config:  application,
		appData: NewAppData(options),
		account: resolved,
		files:   files,
		storage: provider,
	}
}

// uploadRequest builds a multipart POST with one "uploader" file part.
func uploadRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(uploadFormField, filename)
	if nil != err {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); nil != err {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); nil != err {
		t.Fatalf("close writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/?/Upload/&q[]=/0/", body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// decodeResponse decodes the JSON upload envelope.
func decodeResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var document map[string]any
	if err := json.Unmarshal(body, &document); nil != err {
		t.Fatalf("response is not JSON (%v): %s", err, body)
	}
	return document
}

func TestUploadStoresTheAttachment(t *testing.T) {
	fixture := newTransferFixture(t)

	// A PNG signature is enough for MimeType detection.
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x00}
	recorder := httptest.NewRecorder()
	fixture.appData.Upload(recorder, uploadRequest(t, "note.png", png), &web.Route{Paths: []string{"Upload", "0"}})

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "application/json; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}

	document := decodeResponse(t, recorder.Body.Bytes())
	if "Upload" != document["Action"] {
		t.Errorf("Action = %v, want Upload", document["Action"])
	}

	result, ok := document["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", document["Result"])
	}
	attachment, ok := result["Attachment"].(map[string]any)
	if !ok {
		t.Fatalf("Result.Attachment = %#v", result["Attachment"])
	}

	if "note.png" != attachment["name"] {
		t.Errorf("name = %v", attachment["name"])
	}
	if "image/png" != attachment["mimeType"] {
		t.Errorf("mimeType = %v, want image/png", attachment["mimeType"])
	}
	if float64(len(png)) != attachment["size"] {
		t.Errorf("size = %v, want %d", attachment["size"], len(png))
	}

	tempName, _ := attachment["tempName"].(string)
	if !strings.HasPrefix(tempName, "upload-post-") || !strings.HasSuffix(tempName, ".png") {
		t.Errorf("tempName = %q, want upload-post-….png", tempName)
	}

	// The upload landed in the files provider under the tempName key.
	if !fixture.files.FileExists(fixture.account, tempName) {
		t.Errorf("the uploaded file %q is missing from the files provider", tempName)
	}
}

func TestUploadWithoutAFileReportsTheError(t *testing.T) {
	fixture := newTransferFixture(t)

	// No multipart body at all: empty($_FILES) => UPLOAD_ERR_INI_SIZE.
	request := httptest.NewRequest(http.MethodPost, "/?/Upload/&q[]=/0/", strings.NewReader(""))
	recorder := httptest.NewRecorder()
	fixture.appData.Upload(recorder, request, &web.Route{Paths: []string{"Upload", "0"}})

	document := decodeResponse(t, recorder.Body.Bytes())
	result, _ := document["Result"].(map[string]any)
	if nil == result {
		t.Fatalf("Result = %#v", document["Result"])
	}
	if float64(uploadErrIniSize) != result["code"] {
		t.Errorf("code = %v, want %d", result["code"], uploadErrIniSize)
	}
	if "File is too big" != result["Error"] {
		t.Errorf("Error = %v", result["Error"])
	}
}

func TestUploadBackgroundRejectsNonImages(t *testing.T) {
	fixture := newTransferFixture(t)
	fixture.config.Set("webmail", "allow_user_background", true)

	recorder := httptest.NewRecorder()
	fixture.appData.UploadBackground(recorder, uploadRequest(t, "bg.txt", []byte("not an image")),
		&web.Route{Paths: []string{"UploadBackground", "0"}})

	document := decodeResponse(t, recorder.Body.Bytes())
	if "UploadBackground" != document["Action"] {
		t.Errorf("Action = %v", document["Action"])
	}
	if false != document["Result"] {
		t.Errorf("Result = %#v, want false", document["Result"])
	}
	if float64(uploadErrFileType) != document["code"] {
		t.Errorf("code = %v, want %d", document["code"], uploadErrFileType)
	}
	if "Invalid file type" != document["message"] {
		t.Errorf("message = %v", document["message"])
	}
}

func TestUploadBackgroundStoresTheImage(t *testing.T) {
	fixture := newTransferFixture(t)
	fixture.config.Set("webmail", "allow_user_background", true)

	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x01, 0x02, 0x03}
	recorder := httptest.NewRecorder()
	fixture.appData.UploadBackground(recorder, uploadRequest(t, "bg.png", png),
		&web.Route{Paths: []string{"UploadBackground", "0"}})

	document := decodeResponse(t, recorder.Body.Bytes())
	result, ok := document["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", document["Result"])
	}
	if "bg.png" != result["name"] {
		t.Errorf("name = %v", result["name"])
	}
	hash, _ := result["hash"].(string)
	if "" == hash {
		t.Error("hash must not be empty")
	}

	// The background blob is kept in the CONFIG storage.
	blob, ok := fixture.storage.Get(fixture.account, storage.TypeConfig, "background")
	if !ok || "" == blob {
		t.Fatal("the background blob is missing from the storage provider")
	}
	if !strings.Contains(blob, `"ContentType":"image/png"`) {
		t.Errorf("background blob = %s", blob)
	}

	// The settings hold the reported name/hash.
	settings := storage.NewSettingsStorage(fixture.storage).Load(fixture.account)
	if "bg.png" != settings.GetConf("UserBackgroundName", "") {
		t.Errorf("UserBackgroundName = %v", settings.GetConf("UserBackgroundName", ""))
	}
	if hash != settings.GetConf("UserBackgroundHash", "") {
		t.Errorf("UserBackgroundHash = %v, want %s", settings.GetConf("UserBackgroundHash", ""), hash)
	}
}

func TestUploadUserMessage(t *testing.T) {
	tests := []struct {
		code       int
		clientCode int
		message    string
	}{
		{uploadErrOK, 0, ""},
		{uploadErrIniSize, uploadErrIniSize, "File is too big"},
		{uploadErrConfigSize, uploadErrConfigSize, "File is too big"},
		{uploadErrOnSaving, uploadErrOnSaving, "Error saving file"},
		{uploadErrFileType, uploadErrFileType, "Invalid file type"},
		{uploadErrEmptyFile, uploadErrEmptyFile, "File is empty"},
		{12345, uploadErrUnknown, "Unknown error"},
	}

	for _, test := range tests {
		clientCode, message := uploadUserMessage(test.code)
		if clientCode != test.clientCode || message != test.message {
			t.Errorf("uploadUserMessage(%d) = (%d, %q), want (%d, %q)",
				test.code, clientCode, message, test.clientCode, test.message)
		}
	}
}
