package contacts

// Tests of the Step 22 sqlite address book: the schema, the save→list→search→
// delete round-trip, the jCard passthrough and the CSV/VCF exports.

import (
	"encoding/csv"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
)

// testAccount builds the address book account.
func testAccount() *account.Account {
	main := account.NewMainAccount()
	main.SetCredentials(nil, "user@example.com", "user@example.com", "secret", "", "")
	return &main.Account
}

// newProvider builds a global (private data) address book in a temp folder.
func newProvider(t *testing.T) *Provider {
	t.Helper()

	return New(Options{
		Version:     "0.0.0",
		Enabled:     true,
		PrivateData: t.TempDir(),
		Global:      true,
	})
}

// sampleJCard is the jCard the frontend sends for a new contact (a JSON string,
// like ContactModel.toJSON()).
const sampleJCard = `["vcard",[
	["version",{},"text","4.0"],
	["fn",{},"text","Alice Example"],
	["n",{},"text",["Example","Alice","","",""]],
	["email",{"type":"HOME"},"text","alice@example.com"],
	["tel",{"type":"CELL"},"text","+123456789"]
]]`

// TestSchemaAndSaveRoundTrip checks that the schema is created, a contact is
// stored and read back through the JSON document.
func TestSchemaAndSaveRoundTrip(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	id, ok := provider.ContactSave(resolved, sampleJCard, "")
	if !ok || "" == id {
		t.Fatalf("ContactSave = %q, %v", id, ok)
	}

	list, count := provider.GetContacts(resolved, 0, 20, "")
	if 1 != count || 1 != len(list) {
		t.Fatalf("GetContacts = %d/%d, want 1/1", len(list), count)
	}

	document, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("contact = %#v", list[0])
	}
	if "Object/Contact" != document["@Object"] {
		t.Errorf("@Object = %v", document["@Object"])
	}
	if id != document["id"] {
		t.Errorf("id = %v, want %s", document["id"], id)
	}

	card, err := parseJCard(document["jCard"])
	if nil != err {
		t.Fatalf("jCard is not parseable: %v", err)
	}
	if "Alice Example" != card.FN() {
		t.Errorf("FN = %q", card.FN())
	}
	if emails := card.values("email"); 1 != len(emails) || "alice@example.com" != emails[0] {
		t.Errorf("EMAIL = %v", emails)
	}
	// REV/PRODID are stamped on save.
	if "" == stringValue(card.first("rev")) {
		t.Error("REV must be stamped on save")
	}
	if "SnappyMail-0.0.0" != stringValue(card.first("prodid")) {
		t.Errorf("PRODID = %q", stringValue(card.first("prodid")))
	}
	// The KDE/SnappyMail X-CRYPTO property is added by setVCard().
	if nil == card.first("x-crypto") {
		t.Error("X-CRYPTO must be added on save")
	}
}

// TestSearchAndDelete checks the LIKE search and the soft delete.
func TestSearchAndDelete(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	if _, ok := provider.ContactSave(resolved, sampleJCard, ""); !ok {
		t.Fatal("save failed")
	}
	bob := `["vcard",[["fn",{},"text","Bob Builder"],["email",{},"text","bob@example.org"]]]`
	if _, ok := provider.ContactSave(resolved, bob, ""); !ok {
		t.Fatal("save bob failed")
	}

	if _, count := provider.GetContacts(resolved, 0, 20, ""); 2 != count {
		t.Errorf("count = %d, want 2", count)
	}
	if _, count := provider.GetContacts(resolved, 0, 20, "alice"); 1 != count {
		t.Errorf("search alice count = %d, want 1", count)
	}
	if _, count := provider.GetContacts(resolved, 0, 20, "example.org"); 1 != count {
		t.Errorf("search example.org count = %d, want 1", count)
	}

	// Delete the first contact (id 1) and confirm it is gone but bob stays.
	if !provider.DeleteContacts(resolved, []int{1}) {
		t.Fatal("DeleteContacts failed")
	}
	list, count := provider.GetContacts(resolved, 0, 20, "")
	if 1 != count || 1 != len(list) {
		t.Fatalf("after delete count = %d/%d, want 1/1", len(list), count)
	}
	document := list[0].(map[string]any)
	card, _ := parseJCard(document["jCard"])
	if "Bob Builder" != card.FN() {
		t.Errorf("remaining contact FN = %q, want Bob Builder", card.FN())
	}
}

// TestCSVExportColumnOrder checks the exact column order of VCardToCsv().
func TestCSVExportColumnOrder(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	if _, ok := provider.ContactSave(resolved, sampleJCard, ""); !ok {
		t.Fatal("save failed")
	}

	data, ok := provider.Export(resolved, "csv")
	if !ok {
		t.Fatal("Export(csv) failed")
	}

	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if nil != err {
		t.Fatalf("csv parse: %v", err)
	}
	if 2 != len(records) {
		t.Fatalf("rows = %d, want header + 1", len(records))
	}

	header := records[0]
	if len(header) != len(csvHeader) {
		t.Fatalf("header columns = %d, want %d", len(header), len(csvHeader))
	}
	if "Title" != header[0] || "First Name" != header[1] || "Last Name" != header[3] ||
		"E-mail Address" != header[10] || "Business Country" != header[28] {
		t.Errorf("header order = %v", header)
	}

	row := records[1]
	if "Alice Example" != row[0] { // Title
		t.Errorf("Title = %q", row[0])
	}
	if "Alice" != row[1] { // First Name (N[1])
		t.Errorf("First Name = %q", row[1])
	}
	if "Example" != row[3] { // Last Name (N[0])
		t.Errorf("Last Name = %q", row[3])
	}
	if "alice@example.com" != row[10] { // E-mail Address
		t.Errorf("E-mail Address = %q", row[10])
	}
	if "+123456789" != row[14] { // Mobile Phone (TEL CELL)
		t.Errorf("Mobile Phone = %q", row[14])
	}
}

// TestVCFExport checks the vCard text form.
func TestVCFExport(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	if _, ok := provider.ContactSave(resolved, sampleJCard, ""); !ok {
		t.Fatal("save failed")
	}

	data, ok := provider.Export(resolved, "vcf")
	if !ok {
		t.Fatal("Export(vcf) failed")
	}

	text := string(data)
	for _, want := range []string{"BEGIN:VCARD", "END:VCARD", "FN:Alice Example", "alice@example.com"} {
		if !strings.Contains(text, want) {
			t.Errorf("vcf is missing %q:\n%s", want, text)
		}
	}
}

// TestImportVCard checks the .vcf import round-trips through save.
func TestImportVCard(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	vcf := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Carol Smith\r\nN:Smith;Carol;;;\r\nEMAIL:carol@example.net\r\nEND:VCARD\r\n"
	if count := provider.ImportVcfFile(resolved, []byte(vcf)); 1 != count {
		t.Fatalf("ImportVcfFile = %d, want 1", count)
	}

	list, _ := provider.GetContacts(resolved, 0, 20, "")
	if 1 != len(list) {
		t.Fatalf("contacts = %d, want 1", len(list))
	}
	card, _ := parseJCard(list[0].(map[string]any)["jCard"])
	if "Carol Smith" != card.FN() {
		t.Errorf("FN = %q, want Carol Smith", card.FN())
	}
	if emails := card.values("email"); 1 != len(emails) || "carol@example.net" != emails[0] {
		t.Errorf("EMAIL = %v", emails)
	}
}

// TestImportCSV checks the CSV import maps common headers.
func TestImportCSV(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	csvData := "First Name;Last Name;E-mail Address\r\nDave;Jones;dave@example.net\r\n"
	if count := provider.ImportCsvFile(resolved, []byte(csvData), "First Name;Last Name;E-mail Address"); 1 != count {
		t.Fatalf("ImportCsvFile = %d, want 1", count)
	}

	list, _ := provider.GetContacts(resolved, 0, 20, "")
	if 1 != len(list) {
		t.Fatalf("contacts = %d, want 1", len(list))
	}
	card, _ := parseJCard(list[0].(map[string]any)["jCard"])
	parts := card.NParts()
	if "Dave" != parts[1] || "Jones" != parts[0] {
		t.Errorf("N = %v, want first Dave last Jones", parts)
	}
	if emails := card.values("email"); 1 != len(emails) || "dave@example.net" != emails[0] {
		t.Errorf("EMAIL = %v", emails)
	}
}

// TestUpdateContact checks that saving with a numeric uid updates the row
// instead of inserting a new one.
func TestUpdateContact(t *testing.T) {
	provider := newProvider(t)
	resolved := testAccount()

	id, ok := provider.ContactSave(resolved, sampleJCard, "")
	if !ok {
		t.Fatal("save failed")
	}

	updated := `["vcard",[["fn",{},"text","Alice Updated"],["email",{},"text","alice@example.com"]]]`
	newID, ok := provider.ContactSave(resolved, updated, id)
	if !ok || newID != id {
		t.Fatalf("update = %q/%v, want %q/true", newID, ok, id)
	}

	list, count := provider.GetContacts(resolved, 0, 20, "")
	if 1 != count || 1 != len(list) {
		t.Fatalf("after update count = %d, want 1", count)
	}
	card, _ := parseJCard(list[0].(map[string]any)["jCard"])
	if "Alice Updated" != card.FN() {
		t.Errorf("FN = %q, want Alice Updated", card.FN())
	}
}

// TestInactiveProvider checks that a disabled address book reports empty.
func TestInactiveProvider(t *testing.T) {
	provider := New(Options{Version: "0.0.0", Enabled: false, PrivateData: t.TempDir(), Global: true})
	if provider.IsActive(testAccount()) {
		t.Error("IsActive must be false when disabled")
	}
	if _, count := provider.GetContacts(testAccount(), 0, 20, ""); 0 != count {
		t.Errorf("count = %d, want 0", count)
	}
}

// TestDatabasePath checks the global vs per-account resolution.
func TestDatabasePath(t *testing.T) {
	dir := t.TempDir()
	global := New(Options{Enabled: true, PrivateData: dir, Global: true})
	if path, ok := global.databasePath(testAccount()); !ok || filepath.Join(dir, "AddressBook.sqlite") != path {
		t.Errorf("global path = %q, %v", path, ok)
	}

	local := New(Options{
		Enabled:     true,
		PrivateData: dir,
		RootPath:    func(*account.Account) string { return filepath.Join(dir, "user") },
	})
	if path, ok := local.databasePath(testAccount()); !ok ||
		filepath.Join(dir, "user", "AddressBook.sqlite") != path {
		t.Errorf("local path = %q, %v", path, ok)
	}
}
