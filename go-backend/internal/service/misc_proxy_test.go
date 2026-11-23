package service

// Tests of step 13c: the external image proxy of ?/ProxyExternal/<urlsafe-b64>.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/cryptox"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// proxyPNG is a minimal PNG signature, enough for MimeType detection.
var proxyPNG = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// proxyRequest builds a "?/ProxyExternal/<encoded>" request for target.
func proxyRequest(target string) (*http.Request, *web.Route) {
	encoded := cryptox.URLSafeBase64Encode(target)
	return httptest.NewRequest(http.MethodGet, "/?/ProxyExternal/"+encoded+"/", nil),
		&web.Route{Paths: []string{"ProxyExternal", encoded}}
}

// imageServer serves a fixed PNG for every path.
func imageServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(proxyPNG)
	}))
	t.Cleanup(server.Close)

	return server
}

func TestProxyExternalStreamsAnImage(t *testing.T) {
	server := imageServer(t)
	fixture := newTransferFixture(t)

	target := server.URL + "/logo.png"
	request, route := proxyRequest(target)

	recorder := httptest.NewRecorder()
	if !fixture.appData.ProxyExternal(recorder, request, route) {
		t.Fatal("ProxyExternal must report the request as handled")
	}

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "image/png" != got {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := recorder.Header().Get("Cache-Control"); "public" != got {
		t.Errorf("Cache-Control = %q, want public", got)
	}
	if "" == recorder.Header().Get("Expires") {
		t.Error("Expires must be set for a proxied image")
	}
	if !strings.HasSuffix(recorder.Header().Get("Expires"), " UTC") {
		t.Errorf("Expires = %q, want a … UTC date", recorder.Header().Get("Expires"))
	}
	// X-Content-Location is the requested URL, X-Content-Redirect-Location the
	// final one (identical without a redirect).
	if got := recorder.Header().Get("X-Content-Location"); target != got {
		t.Errorf("X-Content-Location = %q, want %q", got, target)
	}
	if got := recorder.Header().Get("X-Content-Redirect-Location"); target != got {
		t.Errorf("X-Content-Redirect-Location = %q, want %q", got, target)
	}
	if !bytes.Equal(proxyPNG, recorder.Body.Bytes()) {
		t.Errorf("body = %x, want the PNG bytes", recorder.Body.Bytes())
	}
}

func TestProxyExternalFollowsARedirect(t *testing.T) {
	final := imageServer(t)

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No body: the client re-uses the temp file across redirects, so an
		// empty redirect keeps the streamed bytes exactly the final image.
		w.Header().Set("Location", final.URL+"/real.png")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()

	fixture := newTransferFixture(t)
	target := redirect.URL + "/start.png"
	request, route := proxyRequest(target)

	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("X-Content-Redirect-Location"); final.URL+"/real.png" != got {
		t.Errorf("X-Content-Redirect-Location = %q, want the final URL", got)
	}
	if got := recorder.Header().Get("X-Content-Location"); target != got {
		t.Errorf("X-Content-Location = %q, want the original URL", got)
	}
	if !bytes.Equal(proxyPNG, recorder.Body.Bytes()) {
		t.Errorf("body = %x, want the PNG bytes", recorder.Body.Bytes())
	}
}

func TestProxyExternalRejectsNonImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not an image"))
	}))
	defer server.Close()

	fixture := newTransferFixture(t)
	request, route := proxyRequest(server.URL + "/page.txt")

	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if got := recorder.Header().Get("X-Content-Error"); "200 text/plain" != got {
		t.Errorf("X-Content-Error = %q, want \"200 text/plain\"", got)
	}
	if 0 != recorder.Body.Len() {
		t.Errorf("body = %q, want nothing", recorder.Body.String())
	}
}

func TestProxyExternalRejectsAnUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	fixture := newTransferFixture(t)
	request, route := proxyRequest(server.URL + "/secret.png")

	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	// A non-200 image is reported with its status, not streamed.
	if got := recorder.Header().Get("X-Content-Error"); "403 image/png" != got {
		t.Errorf("X-Content-Error = %q, want \"403 image/png\"", got)
	}
}

func TestProxyExternalDisabled(t *testing.T) {
	server := imageServer(t)
	fixture := newTransferFixture(t)
	fixture.config.Set("labs", "use_local_proxy_for_external_images", false)

	request, route := proxyRequest(server.URL + "/logo.png")
	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if "" != recorder.Header().Get("X-Content-Location") {
		t.Error("a disabled proxy must not set X-Content-Location")
	}
}

func TestProxyExternalRequiresAnAccount(t *testing.T) {
	server := imageServer(t)
	fixture := newTransferFixture(t)

	// A session without an account (and without an authenticator) is a guest.
	fixture.appData.options.NewSession = func(w http.ResponseWriter, r *http.Request) *Session {
		return &Session{Request: r, Response: w}
	}

	request, route := proxyRequest(server.URL + "/logo.png")
	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if "" != recorder.Header().Get("X-Content-Location") {
		t.Error("a guest must not reach the fetch")
	}
}

func TestProxyExternalRefusesANonHTTPScheme(t *testing.T) {
	fixture := newTransferFixture(t)

	target := "ftp://example.com/image.png"
	request, route := proxyRequest(target)

	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, request, route)

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	// The scheme refusal is a transport error, so only the location is known.
	if got := recorder.Header().Get("X-Content-Location"); target != got {
		t.Errorf("X-Content-Location = %q, want %q", got, target)
	}
	if "" != recorder.Header().Get("X-Content-Error") {
		t.Errorf("X-Content-Error = %q, want empty for a transport error",
			recorder.Header().Get("X-Content-Error"))
	}
}

func TestProxyExternalEmptyDataIs404(t *testing.T) {
	fixture := newTransferFixture(t)

	recorder := httptest.NewRecorder()
	fixture.appData.ProxyExternal(recorder, httptest.NewRequest(http.MethodGet, "/?/ProxyExternal/", nil),
		&web.Route{Paths: []string{"ProxyExternal"}})

	if http.StatusNotFound != recorder.Code {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestRegisterAddsProxyExternal(t *testing.T) {
	server := imageServer(t)
	fixture := newTransferFixture(t)

	router := web.NewRouter(web.DefaultConfig(), nil, fixture.tree.paths(), nil)
	Register(router, fixture.appData)

	encoded := cryptox.URLSafeBase64Encode(server.URL + "/logo.png")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/?/ProxyExternal/"+encoded+"/", nil))

	if http.StatusOK != recorder.Code {
		t.Fatalf("GET ?/ProxyExternal = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "image/png" != got {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.Equal(proxyPNG, recorder.Body.Bytes()) {
		t.Errorf("body = %x, want the PNG bytes", recorder.Body.Bytes())
	}
}
