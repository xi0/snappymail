package account

// The identity model of SnappyMail: RainLoop\Model\Identity.
//
// An identity is one "from" address of an account (the primary identity has an
// empty Id and always mirrors the account address) plus its signature, reply-to,
// BCC, per-identity sent folder and PGP/S/MIME preferences. The documents are
// stored as JSON under the CONFIG key "identities" (see identities.go) and are
// also returned to the frontend as "Object/Identity" documents, so both the
// storage layout and the jsonSerialize() shape are part of the contract.

import "github.com/xi0/snappymail/go-backend/internal/domain"

// Identity is RainLoop\Model\Identity.
//
// The fields are private on purpose: the PHP class exposes them through
// __get()/set*() accessors and the JSON document uses different key spellings
// ("Id" vs "id"), so keeping them behind accessors avoids mixing the two.
type Identity struct {
	id                    string
	label                 string
	email                 string
	name                  string
	replyTo               string
	bcc                   string
	signature             string
	sentFolder            string
	signatureInsertBefore bool
	pgpEncrypt            bool
	pgpSign               bool
	smimeKey              string
	smimeCertificate      string
	// exists is the public $exists property: the primary identity of an account
	// that never had one is reported as "not existing" so the frontend opens the
	// identity editor.
	exists bool
}

// NewIdentity builds an identity (Identity::__construct()).
//
// Like PHP the "exists" flag defaults to true; Identities::GetIdentities()
// clears it for a synthesised primary identity.
func NewIdentity(id, email string) *Identity {
	return &Identity{id: id, email: email, exists: true}
}

// Id is Identity::Id(); fillOnEmpty mirrors the $bFillOnEmpty argument that
// returns "---" for the primary identity (the array key of the PHP provider).
func (i *Identity) Id(fillOnEmpty ...bool) string {
	if nil == i {
		return ""
	}
	if 0 < len(fillOnEmpty) && fillOnEmpty[0] && "" == i.id {
		return "---"
	}
	return i.id
}

// Email is Identity::Email().
func (i *Identity) Email() string {
	if nil == i {
		return ""
	}
	return i.email
}

// Name is Identity::Name().
func (i *Identity) Name() string {
	if nil == i {
		return ""
	}
	return i.name
}

// ReplyTo is Identity::ReplyTo().
func (i *Identity) ReplyTo() string {
	if nil == i {
		return ""
	}
	return i.replyTo
}

// Bcc is the $bcc property.
func (i *Identity) Bcc() string {
	if nil == i {
		return ""
	}
	return i.bcc
}

// Signature is the $signature property.
func (i *Identity) Signature() string {
	if nil == i {
		return ""
	}
	return i.signature
}

// SentFolder is the $sentFolder property.
func (i *Identity) SentFolder() string {
	if nil == i {
		return ""
	}
	return i.sentFolder
}

// Exists is the public $exists property.
func (i *Identity) Exists() bool { return nil != i && i.exists }

// SetExists is the (public) $exists assignment of the PHP model.
func (i *Identity) SetExists(exists bool) {
	if nil != i {
		i.exists = exists
	}
}

// SetId is Identity::SetId().
func (i *Identity) SetId(id string) *Identity {
	if nil == i {
		return nil
	}
	i.id = id
	return i
}

// SetEmail is Identity::SetEmail().
func (i *Identity) SetEmail(email string) *Identity {
	if nil == i {
		return nil
	}
	i.email = email
	return i
}

// SetName is Identity::SetName().
func (i *Identity) SetName(name string) *Identity {
	if nil == i {
		return nil
	}
	i.name = name
	return i
}

// Validate is Identity::Validate(): an identity needs an address.
func (i *Identity) Validate() bool {
	return nil != i && !phpEmpty(i.email)
}

// IsAccountIdentities is Identity::IsAccountIdentities(): the primary identity
// has no id of its own and mirrors the account address.
func (i *Identity) IsAccountIdentities() bool {
	return nil != i && "" == i.id
}

// FromJSON is Identity::FromJSON(): fill the identity from a decoded document.
//
// fromJSON (the PHP $bJson) converts the address to punycode, which the client
// supplied form needs; a stored document already holds the ASCII address.
func (i *Identity) FromJSON(data map[string]any, fromJSON bool) bool {
	if nil == i || phpEmpty(data["Email"]) {
		return false
	}

	i.id = ""
	if !phpEmpty(data["Id"]) {
		i.id = phpString(data["Id"])
	}

	i.label = fieldString(data, "Label")
	i.email = phpString(data["Email"])
	if fromJSON {
		i.email = domain.EmailToASCII(i.email)
	}
	i.name = fieldString(data, "Name")

	i.replyTo = ""
	if !phpEmpty(data["ReplyTo"]) {
		i.replyTo = phpString(data["ReplyTo"])
	}
	i.bcc = ""
	if !phpEmpty(data["Bcc"]) {
		i.bcc = phpString(data["Bcc"])
	}
	i.signature = ""
	if !phpEmpty(data["Signature"]) {
		i.signature = phpString(data["Signature"])
	}

	i.signatureInsertBefore = !phpEmpty(data["SignatureInsertBefore"])
	i.sentFolder = fieldString(data, "sentFolder")
	i.pgpEncrypt = !phpEmpty(data["pgpEncrypt"])
	i.pgpSign = !phpEmpty(data["pgpSign"])
	i.smimeKey = fieldString(data, "smimeKey")
	i.smimeCertificate = fieldString(data, "smimeCertificate")

	return true
}

// IdentityDocument is Identity::ToSimpleJSON(): the stored document.
//
// The field order is the PHP array order, because the document is written back
// to disk with json_encode() and both backends read the same file.
type IdentityDocument struct {
	ID                    string `json:"Id"`
	Label                 string `json:"Label"`
	Email                 string `json:"Email"`
	Name                  string `json:"Name"`
	ReplyTo               string `json:"ReplyTo"`
	Bcc                   string `json:"Bcc"`
	Signature             string `json:"Signature"`
	SignatureInsertBefore bool   `json:"SignatureInsertBefore"`
	SentFolder            string `json:"sentFolder"`
	PgpEncrypt            bool   `json:"pgpEncrypt"`
	PgpSign               bool   `json:"pgpSign"`
	SmimeKey              string `json:"smimeKey"`
	SmimeCertificate      string `json:"smimeCertificate"`
}

// ToSimpleJSON is Identity::ToSimpleJSON().
func (i *Identity) ToSimpleJSON() IdentityDocument {
	if nil == i {
		return IdentityDocument{}
	}
	return IdentityDocument{
		ID:                    i.id,
		Label:                 i.label,
		Email:                 i.email,
		Name:                  i.name,
		ReplyTo:               i.replyTo,
		Bcc:                   i.bcc,
		Signature:             i.signature,
		SignatureInsertBefore: i.signatureInsertBefore,
		SentFolder:            i.sentFolder,
		PgpEncrypt:            i.pgpEncrypt,
		PgpSign:               i.pgpSign,
		SmimeKey:              i.smimeKey,
		SmimeCertificate:      i.smimeCertificate,
	}
}

// JSONSerialize is Identity::jsonSerialize(): the "Object/Identity" document the
// frontend revives into an IdentityModel.
func (i *Identity) JSONSerialize() any {
	if nil == i {
		return map[string]any{}
	}
	return map[string]any{
		"@Object":               "Object/Identity",
		"id":                    i.id,
		"label":                 i.label,
		"email":                 domain.EmailToUTF8(i.email),
		"name":                  i.name,
		"replyTo":               i.replyTo,
		"bcc":                   i.bcc,
		"signature":             i.signature,
		"signatureInsertBefore": i.signatureInsertBefore,
		"sentFolder":            i.sentFolder,
		"pgpEncrypt":            i.pgpEncrypt,
		"pgpSign":               i.pgpSign,
		"smimeKey":              i.smimeKey,
		"smimeCertificate":      i.smimeCertificate,
		"exists":                i.exists,
	}
}

// fieldString is PHP's "(string) (isset($aData[$name]) ? $aData[$name] : '')":
// a missing or null key is the empty string.
func fieldString(data map[string]any, name string) string {
	if value, ok := data[name]; ok && nil != value {
		return phpString(value)
	}
	return ""
}
