package handler

// The S/MIME actions of \RainLoop\Actions\SMime (Step 26):
// DoGetSMimeCertificate, DoSMimeGetCertificates, DoSMimeCreateCertificate,
// DoSMimeExportPrivateKey, DoSMimeDecryptMessage, DoSMimeVerifyMessage,
// DoSMimeImportCertificate and DoSMimeImportCertificatesFromMessage.
//
// Two halves meet here, like in the OpenPGP step:
//
//   - the *engine* of SnappyMail\SMime\{Certificate,OpenSSL} (internal/smime): a
//     self-signed certificate, the ".smime" home directory of the main account,
//     its certificate list and the PKCS#7 decrypt/verify; and
//   - the IMAP fetches of DoSMimeDecryptMessage/DoSMimeVerifyMessage, which go
//     through the per-request connection of Context.MailClient() so the
//     InvalidToken/ConnectionError/AuthError codes stay the ones PHP reports.
//
// The action interface is injected (SMIMEFactory) so the package stays
// independent of the storage layout, the cookie jar and the install tree.
//
// Not ported, by design of the step split:
//   - the compose-time S/MIME sign/encrypt branch of
//     Actions\Messages::buildMessage() (Step 19);
//   - the auto-verify branch of Actions\Messages::DoMessage() (Step 17);
//   - Actions\Accounts::verifyIdentity()'s Certificate() probe (Step 24).
//   DoSMimeImportCertificatesFromMessage() is ported as-is: the PHP body is
//   commented out, so the typed method returns nothing and PHP raises a
//   TypeError, which the JSON layer reports as UnknownError.

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/appctx"
	"github.com/xi0/snappymail/go-backend/internal/imapclient"
	"github.com/xi0/snappymail/go-backend/internal/smime"
)

// smimeSignedDataPattern is the '/smime-type=["\']?signed-data/' match of
// DoSMimeDecryptMessage().
var smimeSignedDataPattern = regexp.MustCompile(`smime-type=["']?signed-data`)

// SMIME is the Actions\SMime dependency: the certificate creator of
// SnappyMail\SMime\Certificate and the ".smime" store of
// SnappyMail\SMime\OpenSSL.
//
// A nil provider (a request without a usable session) reproduces PHP's
// "call to a member function on null": the affected actions report
// UnknownError (999).
type SMIME interface {
	// NewCertificate is new \SnappyMail\SMime\Certificate() (the certificate
	// creator, which needs no home directory).
	NewCertificate() *smime.Certificate
	// Certificates is OpenSSL::certificates().
	Certificates() ([]smime.CertificateInfo, error)
	// StoreCertificate is OpenSSL::storeCertificate().
	StoreCertificate(certificate string) (bool, error)
	// SetCertificate is OpenSSL::setCertificate().
	SetCertificate(certificate string) error
	// SetPrivateKey is OpenSSL::setPrivateKey().
	SetPrivateKey(privateKey, passphrase string) error
	// ExportPrivateKey is OpenSSL::exportPrivateKey().
	ExportPrivateKey(passphrase string) (string, error)
	// Decrypt is OpenSSL::decrypt().
	Decrypt(body string) (string, error)
	// Verify is OpenSSL::verify(); opaque is the !$detached flag of the action.
	Verify(body string, opaque bool) (*smime.VerifyResult, error)
}

// SMIMEFactory builds the S/MIME surface of one request from the resolved
// session.
type SMIMEFactory func(session Session) SMIME

// RegisterSMIME registers the DoSMime*/DoGetSMimeCertificate actions.
func RegisterSMIME(d *Dispatcher) {
	d.Register("GetSMimeCertificate", doGetSMimeCertificate)
	d.Register("SMimeGetCertificates", doSMimeGetCertificates)
	d.Register("SMimeCreateCertificate", doSMimeCreateCertificate)
	d.Register("SMimeExportPrivateKey", doSMimeExportPrivateKey)
	d.Register("SMimeDecryptMessage", doSMimeDecryptMessage)
	d.Register("SMimeVerifyMessage", doSMimeVerifyMessage)
	d.Register("SMimeImportCertificate", doSMimeImportCertificate)
	d.Register("SMimeImportCertificatesFromMessage", doSMimeImportCertificatesFromMessage)
}

// smimeProvider is the wired S/MIME surface, or nil.
func (c *Context) smimeProvider() SMIME {
	if nil == c || nil == c.dispatcher || nil == c.dispatcher.newSMIME {
		return nil
	}
	return c.dispatcher.newSMIME(c.session)
}

// requireSMIME is "$this->SMIME()->method()": a missing surface is the PHP fatal
// error of calling a method on null, reported as UnknownError.
func (c *Context) requireSMIME(method string) (SMIME, error) {
	provider := c.smimeProvider()
	if nil == provider {
		return nil, clientError(appctx.UnknownError,
			"Call to a member function "+method+"() on null")
	}
	return provider, nil
}

// doGetSMimeCertificate is Actions\SMime::DoGetSMimeCertificate().
//
// The PHP result is array_values(array_unique(['key'=>'', 'pkey'=>'', 'cert'=>''])),
// which collapses to a single empty string — ported as-is.
func doGetSMimeCertificate(ctx *Context) (any, error) {
	return ctx.DefaultResponse([]any{""}, nil), nil
}

// doSMimeGetCertificates is Actions\SMime::DoSMimeGetCertificates().
func doSMimeGetCertificates(ctx *Context) (any, error) {
	provider, err := ctx.requireSMIME("certificates")
	if nil != err {
		return nil, err
	}

	certificates, err := provider.Certificates()
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(certificates, nil), nil
}

// doSMimeCreateCertificate is Actions\SMime::DoSMimeCreateCertificate().
//
// The certificate is signed by the bundled SnappyMail CA (or self-signed when
// that file is not readable, see internal/smime).
func doSMimeCreateCertificate(ctx *Context) (any, error) {
	resolved, err := ctx.requireAccount()
	if nil != err {
		return nil, err
	}

	var certificate *smime.Certificate
	if provider := ctx.smimeProvider(); nil != provider {
		certificate = provider.NewCertificate()
	} else {
		// The creator itself needs no home directory.
		certificate, err = smime.NewCertificate("", "")
		if nil != err {
			return nil, err
		}
	}

	name := ctx.stringParam("name", "")
	if "" == name && nil != resolved {
		name = resolved.Name()
	}
	email := ctx.stringParam("email", "")
	if "" == email && nil != resolved {
		email = resolved.Email()
	}

	certificate.DistinguishedName[smime.DNCommonName] = name
	certificate.DistinguishedName[smime.DNEmailAddress] = email

	result, err := certificate.CreateSelfSigned(
		ctx.stringParam("passphrase", ""),
		ctx.stringParam("privateKey", ""),
	)
	if nil != err {
		return nil, err
	}
	if 0 == len(result) {
		return ctx.DefaultResponse(false, nil), nil
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSMimeExportPrivateKey is Actions\SMime::DoSMimeExportPrivateKey().
func doSMimeExportPrivateKey(ctx *Context) (any, error) {
	provider, err := ctx.requireSMIME("setPrivateKey")
	if nil != err {
		return nil, err
	}

	if err := provider.SetPrivateKey(
		ctx.stringParam("privateKey", ""),
		ctx.stringParam("oldPassphrase", ""),
	); nil != err {
		return nil, err
	}

	result, err := provider.ExportPrivateKey(ctx.stringParam("newPassphrase", ""))
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSMimeDecryptMessage is Actions\SMime::DoSMimeDecryptMessage().
func doSMimeDecryptMessage(ctx *Context) (any, error) {
	folder := ctx.stringParam("folder", "")
	uid := ctx.intParam("uid")

	partID, err := ctx.validatePartID(ctx.stringParam("partId", ""))
	if nil != err {
		return nil, err
	}

	certificate := ctx.stringParam("certificate", "")
	privateKey := ctx.stringParam("privateKey", "")
	passphrase := ctx.stringParam("passphrase", "")

	_, client, err := ctx.MailClient()
	if nil != err {
		return nil, err
	}
	if nil == client {
		return nil, clientError(appctx.InvalidToken, "Account undefined")
	}

	imap := client.ImapClient()
	if _, err := imap.FolderExamine(folder, false); nil != err {
		return nil, err
	}

	body, err := fetchDecryptBody(imap, uid, partID)
	if nil != err {
		return nil, err
	}

	provider, err := ctx.requireSMIME("decrypt")
	if nil != err {
		return nil, err
	}

	if err := provider.SetCertificate(certificate); nil != err {
		return nil, err
	}
	if err := provider.SetPrivateKey(privateKey, passphrase); nil != err {
		return nil, err
	}

	plain, err := provider.Decrypt(body)
	if nil != err {
		return nil, err
	}
	if "" == plain {
		return ctx.DefaultResponse(false, nil), nil
	}

	result := map[string]any{"data": plain}

	// The decrypted content may be signed as well; unwrap it so the frontend
	// gets the plaintext and the verification verdict.
	if strings.Contains(plain, "multipart/signed") || smimeSignedDataPattern.MatchString(plain) {
		verified, err := provider.Verify(plain, true)
		if nil != err {
			return nil, err
		}

		result["signed"] = map[string]any{
			"success": nil != verified && verified.Success,
		}
		if nil != verified && verified.Opaque && "" != verified.Body {
			result["data"] = verified.Body
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSMimeVerifyMessage is Actions\SMime::DoSMimeVerifyMessage().
func doSMimeVerifyMessage(ctx *Context) (any, error) {
	body := ctx.stringParam("bodyPart", "")

	partID, err := ctx.validatePartID(ctx.stringParam("partId", ""))
	if nil != err {
		return nil, err
	}
	sigPartID, err := ctx.validatePartID(ctx.stringParam("sigPartId", ""))
	if nil != err {
		return nil, err
	}

	detached := ctx.boolParam("detached")
	uid := ctx.intParam("uid")

	// PHP only keeps $oImapClient when the body had to be fetched from IMAP;
	// the signature import below reuses it (and not the connection).
	var imap *imapclient.Client

	if "" == body && "" != partID {
		_, client, err := ctx.MailClient()
		if nil != err {
			return nil, err
		}
		if nil == client {
			return nil, clientError(appctx.InvalidToken, "Account undefined")
		}

		imap = client.ImapClient()
		if _, err := imap.FolderExamine(ctx.stringParam("folder", ""), false); nil != err {
			return nil, err
		}

		if body, err = fetchMessagePart(imap, uid, partID); nil != err {
			return nil, err
		}
	}

	provider, err := ctx.requireSMIME("verify")
	if nil != err {
		return nil, err
	}

	verified, err := provider.Verify(body, !detached)
	if nil != err {
		return nil, err
	}

	result := map[string]any{"body": nil, "success": nil != verified && verified.Success}
	if nil != verified && verified.Opaque {
		result["body"] = verified.Body
	}

	// Import the certificates of the signature automatically.
	signature := ctx.stringParam("sigPart", "")
	certificatePartID := sigPartID
	if "" == certificatePartID {
		certificatePartID = partID
	}
	if "" == signature && "" != certificatePartID && nil != imap {
		if signature, err = fetchMessagePart(imap, uid, certificatePartID); nil != err {
			return nil, err
		}
	}
	if "" != signature {
		// openssl_pkcs7_read() failures only log in PHP, so a signature part
		// without certificates is not an error here either.
		for _, certificate := range smime.CertificatesOfPKCS7(strings.TrimSpace(signature)) {
			_, _ = provider.StoreCertificate(certificate)
		}
	}

	return ctx.DefaultResponse(result, nil), nil
}

// doSMimeImportCertificate is Actions\SMime::DoSMimeImportCertificate().
func doSMimeImportCertificate(ctx *Context) (any, error) {
	provider, err := ctx.requireSMIME("storeCertificate")
	if nil != err {
		return nil, err
	}

	stored, err := provider.StoreCertificate(ctx.stringParam("pem", ""))
	if nil != err {
		return nil, err
	}

	return ctx.DefaultResponse(stored, nil), nil
}

// doSMimeImportCertificatesFromMessage is
// Actions\SMime::DoSMimeImportCertificatesFromMessage().
//
// The PHP body is commented out, so the method falls through without a return
// statement and PHP raises "Return value must be of type array, none returned";
// the JSON layer reports that as UnknownError. Ported as-is.
func doSMimeImportCertificatesFromMessage(ctx *Context) (any, error) {
	return nil, clientError(appctx.UnknownError,
		"Return value must be of type array, none returned")
}

// fetchMessagePart is MailSo\Imap\ImapClient::FetchMessagePart(): the MIME
// headers of the part followed by its body.
func fetchMessagePart(imap *imapclient.Client, uid int, partID string) (string, error) {
	return fetchPart(imap, uid, partID, true)
}

// fetchDecryptBody is the (inline) fetch of DoSMimeDecryptMessage(), which
// returns only the header block for the TEXT section — a PHP quirk kept as-is.
func fetchDecryptBody(imap *imapclient.Client, uid int, partID string) (string, error) {
	return fetchPart(imap, uid, partID, false)
}

// fetchPart reads one MIME part: the section plus its MIME headers, because
// "an empty section specification refers to the entire message, but Dovecot does
// not return it with BODY.PEEK[1]" (the PHP workaround).
//
// withTextBody is FetchMessagePart()'s "return $sHeader . BODY[TEXT]" for the
// TEXT section; DoSMimeDecryptMessage() drops that part of the body.
func fetchPart(imap *imapclient.Client, uid int, partID string, withTextBody bool) (string, error) {
	items := []string{
		imapclient.FetchBodyPeek + "[" + partID + "]",
		imapclient.FetchBodyPeek + "[" + partID + ".MIME]",
	}

	// The TEXT section has no MIME headers of its own, so the message headers
	// are fetched instead.
	header := imapclient.FetchBody + "[" + partID + ".MIME]"
	if "TEXT" == partID {
		items = []string{
			imapclient.FetchBodyPeek + "[" + partID + "]",
			imapclient.FetchBodyHeaderPeek,
		}
		header = imapclient.FetchBodyHeader
	}

	response, err := firstFetchResponse(imap, uid, items)
	if nil != err {
		return "", err
	}

	body := response.FetchString(header)
	if "TEXT" == partID && !withTextBody {
		return body, nil
	}

	part, _ := response.GetBodyPartValue(partID)

	return body + part, nil
}

// firstFetchResponse is the "[0]" of the PHP fetches; an empty response is the
// "trying to read a property on null" fatal, reported as UnknownError.
func firstFetchResponse(
	imap *imapclient.Client,
	uid int,
	items []string,
) (*imapclient.FetchResponse, error) {
	responses, err := imap.Fetch(items, strconv.Itoa(uid), true)
	if nil != err {
		return nil, err
	}
	if 0 == len(responses) || nil == responses[0] {
		return nil, clientError(appctx.UnknownError, "No message part found")
	}
	return responses[0], nil
}
