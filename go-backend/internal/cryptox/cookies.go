package cryptox

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxCookieSize is the browser budget of SnappyMail\Cookies::set(): the 4 KB
// limit covers the whole cookie, so the value is split at
// 4000 - strlen(path . name).
const maxCookieSize = 4000

// CookieOptions are the parts of the cookie behaviour that come from
// application.ini.
type CookieOptions struct {
	// Path is labs.cookie_default_path; empty means "/".
	Path string
	// SameSite is security.cookie_samesite ("Strict", "Lax", "None").
	SameSite string
	// Secure is labs.cookie_default_secure.
	Secure bool
	// RequestIsSecure tells whether the current request arrived over HTTPS
	// (isset($_SERVER['HTTPS'])).
	RequestIsSecure bool
}

// Jar ports SnappyMail\Cookies for one request. It reads the request cookies
// and records the cookies written to the response, so a value written earlier
// in the same request is visible to Get() again (PHP updates $_COOKIE).
//
// A Jar is request scoped and not safe for concurrent use.
type Jar struct {
	path     string
	sameSite http.SameSite
	secure   bool

	request *http.Request

	// values are the cookies set during this request.
	values map[string]string
	// removed are the cookies cleared during this request.
	removed map[string]bool
}

// NewJar builds the cookie helpers for a request.
func NewJar(request *http.Request, options CookieOptions) *Jar {
	path := options.Path
	if "" == path {
		path = "/"
	}

	sameSiteValue := strings.ToLower(strings.TrimSpace(options.SameSite))
	secure := options.Secure || options.RequestIsSecure ||
		"none" == sameSiteValue

	return &Jar{
		path:     path,
		sameSite: phpSameSite(sameSiteValue),
		secure:   secure,
		request:  request,
		values:   make(map[string]string),
		removed:  make(map[string]bool),
	}
}

// phpSameSite maps security.cookie_samesite onto net/http's mode. Like PHP, an
// empty value sends no SameSite attribute; an unknown value is treated the same
// way (PHP 8 rejects it, but the configuration default is always valid).
func phpSameSite(value string) http.SameSite {
	switch value {
	case "none":
		return http.SameSiteNoneMode
	case "lax":
		return http.SameSiteLaxMode
	case "strict":
		return http.SameSiteStrictMode
	}
	return http.SameSiteDefaultMode
}

// cookies merges the request cookies with the ones set/cleared in this request.
func (j *Jar) cookies() map[string]string {
	result := make(map[string]string)

	if nil != j.request {
		for _, cookie := range j.request.Cookies() {
			result[cookie.Name] = cookie.Value
		}
	}

	for name := range j.removed {
		delete(result, name)
	}
	for name, value := range j.values {
		result[name] = value
	}

	return result
}

// Has reports whether the exact cookie name is present, like
// isset($_COOKIE[$sName]) in Cookies::getSecure().
func (j *Jar) Has(name string) bool {
	_, ok := j.cookies()[name]
	return ok
}

// Get is Cookies::get(): collect $name and $name~1, $name~2, … and concatenate
// them sorted by the full cookie name. ksort() sorts the names as strings, so
// "name~10" comes before "name~2"; that quirk is kept on purpose.
func (j *Jar) Get(name string) string {
	values := j.cookies()

	parts := make([]string, 0, 1)
	for cookieName := range values {
		if cookiePart(cookieName) == name {
			parts = append(parts, cookieName)
		}
	}
	if 0 == len(parts) {
		return ""
	}

	sort.Strings(parts)

	var builder strings.Builder
	for _, cookieName := range parts {
		builder.WriteString(values[cookieName])
	}
	return builder.String()
}

// cookiePart is PHP's strtok($name, '~'): everything before the first tilde.
func cookiePart(name string) string {
	if index := strings.IndexByte(name, '~'); 0 <= index {
		return name[:index]
	}
	return name
}

// Set is Cookies::set(): split the value into the 4 KB budget, expire stale
// higher numbered parts and record the new parts in the request state.
//
// A zero expires means a session cookie. An empty value deletes the cookie.
func (j *Jar) Set(w http.ResponseWriter, name, value string, expires time.Time, httpOnly bool) {
	maxSize := maxCookieSize - len(j.path+name)
	if 1 > maxSize {
		maxSize = 1
	}

	last := -1
	for index, part := range splitString(value, maxSize) {
		cookieName := name
		if 0 < index {
			cookieName = name + "~" + strconv.Itoa(index)
		}
		j.write(w, cookieName, part, expires, httpOnly)
		last = index
	}

	// Delete unused old split parts, like Cookies::set().
	for cookieName := range j.cookies() {
		index, ok := cookiePartIndex(cookieName, name)
		if ok && index > last {
			j.write(w, cookieName, "", time.Time{}, httpOnly)
		}
	}
}

// Clear is Cookies::clear(): delete the base cookie and every split part.
func (j *Jar) Clear(w http.ResponseWriter, name string) {
	j.write(w, name, "", time.Time{}, true)

	for cookieName := range j.cookies() {
		if cookiePart(cookieName) == name {
			j.write(w, cookieName, "", time.Time{}, true)
		}
	}
}

// SetSecure is Cookies::setSecure(): urlsafe-base64(EncryptToJSON(data)).
func (j *Jar) SetSecure(crypter *Crypter, w http.ResponseWriter, name string, data any) error {
	if nil == data {
		j.Clear(w, name)
		return nil
	}

	encrypted, err := crypter.EncryptToJSON(data, "")
	if nil != err {
		return err
	}

	j.Set(w, name, URLSafeBase64Encode(encrypted), time.Time{}, true)
	return nil
}

// GetSecure is Cookies::getSecure(): the decrypted data of a cookie, with
// found=false when the cookie does not exist.
func (j *Jar) GetSecure(crypter *Crypter, name string) (value any, found bool, err error) {
	if !j.Has(name) {
		return nil, false, nil
	}

	decoded, err := URLSafeBase64Decode(j.Get(name))
	if nil != err {
		return nil, true, err
	}

	value, err = crypter.DecryptFromJSON(string(decoded), "")
	if nil != err {
		return nil, true, err
	}

	return value, true, nil
}

// write sets one cookie and records it in the request state.
func (j *Jar) write(w http.ResponseWriter, name, value string, expires time.Time, httpOnly bool) {
	if "" == value {
		delete(j.values, name)
		j.removed[name] = true
	} else {
		j.values[name] = value
		delete(j.removed, name)
	}

	if nil == w {
		return
	}

	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     j.path,
		Secure:   j.secure,
		HttpOnly: httpOnly,
	}

	if http.SameSiteDefaultMode != j.sameSite {
		cookie.SameSite = j.sameSite
	}

	if "" == value {
		// Like Cookies::_set() with an empty value and an existing cookie:
		// an expiry far in the past.
		cookie.MaxAge = -1
		cookie.Expires = time.Now().Add(-30 * 24 * time.Hour)
	} else if !expires.IsZero() {
		cookie.Expires = expires
	}

	removeSetCookie(w, name)
	http.SetCookie(w, cookie)
}

// removeSetCookie drops an earlier Set-Cookie header for name, mirroring the
// header_remove() dance of Cookies::_set().
func removeSetCookie(w http.ResponseWriter, name string) {
	header := w.Header()

	lines := header["Set-Cookie"]
	if 0 == len(lines) {
		return
	}

	prefix := name + "="
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		kept = append(kept, line)
	}

	if 0 == len(kept) {
		header.Del("Set-Cookie")
		return
	}
	header["Set-Cookie"] = kept
}

// splitString is PHP's str_split($value, $size), including the single empty
// chunk for an empty value.
func splitString(value string, size int) []string {
	if 1 > size {
		size = 1
	}
	if "" == value {
		return []string{""}
	}

	parts := make([]string, 0, (len(value)+size-1)/size)
	for len(value) > size {
		parts = append(parts, value[:size])
		value = value[size:]
	}
	return append(parts, value)
}

// cookiePartIndex returns the numeric suffix of "name~N", if any. The base name
// itself has no suffix, like explode('~', $name) with one element.
func cookiePartIndex(cookieName, name string) (int, bool) {
	if !strings.HasPrefix(cookieName, name+"~") {
		return 0, false
	}

	index, err := strconv.Atoi(cookieName[len(name)+1:])
	if nil != err || 0 >= index {
		return 0, false
	}
	return index, true
}
