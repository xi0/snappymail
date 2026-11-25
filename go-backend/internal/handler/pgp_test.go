package handler

// Tests of the Step 25 OpenPGP actions: the key escrow of
// SnappyMail\PGP\Backup (including the "still decryptable after a restart"
// property), the engine delegation of the DoGnupg* actions and the envelope
// shapes of the frontend contract.

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/pgp"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// testPGPSalt is APP_SALT of the PGP tests (any hex string will do).
const testPGPSalt = "0123456789abcdef0123456789abcdef"

// testPassphrase is the passphrase of the generated test key pair.
const testPassphrase = "s3cret"

// fakePGP is a handler.PGP with the real key escrow and a canned engine.
type fakePGP struct {
	store   *pgp.Store
	engine  pgp.Engine
	engineE error

	searchKey func(string) (string, error)
	findKeyID func(string) (string, error)

	stored  []string
	keyIDs  []string
	queries []string
}

// StoreKey records the call and stores the block.
func (f *fakePGP) StoreKey(key, keyID string) (bool, error) {
	f.stored = append(f.stored, key)
	f.keyIDs = append(f.keyIDs, keyID)
	if nil == f.store {
		return false, nil
	}
	return f.store.StoreKey(key, keyID)
}

// Keys is Backup::getKeys().
func (f *fakePGP) Keys() (pgp.StoredKeys, error) {
	if nil == f.store {
		return pgp.StoredKeys{Public: []pgp.StoredKey{}, Private: []pgp.StoredKey{}}, nil
	}
	return f.store.Keys()
}

// Engine is Actions\Pgp::GnuPG().
func (f *fakePGP) Engine() (pgp.Engine, error) { return f.engine, f.engineE }

// SearchKey is Keyservers::get().
func (f *fakePGP) SearchKey(query string) (string, error) {
	f.queries = append(f.queries, query)
	if nil != f.searchKey {
		return f.searchKey(query)
	}
	return "", nil
}

// FindKeyID is Keyservers::index()[0]['keyid'].
func (f *fakePGP) FindKeyID(query string) (string, error) {
	f.queries = append(f.queries, query)
	if nil != f.findKeyID {
		return f.findKeyID(query)
	}
	return "", nil
}

// pgpFixture builds the storage/crypt/engine stack of the PGP tests.
func pgpFixture(t *testing.T) (*fakePGP, pgp.Engine, *account.Context, *storage.Provider) {
	t.Helper()

	provider := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))
	context := newAccountContext(provider)

	engine, err := pgp.NewOpenPGP(t.TempDir())
	if nil != err {
		t.Fatalf("NewOpenPGP: %v", err)
	}

	return &fakePGP{store: pgp.NewStore(context, provider), engine: engine}, engine, context, provider
}

// newAccountContext builds the account context of a storage provider, the way
// main builds one per request (a fresh instance is a "restarted backend").
func newAccountContext(provider *storage.Provider) *account.Context {
	main := account.NewMainAccount()
	main.SetCredentials(nil, testEmail, testEmail, "secret", "", "")

	context := &account.Context{
		Salt:    testPGPSalt,
		Storage: provider,
		Crypt:   cryptox.New(testPGPSalt, "0.0.0", cryptox.DefaultCipher),
	}
	context.MainAccount = func() (*account.MainAccount, error) { return main, nil }

	return context
}

// pgpDispatcher builds a dispatcher wired to the given PGP provider.
func pgpDispatcher(session Session, provider PGP) *Dispatcher {
	return NewDispatcher(Options{
		Version: "0.0.0",
		Session: func(http.ResponseWriter, *http.Request) Session { return session },
		PGP:     func(Session) PGP { return provider },
	})
}

// pgpContext builds the per-request context of one PGP action.
func pgpContext(dispatcher *Dispatcher, params map[string]any) *Context {
	return &Context{dispatcher: dispatcher, params: params}
}

// generateKeyPair generates a key pair in the engine and returns its
// fingerprint and armor.
func generateKeyPair(t *testing.T, engine pgp.Engine) (string, string, string) {
	t.Helper()

	fingerprint, err := engine.GenerateKey("Test User <test@example.com>", testPassphrase)
	if nil != err {
		t.Fatalf("GenerateKey: %v", err)
	}

	publicKey, err := engine.Export(fingerprint, nil)
	if nil != err {
		t.Fatalf("Export (public): %v", err)
	}

	passphrase := testPassphrase
	privateKey, err := engine.Export(fingerprint, &passphrase)
	if nil != err {
		t.Fatalf("Export (private): %v", err)
	}

	return fingerprint, publicKey, privateKey
}

// TestPGPActionsRegistered checks that every action name of the PHP trait has a
// handler ("method_exists()" of ServiceJson()).
func TestPGPActionsRegistered(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})

	for _, action := range []string{
		"GetPGPKeys", "PgpSearchKey", "GnupgDecrypt", "GnupgGetKeys",
		"GnupgExportKey", "GnupgGenerateKey", "GnupgDeleteKey", "PgpImportKey",
		"GetStoredPGPKeys", "PgpStoreKeyPair", "StorePGPKey", "PgpVerifyMessage",
	} {
		if !dispatcher.registry.Has(action) {
			t.Errorf("action %q is not registered", action)
		}
	}
}

// TestPGPStoreSurvivesARestart is the acceptance check of the step: a key pair
// stored through Backup::PGPKey() is still decryptable by a *new* backend
// instance (a fresh crypt engine and account object over the same folder).
func TestPGPStoreSurvivesARestart(t *testing.T) {
	provider := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))

	first := pgp.NewStore(newAccountContext(provider), provider)

	engine, err := pgp.NewOpenPGP(t.TempDir())
	if nil != err {
		t.Fatalf("NewOpenPGP: %v", err)
	}
	_, publicKey, privateKey := generateKeyPair(t, engine)

	if ok, err := first.StoreKey(publicKey, ""); nil != err || !ok {
		t.Fatalf("StoreKey (public) = %v, %v", ok, err)
	}
	if ok, err := first.StoreKey(privateKey, ""); nil != err || !ok {
		t.Fatalf("StoreKey (private) = %v, %v", ok, err)
	}
	if ok, err := first.StoreKey("nothing useful", ""); nil != err || ok {
		t.Errorf("StoreKey (garbage) = %v, %v", ok, err)
	}

	// A second instance ("after a restart") reads and decrypts them again.
	second := pgp.NewStore(newAccountContext(provider), provider)

	keys, err := second.Keys()
	if nil != err {
		t.Fatalf("Keys: %v", err)
	}
	if 1 != len(keys.Public) {
		t.Fatalf("public keys = %+v", keys.Public)
	}
	if 1 != len(keys.Private) {
		t.Fatalf("private keys = %+v", keys.Private)
	}
	if publicKey != keys.Public[0].Value {
		t.Error("the public key was not stored verbatim")
	}
	if privateKey != keys.Private[0].Value {
		t.Error("the stored key pair does not decrypt to the original block")
	}
	if !strings.HasSuffix(keys.Private[0].ID, ".key") {
		t.Errorf("private id = %q", keys.Private[0].ID)
	}
	if !strings.HasSuffix(keys.Public[0].ID, "_public.asc") {
		t.Errorf("public id = %q", keys.Public[0].ID)
	}

	// An explicit key id is prefixed with 0x, like PHP.
	if ok, err := second.StoreKey(privateKey, "DEADBEEF"); nil != err || !ok {
		t.Fatalf("StoreKey (keyId) = %v, %v", ok, err)
	}
	keys, err = second.Keys()
	if nil != err {
		t.Fatalf("Keys: %v", err)
	}
	if 2 != len(keys.Private) {
		t.Fatalf("private keys = %+v", keys.Private)
	}
	found := false
	for _, key := range keys.Private {
		if "0xDEADBEEF.key" == key.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("the 0x prefixed file is missing: %+v", keys.Private)
	}
}

// TestPGPStoredKeyWithAnotherAccountKey checks that the escrow belongs to the
// account: a second account has its own crypt key, which cannot open the stored
// blob (CryptKeyError 111, the same failure PHP reports).
func TestPGPStoredKeyWithAnotherAccountKey(t *testing.T) {
	provider := storage.NewProvider(storage.NewFileStorage(t.TempDir(), false))

	writer := pgp.NewStore(newAccountContext(provider), provider)

	_, _, privateKey := generateKeyPair(t, mustEngine(t))
	if ok, err := writer.StoreKey(privateKey, ""); nil != err || !ok {
		t.Fatalf("StoreKey = %v, %v", ok, err)
	}

	// A second account (different password -> different crypt key).
	other := account.NewMainAccount()
	other.SetCredentials(nil, testEmail, testEmail, "another-secret", "", "")
	context := &account.Context{
		Salt:    testPGPSalt,
		Storage: provider,
		Crypt:   cryptox.New(testPGPSalt, "0.0.0", cryptox.DefaultCipher),
	}
	context.MainAccount = func() (*account.MainAccount, error) { return other, nil }

	if _, err := pgp.NewStore(context, provider).Keys(); nil == err {
		t.Error("a foreign crypt key must not open the escrow")
	}
}

// mustEngine builds a throwaway Go engine.
func mustEngine(t *testing.T) pgp.Engine {
	t.Helper()

	engine, err := pgp.NewOpenPGP(t.TempDir())
	if nil != err {
		t.Fatalf("NewOpenPGP: %v", err)
	}
	return engine
}

// TestGetPGPKeys checks the merged document: the escrowed keys plus the public
// exports of the engine, de-duplicated.
func TestGetPGPKeys(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	_, publicKey, privateKey := generateKeyPair(t, engine)

	if ok, err := provider.store.StoreKey(publicKey, ""); nil != err || !ok {
		t.Fatalf("StoreKey (public) = %v, %v", ok, err)
	}
	if ok, err := provider.store.StoreKey(privateKey, ""); nil != err || !ok {
		t.Fatalf("StoreKey (private) = %v, %v", ok, err)
	}

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)
	response := mustResponse(doGetPGPKeys(pgpContext(dispatcher, map[string]any{})))
	if nil == response {
		t.Fatal("doGetPGPKeys failed")
	}

	list, ok := response["Result"].([]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	// public + private from the escrow, plus the engine export of the public key
	// (which de-duplicates against the escrowed public block).
	if 2 != len(list) {
		t.Fatalf("Result = %d entries: %#v", len(list), list)
	}
	if publicKey != list[0] || privateKey != list[1] {
		t.Errorf("Result order = %#v", list)
	}
}

// TestGetPGPKeysWithoutProvider checks the empty document of a request without
// the PGP wiring.
func TestGetPGPKeysWithoutProvider(t *testing.T) {
	dispatcher := NewDispatcher(Options{Version: "0.0.0"})
	response := mustResponse(doGetPGPKeys(pgpContext(dispatcher, map[string]any{})))
	if nil == response {
		t.Fatal("doGetPGPKeys failed")
	}
	list, ok := response["Result"].([]any)
	if !ok || 0 != len(list) {
		t.Errorf("Result = %#v", response["Result"])
	}
}

// TestGetStoredPGPKeys checks the Backup::getKeys() document.
func TestGetStoredPGPKeys(t *testing.T) {
	provider, _, _, _ := pgpFixture(t)

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)
	response := mustResponse(doGetStoredPGPKeys(pgpContext(dispatcher, map[string]any{})))
	if nil == response {
		t.Fatal("doGetStoredPGPKeys failed")
	}

	// The document is the StoredKeys value; json.Marshal() renders the two
	// "public"/"private" arrays the frontend reads.
	document, ok := response["Result"].(pgp.StoredKeys)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if nil == document.Public || nil == document.Private {
		t.Errorf("Result = %+v", document)
	}

	// Without a provider the two lists are empty as well.
	empty := NewDispatcher(Options{Version: "0.0.0"})
	response = mustResponse(doGetStoredPGPKeys(pgpContext(empty, map[string]any{})))
	document, _ = response["Result"].(pgp.StoredKeys)
	if nil == document.Public || nil == document.Private {
		t.Errorf("Result = %+v", document)
	}
}

// TestPgpStoreKeyPair checks the bitmask of DoPgpStoreKeyPair.
func TestPgpStoreKeyPair(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	_, publicKey, privateKey := generateKeyPair(t, engine)

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	// onServer = 1 (public only) and inGnuPG = 3 (both).
	response := mustResponse(doPgpStoreKeyPair(pgpContext(dispatcher, map[string]any{
		"publicKey":  publicKey,
		"privateKey": privateKey,
		"onServer":   1,
		"inGnuPG":    3,
	})))
	if nil == response {
		t.Fatal("doPgpStoreKeyPair failed")
	}

	document, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	onServer, _ := document["onServer"].([]any)
	inGnuPG, _ := document["inGnuPG"].([]any)
	if 2 != len(onServer) || 2 != len(inGnuPG) {
		t.Fatalf("Result = %#v", document)
	}
	if true != onServer[0] || false != onServer[1] {
		t.Errorf("onServer = %#v", onServer)
	}
	for index := range []int{0, 1} {
		// The import result is the IMPORT_RES document of the engine.
		if _, ok := inGnuPG[index].(pgp.ImportStats); !ok {
			t.Fatalf("inGnuPG[%d] = %#v", index, inGnuPG[index])
		}
	}

	// Only the exported block was handed to the escrow.
	if 1 != len(provider.stored) {
		t.Errorf("stored = %d blocks", len(provider.stored))
	}
}

// TestStorePGPKey checks the keyId branch of DoStorePGPKey.
func TestStorePGPKey(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	_, publicKey, _ := generateKeyPair(t, engine)

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	if response := mustResponse(doStorePGPKey(pgpContext(dispatcher, map[string]any{
		"key":   publicKey,
		"keyId": "0123456789ABCDEF",
	}))); nil == response || true != response["Result"] {
		t.Fatalf("Result = %#v", response)
	}

	if response := mustResponse(doStorePGPKey(pgpContext(dispatcher, map[string]any{
		"key": publicKey,
	}))); nil == response || false != response["Result"] {
		t.Fatalf("Result without keyId = %#v", response)
	}

	if 1 != len(provider.keyIDs) || "0123456789ABCDEF" != provider.keyIDs[0] {
		t.Errorf("stored key ids = %#v", provider.keyIDs)
	}
}

// TestGnupgActionsWithoutEngine checks the "if (!$GPG) return FalseResponse()"
// branches.
func TestGnupgActionsWithoutEngine(t *testing.T) {
	provider, _, _, _ := pgpFixture(t)
	provider.engine = nil

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	actions := []struct {
		name   string
		action ActionFunc
		params map[string]any
	}{
		{"GnupgDecrypt", doGnupgDecrypt, map[string]any{"data": "-----BEGIN PGP MESSAGE-----"}},
		{"GnupgGetKeys", doGnupgGetKeys, map[string]any{}},
		{"GnupgExportKey", doGnupgExportKey, map[string]any{"keyId": "x"}},
		{"GnupgGenerateKey", doGnupgGenerateKey, map[string]any{"email": "x@example.com"}},
		{"GnupgDeleteKey", doGnupgDeleteKey, map[string]any{"keyId": "x"}},
	}

	for _, test := range actions {
		response := mustResponse(test.action(pgpContext(dispatcher, test.params)))
		if nil == response {
			t.Errorf("%s failed", test.name)
			continue
		}
		if false != response["Result"] {
			t.Errorf("%s Result = %#v, want false", test.name, response["Result"])
		}
	}
}

// TestGnupgGetKeys checks the allKeysInfo document of a wired engine.
func TestGnupgGetKeys(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	generateKeyPair(t, engine)

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)
	response := mustResponse(doGnupgGetKeys(pgpContext(dispatcher, map[string]any{})))
	if nil == response {
		t.Fatal("doGnupgGetKeys failed")
	}

	document, ok := response["Result"].(pgp.KeySet)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if 1 != len(document.Public) {
		t.Fatalf("public = %+v", document.Public)
	}
	if !document.Public[0].CanVerify || document.Public[0].CanSign {
		t.Errorf("can_verify = %+v", document.Public[0])
	}
	if 0 == len(document.Public[0].Subkeys) || "" == document.Public[0].Subkeys[0].Fingerprint {
		t.Errorf("subkeys = %+v", document.Public[0].Subkeys)
	}
}

// TestGnupgGenerateDeleteExport checks the engine round trip through the
// actions.
func TestGnupgGenerateDeleteExport(t *testing.T) {
	provider, _, _, _ := pgpFixture(t)
	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	generated := mustResponse(doGnupgGenerateKey(pgpContext(dispatcher, map[string]any{
		"name":       "Test User",
		"email":      "test@example.com",
		"passphrase": testPassphrase,
	})))
	if nil == generated {
		t.Fatal("doGnupgGenerateKey failed")
	}
	fingerprint, ok := generated["Result"].(string)
	if !ok || "" == fingerprint {
		t.Fatalf("Result = %#v", generated["Result"])
	}

	exported := mustResponse(doGnupgExportKey(pgpContext(dispatcher, map[string]any{
		"keyId": fingerprint,
	})))
	if nil == exported {
		t.Fatal("doGnupgExportKey failed")
	}
	publicKey, _ := exported["Result"].(string)
	if !strings.Contains(publicKey, "PGP PUBLIC KEY BLOCK") {
		t.Fatalf("Result = %#v", exported["Result"])
	}

	privateExport := mustResponse(doGnupgExportKey(pgpContext(dispatcher, map[string]any{
		"keyId":      fingerprint,
		"isPrivate":  true,
		"passphrase": testPassphrase,
	})))
	if nil == privateExport {
		t.Fatal("doGnupgExportKey (private) failed")
	}
	if block, _ := privateExport["Result"].(string); !strings.Contains(block, "PGP PRIVATE KEY BLOCK") {
		t.Fatalf("private Result = %#v", privateExport["Result"])
	}

	deleted := mustResponse(doGnupgDeleteKey(pgpContext(dispatcher, map[string]any{
		"keyId": fingerprint,
	})))
	if nil == deleted {
		t.Fatal("doGnupgDeleteKey failed")
	}
	if true != deleted["Result"] {
		t.Errorf("delete Result = %#v", deleted["Result"])
	}
}

// TestPgpImportKey checks the inline key branch, the key server branch and the
// empty document of a request without any key.
func TestPgpImportKey(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	_, publicKey, _ := generateKeyPair(t, engine)

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	// An inline key: stored in the escrow and imported into the engine.
	response := mustResponse(doPgpImportKey(pgpContext(dispatcher, map[string]any{
		"key":    publicKey,
		"backup": 1,
		"gnuPG":  1,
	})))
	if nil == response {
		t.Fatal("doPgpImportKey failed")
	}
	document, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if true != document["backup"] {
		t.Errorf("backup = %#v", document["backup"])
	}
	if true != document["gnuPG"] {
		t.Errorf("gnuPG = %#v", document["gnuPG"])
	}

	// No key at all: PHP answers array().
	empty := mustResponse(doPgpImportKey(pgpContext(dispatcher, map[string]any{})))
	if nil == empty {
		t.Fatal("doPgpImportKey failed")
	}
	if list, ok := empty["Result"].([]any); !ok || 0 != len(list) {
		t.Errorf("Result = %#v, want []", empty["Result"])
	}

	// The key server branch: the e-mail address is reduced to the address and
	// the found key id is looked up.
	provider.findKeyID = func(search string) (string, error) {
		if "john@example.com" != search {
			t.Errorf("index search = %q", search)
		}
		return "0123456789ABCDEF", nil
	}
	provider.searchKey = func(keyID string) (string, error) {
		if "0123456789ABCDEF" != keyID {
			t.Errorf("get search = %q", keyID)
		}
		return publicKey, nil
	}

	fetched := mustResponse(doPgpImportKey(pgpContext(dispatcher, map[string]any{
		"email": "John Doe <john@example.com>",
	})))
	if nil == fetched {
		t.Fatal("doPgpImportKey (keyserver) failed")
	}
	if result, ok := fetched["Result"].(map[string]any); !ok || false != result["backup"] {
		t.Errorf("Result = %#v", fetched["Result"])
	}
}

// TestPgpSearchKey checks the key server passthrough.
func TestPgpSearchKey(t *testing.T) {
	provider, _, _, _ := pgpFixture(t)
	provider.searchKey = func(string) (string, error) {
		return "-----BEGIN PGP PUBLIC KEY BLOCK-----", nil
	}

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)
	response := mustResponse(doPgpSearchKey(pgpContext(dispatcher, map[string]any{
		"query": "0x0123456789ABCDEF",
	})))
	if nil == response {
		t.Fatal("doPgpSearchKey failed")
	}
	if "-----BEGIN PGP PUBLIC KEY BLOCK-----" != response["Result"] {
		t.Errorf("Result = %#v", response["Result"])
	}

	// No answer at all: false.
	provider.searchKey = nil
	empty := mustResponse(doPgpSearchKey(pgpContext(dispatcher, map[string]any{"query": "x"})))
	if nil == empty {
		t.Fatal("doPgpSearchKey failed")
	}
	if false != empty["Result"] {
		t.Errorf("Result = %#v, want false", empty["Result"])
	}

	// An error of the key servers becomes the error envelope (999).
	provider.searchKey = func(string) (string, error) {
		return "", errors.New("Could not obtain public key from the keyservers.")
	}
	if _, err := doPgpSearchKey(pgpContext(dispatcher, map[string]any{"query": "x"})); nil == err {
		t.Error("a key server failure must be reported")
	}
}

// TestPgpVerifyMessage checks the bodyPart branch of DoPgpVerifyMessage.
func TestPgpVerifyMessage(t *testing.T) {
	provider, engine, _, _ := pgpFixture(t)
	fingerprint, _, _ := generateKeyPair(t, engine)

	signed, err := engine.Sign("Signed text\r\n")
	if nil != err {
		t.Fatalf("Sign: %v", err)
	}

	dispatcher := pgpDispatcher(&folderSession{account: folderAccount()}, provider)

	response := mustResponse(doPgpVerifyMessage(pgpContext(dispatcher, map[string]any{
		"bodyPart": signed,
	})))
	if nil == response {
		t.Fatal("doPgpVerifyMessage failed")
	}

	document, ok := response["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", response["Result"])
	}
	if fingerprint != document["fingerprint"] {
		t.Errorf("fingerprint = %#v", document["fingerprint"])
	}
	if 0 != document["status"] || 0 != document["summary"] || 0 != document["validity"] {
		t.Errorf("status document = %#v", document)
	}
	if "" != document["message"] {
		t.Errorf("message = %#v", document["message"])
	}

	// tryGnuPG = 0: the extracted text and the signature are returned instead.
	raw := mustResponse(doPgpVerifyMessage(pgpContext(dispatcher, map[string]any{
		"bodyPart":  "line1\nline2",
		"sigPart":   "sig",
		"tryGnuPG":  0,
	})))
	if nil == raw {
		t.Fatal("doPgpVerifyMessage failed")
	}
	parts, ok := raw["Result"].(map[string]any)
	if !ok {
		t.Fatalf("Result = %#v", raw["Result"])
	}
	if "line1\r\nline2" != parts["text"] {
		t.Errorf("text = %#v", parts["text"])
	}
	if "sig" != parts["signature"] {
		t.Errorf("signature = %#v", parts["signature"])
	}

	// Without an engine the verification answers false.
	provider.engine = nil
	none := mustResponse(doPgpVerifyMessage(pgpContext(dispatcher, map[string]any{
		"bodyPart": signed,
	})))
	if nil == none {
		t.Fatal("doPgpVerifyMessage failed")
	}
	if false != none["Result"] {
		t.Errorf("Result = %#v, want false", none["Result"])
	}
}

// TestPgpCapabilities checks the two capabilities of Actions::GetCapa().
func TestPgpCapabilities(t *testing.T) {
	ctx := pgpContext(NewDispatcher(Options{Version: "0.0.0"}), map[string]any{})

	// The defaults of application.ini enable both.
	if !ctx.hasCapa("OpenPGP") {
		t.Error("OpenPGP must be enabled by default")
	}
	if !ctx.hasCapa("GnuPG") {
		t.Error("GnuPG must be enabled by default")
	}
}
