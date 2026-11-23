package mime

// Tests of the MIME header/email models used by the message list.

import "testing"

func TestHeaderCollectionParsesAndDecodes(t *testing.T) {
	raw := "Subject: =?UTF-8?B?SGVsbG8gd29ybGQ=?=\r\n" +
		"From: Alice <alice@example.com>\r\n" +
		"To: bob@example.com, Carol <carol@example.com>\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Date: Thu, 10 Jun 2010 08:58:33 -0700\r\n"

	headers := NewHeaderCollection(raw, "")

	if got := headers.ValueByName(HeaderSubject); "Hello world" != got {
		t.Errorf("subject = %q, want %q", got, "Hello world")
	}
	if got := headers.ParameterValue(HeaderContentType, ParameterCharset); "utf-8" != got {
		t.Errorf("charset = %q", got)
	}

	from := headers.GetAsEmailCollection(HeaderFrom)
	if 1 != from.Count() {
		t.Fatalf("from count = %d", from.Count())
	}
	email := from.Iterate()[0]
	if "alice@example.com" != email.GetEmail() || "Alice" != email.GetDisplayName() {
		t.Errorf("from = %q / %q", email.GetEmail(), email.GetDisplayName())
	}

	to := headers.GetAsEmailCollection(HeaderTo)
	if 2 != to.Count() {
		t.Fatalf("to count = %d", to.Count())
	}
	if "bob@example.com" != to.Iterate()[0].GetEmail() {
		t.Errorf("to[0] = %q", to.Iterate()[0].GetEmail())
	}
	if "Carol" != to.Iterate()[1].GetDisplayName() {
		t.Errorf("to[1] name = %q", to.Iterate()[1].GetDisplayName())
	}

	serialized, ok := headers.JSONSerialize().(map[string]any)
	if !ok || "Collection/MimeHeaderCollection" != serialized["@Object"] {
		t.Fatalf("serialized = %v", headers.JSONSerialize())
	}
	collection, _ := serialized["@Collection"].([]any)
	if 0 == len(collection) {
		t.Fatal("the header collection is empty")
	}
	firstHeader, _ := collection[0].(*Header)
	if nil == firstHeader || "Object/MimeHeader" != firstHeader.JSONSerialize().(map[string]any)["@Object"] {
		t.Errorf("first header = %v", collection[0])
	}
}

func TestEmailParse(t *testing.T) {
	cases := []struct {
		raw   string
		email string
		name  string
	}{
		{`Alice <alice@example.com>`, "alice@example.com", "Alice"},
		{`"Bob Smith" <bob@example.com>`, "bob@example.com", "Bob Smith"},
		{`carol@example.com`, "carol@example.com", ""},
		{`Dave (comment)`, "", "Dave"},
	}

	for _, test := range cases {
		email := ParseEmail(test.raw)
		if nil == email {
			t.Errorf("ParseEmail(%q) = nil", test.raw)
			continue
		}
		if email.GetEmail() != test.email || email.GetDisplayName() != test.name {
			t.Errorf("ParseEmail(%q) = %q / %q, want %q / %q",
				test.raw, email.GetEmail(), email.GetDisplayName(), test.email, test.name)
		}
	}
}

func TestEmailCollectionJSON(t *testing.T) {
	collection := NewEmailCollection("Alice <alice@example.com>, bob@example.com")
	serialized, ok := collection.JSONSerialize().([]any)
	if !ok || 2 != len(serialized) {
		t.Fatalf("serialized = %v", collection.JSONSerialize())
	}
	first, _ := serialized[0].(*Email)
	if nil == first {
		t.Fatal("first entry is not an Email")
	}
	doc, _ := first.JSONSerialize().(map[string]any)
	if "Object/Email" != doc["@Object"] || "alice@example.com" != doc["email"] || "none" != doc["dkimStatus"] {
		t.Errorf("email doc = %v", doc)
	}
}

func TestDateParsing(t *testing.T) {
	if got := ParseRFC2822DateString("Thu, 10 Jun 2010 08:58:33 -0700 (PDT)"); 0 == got {
		t.Error("RFC 2822 date was not parsed")
	}
	if got := ParseInternalDateString("10-Jan-2012 01:58:17 -0800"); 0 == got {
		t.Error("INTERNALDATE was not parsed")
	}
	if got := ParseRFC2822DateString(""); 0 != got {
		t.Errorf("empty date = %d", got)
	}
	if got := ParseRFC2822DateString("not a date"); 0 != got {
		t.Errorf("invalid date = %d", got)
	}
}
