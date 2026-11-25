package account

// Tests of the Step 24 identity model (Model\Identity).

import "testing"

// TestIdentityFromJSON checks the typed read of a stored document and of a
// client supplied (json) document, including the punycode conversion.
func TestIdentityFromJSON(t *testing.T) {
	identity := NewIdentity("", "")
	document := map[string]any{
		"Id":                    "abc",
		"Label":                 "Work",
		"Email":                 "user@example.com",
		"Name":                  "User",
		"ReplyTo":               "reply@example.com",
		"Bcc":                   "bcc@example.com",
		"Signature":             "Hello\nWorld",
		"SignatureInsertBefore": true,
		"sentFolder":            "Sent",
		"pgpEncrypt":            1,
		"pgpSign":               "1",
		"smimeKey":              "KEY",
		"smimeCertificate":      "CERT",
	}

	if !identity.FromJSON(document, false) {
		t.Fatal("FromJSON failed")
	}

	if "abc" != identity.Id() || "abc" != identity.Id(true) {
		t.Errorf("Id = %q", identity.Id())
	}
	if "user@example.com" != identity.Email() {
		t.Errorf("Email = %q", identity.Email())
	}
	if "User" != identity.Name() {
		t.Errorf("Name = %q", identity.Name())
	}
	if "reply@example.com" != identity.ReplyTo() {
		t.Errorf("ReplyTo = %q", identity.ReplyTo())
	}
	if "bcc@example.com" != identity.Bcc() {
		t.Errorf("Bcc = %q", identity.Bcc())
	}
	if "Hello\nWorld" != identity.Signature() {
		t.Errorf("Signature = %q", identity.Signature())
	}
	if "Sent" != identity.SentFolder() {
		t.Errorf("SentFolder = %q", identity.SentFolder())
	}

	stored := identity.ToSimpleJSON()
	if "abc" != stored.ID || !stored.SignatureInsertBefore || !stored.PgpEncrypt || !stored.PgpSign {
		t.Errorf("ToSimpleJSON = %+v", stored)
	}
	if "KEY" != stored.SmimeKey || "CERT" != stored.SmimeCertificate {
		t.Errorf("ToSimpleJSON smime = %+v", stored)
	}
}

// TestIdentityFromJSONRequiresEmail checks the "!empty($aData['Email'])" guard.
func TestIdentityFromJSONRequiresEmail(t *testing.T) {
	for _, document := range []map[string]any{
		{},
		{"Email": ""},
		{"Email": nil},
		{"Email": "0"}, // empty("0") is true in PHP
	} {
		identity := NewIdentity("", "")
		if identity.FromJSON(document, false) {
			t.Errorf("FromJSON(%v) must fail", document)
		}
	}
}

// TestIdentityFromJSONPunycode checks the $bJson email conversion.
func TestIdentityFromJSONPunycode(t *testing.T) {
	identity := NewIdentity("", "")
	if !identity.FromJSON(map[string]any{"Email": "user@täst.example"}, true) {
		t.Fatal("FromJSON failed")
	}
	if "user@täst.example" == identity.Email() {
		t.Error("a json document must be punycode encoded")
	}

	stored := NewIdentity("", "")
	if !stored.FromJSON(map[string]any{"Email": "user@täst.example"}, false) {
		t.Fatal("FromJSON failed")
	}
	if "user@täst.example" != stored.Email() {
		t.Errorf("a stored document must keep the address, got %q", stored.Email())
	}
}

// TestIdentityPrimary checks Validate/IsAccountIdentities and the Id(true) fill.
func TestIdentityPrimary(t *testing.T) {
	primary := NewIdentity("", "user@example.com")
	if !primary.Validate() {
		t.Error("an identity with an address is valid")
	}
	if !primary.IsAccountIdentities() {
		t.Error("the empty id is the primary identity")
	}
	if "---" != primary.Id(true) || "" != primary.Id() {
		t.Errorf("Id(true) = %q, Id = %q", primary.Id(true), primary.Id())
	}

	empty := NewIdentity("x", "")
	if empty.Validate() {
		t.Error("an identity without an address is invalid")
	}
	if empty.IsAccountIdentities() {
		t.Error("a non-empty id is not the primary identity")
	}
}

// TestIdentityJSONSerialize checks the "Object/Identity" document the frontend
// revives into an IdentityModel.
func TestIdentityJSONSerialize(t *testing.T) {
	identity := NewIdentity("abc", "alias@example.com")
	identity.SetName("Alias")
	identity.SetExists(true)

	document, ok := identity.JSONSerialize().(map[string]any)
	if !ok {
		t.Fatal("JSONSerialize must return a map")
	}

	if "Object/Identity" != document["@Object"] {
		t.Errorf("@Object = %v", document["@Object"])
	}
	if "abc" != document["id"] || "alias@example.com" != document["email"] || "Alias" != document["name"] {
		t.Errorf("document = %v", document)
	}
	if true != document["exists"] {
		t.Errorf("exists = %v", document["exists"])
	}

	// The synthesised primary identity reports exists = false (popup_identity off).
	primary := NewIdentity("", "user@example.com")
	primary.SetExists(false)
	if false != primary.JSONSerialize().(map[string]any)["exists"] {
		t.Error("exists must follow SetExists")
	}
}
