package httpclient

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testVersion is the APP_VERSION of the user agent under test.
const testVersion = "0.0.0"

func newClient() *Client { return New(testVersion) }

func TestURIHasAllowedScheme(t *testing.T) {
	allowed := []string{
		"http://example.com/",
		"https://example.com/a?b=c",
		"HTTPS://EXAMPLE.COM",
	}

	for _, uri := range allowed {
		if !URIHasAllowedScheme(uri) {
			t.Errorf("%q must be fetchable", uri)
		}
	}

	refused := []string{
		"",
		"ftp://example.com/",
		"file:///etc/passwd",
		"gopher://example.com/",
		"//example.com/",
		"example.com",
		"mailto:user@example.com",
		"http:/example.com",
	}

	for _, uri := range refused {
		if URIHasAllowedScheme(uri) {
			t.Errorf("%q must not be fetchable", uri)
		}
	}
}

func TestDoRefusesForeignSchemes(t *testing.T) {
	for _, uri := range []string{"ftp://example.com/x", "file:///etc/passwd", "", "//example.com/"} {
		if _, err := newClient().Do(http.MethodGet, uri, nil, nil); !errors.Is(err, ErrScheme) {
			t.Errorf("Do(%q) error = %v, want ErrScheme", uri, err)
		}
	}

	// A redirect into a foreign scheme is refused as well.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	client := newClient()
	client.MaxRedirects = 3

	if _, err := client.Do(http.MethodGet, server.URL, nil, nil); !errors.Is(err, ErrScheme) {
		t.Errorf("a redirect to file:// error = %v, want ErrScheme", err)
	}
}

func TestDoSendsTheRequest(t *testing.T) {
	type received struct {
		method        string
		userAgent     string
		contentType   string
		authorization string
		body          string
	}

	got := make(chan received, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{
			method:        r.Method,
			userAgent:     r.Header.Get("User-Agent"),
			contentType:   r.Header.Get("Content-Type"),
			authorization: r.Header.Get("Authorization"),
			body:          string(body),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newClient().AddHeader("X-Test: yes")

	response, err := client.Do(http.MethodPost, server.URL, url.Values{"a": {"1"}, "b": {"2"}}, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if http.StatusOK != response.Status {
		t.Errorf("Status = %d, want 200", response.Status)
	}
	if server.URL != response.FinalURI || server.URL != response.RequestURI {
		t.Errorf("FinalURI = %q, RequestURI = %q, want %q", response.FinalURI, response.RequestURI, server.URL)
	}

	request := <-got
	if http.MethodPost != request.method {
		t.Errorf("method = %q, want POST", request.method)
	}
	if "SnappyMail/"+testVersion != request.userAgent {
		t.Errorf("User-Agent = %q", request.userAgent)
	}
	// PHP: a request with a body defaults to a form encoded content type.
	if "application/x-www-form-urlencoded" != request.contentType {
		t.Errorf("Content-Type = %q", request.contentType)
	}
	if "a=1&b=2" != request.body {
		t.Errorf("body = %q, want the http_build_query() result", request.body)
	}
}

func TestDoAppendsAGetBodyToTheQuery(t *testing.T) {
	var query, body string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		read, _ := io.ReadAll(r.Body)
		body = string(read)
	}))
	defer server.Close()

	client := newClient()

	// No query yet: the body is appended with "?".
	if _, err := client.Do(http.MethodGet, server.URL, "a=1", nil); nil != err {
		t.Fatalf("Do: %v", err)
	}
	if "a=1" != query {
		t.Errorf("query = %q, want a=1", query)
	}
	if "" != body {
		t.Errorf("body = %q, want nothing (PHP moves a GET body into the URL)", body)
	}

	// With a query: the body is appended with "&".
	if _, err := client.Do(http.MethodGet, server.URL+"?x=0", "a=1", nil); nil != err {
		t.Fatalf("Do: %v", err)
	}
	if "x=0&a=1" != query {
		t.Errorf("query = %q, want x=0&a=1", query)
	}
}

func TestDoFollowsRedirects(t *testing.T) {
	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)

		switch r.URL.Path {
		case "/start":
			w.Header().Set("Location", "/middle")
			w.WriteHeader(http.StatusFound)
		case "/middle":
			// A relative target resolves against the current directory.
			w.Header().Set("Location", "final")
			w.WriteHeader(http.StatusMovedPermanently)
		default:
			_, _ = io.WriteString(w, "done")
		}
	}))
	defer server.Close()

	client := newClient()
	client.MaxRedirects = 2

	response, err := client.Do(http.MethodGet, server.URL+"/start", nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}

	if http.StatusOK != response.Status {
		t.Errorf("Status = %d, want 200", response.Status)
	}
	if "done" != string(response.Body) {
		t.Errorf("Body = %q", response.Body)
	}
	if server.URL+"/final" != response.FinalURI {
		t.Errorf("FinalURI = %q, want %s", response.FinalURI, server.URL+"/final")
	}
	if server.URL+"/start" != response.RequestURI {
		t.Errorf("RequestURI = %q, want the original URI", response.RequestURI)
	}
	if 3 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 3", requests)
	}
}

func TestDoStopsAfterMaxRedirects(t *testing.T) {
	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Location", "/loop")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	client := newClient()
	client.MaxRedirects = 2

	response, err := client.Do(http.MethodGet, server.URL+"/loop", nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}

	// One request per followed redirect plus the first one.
	if 3 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want MaxRedirects+1", requests)
	}
	// The last redirect is returned as-is (PHP: the else branch of doRequest).
	if http.StatusFound != response.Status {
		t.Errorf("Status = %d, want the last 302", response.Status)
	}
	if server.URL+"/loop" != response.FinalURI {
		t.Errorf("FinalURI = %q", response.FinalURI)
	}

	// No redirect at all is the MaxRedirects = 0 default.
	atomic.StoreInt32(&requests, 0)

	response, err = newClient().Do(http.MethodGet, server.URL+"/loop", nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if 1 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 1 without MaxRedirects", requests)
	}
	if http.StatusFound != response.Status {
		t.Errorf("Status = %d, want 302", response.Status)
	}
}

func TestDoDoesNotFollowNonGetRedirects(t *testing.T) {
	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Location", "/elsewhere")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()

	client := newClient()
	client.MaxRedirects = 3

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		atomic.StoreInt32(&requests, 0)

		response, err := client.Do(method, server.URL, "a=1", nil)
		if nil != err {
			t.Fatalf("Do(%s): %v", method, err)
		}
		if 1 != atomic.LoadInt32(&requests) {
			t.Errorf("%s: requests = %d, want 1", method, requests)
		}
		if http.StatusSeeOther != response.Status {
			t.Errorf("%s: Status = %d, want 303", method, response.Status)
		}
	}

	// HEAD is followed, like GET. (The URL has a path, because a relative
	// Location cannot be resolved against a bare "http://host" — see
	// Response.RedirectLocation.)
	atomic.StoreInt32(&requests, 0)
	if _, err := client.Do(http.MethodHead, server.URL+"/head", nil, nil); nil != err {
		t.Fatalf("Do(HEAD): %v", err)
	}
	if 4 != atomic.LoadInt32(&requests) {
		t.Errorf("HEAD: requests = %d, want 4", requests)
	}
}

func TestRedirectLocation(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		final    string
		location string
		want     string
	}{
		{
			name:     "absolute",
			request:  "https://a.example/start",
			final:    "https://a.example/start",
			location: "https://b.example/x",
			want:     "https://b.example/x",
		},
		{
			name:     "root relative",
			request:  "https://a.example/deep/start",
			final:    "https://a.example/deep/start",
			location: "/x",
			want:     "https://a.example/x",
		},
		{
			name:     "directory relative",
			request:  "https://a.example/deep/start",
			final:    "https://a.example/deep/start",
			location: "x/y",
			want:     "https://a.example/deep/x/y",
		},
		{
			name:     "directory relative with query",
			request:  "https://a.example/deep/start?q=1",
			final:    "https://a.example/deep/start?q=1",
			location: "x",
			want:     "https://a.example/deep/x",
		},
		{
			name:     "protocol relative",
			request:  "https://a.example/start",
			final:    "https://a.example/start",
			location: "//b.example/x",
			want:     "https://b.example/x",
		},
		{
			name:     "port",
			request:  "http://a.example:8080/start",
			final:    "http://a.example:8080/start",
			location: "/x",
			want:     "http://a.example:8080/x",
		},
		{
			name:     "missing",
			request:  "https://a.example/start",
			final:    "https://a.example/start",
			location: "",
			want:     "",
		},
	}

	for _, test := range tests {
		response := &Response{
			RequestURI: test.request,
			FinalURI:   test.final,
			Headers:    http.Header{},
		}
		if "" != test.location {
			response.Headers.Set("Location", test.location)
		}

		if got := response.RedirectLocation(); test.want != got {
			t.Errorf("%s: RedirectLocation() = %q, want %q", test.name, got, test.want)
		}
	}
}

// gzipped builds the gzip stream of value.
func gzipped(t *testing.T, value string) []byte {
	t.Helper()

	var buffer bytes.Buffer

	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(value)); nil != err {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); nil != err {
		t.Fatalf("gzip close: %v", err)
	}

	return buffer.Bytes()
}

func TestDoDecodesGzipBodies(t *testing.T) {
	const body = "compressed payload"

	var acceptEncoding string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(gzipped(t, body))
	}))
	defer server.Close()

	response, err := newClient().Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}

	if body != string(response.Body) {
		t.Errorf("Body = %q, want the decoded payload", response.Body)
	}
	// The PHP client never announces an encoding itself.
	if "" != acceptEncoding {
		t.Errorf("Accept-Encoding = %q, want nothing", acceptEncoding)
	}
}

func TestDoCapsTheResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer server.Close()

	client := newClient()
	client.MaxResponseKB = 1

	response, err := client.Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if 1024 != len(response.Body) {
		t.Errorf("len(Body) = %d, want the 1 KB cap", len(response.Body))
	}

	// A zero cap reads the whole body.
	client.MaxResponseKB = 0

	response, err = client.Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if 4096 != len(response.Body) {
		t.Errorf("len(Body) = %d, want the whole body", len(response.Body))
	}
}

func TestDoStreamsTheBody(t *testing.T) {
	raw := gzipped(t, "streamed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	var stream bytes.Buffer

	client := newClient()
	client.StreamBodyTo(&stream)

	response, err := client.Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}

	// The raw bytes are streamed and Response.Body stays empty.
	if !bytes.Equal(raw, stream.Bytes()) {
		t.Errorf("stream = %q, want the raw body", stream.Bytes())
	}
	if 0 != len(response.Body) {
		t.Errorf("Body = %q, want nothing when streaming", response.Body)
	}
	if "gzip" != response.GetHeader("content-encoding") {
		t.Errorf("content-encoding = %q", response.GetHeader("content-encoding"))
	}

	// Clearing the target restores the buffered mode.
	client.StreamBodyTo(nil)

	response, err = client.Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if "streamed" != string(response.Body) {
		t.Errorf("Body = %q, want the decoded payload", response.Body)
	}
}

func TestDoRewindsTheStreamTargetOnRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "/start" == r.URL.Path {
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
			_, _ = io.WriteString(w, "aaaaa")
			return
		}
		_, _ = io.WriteString(w, "bbbbb")
	}))
	defer server.Close()

	target, err := os.CreateTemp(t.TempDir(), "stream")
	if nil != err {
		t.Fatalf("create temp file: %v", err)
	}
	defer func() {
		_ = target.Close()
	}()

	client := newClient()
	client.MaxRedirects = 1
	client.StreamBodyTo(target)

	if _, err := client.Do(http.MethodGet, server.URL+"/start", nil, nil); nil != err {
		t.Fatalf("Do: %v", err)
	}

	content, err := os.ReadFile(target.Name())
	if nil != err {
		t.Fatalf("read the stream target: %v", err)
	}

	// PHP rewinds the target before the second attempt, so the redirect body
	// overwrites the first one instead of being appended.
	if "bbbbb" != string(content) {
		t.Errorf("stream target = %q, want the body of the final response", content)
	}
}

func TestDoTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	client := newClient()
	client.Timeout = 20 * time.Millisecond

	if _, err := client.Do(http.MethodGet, server.URL, nil, nil); !errors.Is(err, ErrTimeout) {
		t.Errorf("error = %v, want ErrTimeout", err)
	}
}

func TestDoAuthenticatesAfterAChallenge(t *testing.T) {
	const expected = "Basic dXNlcjpwYXNz" // user:pass

	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)

		if expected != r.Header.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "secret")
	}))
	defer server.Close()

	response, err := newClient().SetAuthBasic("user", "pass").Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if http.StatusOK != response.Status {
		t.Fatalf("Status = %d, want 200", response.Status)
	}
	if "secret" != string(response.Body) {
		t.Errorf("Body = %q", response.Body)
	}
	if 2 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 2 (one challenge, one authenticated)", requests)
	}

	// Without credentials the 401 is returned untouched.
	atomic.StoreInt32(&requests, 0)

	response, err = newClient().Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if http.StatusUnauthorized != response.Status {
		t.Errorf("Status = %d, want 401", response.Status)
	}
	if 1 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 1", requests)
	}

	// An explicit Authorization header is not doubled.
	atomic.StoreInt32(&requests, 0)

	headers := http.Header{}
	headers.Set("Authorization", expected)
	if _, err := newClient().SetAuthBasic("user", "pass").Do(http.MethodGet, server.URL, nil, headers); nil != err {
		t.Fatalf("Do: %v", err)
	}
	if 1 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 1 with an explicit Authorization header", requests)
	}
}

func TestDoBearerAuthentication(t *testing.T) {
	var requests int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)

		if "Bearer token" != r.Header.Get("Authorization") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	response, err := newClient().SetAuthBearer("user", "token").Do(http.MethodGet, server.URL, nil, nil)
	if nil != err {
		t.Fatalf("Do: %v", err)
	}
	if http.StatusOK != response.Status {
		t.Fatalf("Status = %d, want 200", response.Status)
	}
	if 2 != atomic.LoadInt32(&requests) {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestGetHeaderFallsBackThroughNames(t *testing.T) {
	response := &Response{Headers: http.Header{}}
	response.Headers.Set("Content-Type", "image/png")

	if "" != response.GetHeader("content-length") {
		t.Errorf("GetHeader() = %q, want empty for a missing header", response.GetHeader("content-length"))
	}
	if "image/png" != response.GetHeader("content-length", "Content-Type") {
		t.Errorf("GetHeader() = %q, want the fallback value", response.GetHeader("content-length", "Content-Type"))
	}
	if "image/png" != response.GetHeader("CONTENT-TYPE") {
		t.Errorf("GetHeader() is not case-insensitive")
	}

	response.Headers.Add("X-Multi", "a")
	response.Headers.Add("X-Multi", "b")

	if "a" != response.GetHeader("x-multi") {
		t.Errorf("GetHeader() = %q, want the first value", response.GetHeader("x-multi"))
	}
	if got := strings.Join(response.HeaderValues("x-multi"), ","); "a,b" != got {
		t.Errorf("HeaderValues() = %q, want a,b", got)
	}

	if "" != (&Response{}).GetHeader("x-missing") {
		t.Error("GetHeader() on an empty response must be empty")
	}

	var nilResponse *Response
	if "" != nilResponse.GetHeader("x") {
		t.Error("GetHeader() on a nil response must be empty")
	}
}

func TestDoRejectsUnsupportedBodies(t *testing.T) {
	if _, err := newClient().Do(http.MethodPost, "http://example.com/", 42, nil); nil == err {
		t.Error("an int body must be rejected")
	}
}
