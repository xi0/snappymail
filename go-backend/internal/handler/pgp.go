package handler

// The OpenPGP actions of \RainLoop\Actions\Pgp (Step 25): DoGetPGPKeys,
// DoPgpSearchKey, DoGnupgDecrypt, DoGnupgGetKeys, DoGnupgExportKey,
// DoGnupgGenerateKey, DoGnupgDeleteKey, DoPgpImportKey, DoGetStoredPGPKeys,
// DoPgpStoreKeyPair, DoStorePGPKey and DoPgpVerifyMessage.
//
// Two halves meet here:
//
//   - the *server side key escrow* of SnappyMail\PGP\Backup (the ".pgp" folder of
//     the main account, sealed with the account crypt key), reached through the
//     injected PGP provider so the handler stays independent of the storage and
//     cookie layers; and
//   - the *engine* of SnappyMail\PGP\GnuPG::getInstance() (the gpg binary, or the
//     pure Go OpenPGP engine of internal/pgp). A nil engine reproduces the
//     "if (!$GPG) return FalseResponse()" branches.
//
// Signing and encrypting an *outgoing* message stays a browser task (OpenPGP.js)
// exactly like in PHP: DoSendMessage() only renders the `signed`/`encrypted`
// wire shapes of step 19, so this step adds no compose hook.

import (
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/mime"
	"github.com/xi0/snappymail/go-backend/internal/pgp"
)

// emailSearchPattern is the '/[^\s<>]+@[^\s<>]+/' match of DoPgpImportKey().
var emailSearchPattern = regexp.MustCompile(`[^\s<>]+@[^\s<>]+`)

// PGP is the Actions\Pgp dependency: the key escrow of SnappyMail\PGP\Backup,
// the engine factory of Actions\Pgp::GnuPG() and the key server lookups.
//
// A nil provider (a request without a usable session) reports no keys and no
// engine, which is what PHP does without a main account.
type PGP interface {
	// StoreKey is Backup::PGPKey().
	StoreKey(key, keyID string) (bool, error)
	// Keys is Backup::getKeys().
	Keys() (pgp.StoredKeys, error)
	// Engine is Actions\Pgp::GnuPG(): nil when no engine is available.
	Engine() (pgp.Engine, error)
	// SearchKey is Keyservers::get(): the armored key block, or "" when the
	// key servers had no answer.
	SearchKey(keyID string) (string, error)
	// FindKeyID is Keyservers::index($search)[0]['keyid'].
	FindKeyID(search string) (string, error)
}

// PGPFactory builds the PGP surface of one request from the resolved session.
type PGPFactory func(session Session) PGP

// RegisterPGP registers the DoPgp*/DoGnupg* actions.
func RegisterPGP(d *Dispatcher) {
	d.Register("GetPGPKeys", doGetPGPKeys)
	d.Register("PgpSearchKey", doPgpSearchKey)
	d.Register("GnupgDecrypt", doGnupgDecrypt)
	d.Register("GnupgGetKeys", doGnupgGetKeys)
	d.Register("GnupgExportKey", doGnupgExportKey)
	d.Register("GnupgGenerateKey", doGnupgGenerateKey)
	d.Register("GnupgDeleteKey", doGnupgDeleteKey)
	d.Register("PgpImportKey", doPgpImportKey)
	d.Register("GetStoredPGPKeys", doGetStoredPGPKeys)
	d.Register("PgpStoreKeyPair", doPgpStoreKeyPair)
	d.Register("StorePGPKey", doStorePGPKey)
	d.Register("PgpVerifyMessage", doPgpVerifyMessage)
}

// pgpProvider is the wired PGP surface, or nil.
func (c *Context) pgpProvider() PGP {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newPGP {
		return nil
	}
	return c.dispatcher.newPGP(c.session)
}

// gnuPGEngine is Actions\Pgp::GnuPG(): (nil, nil) when no engine is available,
// the error of a unusable home directory otherwise.
func (c *Context) gnuPGEngine() (pgp.Engine, error) {
	provider := c.pgpProvider()
	if nil == provider {
		return nil, nil
	}
	return provider.Engine()
}

// doGetPGPKeys is Actions\Pgp::DoGetPGPKeys().
func doGetPGPKeys(ctx *Context) (any, error) {
	result := []any{}

	provider := ctx.pgpProvider()
	if nil == provider {
		return ctx.DefaultResponse(result, nil), nil
	}

	keys, err := provider.Keys()
	if nil != err {
		return nil, err
	}
	for _, key := range keys.Public {
		result = append(result, key.Value)
	}
	for _, key := range keys.Private {
		result = append(result, key.Value)
	}

	engine, err := provider.Engine()
	if nil != err {
		return nil, err
	}
	if nil != engine {
		for _, key := range engine.AllKeysInfo("").Public {
			if 0 == len(key.Subkeys) {
				continue
			}
			target := key.Subkeys[0].Fingerprint
			if "" == target {
				target = key.Subkeys[0].KeyID
			}
			exported, exportErr := engine.Export(target, nil)
			if nil != exportErr || "" == exported {
				// PHP: "if ($key) $result[] = $key;" — a key the engine cannot
				// export is skipped, not fatal.
				continue
			}
			result = append(result, exported)
		}
	}

	return ctx.DefaultResponse(uniqueValues(result), nil), nil
}

// doPgpSearchKey is Actions\Pgp::DoPgpSearchKey().
func doPgpSearchKey(ctx *Context) (any, error) {
	provider := ctx.pgpProvider()
	if nil == provider {
		return ctx.DefaultResponse(false, nil), nil
	}

	key, err := provider.SearchKey(ctx.stringParam("query", ""))
	if nil != err {
		return nil, err
	}
	if "" == key {
		return ctx.DefaultResponse(false, nil), nil
	}

	return ctx.DefaultResponse(key, nil), nil
}

// doGnupgDecrypt is Actions\Pgp::DoGnupgDecrypt().
func doGnupgDecrypt(ctx *Context) (any, error) {
	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.FalseResponse(0, "", ""), nil
	}

	engine.AddDecryptKey(ctx.stringParam("keyId", ""), ctx.stringParam("passphrase", ""))

	// PHP initialises the document with a null body and an empty signature
	// list; the signature part stays empty because the PHP parse is commented
	// out as well.
	result := map[string]any{
		"data":       nil,
		"signatures": []any{},
	}

	if data := ctx.stringParam("data", ""); "" != data {
		plain, decryptErr := engine.Decrypt(data)
		if nil != decryptErr {
			return nil, decryptErr
		}
		result["data"] = plain
		return ctx.DefaultResponse(result, nil), nil
	}

	// No inline data: decrypt the body part of a message.
	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return ctx.DefaultResponse(result, nil), nil
	}

	partID, err := ctx.validatePartID(ctx.stringParam("partId", ""))
	if nil != err {
		return nil, err
	}

	var decryptErr error
	client.MessageMimeStream(func(reader io.ReadSeeker, _, _, _ string) {
		content, readErr := io.ReadAll(reader)
		if nil != readErr {
			decryptErr = readErr
			return
		}
		plain, innerErr := engine.Decrypt(string(content))
		if nil != innerErr {
			decryptErr = innerErr
			return
		}
		result["data"] = plain
	}, ctx.stringParam("folder", ""), ctx.intParam("uid"), partID)

	if nil != decryptErr {
		return nil, decryptErr
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doGnupgGetKeys is Actions\Pgp::DoGnupgGetKeys().
func doGnupgGetKeys(ctx *Context) (any, error) {
	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.DefaultResponse(false, nil), nil
	}

	return ctx.DefaultResponse(engine.AllKeysInfo(""), nil), nil
}

// doGnupgExportKey is Actions\Pgp::DoGnupgExportKey().
func doGnupgExportKey(ctx *Context) (any, error) {
	var passphrase *string
	if ctx.boolParam("isPrivate") {
		value := ctx.stringParam("passphrase", "")
		passphrase = &value
	}

	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.DefaultResponse(false, nil), nil
	}

	key, err := engine.Export(ctx.stringParam("keyId", ""), passphrase)
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(key, nil), nil
}

// doGnupgGenerateKey is Actions\Pgp::DoGnupgGenerateKey().
func doGnupgGenerateKey(ctx *Context) (any, error) {
	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.DefaultResponse(false, nil), nil
	}

	name := ctx.stringParam("name", "")
	email := ctx.stringParam("email", "")

	uid := email
	if "" != name {
		uid = name + " <" + email + ">"
	}

	fingerprint, err := engine.GenerateKey(uid, ctx.stringParam("passphrase", ""))
	if nil != err {
		return nil, err
	}
	if "" == fingerprint {
		return ctx.DefaultResponse(false, nil), nil
	}

	return ctx.DefaultResponse(fingerprint, nil), nil
}

// doGnupgDeleteKey is Actions\Pgp::DoGnupgDeleteKey().
func doGnupgDeleteKey(ctx *Context) (any, error) {
	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.DefaultResponse(false, nil), nil
	}

	deleted, err := engine.DeleteKey(ctx.stringParam("keyId", ""), ctx.boolParam("isPrivate"))
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(deleted, nil), nil
}

// doPgpImportKey is Actions\Pgp::DoPgpImportKey().
//
// The key server lookups are best effort: the PHP try/catch ignores every error
// they may raise ("if (!$sKey) try { … } catch (\Throwable $e) { }").
func doPgpImportKey(ctx *Context) (any, error) {
	provider := ctx.pgpProvider()

	key := ctx.stringParam("key", "")
	keyID := ctx.stringParam("keyId", "")
	email := ctx.stringParam("email", "")

	if "" == key && nil != provider {
		if "" == keyID {
			if match := emailSearchPattern.FindString(email); "" != match {
				email = match
			}
			if "" != email {
				if found, err := provider.FindKeyID(email); nil == err && "" != found {
					keyID = found
				}
			}
		}
		if "" != keyID {
			if fetched, err := provider.SearchKey(keyID); nil == err {
				key = fetched
			}
		}
	}

	// PHP returns array() — an empty JSON array — when no key was found.
	if "" == key {
		return ctx.DefaultResponse([]any{}, nil), nil
	}

	key = strings.TrimSpace(key)

	result := map[string]any{}

	backup := false
	if ctx.boolParam("backup") && nil != provider {
		stored, err := provider.StoreKey(key, "")
		if nil != err {
			return nil, err
		}
		backup = stored
	}
	result["backup"] = backup

	imported := false
	if ctx.boolParam("gnuPG") {
		engine, err := ctx.gnuPGEngine()
		if nil != err {
			return nil, err
		}
		if nil == engine {
			// PHP: "Call to a member function import() on null".
			return nil, appctx.NewClientError(appctx.UnknownError, nil, "gnupg not supported")
		}
		stats, err := engine.Import(key)
		if nil != err {
			return nil, err
		}
		imported = 0 < stats.Imported || 0 < stats.SecretImported || 0 < stats.Unchanged
	}
	result["gnuPG"] = imported

	return ctx.DefaultResponse(result, nil), nil
}

// doGetStoredPGPKeys is Actions\Pgp::DoGetStoredPGPKeys().
func doGetStoredPGPKeys(ctx *Context) (any, error) {
	if provider := ctx.pgpProvider(); nil != provider {
		keys, err := provider.Keys()
		if nil != err {
			return nil, err
		}
		return ctx.DefaultResponse(keys, nil), nil
	}

	return ctx.DefaultResponse(pgp.StoredKeys{Public: []pgp.StoredKey{}, Private: []pgp.StoredKey{}}, nil), nil
}

// doPgpStoreKeyPair is Actions\Pgp::DoPgpStoreKeyPair().
func doPgpStoreKeyPair(ctx *Context) (any, error) {
	publicKey := ctx.stringParam("publicKey", "")
	privateKey := ctx.stringParam("privateKey", "")

	onServer := []any{false, false}
	inGnuPG := []any{false, false}

	result := map[string]any{
		"onServer": onServer,
		"inGnuPG":  inGnuPG,
	}

	provider := ctx.pgpProvider()

	serverMask := ctx.intParam("onServer")
	if "" != publicKey && 0 != serverMask&1 {
		stored, err := storeKey(provider, publicKey, "")
		if nil != err {
			return nil, err
		}
		onServer[0] = stored
	}
	if "" != privateKey && 0 != serverMask&2 {
		stored, err := storeKey(provider, privateKey, "")
		if nil != err {
			return nil, err
		}
		onServer[1] = stored
	}

	gnupgMask := ctx.intParam("inGnuPG")
	if 0 != gnupgMask {
		engine, err := ctx.gnuPGEngine()
		if nil != err {
			return nil, err
		}
		if nil == engine {
			// PHP: "Call to a member function import() on null".
			return nil, appctx.NewClientError(appctx.UnknownError, nil, "gnupg not supported")
		}
		if "" != publicKey && 0 != gnupgMask&1 {
			stats, err := engine.Import(publicKey)
			if nil != err {
				return nil, err
			}
			inGnuPG[0] = stats
		}
		if "" != privateKey && 0 != gnupgMask&2 {
			stats, err := engine.Import(privateKey)
			if nil != err {
				return nil, err
			}
			inGnuPG[1] = stats
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// storeKey is Backup::PGPKey() through the provider, false without one.
func storeKey(provider PGP, key, keyID string) (bool, error) {
	if nil == provider {
		return false, nil
	}
	return provider.StoreKey(key, keyID)
}

// doStorePGPKey is Actions\Pgp::DoStorePGPKey().
func doStorePGPKey(ctx *Context) (any, error) {
	key := ctx.stringParam("key", "")
	keyID := ctx.stringParam("keyId", "")

	if "" == key || "" == keyID {
		return ctx.DefaultResponse(false, nil), nil
	}

	stored, err := storeKey(ctx.pgpProvider(), key, keyID)
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(stored, nil), nil
}

// doPgpVerifyMessage is Actions\Pgp::DoPgpVerifyMessage().
//
// The verify itself only runs server side: OpenPGP.js sets useGnuPG to 0, so
// the frontend asks for a server check only when the user enabled it. Without an
// engine the action answers false, exactly like the PHP "else { $result = false; }".
func doPgpVerifyMessage(ctx *Context) (any, error) {
	var result map[string]any

	if bodyPart := ctx.stringParam("bodyPart", ""); "" != bodyPart {
		result = map[string]any{
			"text":      crlfNewlines(bodyPart),
			"signature": ctx.stringParam("sigPart", ""),
		}
	} else {
		parts, err := ctx.fetchSignedParts()
		if nil != err {
			return nil, err
		}
		result = map[string]any{"text": parts.text, "signature": parts.signature}
	}

	// PHP: "if ($this->GetActionParam('tryGnuPG', 1))" — only then the server
	// verifies; without it the frontend gets the extracted text and signature.
	if !ctx.boolParamDefaultTrue("tryGnuPG") {
		return ctx.DefaultResponse(result, nil), nil
	}

	text, _ := result["text"].(string)
	signature, _ := result["signature"].(string)

	engine, err := ctx.gnuPGEngine()
	if nil != err {
		return nil, err
	}
	if nil == engine {
		return ctx.DefaultResponse(false, nil), nil
	}

	signatures, err := engine.Verify(text, signature)
	if nil != err {
		return nil, err
	}
	if 0 == len(signatures) {
		return ctx.DefaultResponse(false, nil), nil
	}

	first := signatures[0]

	return ctx.DefaultResponse(map[string]any{
		"fingerprint": first.Fingerprint,
		"validity":    first.Validity,
		"status":      first.Status,
		"summary":     first.Summary,
		// The GNUPG_SIGSUM_* message table only exists with the pecl extension,
		// so the PHP default (an empty string) is what the frontend sees.
		"message": "",
	}, nil), nil
}

// signedParts is the {text, signature} pair of the message branch.
type signedParts struct {
	text      string
	signature string
}

// fetchSignedParts is the IMAP branch of DoPgpVerifyMessage(): the body part
// (plus its MIME headers) and the detached signature of a signed message.
func (c *Context) fetchSignedParts() (signedParts, error) {
	result := signedParts{}

	folder := c.stringParam("folder", "")
	uid := c.intParam("uid")

	partID, err := c.validatePartID(c.stringParam("partId", ""))
	if nil != err {
		return result, err
	}
	sigPartID, err := c.validatePartID(c.stringParam("sigPartId", ""))
	if nil != err {
		return result, err
	}

	_, client, err := c.MailClient()
	if nil != err {
		return result, err
	}
	if nil == client {
		return result, appctx.NewClientError(appctx.InvalidToken, nil, "Account undefined")
	}

	imap := client.ImapClient()
	if _, err := imap.FolderExamine(folder, false); nil != err {
		return result, err
	}

	fetchItems := []string{
		imapclient.FetchBodyPeek + "[" + partID + "]",
		// An empty section specification refers to the entire message, but
		// Dovecot does not return it with BODY.PEEK[1], so the MIME headers are
		// fetched as well (the same workaround as PHP).
		imapclient.FetchBodyPeek + "[" + partID + ".MIME]",
	}
	if "" != sigPartID {
		fetchItems = append(fetchItems, imapclient.FetchBodyPeek+"["+sigPartID+"]")
	}

	responses, err := imap.Fetch(fetchItems, strconv.Itoa(uid), true)
	if nil != err {
		return result, err
	}
	if 0 == len(responses) {
		return result, nil
	}
	response := responses[0]

	bodyMime := response.FetchString(imapclient.FetchBody + "[" + partID + ".MIME]")

	if "" != sigPartID {
		body, _ := response.GetBodyPartValue(partID)
		result.text = crlfNewlines(bodyMime + body)
		result.signature = asciiOnly(response.FetchString(imapclient.FetchBody + "[" + sigPartID + "]"))
		return result, nil
	}

	// Clearsigned text: the transfer encoding of the part decides the decoding.
	body, _ := response.GetBodyPartValue(partID)
	headers := mime.NewHeaderCollection(bodyMime, "")
	encoding := strings.ToLower(strings.TrimSpace(headers.ValueByName(mime.HeaderContentTransferEncoding)))
	if "base64" == encoding || "quoted-printable" == encoding {
		result.text = mime.DecodeEncodingValue(body, encoding)
		return result, nil
	}

	result.text = body
	return result, nil
}

// uniqueValues is array_values(array_unique($array)).
func uniqueValues(values []any) []any {
	result := make([]any, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		text, _ := value.(string)
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, value)
	}

	return result
}

// boolParamDefaultTrue is "GetActionParam($key, 1)" for a flag that defaults to
// enabled (tryGnuPG).
func (c *Context) boolParamDefaultTrue(key string) bool {
	if !c.HasParam(key) {
		return true
	}
	return phpTruthy(c.GetParam(key, 1))
}

// crlfNewlines is preg_replace('/\r?\n/su', "\r\n", $value).
func crlfNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

// asciiOnly is preg_replace('/[^\x00-\x7F]/', '', $value): the signature block
// of the IMAP fetch is filtered down to ASCII, like PHP does.
func asciiOnly(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if 0x80 > value[index] {
			builder.WriteByte(value[index])
		}
	}
	return builder.String()
}
