package service

// The production implementations of the step 25 handler surfaces:
// Actions\Pgp::GnuPG() (the per-account gnupg home directory and its engine),
// the key escrow of SnappyMail\PGP\Backup and the key server lookups of
// SnappyMail\PGP\Keyservers.
//
// main wires NewPGP() into handler.Options.PGP for every request, so the
// DoPgp*/DoGnupg* actions see exactly the account the cookie stack resolved.

import (
	"strings"

	"github.com/xi0/snappymail/go-backend/internal/account"
	"github.com/xi0/snappymail/go-backend/internal/auth"
	"github.com/xi0/snappymail/go-backend/internal/pgp"
	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// gnupgFolder is the ROOT subfolder Actions\Pgp::GnuPG() uses.
const gnupgFolder = ".gnupg"

// PGPOptions is the state of one request's PGP surface.
type PGPOptions struct {
	// Session resolves the main account (getMainAccountFromToken).
	Session *auth.Authenticator
	// Storage is Actions::StorageProvider(): the ROOT folder of the account.
	Storage *storage.Provider
	// Version is APP_VERSION (the keyserver user agent).
	Version string
}

// PGP is the handler.PGP implementation.
type PGP struct {
	options PGPOptions
	store   *pgp.Store
	servers *pgp.Keyservers
}

// NewPGP builds the PGP surface of one request.
func NewPGP(options PGPOptions) *PGP {
	servers := pgp.NewKeyservers(options.Version)

	var store *pgp.Store
	if nil != options.Session && nil != options.Session.Context {
		store = pgp.NewStore(options.Session.Context, options.Storage)
	}

	return &PGP{options: options, store: store, servers: servers}
}

// StoreKey is Backup::PGPKey().
func (p *PGP) StoreKey(key, keyID string) (bool, error) {
	if nil == p || nil == p.store {
		return false, nil
	}
	return p.store.StoreKey(key, keyID)
}

// Keys is Backup::getKeys().
func (p *PGP) Keys() (pgp.StoredKeys, error) {
	empty := pgp.StoredKeys{Public: []pgp.StoredKey{}, Private: []pgp.StoredKey{}}
	if nil == p || nil == p.store {
		return empty, nil
	}
	return p.store.Keys()
}

// Engine is Actions\Pgp::GnuPG(): the engine of the account's gnupg home
// directory, or nil when the request has no main account.
//
// PHP creates the folder with MailSo\Base\Utils::mkdir() and reports a home
// directory that is not writable as a thrown exception, which the JSON layer
// turns into UnknownError; pgp.GetInstance() does the same.
func (p *PGP) Engine() (pgp.Engine, error) {
	if nil == p || nil == p.options.Session {
		return nil, nil
	}

	main, err := p.options.Session.GetMainAccountFromToken(true)
	if nil != err {
		return nil, err
	}
	if nil == main {
		return nil, nil
	}

	return p.engineFor(main)
}

// engineFor builds the engine of a main account.
func (p *PGP) engineFor(main *account.MainAccount) (pgp.Engine, error) {
	if nil == p.options.Storage || nil == main {
		return nil, nil
	}

	base := p.options.Storage.GenerateFilePath(main, storage.TypeRoot, true)
	if "" == base {
		return nil, nil
	}

	homedir := strings.TrimRight(base, `/\`) + "/" + gnupgFolder

	return pgp.GetInstance(homedir)
}

// SearchKey is Keyservers::get().
func (p *PGP) SearchKey(keyID string) (string, error) {
	if nil == p || nil == p.servers {
		return "", nil
	}
	return p.servers.Get(keyID)
}

// FindKeyID is Keyservers::index($search)[0]['keyid'].
func (p *PGP) FindKeyID(search string) (string, error) {
	if nil == p || nil == p.servers {
		return "", nil
	}

	keys, err := p.servers.Index(search, true, false)
	if nil != err {
		return "", err
	}
	if 0 == len(keys) {
		return "", nil
	}

	return keys[0].KeyID, nil
}
