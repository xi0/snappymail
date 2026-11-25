package service

// ServiceActions::ServiceRaw() and the Actions\Raw trait:
// ?/Raw/&q[]=/0/<Action>/<RawKey>/
//
// The RawKey is an unsigned, client controlled url-safe base64 JSON blob. The
// embedded account hash is validated against the current account before any
// folder/uid/file identifier is trusted (Actions::decodeRawKey()).
//
// The message-part branches need MailSo\Mail\MailClient->MessageMimeStream()
// (step 14), so they run behind the MessageStreamer interface; the account
// background and the uploaded-file branches are fully implemented here.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/storage"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// MessageStreamer is the MailSo\Mail\MailClient->MessageMimeStream() surface the
// Raw message endpoints need. Step 14 wires the real client; a nil value makes
// the message branches report "not found", exactly like an unreachable mailbox.
type MessageStreamer interface {
	// MessageMimeStream calls handler with the stream of one MIME part and
	// reports whether the part was found.
	MessageMimeStream(
		handler func(reader io.ReadSeeker, contentType, fileName, mimeIndex string),
		folder string, uid int, mimeIndex string,
	) bool
}

var (
	// rawRangePattern is the '/^bytes=([0-9]+)-([0-9]*)/i' matcher of rawSmart().
	rawRangePattern = regexp.MustCompile(`(?i)^bytes=([0-9]+)-([0-9]*)`)
	// rawPartIDPattern is FetchType::isValidPartId().
	rawPartIDPattern = regexp.MustCompile(`^(?:\d+\.)*\d+$`)
	// rawNonAlnumPattern / rawDotPattern are the clearFileName() rewrites.
	rawNonAlnumPattern = regexp.MustCompile(`[^a-zA-Z0-9]`)
	rawDotPattern      = regexp.MustCompile(`\.+`)
	// rawSpacePattern is the '/[\s]+/u' collapse of StripSpaces().
	rawSpacePattern = regexp.MustCompile(`\s+`)
)

// Raw is ServiceActions::ServiceRaw(): dispatch ?/Raw/<Action>/<RawKey>.
func (a *AppData) Raw(w http.ResponseWriter, r *http.Request, route *web.Route) bool {
	rawKey := ""
	if 3 < len(route.Paths) {
		rawKey = route.Paths[3]
	}

	action := ""
	if 2 < len(route.Paths) {
		action = route.Paths[2]
	}

	rawError := ""

	switch action {
	case "View", "Download", "ViewThumbnail", "ViewAsPlain", "UserBackground", "ContactsVcf", "ContactsCsv":
		w.Header().Set("X-Raw-Action", "Raw"+action)
		w.Header().Set("Content-Security-Policy", "script-src 'none'; child-src 'none'")

		ok := false
		switch action {
		case "View":
			ok = a.rawSmart(w, r, rawKey, false, false)
		case "Download":
			ok = a.rawSmart(w, r, rawKey, true, false)
		case "ViewThumbnail":
			ok = a.rawSmart(w, r, rawKey, false, true)
		case "ViewAsPlain":
			ok = a.rawViewAsPlain(w, r, rawKey)
		case "UserBackground":
			ok = a.rawUserBackground(w, r, rawKey)
		case "ContactsVcf":
			ok = a.rawContacts(w, r, "vcf")
		case "ContactsCsv":
			ok = a.rawContacts(w, r, "csv")
		}

		if !ok {
			rawError = "False result"
		}

	case "":
		rawError = "Empty action"
	default:
		rawError = `Unknown action "` + action + `"`
	}

	if "" != rawError {
		a.rawLog(r, rawError)
	}

	return true
}

// rawLog writes a raw failure line, like Actions::logWrite() of ServiceRaw().
func (a *AppData) rawLog(_ *http.Request, message string) {
	// The per-request logger is not part of the AppData wiring yet; the message
	// is kept for step 33 when raw logging is unified with the JSON logger.
	_ = message
}

// rawSmart is Actions\Raw::rawSmart(): a message, a message attachment or a
// stored file, with Range support.
func (a *AppData) rawSmart(w http.ResponseWriter, r *http.Request, rawKey string, download, thumbnail bool) bool {
	resolved, _ := accounts(a.session(w, r))
	values := a.decodeRawKey(rawKey, resolved)
	if nil == values {
		return false
	}

	rangeStart, rangeEnd := "", ""
	isRange := false
	if header := strings.TrimSpace(httpx.GetHeader(r, "Range")); "" != header &&
		"bytes=0-" != strings.ToLower(header) {
		if match := rawRangePattern.FindStringSubmatch(header); nil != match {
			rangeStart = match[1]
			rangeEnd = match[2]
			isRange = true
		}
	}

	if !isRange {
		a.verifyCacheByKey(w, rawKey)
	}

	mimeIndex, err := a.validatePartID(phpString(values["mimeIndex"]))
	if nil != err {
		return false
	}

	contentTypeIn := phpString(values["mimeType"])
	fileNameIn := phpString(values["fileName"])

	if fileHash := phpString(values["fileHash"]); "" != fileHash {
		return a.rawFromFile(w, resolved, fileHash, contentTypeIn, fileNameIn, mimeIndex)
	}

	folder := phpString(values["folder"])
	uid := phpInt(values["uid"])
	if "" == folder || 1 > uid {
		return false
	}

	if nil == resolved || nil == a.options.MessageStreamer {
		return false
	}

	return a.options.MessageStreamer.MessageMimeStream(
		func(reader io.ReadSeeker, contentType, fileName, streamIndex string) {
			a.rawStream(w, reader, contentType, fileName, streamIndex,
				contentTypeIn, fileNameIn, download, thumbnail, isRange, rangeStart, rangeEnd)
		}, folder, uid, mimeIndex)
}

// rawFromFile is the "$aValues['fileHash']" branch of rawSmart(): an uploaded
// attachment served from the files provider.
func (a *AppData) rawFromFile(w http.ResponseWriter, resolved *account.Account, fileHash, contentTypeIn, fileNameIn, mimeIndex string) bool {
	if nil == resolved || nil == a.options.Files {
		return false
	}

	handle, err := a.options.Files.Open(resolved, fileHash)
	if nil != err {
		return false
	}
	defer func() { _ = handle.Close() }()

	contentTypeOut := accessContentType(fileNameIn, contentTypeIn)
	fileNameOut := clearFileName(fileNameIn, contentTypeIn, mimeIndex, 250)

	w.Header().Set("Content-Type", contentTypeOut)
	httpx.SetContentDisposition(w, httpx.Disposition{Type: "attachment", Filename: fileNameOut})
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Transfer-Encoding", "binary")

	_, _ = io.Copy(w, handle)

	return true
}

// rawViewAsPlain is Actions\Raw::RawViewAsPlain(): the whole message as text.
func (a *AppData) rawViewAsPlain(w http.ResponseWriter, r *http.Request, rawKey string) bool {
	resolved, _ := accounts(a.session(w, r))
	values := a.decodeRawKey(rawKey, resolved)
	if nil == values {
		return false
	}

	folder := phpString(values["folder"])
	uid := phpInt(values["uid"])
	if "" == folder || 1 > uid {
		return false
	}

	mimeIndex, err := a.validatePartID(phpString(values["mimeIndex"]))
	if nil != err {
		return false
	}

	if nil == resolved || nil == a.options.MessageStreamer {
		return false
	}

	a.verifyCacheByKey(w, rawKey)
	w.Header().Set("Content-Type", "text/plain")

	return a.options.MessageStreamer.MessageMimeStream(
		func(reader io.ReadSeeker, _, _, _ string) {
			if nil != reader {
				_, _ = io.Copy(w, reader)
			}
		}, folder, uid, mimeIndex)
}

// contactExporter is the Actions\Contacts raw export surface (?/Raw/ContactsVcf
// and ?/Raw/ContactsCsv). *contacts.Provider satisfies it.
type contactExporter interface {
	Export(account *account.Account, kind string) ([]byte, bool)
}

// rawContacts is Actions\Contacts::RawContactsVcf()/RawContactsCsv(): the
// serialized address book as a download.
func (a *AppData) rawContacts(w http.ResponseWriter, r *http.Request, kind string) bool {
	resolved, _ := accounts(a.session(w, r))
	if nil == resolved || nil == a.options.AddressBook || !a.options.AddressBook.IsActive(resolved) {
		return false
	}

	exporter, ok := a.options.AddressBook.(contactExporter)
	if !ok {
		return false
	}

	data, ok := exporter.Export(resolved, kind)
	if !ok {
		return false
	}

	if "csv" == kind {
		w.Header().Set("Content-Type", "text/csv; charset=UTF-8")
		httpx.SetContentDisposition(w, httpx.Disposition{Type: "attachment", Filename: "contacts.csv"})
	} else {
		w.Header().Set("Content-Type", "text/x-vcard; charset=UTF-8")
		httpx.SetContentDisposition(w, httpx.Disposition{Type: "attachment", Filename: "contacts.vcf"})
	}
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Content-Transfer-Encoding", "binary")
	httpx.NoStore(w)

	_, _ = w.Write(data)
	return true
}

// rawUserBackground is Actions\Raw::RawUserBackground(): the stored account
// background image.
func (a *AppData) rawUserBackground(w http.ResponseWriter, r *http.Request, rawKey string) bool {
	resolved, _ := accounts(a.session(w, r))
	a.verifyCacheByKey(w, rawKey)

	if nil == resolved || nil == a.options.Storage {
		return false
	}

	data, ok := a.options.Storage.Get(resolved, storage.TypeConfig, "background")
	if !ok || "" == data {
		return false
	}

	var blob struct {
		ContentType string `json:"ContentType"`
		Raw         string `json:"Raw"`
	}
	if err := json.Unmarshal([]byte(data), &blob); nil != err {
		return false
	}
	if "" == blob.ContentType || "" == blob.Raw {
		return false
	}

	decoded, err := base64.StdEncoding.DecodeString(blob.Raw)
	if nil != err {
		return false
	}

	w.Header().Set("Content-Type", blob.ContentType)
	_, _ = w.Write(decoded)

	return true
}

// rawStream is the callback body of rawSmart() for a message MIME part.
func (a *AppData) rawStream(
	w http.ResponseWriter,
	reader io.ReadSeeker,
	contentType, fileName, streamIndex,
	contentTypeIn, fileNameIn string,
	download, thumbnail, isRange bool,
	rangeStart, rangeEnd string,
) {
	if nil == reader {
		return
	}

	if "" != fileNameIn {
		fileName = fileNameIn
	}
	fileName = clearFileName(fileName, contentType, streamIndex, 250)

	if strings.HasSuffix(fileName, ".pdf") {
		contentType = "application/pdf"
	} else if "" != contentTypeIn {
		contentType = contentTypeIn
	} else if "" == contentType {
		if detected := mime.FromFilename(fileName); "" != detected {
			contentType = detected
		} else {
			contentType = "application/octet-stream"
		}
	}

	if !download && thumbnail {
		if data, ok := thumbnailPNG(reader, 48); ok {
			w.Header().Set("Content-Type", "image/png")
			httpx.SetContentDisposition(w, httpx.Disposition{
				Type:     "inline",
				Filename: fileName + "_thumb60x60.png",
			})
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		// Fall back to the inline original when the image cannot be decoded.
		if _, err := reader.Seek(0, io.SeekStart); nil != err {
			return
		}
	}

	// The EXIF auto-rotate branch of rawSmart() is not ported (labs.image_exif_auto_rotate);
	// the original orientation is served.

	w.Header().Set("Content-Type", contentType)
	disposition := "inline"
	if download {
		disposition = "attachment"
	}
	httpx.SetContentDisposition(w, httpx.Disposition{Type: disposition, Filename: fileName})
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Transfer-Encoding", "binary")

	if isRange || !download {
		data, _ := io.ReadAll(reader)
		if 0 == len(data) {
			_, _ = io.Copy(w, reader)
			return
		}

		if isRange && ("" != rangeStart || "" != rangeEnd) {
			full := len(data)
			start := phpMax(0, phpInt(rangeStart))
			end := phpMax(0, phpInt(rangeEnd))

			var sliced []byte
			if 0 != end && start < end {
				if start < full {
					sliced = data[start:phpMin(end, full)]
				}
			} else if 0 != start && start < full {
				sliced = data[start:]
			}

			if 0 < len(sliced) {
				contentRangeEnd := end
				if 0 == contentRangeEnd {
					contentRangeEnd = full - 1
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(sliced)))
				w.Header().Set("Content-Range",
					"bytes "+rangeStart+"-"+strconv.Itoa(contentRangeEnd)+"/"+strconv.Itoa(full))
			}

			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(sliced)
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	_, _ = io.Copy(w, reader)
}

// accessContentType is the "$sFileNameIn ends in .pdf ? application/pdf : …"
// resolution of the file-hash branch of rawSmart().
func accessContentType(fileNameIn, contentTypeIn string) string {
	if strings.HasSuffix(fileNameIn, ".pdf") {
		return "application/pdf"
	}
	if "" != contentTypeIn {
		return contentTypeIn
	}
	if detected := mime.FromFilename(fileNameIn); "" != detected {
		return detected
	}
	return "application/octet-stream"
}

// clearFileName is Actions\Raw::clearFileName(): the sanitised download name.
func clearFileName(fileName, contentType, mimeIndex string, maxLength int) string {
	if "" == fileName {
		placeholder := contentType
		if "" != mimeIndex {
			placeholder = mimeIndex + "." + contentType
		}
		fileName = rawNonAlnumPattern.ReplaceAllString(placeholder, ".")
	}

	cleared := stripSpaces(rawDotPattern.ReplaceAllString(fileName, "."))
	extension := getFileExtension(cleared)

	if 10 < maxLength && maxLength < len(cleared)-len(extension) {
		suffix := ""
		if "" != extension {
			suffix = "." + extension
		}
		if maxLength < len(cleared) {
			cleared = cleared[:maxLength] + suffix
		} else {
			cleared = cleared + suffix
		}
	}

	return storage.SecureFileName(utf8Clear(cleared))
}

// stripSpaces is MailSo\Base\Utils::StripSpaces().
func stripSpaces(value string) string {
	value = rawSpacePattern.ReplaceAllString(value, " ")
	value = strings.TrimSpace(value)
	return strings.TrimFunc(value, func(r rune) bool { return r <= 0x1F })
}

// getFileExtension is MailSo\Base\Utils::GetFileExtension().
func getFileExtension(fileName string) string {
	index := strings.LastIndex(fileName, ".")
	if 0 > index {
		return ""
	}
	return strings.ToLower(fileName[index+1:])
}

// utf8Clear is MailSo\Base\Utils::Utf8Clear(): drop invalid UTF-8 bytes.
func utf8Clear(value string) string {
	return strings.ToValidUTF8(value, "")
}

// validatePartID is Actions::validatePartId(): only an empty id, TEXT or dot
// separated integers reach an IMAP FETCH item.
func (a *AppData) validatePartID(partID string) (string, error) {
	partID = strings.TrimSpace(partID)
	if !isValidPartID(partID, true) {
		return "", appctx.NewClientError(appctx.InvalidInputArgument, nil, "Invalid MIME part id")
	}
	return partID, nil
}

// isValidPartID is FetchType::isValidPartId().
func isValidPartID(partID string, allowEmpty bool) bool {
	if "" == partID {
		return allowEmpty
	}
	return "TEXT" == strings.ToUpper(partID) || rawPartIDPattern.MatchString(partID)
}

// encodeRawKey is Actions::encodeRawKey(): the url-safe base64 JSON blob, with
// the current account hash folded in.
func (a *AppData) encodeRawKey(resolved *account.Account, values map[string]any) string {
	document := make(map[string]any, len(values)+1)
	for key, value := range values {
		document[key] = value
	}
	if nil != resolved {
		document["accountHash"] = resolved.Hash(a.options.Salt)
	}

	encoded, err := encodeJSON(document)
	if nil != err {
		return ""
	}
	return cryptox.URLSafeBase64Encode(string(encoded))
}

// decodeRawKey is Actions::decodeRawKey(): the decoded blob, or nil when it is
// invalid or (with a current account) carries another account hash.
func (a *AppData) decodeRawKey(rawKey string, resolved *account.Account) map[string]any {
	if "" == rawKey {
		return nil
	}

	decoded, err := cryptox.URLSafeBase64Decode(rawKey)
	if nil != err {
		return nil
	}

	var parsed any
	if err := json.Unmarshal(decoded, &parsed); nil != err {
		return nil
	}

	var values map[string]any
	var indexed []any
	switch typed := parsed.(type) {
	case map[string]any:
		values = typed
	case []any:
		indexed = typed
	default:
		return nil
	}

	if nil != resolved {
		hash := ""
		if nil != values {
			if stored, ok := values["accountHash"].(string); ok {
				hash = stored
			}
		}
		// Some clients send an indexed array with the account hash at index 3.
		if "" == hash && 3 < len(indexed) {
			if stored, ok := indexed[3].(string); ok {
				hash = stored
			}
		}
		if "" == hash || hash != resolved.Hash(a.options.Salt) {
			return nil
		}
	}

	if nil == values {
		values = map[string]any{}
	}

	return values
}

// verifyCacheByKey is Actions::verifyCacheByKey(). The ETag/304 handling is part
// of step 33, so it is a no-op here.
func (a *AppData) verifyCacheByKey(_ http.ResponseWriter, _ string) {}

// cacheByKey is Actions::cacheByKey(). Like verifyCacheByKey(), the ETag/Expires
// handling is part of step 33, so it stays a no-op stub here: the callers set
// their response cache headers themselves (ServiceProxyExternal) or rely on the
// pipeline defaults (Lang/Css/Plugins).
func (a *AppData) cacheByKey(_ string) {}

// thumbnailPNG is Actions\Raw::loadImage() + Image::cropThumbnailImage(): a
// square PNG thumbnail of box pixels, or ok=false when the image cannot be
// decoded.
func thumbnailPNG(reader io.Reader, box int) ([]byte, bool) {
	source, _, err := image.Decode(reader)
	if nil != err {
		return nil, false
	}

	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if 0 >= width || 0 >= height || 0 >= box {
		return nil, false
	}

	// Cover the box, then crop the centre: cropThumbnailImage() semantics.
	scale := math.Max(float64(box)/float64(width), float64(box)/float64(height))
	cropWidth := math.Min(float64(width), float64(box)/scale)
	cropHeight := math.Min(float64(height), float64(box)/scale)
	offsetX := float64(bounds.Min.X) + (float64(width)-cropWidth)/2
	offsetY := float64(bounds.Min.Y) + (float64(height)-cropHeight)/2

	crop := image.Rect(
		int(math.Round(offsetX)),
		int(math.Round(offsetY)),
		int(math.Round(offsetX+cropWidth)),
		int(math.Round(offsetY+cropHeight)),
	)

	target := image.NewRGBA(image.Rect(0, 0, box, box))
	draw.CatmullRom.Scale(target, target.Bounds(), source, crop, draw.Over, nil)

	var buffer bytes.Buffer
	if err := png.Encode(&buffer, target); nil != err {
		return nil, false
	}

	return buffer.Bytes(), true
}
