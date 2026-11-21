package service

// ServiceActions::privateUpload() and the three upload endpoints
// (ServiceUpload, ServiceUploadContacts, ServiceUploadBackground) plus the
// Actions::Upload / Actions\Themes::UploadBackground / Actions\Contacts::
// UploadContacts handlers they dispatch to.
//
// The upload envelope is NOT the JSON action envelope: privateUpload() builds
// {Action, Result} (plus code/message/Error on failure) and never adds "epoch",
// and the multipart field is always named "uploader". The per-endpoint size
// limit comes from webmail.attachment_size_limit (Upload) or is fixed at 5 MB
// (contacts) / 1 MB (background).

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// The upload error codes of RainLoop\Enumerations\UploadError plus the PHP
// UPLOAD_ERR_* values it maps onto.
const (
	uploadErrOK         = 0
	uploadErrIniSize    = 1
	uploadErrFormSize   = 2
	uploadErrPartial    = 3
	uploadErrNoFile     = 4
	uploadErrNoTmpDir   = 6
	uploadErrCantWrite  = 7
	uploadErrExtension  = 8
	uploadErrFileType   = 98
	uploadErrUnknown    = 99
	uploadErrConfigSize = 1001
	uploadErrOnSaving   = 1002
	uploadErrEmptyFile  = 1003
)

// uploadFormField is the $_FILES key privateUpload() reads.
const uploadFormField = "uploader"

// uploadErrorMessages is RainLoop\Enumerations\UploadError::$messages.
var uploadErrorMessages = map[int]string{
	uploadErrIniSize:    "Filesize exceeds the upload_max_filesize directive in php.ini",
	uploadErrFormSize:   "Filesize exceeds the MAX_FILE_SIZE directive that was specified in the html form",
	uploadErrPartial:    "File was only partially uploaded",
	uploadErrNoFile:     "No file was uploaded",
	uploadErrNoTmpDir:   "Missing a temporary folder",
	uploadErrCantWrite:  "Failed to write file to disk",
	uploadErrExtension:  "File upload stopped by extension",
	uploadErrFileType:   "Invalid file type",
	uploadErrUnknown:    "Unknown error",
	uploadErrConfigSize: "Filesize exceeds the config setting",
	uploadErrOnSaving:   "Error saving file",
	uploadErrEmptyFile:  "File is empty",
}

// settingsSaver is Providers\Settings::Save() (RainLoop\Settings::save()).
type settingsSaver interface {
	Save(account storage.Account, settings *storage.Settings) error
}

// contactImporter is the Actions\Contacts import surface UploadContacts needs
// (step 22 wires it). A nil importer imports nothing, returning 0 like PHP does
// without an active address book.
type contactImporter interface {
	ImportVcfFile(account *account.Account, data []byte) int
	ImportCsvFile(account *account.Account, data []byte, header string) int
}

// uploadUserMessage is UploadError::getUserMessage(): the client error code and
// the user facing message of an upload failure.
func uploadUserMessage(code int) (clientCode int, message string) {
	clientCode = code

	switch code {
	case uploadErrOK,
		uploadErrPartial,
		uploadErrNoFile,
		uploadErrFileType,
		uploadErrEmptyFile:
		// The message (if any) of the code itself.

	case uploadErrIniSize, uploadErrFormSize, uploadErrConfigSize:
		return clientCode, "File is too big"

	case uploadErrNoTmpDir, uploadErrCantWrite, uploadErrExtension, uploadErrOnSaving:
		clientCode = uploadErrOnSaving

	default:
		clientCode = uploadErrUnknown
	}

	return clientCode, uploadErrorMessages[clientCode]
}

// defaultResponse is Actions\Response::DefaultResponse(): the {Action, Result}
// envelope plus the code/message defaults when the result is exactly false.
func defaultResponse(result any, additional map[string]any) map[string]any {
	response := map[string]any{"Action": "", "Result": result}

	if isFalseResult(result) {
		if _, ok := additional["code"]; !ok {
			response["code"] = 0
		}
		if _, ok := additional["message"]; !ok {
			response["message"] = ""
		}
	}

	for key, value := range additional {
		response[key] = value
	}

	return response
}

// falseResponse is Actions\Response::FalseResponse().
func falseResponse(code int, message, additionalMessage string) map[string]any {
	return defaultResponse(false, map[string]any{
		"code":              code,
		"message":           message,
		"messageAdditional": additionalMessage,
	})
}

// exceptionResponse is Actions\Response::ExceptionResponse(): a ClientError
// keeps its notification code and message, anything else is an UnknownError.
func exceptionResponse(err error) map[string]any {
	var client *appctx.ClientError
	if errors.As(err, &client) {
		return defaultResponse(false, map[string]any{
			"code":              client.Code,
			"message":           client.Message,
			"messageAdditional": client.AdditionalMessage,
			"ExceptionCode":     0,
		})
	}

	code := appctx.UnknownError
	message := ""
	if nil != err {
		message = err.Error()
	}
	return defaultResponse(false, map[string]any{
		"code":              code,
		"message":           message,
		"messageAdditional": "",
		"ExceptionCode":     0,
	})
}

// isFalseResult reports whether result is the boolean false that triggers the
// code/message defaults (PHP: "false === $mResult").
func isFalseResult(result any) bool {
	value, ok := result.(bool)
	return ok && !value
}

// uploadFile mirrors the PHP $_FILES entry after validation: name, size, the
// client supplied content type and the readable content.
type uploadFile struct {
	Name    string
	Size    int64
	Type    string
	Content multipart.File
}

// Upload is ServiceActions::ServiceUpload(): ?/Upload.
func (a *AppData) Upload(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	return a.privateUpload(w, r, "Upload", 0)
}

// UploadContacts is ServiceActions::ServiceUploadContacts(): a 5 MB cap.
func (a *AppData) UploadContacts(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	return a.privateUpload(w, r, "UploadContacts", 5)
}

// UploadBackground is ServiceActions::ServiceUploadBackground(): a 1 MB cap.
func (a *AppData) UploadBackground(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	return a.privateUpload(w, r, "UploadBackground", 1)
}

// privateUpload is ServiceActions::privateUpload(): parse the "uploader" field,
// enforce the size limit, dispatch to the action and wrap the result in the
// upload envelope.
func (a *AppData) privateUpload(w http.ResponseWriter, r *http.Request, action string, sizeLimitMB int) bool {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	// privateUpload() catches every Throwable and reports ExceptionResponse().
	response := func() (result map[string]any) {
		defer func() {
			if recovered := recover(); nil != recovered {
				result = exceptionResponse(panicError(recovered))
			}
		}()

		if nil == a.options.Files {
			return exceptionResponse(appctx.NewClientError(appctx.UnknownError, nil, ""))
		}

		return a.runUpload(w, r, action, sizeLimitMB)
	}()

	if nil == response {
		response = exceptionResponse(appctx.NewClientError(appctx.UnknownError, nil, ""))
	}

	// $aResponse['Action'] = $sAction;
	response["Action"] = action

	encoded, err := encodeJSON(response)
	if nil != err {
		w.WriteHeader(http.StatusInternalServerError)
		return true
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)

	return true
}

// runUpload is the try block of privateUpload().
func (a *AppData) runUpload(w http.ResponseWriter, r *http.Request, action string, sizeLimitMB int) map[string]any {
	file, uploadError := a.parseUpload(r, sizeLimitMB)
	if nil != file {
		defer func() { _ = file.Content.Close() }()
	}

	session := a.session(w, r)
	resolved, _ := accounts(session)
	if nil == resolved {
		// getAccountFromToken() of every action throws AuthError without one.
		return exceptionResponse(appctx.NewClientError(appctx.AuthError, nil, ""))
	}

	var response map[string]any
	switch action {
	case "Upload":
		response = a.doUpload(resolved, file, uploadError)
	case "UploadContacts":
		response = a.doUploadContacts(resolved, file, uploadError)
	case "UploadBackground":
		response = a.doUploadBackground(resolved, file, uploadError)
	default:
		return exceptionResponse(appctx.NewClientError(appctx.UnknownError, nil, ""))
	}

	// $this->Plugins()->RunHook('filter.upload-response', array(&$aResponse));
	if nil != a.options.Plugins && nil != response {
		a.options.Plugins.RunHook("filter.upload-response", &response)
	}

	return response
}

// parseUpload is the $_FILES part of privateUpload(): the accepted file (or nil)
// and the upload error code.
func (a *AppData) parseUpload(r *http.Request, sizeLimitMB int) (*uploadFile, int) {
	limit := int64(sizeLimitMB) * 1024 * 1024
	if 0 >= limit {
		limit = int64(a.config().GetInt("webmail", "attachment_size_limit", 0)) * 1024 * 1024
	}

	// PHP parses multipart bodies before the endpoint runs; Go has to ask.
	if err := r.ParseMultipartForm(32 << 20); nil != err {
		// A body that is not multipart (or that cannot be parsed) is the
		// empty($_FILES) case: UPLOAD_ERR_INI_SIZE.
		return nil, uploadErrIniSize
	}

	headers := r.MultipartForm.File[uploadFormField]
	if 0 == len(headers) {
		if 0 == len(r.MultipartForm.File) {
			return nil, uploadErrIniSize
		}
		return nil, uploadErrEmptyFile
	}

	header := headers[0]

	content, err := header.Open()
	if nil != err {
		return nil, uploadErrCantWrite
	}

	file := &uploadFile{
		Name:    header.Filename,
		Size:    header.Size,
		Type:    header.Header.Get("Content-Type"),
		Content: content,
	}

	if 0 < limit && limit < file.Size {
		_ = content.Close()
		return nil, uploadErrConfigSize
	}

	return file, uploadErrOK
}

// uploadErrorResponse is the "if (UPLOAD_ERR_OK !== $iError)" block of
// Actions::Upload: the code/Error fields, empty when there is no message.
func uploadErrorResponse(response map[string]any, uploadError int) map[string]any {
	if uploadErrOK == uploadError {
		return response
	}

	clientCode, message := uploadUserMessage(uploadError)
	if "" != message {
		response["code"] = clientCode
		response["Error"] = message
	}

	return response
}

// doUpload is Actions::Upload(): store the upload in the files provider and
// report the attachment metadata.
func (a *AppData) doUpload(resolved *account.Account, file *uploadFile, uploadError int) map[string]any {
	response := map[string]any{}

	if nil != resolved && uploadErrOK == uploadError && nil != file {
		savedName := "upload-post-" + md5Hex(file.Name+randomToken())

		// Detect content-type: fromFile() ?: fromFilename().
		if detected := detectUploadMime(file); "" != detected {
			file.Type = detected
			savedName += mime.ToExtension(detected)
		}

		if err := a.options.Files.PutFile(resolved, savedName, file.Content); nil != err {
			uploadError = uploadErrOnSaving
		} else {
			response["Attachment"] = map[string]any{
				"name":     file.Name,
				"tempName": savedName,
				"mimeType": file.Type,
				"size":     file.Size,
			}
		}
	}

	response = uploadErrorResponse(response, uploadError)

	return defaultResponse(response, map[string]any{})
}

// doUploadContacts is Actions\Contacts::UploadContacts(): save the upload and
// import it as VCF or CSV.
func (a *AppData) doUploadContacts(resolved *account.Account, file *uploadFile, uploadError int) map[string]any {
	var result any = false

	if nil != resolved && uploadErrOK == uploadError && nil != file {
		savedName := "upload-post-" + md5Hex(file.Name+randomToken())

		if err := a.options.Files.PutFile(resolved, savedName, file.Content); nil != err {
			uploadError = uploadErrOnSaving
		} else {
			result = a.importContacts(resolved, savedName)
			a.options.Files.Clear(resolved, savedName)
		}
	}

	if uploadErrOK != uploadError {
		clientCode, message := uploadUserMessage(uploadError)
		if "" != message {
			return falseResponse(clientCode, message, "")
		}
	}

	return defaultResponse(result, map[string]any{})
}

// importContacts reads the start of the saved upload and imports it as VCF or
// CSV, returning the imported count (0 without a usable address book).
func (a *AppData) importContacts(resolved *account.Account, savedName string) any {
	importer, ok := a.options.AddressBook.(contactImporter)
	if !ok || nil == a.options.AddressBook {
		return 0
	}

	handle, err := a.options.Files.Open(resolved, savedName)
	if nil != err {
		return 0
	}
	defer func() { _ = handle.Close() }()

	start := make([]byte, 128)
	read, _ := handle.Read(start)
	header := strings.TrimSpace(string(start[:read]))

	switch {
	case strings.Contains(header, "BEGIN:VCARD"):
		data, _ := io.ReadAll(handle)
		return importer.ImportVcfFile(resolved, data)
	case strings.Contains(header, ",") || strings.Contains(header, ";"):
		data, _ := io.ReadAll(handle)
		return importer.ImportCsvFile(resolved, data, header)
	}

	return false
}

// doUploadBackground is Actions\Themes::UploadBackground(): accept an image,
// store it as the account background and report its name and hash.
func (a *AppData) doUploadBackground(resolved *account.Account, file *uploadFile, uploadError int) map[string]any {
	if !phpBool(a.capa(false, resolved)["UserBackground"]) {
		return falseResponse(0, "", "")
	}

	name := ""
	hash := ""

	if nil != resolved && uploadErrOK == uploadError && nil != file {
		mimeType := detectUploadMime(file)
		if "" == mimeType {
			mimeType = file.Type
		}

		switch mimeType {
		case "image/png", "image/jpg", "image/jpeg", "image/webp":
			savedName := "upload-post-" + md5Hex(file.Name+randomToken()) + mime.ToExtension(mimeType)

			if err := a.options.Files.PutFile(resolved, savedName, file.Content); nil != err {
				uploadError = uploadErrOnSaving
			} else {
				name, hash = a.storeBackground(resolved, file.Name, mimeType, savedName)
			}

			a.options.Files.Clear(resolved, savedName)

		default:
			uploadError = uploadErrFileType
		}
	}

	if uploadErrOK != uploadError {
		clientCode, message := uploadUserMessage(uploadError)
		if "" != message {
			return falseResponse(clientCode, message, "")
		}
	}

	if "" != name && "" != hash {
		return defaultResponse(map[string]any{"name": name, "hash": hash}, map[string]any{})
	}

	return defaultResponse(false, map[string]any{})
}

// storeBackground reads the saved image, keeps it in the CONFIG "background"
// blob and records its name/hash in the settings.
func (a *AppData) storeBackground(resolved *account.Account, fileName, mimeType, savedName string) (string, string) {
	handle, err := a.options.Files.Open(resolved, savedName)
	if nil != err {
		return "", ""
	}
	defer func() { _ = handle.Close() }()

	data, _ := io.ReadAll(handle)
	if 0 == len(data) {
		return "", ""
	}

	name := fileName
	if "" == name {
		name = "_"
	}

	blob, err := encodeJSON(map[string]any{
		"ContentType": mimeType,
		"Raw":         base64.StdEncoding.EncodeToString(data),
	})
	if nil != err {
		return "", ""
	}

	if nil == a.options.Storage {
		return "", ""
	}
	if err := a.options.Storage.Put(resolved, storage.TypeConfig, "background", string(blob)); nil != err {
		return "", ""
	}

	saver, ok := a.options.Settings.(settingsSaver)
	if !ok {
		return "", ""
	}

	settings := a.options.Settings.Load(resolved)
	if nil == settings {
		return "", ""
	}

	hash := sha1Rand(name + a.options.Version + a.options.Salt)
	settings.SetConf("UserBackgroundName", name)
	settings.SetConf("UserBackgroundHash", hash)
	if err := saver.Save(resolved, settings); nil != err {
		return "", ""
	}

	return name, hash
}

// detectUploadMime is MimeType::fromFile() ?: MimeType::fromFilename() for an
// in-memory upload, rewinding the content for the caller.
func detectUploadMime(file *uploadFile) string {
	header := make([]byte, 4096)
	read, _ := io.ReadFull(file.Content, header)

	if seeker, ok := file.Content.(io.Seeker); ok {
		_, _ = seeker.Seek(0, io.SeekStart)
	}

	if detected := mime.FromContent(header[:read], file.Name); "" != detected {
		return detected
	}

	return mime.FromFilename(file.Name)
}

// randomToken is random_bytes(16) as hex: the stand-in for PHP's $_FILES
// tmp_name when the upload key is built.
func randomToken() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); nil != err {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buffer)
}

// sha1Rand is MailSo\Base\Utils::Sha1Rand(): sha1(additionalSalt . random_bytes).
func sha1Rand(additionalSalt string) string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); nil != err {
		return storage.SHA1(additionalSalt + strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	return storage.SHA1(additionalSalt + string(buffer))
}
