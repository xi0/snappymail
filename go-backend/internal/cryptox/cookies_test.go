package cryptox

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// requestWithCookies builds a request carrying the given name/value pairs.
func requestWithCookies(pairs map[string]string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for name, value := range pairs {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	return request
}

// setCookieLines returns the raw Set-Cookie headers written to the recorder.
func setCookieLines(recorder *httptest.ResponseRecorder) []string {
	return recorder.Header().Values("Set-Cookie")
}

func TestCookieGetReturnsEmptyWithoutCookie(t *testing.T) {
	jar := NewJar(requestWithCookies(nil), CookieOptions{})

	if got := jar.Get("smsession"); "" != got {
		t.Errorf("Get = %q, want empty", got)
	}
	if jar.Has("smsession") {
		t.Error("Has = true without a cookie")
	}
}

func TestCookieSplitsAndReassemblesAt4KB(t *testing.T) {
	const name = "smsession"
	value := strings.Repeat("a", 10000)

	jar := NewJar(nil, CookieOptions{})
	recorder := httptest.NewRecorder()
	jar.Set(recorder, name, value, time.Time{}, true)

	// In-request visibility (PHP updates $_COOKIE).
	if got := jar.Get(name); value != got {
		t.Errorf("same request Get = %d bytes, want %d", len(got), len(value))
	}

	// Chunk size is 4000 - strlen(path . name); the continuation parts all use
	// the base name length, like PHP.
	maxSize := maxCookieSize - len("/"+name)
	wantParts := []int{maxSize, maxSize, len(value) - 2*maxSize}
	wantNames := []string{name, name + "~1", name + "~2"}

	cookies := recorder.Result().Cookies()
	if len(wantNames) != len(cookies) {
		t.Fatalf("got %d cookies, want %d: %v", len(cookies), len(wantNames), cookies)
	}
	for index, cookie := range cookies {
		if wantNames[index] != cookie.Name {
			t.Errorf("cookie[%d].Name = %q, want %q", index, cookie.Name, wantNames[index])
		}
		if wantParts[index] != len(cookie.Value) {
			t.Errorf("cookie[%d] value length = %d, want %d", index, len(cookie.Value), wantParts[index])
		}
	}

	// A fresh request sees the same value.
	reassembled := NewJar(requestWithCookies(cookieValues(cookies)), CookieOptions{})
	if got := reassembled.Get(name); value != got {
		t.Errorf("reassembled = %d bytes, want %d", len(got), len(value))
	}
}

// cookieValues turns parsed cookies into the name/value map of a next request.
func cookieValues(cookies []*http.Cookie) map[string]string {
	values := make(map[string]string, len(cookies))
	for _, cookie := range cookies {
		if "" != cookie.Value {
			values[cookie.Name] = cookie.Value
		}
	}
	return values
}

func TestCookieGetSortsFullNamesLikePHP(t *testing.T) {
	// ksort() compares the full cookie name as a string, so "x~10" is ordered
	// before "x~2". The quirk is part of the PHP behaviour.
	jar := NewJar(requestWithCookies(map[string]string{
		"x":    "0",
		"x~1":  "1",
		"x~10": "10",
		"x~2":  "2",
	}), CookieOptions{})

	if got := jar.Get("x"); "01102" != got {
		t.Errorf("Get = %q, want 01102 (name, name~1, name~10, name~2)", got)
	}
}

func TestCookieReassemblesOnlyTheMatchingPrefix(t *testing.T) {
	jar := NewJar(requestWithCookies(map[string]string{
		"smsession":       "a",
		"smsession~1":     "b",
		"smsession_other": "c",
		"smauth":          "d",
	}), CookieOptions{})

	if got := jar.Get("smsession"); "ab" != got {
		t.Errorf("Get = %q, want ab", got)
	}
}

func TestCookieSetExpiresStaleSplitParts(t *testing.T) {
	jar := NewJar(requestWithCookies(map[string]string{
		"x":   "old-base",
		"x~2": "old-part-2",
	}), CookieOptions{})

	recorder := httptest.NewRecorder()
	jar.Set(recorder, "x", "new", time.Time{}, true)

	lines := setCookieLines(recorder)
	if !hasCookieLine(lines, "x~2=") {
		t.Errorf("the stale x~2 part was not expired: %v", lines)
	}
	if hasCookieLine(lines, "x~1=") {
		t.Errorf("x~1 never existed and must not be written: %v", lines)
	}
}

func TestCookieClearRemovesBaseAndParts(t *testing.T) {
	jar := NewJar(requestWithCookies(map[string]string{
		"x":   "base",
		"x~1": "part",
	}), CookieOptions{})

	recorder := httptest.NewRecorder()
	jar.Clear(recorder, "x")

	lines := setCookieLines(recorder)
	if !hasCookieLine(lines, "x=") {
		t.Errorf("base cookie was not cleared: %v", lines)
	}
	if !hasCookieLine(lines, "x~1=") {
		t.Errorf("split part was not cleared: %v", lines)
	}

	if jar.Has("x") || jar.Has("x~1") || "" != jar.Get("x") {
		t.Error("cleared cookies must not be visible in the same request")
	}
}

func TestCookieSetEmptyDeletes(t *testing.T) {
	jar := NewJar(requestWithCookies(map[string]string{"x": "base"}), CookieOptions{})

	recorder := httptest.NewRecorder()
	jar.Set(recorder, "x", "", time.Time{}, true)

	if !hasCookieLine(setCookieLines(recorder), "x=") {
		t.Error("an empty value must write a deletion cookie")
	}
	if jar.Has("x") {
		t.Error("the deleted cookie must not be visible in the same request")
	}
}

func TestCookieSetReplacesAnEarlierValue(t *testing.T) {
	jar := NewJar(nil, CookieOptions{})
	recorder := httptest.NewRecorder()

	jar.Set(recorder, "x", "first", time.Time{}, true)
	jar.Set(recorder, "x", "second", time.Time{}, true)

	lines := setCookieLines(recorder)
	if 1 != len(lines) {
		t.Fatalf("Set-Cookie lines = %v, want a single line", lines)
	}
	if !strings.Contains(lines[0], "x=second") {
		t.Errorf("line = %q, want x=second", lines[0])
	}
	if "second" != jar.Get("x") {
		t.Errorf("Get = %q, want second", jar.Get("x"))
	}
}

func TestCookieOptions(t *testing.T) {
	jar := NewJar(nil, CookieOptions{Path: "/mail", SameSite: "None"})
	recorder := httptest.NewRecorder()
	jar.Set(recorder, "smmailtoauth", "value", time.Time{}, true)

	line := setCookieLines(recorder)[0]
	for _, attribute := range []string{
		"Path=/mail", "Secure", "HttpOnly", "SameSite=None",
	} {
		if !strings.Contains(line, attribute) {
			t.Errorf("Set-Cookie %q is missing %q", line, attribute)
		}
	}
}

func TestCookieSecureFromHTTPSRequest(t *testing.T) {
	jar := NewJar(nil, CookieOptions{RequestIsSecure: true})
	recorder := httptest.NewRecorder()
	jar.Set(recorder, "smsession", "value", time.Time{}, true)

	if line := setCookieLines(recorder)[0]; !strings.Contains(line, "Secure") {
		t.Errorf("Set-Cookie %q is missing Secure", line)
	}
}

func TestCookieSessionVersusExpiry(t *testing.T) {
	jar := NewJar(nil, CookieOptions{})

	session := httptest.NewRecorder()
	jar.Set(session, "smsession", "value", time.Time{}, true)
	if line := setCookieLines(session)[0]; strings.Contains(line, "Expires=") {
		t.Errorf("a zero expiry must be a session cookie: %q", line)
	}

	persistent := httptest.NewRecorder()
	expires := time.Now().Add(30 * 24 * time.Hour)
	jar.Set(persistent, "smctoken", "value", expires, false)
	line := setCookieLines(persistent)[0]
	if !strings.Contains(line, "Expires=") {
		t.Errorf("a non zero expiry must be written: %q", line)
	}
	if strings.Contains(line, "HttpOnly") {
		t.Errorf("httpOnly=false must not add HttpOnly: %q", line)
	}
}

func TestCookieSameSiteMapping(t *testing.T) {
	tests := map[string]string{
		"Strict": "SameSite=Strict",
		"Lax":    "SameSite=Lax",
		"None":   "SameSite=None",
	}

	for value, want := range tests {
		jar := NewJar(nil, CookieOptions{SameSite: value})
		recorder := httptest.NewRecorder()
		jar.Set(recorder, "x", "y", time.Time{}, true)

		if line := setCookieLines(recorder)[0]; !strings.Contains(line, want) {
			t.Errorf("SameSite %q: Set-Cookie %q is missing %q", value, line, want)
		}
	}
}

func TestSecureCookieHelpers(t *testing.T) {
	crypter := testCrypter()
	crypter.DefaultPassphrase = func() (string, error) { return "smctoken", nil }

	jar := NewJar(nil, CookieOptions{})
	recorder := httptest.NewRecorder()

	data := map[string]any{"email": "a@example.com"}
	if err := jar.SetSecure(crypter, recorder, "smaccount", data); nil != err {
		t.Fatalf("SetSecure: %v", err)
	}

	cookies := recorder.Result().Cookies()
	request := requestWithCookies(cookieValues(cookies))
	reader := NewJar(request, CookieOptions{})

	value, found, err := reader.GetSecure(crypter, "smaccount")
	if nil != err {
		t.Fatalf("GetSecure: %v", err)
	}
	if !found {
		t.Fatal("GetSecure did not find the cookie")
	}
	if !reflect.DeepEqual(value, data) {
		t.Errorf("GetSecure = %#v, want %#v", value, data)
	}

	// A missing cookie is reported with found=false and no error.
	if _, found, err := reader.GetSecure(crypter, "smremember"); nil != err || found {
		t.Errorf("GetSecure missing = (found=%v, err=%v), want (false, nil)", found, err)
	}

	// nil data clears the cookie.
	clearRecorder := httptest.NewRecorder()
	if err := reader.SetSecure(crypter, clearRecorder, "smaccount", nil); nil != err {
		t.Fatalf("SetSecure(nil): %v", err)
	}
	if !hasCookieLine(setCookieLines(clearRecorder), "smaccount=") {
		t.Error("SetSecure(nil) must clear the cookie")
	}
}

func TestSplitStringMatchesPHPStrSplit(t *testing.T) {
	tests := []struct {
		value string
		size  int
		want  []string
	}{
		{"", 3, []string{""}},
		{"a", 3, []string{"a"}},
		{"abc", 3, []string{"abc"}},
		{"abcd", 3, []string{"abc", "d"}},
		{"abcdef", 3, []string{"abc", "def"}},
	}

	for _, test := range tests {
		if got := splitString(test.value, test.size); !reflect.DeepEqual(got, test.want) {
			t.Errorf("splitString(%q, %d) = %#v, want %#v", test.value, test.size, got, test.want)
		}
	}
}

func TestCookiePartIndex(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		want     int
		wantOkay bool
	}{
		{"x", "x", 0, false},
		{"x~1", "x", 1, true},
		{"x~10", "x", 10, true},
		{"x~", "x", 0, false},
		{"x~abc", "x", 0, false},
		{"xy~1", "x", 0, false},
	}

	for _, test := range tests {
		got, ok := cookiePartIndex(test.name, test.base)
		if ok != test.wantOkay || got != test.want {
			t.Errorf("cookiePartIndex(%q, %q) = (%d, %v), want (%d, %v)",
				test.name, test.base, got, ok, test.want, test.wantOkay)
		}
	}
}

// hasCookieLine reports whether a Set-Cookie line starts with prefix.
func hasCookieLine(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
