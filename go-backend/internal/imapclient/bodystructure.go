package imapclient

// MailSo\Imap\BodyStructure: the parsed BODYSTRUCTURE of one message. It is only
// needed to derive the text/attachment/pgp/smime layout the message list and
// (Step 17) the message view expose, so the port keeps the field set and the
// predicates of the PHP class and drops nothing that the JSON shape depends on.

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/mime"
)

// BodyStructure is MailSo\Imap\BodyStructure.
type BodyStructure struct {
	contentTypeParams       map[string]string
	charset                 string
	contentID               string
	contentTransferEncoding string
	contentType             string
	description             string
	disposition             string
	fileName                string
	language                string
	location                string
	partID                  string
	size                    int
	subParts                []*BodyStructure
}

// Charset is BodyStructure::Charset().
func (b *BodyStructure) Charset() string { return b.charset }

// ContentTransferEncoding is BodyStructure::ContentTransferEncoding().
func (b *BodyStructure) ContentTransferEncoding() string { return b.contentTransferEncoding }

// ContentType is BodyStructure::ContentType().
func (b *BodyStructure) ContentType() string { return b.contentType }

// PartID is BodyStructure::PartID().
func (b *BodyStructure) PartID() string { return b.partID }

// SubParts is BodyStructure::SubParts().
func (b *BodyStructure) SubParts() []*BodyStructure { return b.subParts }

// FileName is BodyStructure::FileName().
func (b *BodyStructure) FileName(calculateOnEmpty bool) string {
	name := strings.TrimSpace(b.fileName)
	if "" != name || !calculateOnEmpty {
		return name
	}

	index := "-" + b.partID
	mimeType := b.contentType

	switch mimeType {
	case "message/rfc822":
		return "message" + index + ".eml"
	case "text/calendar":
		return "calendar" + index + ".ics"
	case "text/plain":
		return "part" + index + ".txt"
	}

	if match := mustRegexp(`text/(vcard|html|csv|xml|css|asp)`).FindStringSubmatch(mimeType); 2 <= len(match) {
		return "part" + index + "." + match[1]
	}
	if match := mustRegexp(`image/(png|jpeg|gif|bmp|cgm|ief|tiff|webp)`).FindStringSubmatch(mimeType); 2 <= len(match) {
		return "part" + index + "." + match[1]
	}
	if "" != mimeType {
		return strings.Replace(mimeType, "/", index+".", 1)
	}
	if b.isInline() {
		return "inline" + index
	}
	return "part" + index
}

// EstimatedSize is BodyStructure::EstimatedSize().
func (b *BodyStructure) EstimatedSize() int {
	coefficient := 1.0
	switch b.contentTransferEncoding {
	case "base64":
		coefficient = 0.75
	case "quoted-printable":
		coefficient = 0.44
	}
	return int(float64(b.size) * coefficient)
}

// isInline is BodyStructure::isInline().
func (b *BodyStructure) isInline() bool {
	return "inline" == b.disposition || "" != b.contentID
}

// isText is BodyStructure::isText().
func (b *BodyStructure) isText() bool {
	return "text/html" == b.contentType || "text/plain" == b.contentType || "text/x-amp-html" == b.contentType
}

// isPgpEncrypted is BodyStructure::isPgpEncrypted().
func (b *BodyStructure) isPgpEncrypted() bool {
	return "multipart/encrypted" == b.contentType &&
		"application/pgp-encrypted" == strings.ToLower(strings.TrimSpace(b.contentTypeParams["protocol"])) &&
		2 == len(b.subParts) &&
		"application/pgp-encrypted" == b.subParts[0].contentType &&
		"application/octet-stream" == b.subParts[1].contentType
}

// isPgpSigned is BodyStructure::isPgpSigned().
func (b *BodyStructure) isPgpSigned() bool {
	return "multipart/signed" == b.contentType &&
		"application/pgp-signature" == strings.ToLower(strings.TrimSpace(b.contentTypeParams["protocol"])) &&
		2 == len(b.subParts) &&
		"application/pgp-signature" == b.subParts[1].contentType
}

// isSMimeEncrypted is BodyStructure::isSMimeEncrypted().
func (b *BodyStructure) isSMimeEncrypted() bool {
	smimeType := strings.ToLower(strings.TrimSpace(b.contentTypeParams["smime-type"]))
	return isPkcs7Mime(b.contentType) && "" != b.contentTypeParams["smime-type"] &&
		("enveloped-data" == smimeType || "authenveloped-data" == smimeType)
}

// isSMimeSigned is BodyStructure::isSMimeSigned().
func (b *BodyStructure) isSMimeSigned() bool {
	smimeType := strings.ToLower(strings.TrimSpace(b.contentTypeParams["smime-type"]))
	return ("multipart/signed" == b.contentType &&
		isPkcs7Signature(strings.ToLower(strings.TrimSpace(b.contentTypeParams["protocol"]))) &&
		2 == len(b.subParts) &&
		isPkcs7Signature(b.subParts[1].contentType)) ||
		(isPkcs7Mime(b.contentType) && "" != b.contentTypeParams["smime-type"] && "signed-data" == smimeType)
}

// isAttachment is BodyStructure::IsAttachment().
func (b *BodyStructure) isAttachment(parent *BodyStructure) bool {
	return (nil == parent || !parent.isPgpEncrypted()) &&
		("attachment" == b.disposition || (!strings.HasPrefix(b.contentType, "multipart/") && !b.isText()))
}

// IsPgpEncrypted exposes BodyStructure::isPgpEncrypted() for other packages.
func (b *BodyStructure) IsPgpEncrypted() bool { return b.isPgpEncrypted() }

// IsPgpSigned exposes BodyStructure::isPgpSigned() for other packages.
func (b *BodyStructure) IsPgpSigned() bool { return b.isPgpSigned() }

// IsSMimeEncrypted exposes BodyStructure::isSMimeEncrypted() for other packages.
func (b *BodyStructure) IsSMimeEncrypted() bool { return b.isSMimeEncrypted() }

// IsSMimeSigned exposes BodyStructure::isSMimeSigned() for other packages.
func (b *BodyStructure) IsSMimeSigned() bool { return b.isSMimeSigned() }

// IsFlowedFormat is BodyStructure::IsFlowedFormat().
func (b *BodyStructure) IsFlowedFormat() bool {
	return "" != b.contentTypeParams["format"] &&
		"flowed" == strings.ToLower(strings.TrimSpace(b.contentTypeParams["format"])) &&
		!("base64" == b.contentTransferEncoding) &&
		!("quoted-printable" == b.contentTransferEncoding)
}

// walk visits the structure and its children with the parent of each part.
func (b *BodyStructure) walk(fn func(*BodyStructure, *BodyStructure) bool, parent *BodyStructure, collect *[]*BodyStructure) {
	if fn(b, parent) {
		*collect = append(*collect, b)
	}
	for _, subPart := range b.subParts {
		subPart.walk(fn, b, collect)
	}
}

// searchByCallback is BodyStructure::SearchByCallback().
func (b *BodyStructure) searchByCallback(fn func(*BodyStructure, *BodyStructure) bool) []*BodyStructure {
	var result []*BodyStructure
	b.walk(fn, nil, &result)
	return result
}

// GetHtmlAndPlainParts is BodyStructure::GetHtmlAndPlainParts().
func (b *BodyStructure) GetHtmlAndPlainParts() []*BodyStructure {
	parts := b.searchByCallback(func(item, parent *BodyStructure) bool {
		return item.isText() && !item.isAttachment(parent)
	})
	if 0 < len(parts) {
		return parts
	}

	for _, part := range b.searchByCallback(func(item, _ *BodyStructure) bool {
		return "multipart/encrypted" == item.contentType
	}) {
		if part.isPgpEncrypted() && 2 == len(part.subParts) {
			return []*BodyStructure{part.subParts[1]}
		}
	}

	return b.searchByCallback(func(item, _ *BodyStructure) bool {
		return item.isText()
	})
}

// SearchCharset is BodyStructure::SearchCharset().
func (b *BodyStructure) SearchCharset() string {
	parts := b.searchByCallback(func(item, parent *BodyStructure) bool {
		return "" != item.charset && item.isText() && !item.isAttachment(parent)
	})
	if 0 == len(parts) {
		parts = b.searchByCallback(func(item, parent *BodyStructure) bool {
			return "" != item.charset && item.isAttachment(parent)
		})
	}
	if 0 < len(parts) {
		return parts[0].charset
	}
	return ""
}

// SearchAttachmentsParts is BodyStructure::SearchAttachmentsParts().
func (b *BodyStructure) SearchAttachmentsParts() []*BodyStructure {
	return b.searchByCallback(func(item, parent *BodyStructure) bool {
		return item.isAttachment(parent) && (nil == parent || !parent.isPgpEncrypted())
	})
}

// SearchByContentType is BodyStructure::SearchByContentType().
func (b *BodyStructure) SearchByContentType(contentType string) []*BodyStructure {
	contentType = strings.ToLower(contentType)
	return b.searchByCallback(func(item, _ *BodyStructure) bool {
		return contentType == item.contentType
	})
}

// SearchByContentTypes is BodyStructure::SearchByContentTypes().
func (b *BodyStructure) SearchByContentTypes(contentTypes []string) []*BodyStructure {
	return b.searchByCallback(func(item, _ *BodyStructure) bool {
		for _, candidate := range contentTypes {
			if candidate == item.contentType {
				return true
			}
		}
		return false
	})
}

// GetPartByMimeIndex is BodyStructure::GetPartByMimeIndex().
func (b *BodyStructure) GetPartByMimeIndex(mimeIndex string) *BodyStructure {
	if "" == mimeIndex {
		return nil
	}
	if mimeIndex == b.partID {
		return b
	}
	for _, subPart := range b.subParts {
		if part := subPart.GetPartByMimeIndex(mimeIndex); nil != part {
			return part
		}
	}
	return nil
}

// JSONSerialize is BodyStructure::jsonSerialize().
func (b *BodyStructure) JSONSerialize() any {
	return map[string]any{
		"mimeIndex":       b.partID,
		"mimeType":        b.contentType,
		"fileName":        mime.SecureFileName(b.FileName(true)),
		"estimatedSize":   b.EstimatedSize(),
		"cId":             b.contentID,
		"contentLocation": b.location,
		"isInline":        b.isInline(),
	}
}

// NewBodyStructure is BodyStructure::NewInstance(): build the tree from the
// decoded BODYSTRUCTURE value (nested lists of strings / nil).
func NewBodyStructure(value any, partID string) *BodyStructure {
	a, ok := value.([]any)
	if !ok || 2 > len(a) {
		return nil
	}

	contentTypeMain := ""
	contentTypeSub := ""
	var subParts []*BodyStructure
	contentTypeParams := map[string]string{}
	fileName := ""
	charset := ""
	contentID := ""
	description := ""
	contentTransferEncoding := ""
	size := 0
	extra := 0

	if isTokenList(a[0]) {
		contentTypeMain = "multipart"
		contentTypeSub = "mixed"
		subPartIDPrefix := ""
		if "" == partID || '.' == partID[len(partID)-1] {
			subPartIDPrefix = partID
			partID += "TEXT"
		} else if "" != partID {
			subPartIDPrefix = partID + "."
		}

		index := 1
		for extra < len(a) && isTokenList(a[extra]) {
			part := NewBodyStructure(a[extra], subPartIDPrefix+strconv.Itoa(index))
			if nil == part {
				return nil
			}
			if "" == charset {
				charset = part.charset
			}
			subParts = append(subParts, part)
			extra++
			index++
		}

		if extra < len(a) {
			sub, ok := a[extra].(string)
			if !ok {
				return nil
			}
			contentTypeSub = strings.ToLower(sub)
			extra++
			if extra < len(a) && isTokenList(a[extra]) {
				contentTypeParams = keyValueList(a[extra].([]any))
			}
		}
	} else if main, ok := a[0].(string); ok {
		if 7 > len(a) {
			return nil
		}
		sub, ok := a[1].(string)
		if !ok {
			return nil
		}
		contentTypeMain = strings.ToLower(main)
		contentTypeSub = strings.ToLower(sub)

		if isTokenList(a[2]) {
			contentTypeParams = keyValueList(a[2].([]any))
			if value, ok := contentTypeParams["charset"]; ok {
				charset = value
			}
			fileName = decodeAttrParameter(contentTypeParams, "name", charset)
			if "" != fileName {
				contentTypeParams["name"] = fileName
			}
		}

		if nil != a[3] {
			value, ok := a[3].(string)
			if !ok {
				return nil
			}
			contentID = value
		}
		if nil != a[4] {
			value, ok := a[4].(string)
			if !ok {
				return nil
			}
			description = value
		}
		if nil != a[5] {
			value, ok := a[5].(string)
			if !ok {
				return nil
			}
			contentTransferEncoding = value
		}
		if number, ok := intOf(a[6]); ok {
			size = number
		}

		if "" == partID || '.' == partID[len(partID)-1] {
			partID += "1"
		}

		extra = 7
		if "text" == contentTypeMain {
			extra++
		} else if "message" == contentTypeMain && "rfc822" == contentTypeSub {
			extra += 3
		}
	} else {
		return nil
	}

	// Skip body MD5 because most mail servers leave it NIL anyway.
	extra++

	disposition := ""
	if extra < len(a) {
		if dispList, ok := a[extra].([]any); ok && 1 < len(dispList) {
			value, ok := dispList[0].(string)
			if !ok {
				return nil
			}
			disposition = value
			if isTokenList(dispList[1]) {
				dispParams := keyValueList(dispList[1].([]any))
				fileName = decodeAttrParameter(dispParams, "filename", charset)
			}
		}
		extra++
	}

	structure := &BodyStructure{
		contentType:             strings.ToLower(strings.TrimSpace(contentTypeMain + "/" + contentTypeSub)),
		contentTypeParams:       contentTypeParams,
		charset:                 charset,
		contentID:               strings.TrimSpace(contentID),
		description:             description,
		contentTransferEncoding: strings.ToLower(contentTransferEncoding),
		disposition:             strings.ToLower(disposition),
		fileName:                mime.Utf8Clear(fileName),
		size:                    size,
		partID:                  partID,
		subParts:                subParts,
	}

	if extra < len(a) {
		if isTokenList(a[extra]) {
			structure.language = strings.Join(stringList(a[extra].([]any)), ",")
		} else if value, ok := a[extra].(string); ok {
			structure.language = value
		}
		extra++
		if extra < len(a) {
			if value, ok := a[extra].(string); ok {
				structure.location = value
			}
		}
	}

	return structure
}

// isTokenList reports whether value is a nested list (a Go []any).
func isTokenList(value any) bool {
	_, ok := value.([]any)
	return ok
}

// keyValueList is BodyStructure::getKeyValueListFromArrayList().
func keyValueList(list []any) map[string]string {
	result := map[string]string{}
	if 0 != len(list)%2 {
		return result
	}
	for index := 0; index < len(list); index += 2 {
		name, okName := list[index].(string)
		value, okValue := list[index+1].(string)
		if okName && okValue {
			result[strings.ToLower(name)] = value
		}
	}
	return result
}

// decodeAttrParameter is BodyStructure::decodeAttrParameter().
func decodeAttrParameter(params map[string]string, name, charset string) string {
	if value, ok := params[name]; ok {
		return mime.DecodeHeaderValue(value, charset)
	}
	if value, ok := params[name+"*"]; ok {
		if parts := strings.SplitN(value, "''", 2); 2 == len(parts) {
			fromCharset := parts[0]
			if "" == fromCharset {
				fromCharset = "UTF-8"
			}
			return decodeAttributeValue(parts[1], fromCharset)
		}
		return urlQueryUnescape(value)
	}

	suffixPattern := mustRegexp(`^` + regexpQuote(strings.ToLower(name)) + `\*([0-9]+)\*$`)
	values := map[int]string{}
	fromCharset := ""
	charsetIndex := -1
	for key, value := range params {
		match := suffixPattern.FindStringSubmatch(key)
		if 2 > len(match) {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if nil != err {
			continue
		}
		if charsetIndex < index && strings.Contains(value, "''") {
			parts := strings.SplitN(value, "''", 2)
			if 2 == len(parts) && "" != parts[0] {
				charsetIndex = index
				fromCharset = parts[0]
				value = parts[1]
			}
		}
		values[index] = value
	}
	if 0 == len(values) {
		return ""
	}

	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(values[key])
	}
	result := urlQueryUnescape(builder.String())
	if "" != fromCharset {
		return decodeAttributeValue(result, fromCharset)
	}
	return result
}

// decodeAttributeValue decodes a percent-encoded RFC 2231 value from charset.
func decodeAttributeValue(value, charset string) string {
	decoded := urlQueryUnescape(value)
	if converted := mime.DecodeHeaderValue(decoded, charset); converted != "" {
		return converted
	}
	return decoded
}

// urlQueryUnescape is PHP's urldecode().
func urlQueryUnescape(value string) string {
	decoded, err := url.QueryUnescape(value)
	if nil != err {
		return value
	}
	return decoded
}

// intOf converts a decoded atom/string token to an int.
func intOf(value any) (int, bool) {
	switch typed := value.(type) {
	case string:
		number, err := strconv.Atoi(strings.TrimSpace(typed))
		return number, nil == err
	case int:
		return typed, true
	case float64:
		return int(typed), true
	}
	return 0, false
}

// stringList collects the string values of a list.
func stringList(list []any) []string {
	result := make([]string, 0, len(list))
	for _, value := range list {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

// isPkcs7Mime is ContentType::isPkcs7Mime().
func isPkcs7Mime(value string) bool {
	return "application/pkcs7-mime" == value || "application/x-pkcs7-mime" == value
}

// isPkcs7Signature is ContentType::isPkcs7Signature().
func isPkcs7Signature(value string) bool {
	return "application/pkcs7-signature" == value || "application/x-pkcs7-signature" == value
}
