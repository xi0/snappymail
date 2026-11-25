package pgp

// The HKP key server client of SnappyMail\PGP\Keyservers (Step 25).
//
// DoPgpSearchKey and DoPgpImportKey look public keys up on the hosts of
// Keyservers::$hosts (https://keys.openpgp.org for now). The request layout is
// the draft-shaw-openpgp-hkp-00 one PHP builds:
//
//	GET {host}/pks/lookup?op={get|index}&options=mr{&fingerprint=on}{&exact=on}&search={query}
//
// and the "machine readable" index answer is the colon separated
// pub/uid block list parsed by Keyservers::index().

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/xi0/snappymail/go-backend/internal/httpclient"
)

// defaultKeyServerHosts is Keyservers::$hosts.
var defaultKeyServerHosts = []string{"https://keys.openpgp.org"}

// keyServerTimeout is Keyservers::HTTP()->timeout (15 seconds).
const keyServerTimeout = 15 * time.Second

// KeyServerKey is one entry of Keyservers::index().
type KeyServerKey struct {
	KeyID          string         `json:"keyid"`
	Host           string         `json:"host"`
	Algo           int            `json:"algo"`
	KeyLen         int            `json:"keylen"`
	CreationDate   *int           `json:"creationdate"`
	ExpirationDate *int           `json:"expirationdate"`
	UIDs           []KeyServerUID `json:"uids"`
}

// KeyServerUID is one "uid:" line of the index answer.
type KeyServerUID struct {
	UID            string `json:"uid"`
	CreationDate   *int   `json:"creationdate"`
	ExpirationDate *int   `json:"expirationdate"`
}

// Keyservers is the HKP client. The zero value uses the default host, a 15
// second timeout and no response cap (Keyservers::HTTP()->max_response_kb = 0).
type Keyservers struct {
	// Hosts is the host list, in try order.
	Hosts []string
	// Client is the HTTP client; nil builds the PHP defaults.
	Client *httpclient.Client
	// Version is APP_VERSION for the user agent.
	Version string
}

// NewKeyservers builds the client of one request.
func NewKeyservers(version string) *Keyservers {
	return &Keyservers{Version: version}
}

// hosts is the configured host list, or the default one.
func (k *Keyservers) hosts() []string {
	if nil == k || 0 == len(k.Hosts) {
		return defaultKeyServerHosts
	}
	return k.Hosts
}

// client builds the HTTP client of Keyservers::HTTP().
func (k *Keyservers) client() *httpclient.Client {
	if nil != k && nil != k.Client {
		return k.Client
	}

	version := ""
	if nil != k {
		version = k.Version
	}

	client := httpclient.New(version)
	client.Timeout = keyServerTimeout
	// Keyservers::HTTP()->max_response_kb = 0 reads the whole response.
	client.MaxResponseKB = 0

	return client
}

// fetch is Keyservers::fetch(): one HKP lookup against one host.
func (k *Keyservers) fetch(host, operation, search string, fingerprint, exact bool) (*httpclient.Response, error) {
	host = strings.ReplaceAll(host, "hkp://", "http://")
	host = strings.ReplaceAll(host, "hkps://", "https://")

	flags := ""
	if fingerprint {
		flags += "&fingerprint=on"
	}
	if exact {
		flags += "&exact=on"
	}

	uri := host + "/pks/lookup?op=" + operation + "&options=mr" + flags +
		"&search=" + url.QueryEscape(search)

	return k.client().Do("GET", uri, nil, nil)
}

// Get is Keyservers::get(): the armored key block of keyID.
//
// PHP throws "Could not obtain public key from the keyservers." when no host
// answers with a 200; the DoPgpSearchKey action reports that as UnknownError.
func (k *Keyservers) Get(keyID string) (string, error) {
	for _, host := range k.hosts() {
		response, err := k.fetch(host, "get", keyID, false, false)
		if nil != err || nil == response {
			continue
		}
		if 200 != response.Status {
			continue
		}
		return string(response.Body), nil
	}

	return "", errors.New("Could not obtain public key from the keyservers.")
}

// Index is Keyservers::index(): the "machine readable" key list.
func (k *Keyservers) Index(search string, fingerprint, exact bool) ([]KeyServerKey, error) {
	for _, host := range k.hosts() {
		response, err := k.fetch(host, "index", search, fingerprint, exact)
		if nil != err || nil == response {
			continue
		}
		if 200 != response.Status {
			continue
		}
		return parseKeyServerIndex(string(response.Body), host), nil
	}

	return nil, errors.New("Could not obtain public key from the keyservers")
}

// parseKeyServerIndex is the body parser of Keyservers::index():
//
//	pub:<keyid>:<algo>:<keylen>:<creationdate>:<expirationdate>:<flags>
//	uid:<escaped uid string>:<creationdate>:<expirationdate>:<flags>
//
// Flagged or expired entries are skipped, exactly like the PHP loop.
func parseKeyServerIndex(body, host string) []KeyServerKey {
	keys := []KeyServerKey{}
	var current *KeyServerKey

	now := time.Now().Unix()

	for _, line := range strings.Split(body, "\n") {
		fields := strings.Split(line, ":")
		if 0 == len(fields) {
			continue
		}

		switch fields[0] {
		case "pub":
			if nil != current {
				keys = append(keys, *current)
				current = nil
			}
			if 7 != len(fields) {
				continue
			}
			// Ignore flagged or expired keys.
			if "" != fields[6] {
				continue
			}
			if "" != fields[5] && int64(leadingNumber(fields[5])) <= now {
				continue
			}
			current = &KeyServerKey{
				KeyID:          fields[1],
				Host:           host,
				Algo:           leadingNumber(fields[2]),
				KeyLen:         leadingNumber(fields[3]),
				CreationDate:   numberOrNil(fields[4]),
				ExpirationDate: numberOrNil(fields[5]),
				UIDs:           []KeyServerUID{},
			}

		case "uid":
			if nil == current || 5 != len(fields) {
				continue
			}
			if "" != fields[4] {
				continue
			}
			if "" != fields[3] && int64(leadingNumber(fields[3])) <= now {
				continue
			}
			decoded, err := url.QueryUnescape(fields[1])
			if nil != err {
				decoded = fields[1]
			}
			current.UIDs = append(current.UIDs, KeyServerUID{
				UID:            decoded,
				CreationDate:   numberOrNil(fields[2]),
				ExpirationDate: numberOrNil(fields[3]),
			})
		}
	}

	if nil != current {
		keys = append(keys, *current)
	}

	return keys
}

// numberOrNil is "strlen($value) ? intval($value) : null".
func numberOrNil(value string) *int {
	if "" == value {
		return nil
	}
	number := leadingNumber(value)
	return &number
}

// leadingNumber is PHP's intval() for a string.
func leadingNumber(value string) int {
	value = strings.TrimSpace(value)

	sign := 1
	if 0 < len(value) && ('+' == value[0] || '-' == value[0]) {
		if '-' == value[0] {
			sign = -1
		}
		value = value[1:]
	}

	end := 0
	for end < len(value) && '0' <= value[end] && '9' >= value[end] {
		end++
	}
	if 0 == end {
		return 0
	}

	number := 0
	for _, character := range value[:end] {
		number = number*10 + int(character-'0')
	}
	return sign * number
}
