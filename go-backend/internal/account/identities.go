package account

// The identity provider of SnappyMail: RainLoop\Providers\Identities with the
// FileIdentities driver, plus the sorted list of Actions\Accounts::GetIdentities().
//
// Only the file driver is ported (the PHP build ships nothing else either): the
// documents live as a JSON list under the CONFIG key "identities" of the *local*
// storage provider, so an additional account keeps its identities in its own
// subfolder and the PHP backend reads the same file.
//
// The list order is stored separately under "identities_order" (also the local
// provider); older installations kept it as "accounts_identities_order" in the
// main storage, which is migrated on first read exactly like PHP.

import (
	"encoding/json"
	"sort"

	"github.com/xi0/snappymail/go-backend/internal/storage"
)

// The CONFIG keys of the identity documents and their order.
const (
	// identitiesKey is the CONFIG key of the stored identity list.
	identitiesKey = "identities"
	// identitiesOrderKey is the CONFIG key of the sort order.
	identitiesOrderKey = "identities_order"
	// legacyIdentitiesOrderKey is the old CONFIG key of the sort order.
	legacyIdentitiesOrderKey = "accounts_identities_order"
)

// Identities is RainLoop\Providers\Identities.
//
// The PHP provider wraps one or more drivers; the Go port has the single file
// driver, so the merge logic of MergeIdentitiesPerDriver() collapses to the
// documents of that driver.
type Identities struct {
	// localStorage is Actions::LocalStorageProvider(): the CONFIG folder that
	// keeps an additional account in its own subfolder.
	localStorage Storage
	// mainStorage is Actions::StorageProvider(): only read to migrate the legacy
	// identities order key.
	mainStorage Storage
	// popupIdentity is webmail.popup_identity: a synthesised primary identity is
	// reported as "not existing" when it is on, so the frontend opens the editor.
	popupIdentity bool
}

// NewIdentities builds the identity provider.
func NewIdentities(localStorage, mainStorage Storage, popupIdentity bool) *Identities {
	return &Identities{
		localStorage:  localStorage,
		mainStorage:   mainStorage,
		popupIdentity: popupIdentity,
	}
}

// Name is FileIdentities::Name().
func (p *Identities) Name() string { return "File" }

// GetIdentities is Providers\Identities::GetIdentities().
//
// The primary identity (the empty id) always mirrors the account address, and
// when no primary identity is stored a default one is generated. With
// allowMultiple false only the primary identity is returned, which is what
// Capa::IDENTITIES off does in PHP.
func (p *Identities) GetIdentities(acct *Account, allowMultiple bool) []*Identity {
	identities := p.load(acct)

	var primary *Identity
	for _, identity := range identities {
		if identity.IsAccountIdentities() {
			primary = identity
			break
		}
	}

	if nil == primary {
		primary = NewIdentity("", emailOf(acct))
		primary.SetExists(!p.popupIdentity)
		identities = append(identities, primary)
	}

	if !allowMultiple {
		return []*Identity{primary}
	}
	return identities
}

// GetSortedIdentities is Actions\Accounts::GetIdentities(): the provider list
// ordered by the stored identities_order document.
func (p *Identities) GetSortedIdentities(acct *Account, allowMultiple bool) []*Identity {
	identities := p.GetIdentities(acct, allowMultiple)

	orderString, ok := p.get(acct, p.localStorage, identitiesOrderKey)
	legacy := false
	if !ok || "" == orderString {
		orderString, ok = p.get(acct, p.mainStorage, legacyIdentitiesOrderKey)
		legacy = ok && "" != orderString
	}

	order := decodeIdentitiesOrder(orderString)

	if 1 < len(order) {
		// PHP maps the empty id onto "---" before comparing.
		list := make([]string, len(order))
		for index, id := range order {
			if "" == id {
				list[index] = "---"
			} else {
				list[index] = id
			}
		}

		position := make(map[string]int, len(list))
		for index, id := range list {
			if _, exists := position[id]; !exists {
				position[id] = index
			}
		}

		// PHP's usort comparator is internally inconsistent for two found
		// indices > 0; a stable ascending sort by the stored index reproduces the
		// intended order deterministically instead.
		sort.SliceStable(identities, func(first, second int) bool {
			return orderPosition(position, identities[first], len(list)) <
				orderPosition(position, identities[second], len(list))
		})
	}

	if legacy {
		ordered := make([]string, 0, len(order))
		ordered = append(ordered, order...)
		_ = p.put(acct, p.localStorage, identitiesOrderKey, encodeIdentitiesOrder(ordered))
		_ = p.clear(acct, p.mainStorage, legacyIdentitiesOrderKey)
	}

	return identities
}

// orderPosition is array_search($identity->Id(true), $list): the stored index, or
// the end of the list for an identity that is not part of the order document.
func orderPosition(position map[string]int, identity *Identity, missing int) int {
	if index, ok := position[identity.Id(true)]; ok {
		return index
	}
	return missing
}

// UpdateIdentity is Providers\Identities::UpdateIdentity().
func (p *Identities) UpdateIdentity(acct *Account, identity *Identity) {
	identities := p.load(acct)

	replaced := false
	for index, existing := range identities {
		if existing.Id(true) == identity.Id(true) {
			identities[index] = identity
			replaced = true
			break
		}
	}
	if !replaced {
		identities = append(identities, identity)
	}

	p.store(acct, identities)
}

// DeleteIdentity is Providers\Identities::DeleteIdentity(): the primary identity
// can never be removed.
func (p *Identities) DeleteIdentity(acct *Account, id string) {
	identities := p.load(acct)

	result := make([]*Identity, 0, len(identities))
	for _, identity := range identities {
		if identity.Id() == id && !identity.IsAccountIdentities() {
			continue
		}
		result = append(result, identity)
	}

	if len(result) != len(identities) {
		p.store(acct, result)
	}
}

// SaveOrder is the LocalStorageProvider()->Put() of
// Actions\Accounts::DoAccountsAndIdentitiesSortOrder().
func (p *Identities) SaveOrder(acct *Account, ids []string) bool {
	return nil == p.put(acct, p.localStorage, identitiesOrderKey, encodeIdentitiesOrder(ids))
}

// load is FileIdentities::GetIdentities(): the stored documents, validated and
// with the primary identity pinned to the account address.
func (p *Identities) load(acct *Account) []*Identity {
	content, ok := p.get(acct, p.localStorage, identitiesKey)
	if !ok || "" == content {
		return []*Identity{}
	}

	var documents []map[string]any
	if err := json.Unmarshal([]byte(content), &documents); nil != err {
		return []*Identity{}
	}

	result := make([]*Identity, 0, len(documents))
	position := make(map[string]int, len(documents))
	for _, document := range documents {
		identity := NewIdentity("", "")
		if !identity.FromJSON(document, false) {
			continue
		}
		if !identity.Validate() {
			continue
		}
		if identity.IsAccountIdentities() {
			identity.SetEmail(emailOf(acct))
		}

		// The PHP driver keys the identities by Id(true), so a duplicate keeps
		// the position of the first one and the value of the last one.
		if index, exists := position[identity.Id(true)]; exists {
			result[index] = identity
			continue
		}
		position[identity.Id(true)] = len(result)
		result = append(result, identity)
	}

	return result
}

// store is FileIdentities::SetIdentities(): the documents are written as the
// json_encode() list of ToSimpleJSON() documents.
func (p *Identities) store(acct *Account, identities []*Identity) {
	documents := make([]IdentityDocument, 0, len(identities))
	for _, identity := range identities {
		documents = append(documents, identity.ToSimpleJSON())
	}

	encoded, err := json.Marshal(documents)
	if nil != err {
		return
	}

	_ = p.put(acct, p.localStorage, identitiesKey, string(encoded))
}

// get is Provider->Get() on the given storage driver.
func (p *Identities) get(acct *Account, driver Storage, key string) (string, bool) {
	if nil == p || nil == driver || nil == acct {
		return "", false
	}
	return driver.Get(acct, storage.TypeConfig, key)
}

// put is Provider->Put().
func (p *Identities) put(acct *Account, driver Storage, key, value string) error {
	if nil == p || nil == driver || nil == acct {
		return nil
	}
	return driver.Put(acct, storage.TypeConfig, key, value)
}

// clear is Provider->Clear().
func (p *Identities) clear(acct *Account, driver Storage, key string) bool {
	if nil == p || nil == driver || nil == acct {
		return false
	}
	return driver.Clear(acct, storage.TypeConfig, key)
}

// emailOf is $oAccount->Email() with the "" fallback of a nil account.
func emailOf(acct *Account) string {
	if nil == acct {
		return ""
	}
	return acct.Email()
}

// decodeIdentitiesOrder is json_decode($orderString, true)['Identities'] ?? [].
func decodeIdentitiesOrder(content string) []string {
	if "" == content {
		return nil
	}

	var document struct {
		Identities []any `json:"Identities"`
	}
	if err := json.Unmarshal([]byte(content), &document); nil != err {
		return nil
	}

	result := make([]string, 0, len(document.Identities))
	for _, item := range document.Identities {
		result = append(result, phpString(item))
	}
	return result
}

// encodeIdentitiesOrder is json_encode(['Identities' => $list]).
func encodeIdentitiesOrder(ids []string) string {
	if nil == ids {
		ids = []string{}
	}
	encoded, err := json.Marshal(struct {
		Identities []string `json:"Identities"`
	}{Identities: ids})
	if nil != err {
		return `{"Identities":[]}`
	}
	return string(encoded)
}
