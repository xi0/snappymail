package handler

// Actions::decodeRawKey(): the RawKey the GET endpoints carry is an unsigned,
// client controlled url-safe base64 JSON blob. Whenever it addresses account
// data the embedded account hash must match the current account, otherwise
// arbitrary folder/uid/file identifiers would be trusted.

import (
	"encoding/json"
	"strconv"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
)

// decodeRawKey is Actions::decodeRawKey(): the decoded values, keyed by their
// JSON name so an indexed array ("[folder, uid, useThreads, accountHash]")
// becomes "0", "1", …. It returns nil when the blob is not a JSON array/object
// or when accountHash is set and the blob does not carry it.
//
// An empty accountHash skips the validation, like PHP's $oAccount = null.
func decodeRawKey(rawKey, accountHash string) map[string]any {
	if "" == rawKey {
		return nil
	}

	decoded, err := cryptox.URLSafeBase64Decode(rawKey)
	if nil != err {
		return nil
	}

	var parsed any
	if nil != json.Unmarshal(decoded, &parsed) {
		return nil
	}

	values := map[string]any{}
	switch typed := parsed.(type) {
	case map[string]any:
		for key, value := range typed {
			values[key] = value
		}
	case []any:
		for index, value := range typed {
			values[strconv.Itoa(index)] = value
		}
	default:
		return nil
	}

	if "" != accountHash {
		// Some clients send an indexed array with the account hash at index 3
		// e.g. DoMessage: [folder, uid, useThreads, accountHash].
		hash, _ := values["accountHash"].(string)
		if "" == hash {
			hash, _ = values["3"].(string)
		}
		if "" == hash || hash != accountHash {
			return nil
		}
	}

	return values
}
