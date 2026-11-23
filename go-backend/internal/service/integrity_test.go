package service

// Tests of step 13b: the Integrity::test() report of ?/Test, fetched through a
// real HTTP server so the report can probe the static assets it documents.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
	"github.com/xi0/snappymail/go-backend/internal/web"
)

// writeAsset writes a file below a version root.
func writeAsset(t *testing.T, root, relative, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); nil != err {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); nil != err {
		t.Fatalf("write %s: %v", path, err)
	}
}

// reportContents is the content the install tree of the report holds for the
// probed assets.
func reportContents() map[string]string {
	contents := make(map[string]string, len(integrityAssets))
	for _, asset := range integrityAssets {
		contents[asset] = "/* " + asset + " */"
	}
	return contents
}

// newIntegrityServer serves ?/Test from a router whose static tree holds
// "served", while the report compares against the install tree of the AppData
// fixture (reportContents()). The two trees are separate so a stale build can
// be simulated.
func newIntegrityServer(t *testing.T, compression bool, served map[string]string) (*miscFixture, *httptest.Server) {
	t.Helper()

	fixture := newMiscFixture(t)
	for relative, content := range reportContents() {
		writeAsset(t, fixture.tree.root, relative, content)
	}

	tree := newTestTree(t)
	for relative, content := range served {
		writeAsset(t, tree.root, relative, content)
	}

	config := web.DefaultConfig()
	config.CompressOutput = compression

	router := web.NewRouter(config, nil, tree.paths(), nil)
	Register(router, fixture.appData)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return fixture, server
}

// fetchReport requests ?/Test and returns the HTML report.
func fetchReport(t *testing.T, server *httptest.Server) string {
	t.Helper()

	response, err := http.Get(server.URL + "/?/Test/")
	if nil != err {
		t.Fatalf("GET ?/Test: %v", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if http.StatusOK != response.StatusCode {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); "text/html; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	// ServiceTest() calls ServerNoCache().
	if got := response.Header.Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	body, err := io.ReadAll(response.Body)
	if nil != err {
		t.Fatalf("read the report: %v", err)
	}

	return string(body)
}

func TestTestReportComparesTheServedAssets(t *testing.T) {
	_, server := newIntegrityServer(t, false, reportContents())

	body := fetchReport(t, server)

	contents := reportContents()

	for _, asset := range integrityAssets {
		// Integrity::test() prints the URI it probes.
		if uri := "<h2>" + server.URL + "/snappymail/v/0.0.0/" + asset + "</h2>"; !strings.Contains(body, uri) {
			t.Errorf("the report does not probe %s:\n%s", asset, body)
		}

		want := "<div>content-length matches size " + strconv.Itoa(len(contents[asset])) + "</div>"
		if !strings.Contains(body, want) {
			t.Errorf("%s: the report does not carry %q:\n%s", asset, want, body)
		}
	}

	if strings.Contains(body, "content-length mismatch") {
		t.Errorf("nothing is stale, the report must not flag a mismatch:\n%s", body)
	}

	// Without webmail.compress_output the encoded variants are not available.
	for _, encoding := range integrityEncodings {
		want := `<div style="color: red">content-encoding mismatch, should be: ` + encoding + `</div>`
		if !strings.Contains(body, want) {
			t.Errorf("the report must flag the missing %s encoding:\n%s", encoding, body)
		}
	}

	// The PHP version/extension checks of Integrity::test() are always green in
	// Go, so their blocks must not appear at all.
	for _, text := range []string{"PHP version", "PHP extensions", "Invalid Host header"} {
		if strings.Contains(body, text) {
			t.Errorf("the report must not contain %q:\n%s", text, body)
		}
	}
}

func TestTestReportFlagsAContentLengthMismatch(t *testing.T) {
	served := reportContents()
	served["static/css/app.min.css"] = "/* a stale build, longer than the file on disk */"

	_, server := newIntegrityServer(t, false, served)

	body := fetchReport(t, server)

	size := len(reportContents()["static/css/app.min.css"])
	want := `<div style="color: red">content-length mismatch, should be: ` + strconv.Itoa(size) + `</div>`

	if !strings.Contains(body, want) {
		t.Errorf("the report does not flag the stale asset, want %q:\n%s", want, body)
	}
	if count := strings.Count(body, "content-length mismatch"); 1 != count {
		t.Errorf("content-length mismatch = %d, want 1 (only the stale asset):\n%s", count, body)
	}
}

func TestTestReportWithCompression(t *testing.T) {
	_, server := newIntegrityServer(t, true, reportContents())

	body := fetchReport(t, server)

	for _, label := range []string{"<h3>gzip encoded</h3>", "<h3>brotli encoded</h3>"} {
		if !strings.Contains(body, label) {
			t.Fatalf("the report has no %s section:\n%s", label, body)
		}
	}

	// Every asset is served with both codings, so every probe matches.
	if count, want := strings.Count(body, "<div>content-encoding matches</div>"), len(integrityAssets)*2; count != want {
		t.Errorf("content-encoding matches = %d, want %d:\n%s", count, want, body)
	}
	if strings.Contains(body, "content-encoding mismatch") {
		t.Errorf("the compressed server must not report a coding mismatch:\n%s", body)
	}
}

func TestTestReportReportsMissingAssets(t *testing.T) {
	// The AppData install tree (the report's own tree) has no static assets:
	// the served tree does, so the size comparison has nothing to compare with.
	fixture := newMiscFixture(t)

	tree := newTestTree(t)
	for relative, content := range reportContents() {
		writeAsset(t, tree.root, relative, content)
	}

	router := web.NewRouter(web.DefaultConfig(), nil, tree.paths(), nil)
	Register(router, fixture.appData)

	server := httptest.NewServer(router)
	defer server.Close()

	body := fetchReport(t, server)

	if !strings.Contains(body, "asset file not found: ") {
		t.Errorf("the report must flag the missing asset files:\n%s", body)
	}
	if !strings.Contains(body, fixture.tree.root) {
		t.Errorf("the report must name the missing file:\n%s", body)
	}
}

func TestTestReportRejectsAnInvalidHost(t *testing.T) {
	fixture := newMiscFixture(t)

	request := httptest.NewRequest(http.MethodGet, "/?/Test/", nil)
	request.Host = "bad host"

	recorder := httptest.NewRecorder()
	if !fixture.appData.Test(recorder, request, &web.Route{Paths: []string{"Test"}}) {
		t.Fatal("Test must report the request as handled")
	}

	if http.StatusOK != recorder.Code {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); "text/html; charset=utf-8" != got {
		t.Errorf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); "no-store" != got {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	// Integrity::test(): "<p style="color: red">Invalid Host header</p>"
	if !strings.Contains(recorder.Body.String(), "Invalid Host header") {
		t.Errorf("body = %q, want the host error", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "<h2>") {
		t.Error("an invalid host must not probe anything")
	}
}

func TestTestReportReportsUnreachableAssets(t *testing.T) {
	fixture := newMiscFixture(t)

	// Port 1 on the loopback interface has no listener, so every probe fails.
	request := httptest.NewRequest(http.MethodGet, "/?/Test/", nil)
	request.Host = "127.0.0.1:1"

	recorder := httptest.NewRecorder()
	fixture.appData.Test(recorder, request, &web.Route{Paths: []string{"Test"}})

	if !strings.Contains(recorder.Body.String(), "request failed: ") {
		t.Errorf("the report must show the transport error:\n%s", recorder.Body.String())
	}
}

// TestTestReportAgainstTheShippedAssets is the acceptance check of step 13b:
// the report runs against the real install tree and finds every bundle with a
// matching content-length.
func TestTestReportAgainstTheShippedAssets(t *testing.T) {
	repoRoot, err := filepath.Abs("../../..")
	if nil != err {
		t.Fatalf("resolve repository root: %v", err)
	}

	versionRoot := filepath.Join(repoRoot, "snappymail", "v", testVersion)

	if _, statErr := os.Stat(filepath.Join(versionRoot, "static", "js", "min", "app.min.js")); nil != statErr {
		t.Skipf("the shipped install tree is not available: %v", statErr)
	}

	paths := httpx.WebPaths{Version: testVersion, VersionRoot: versionRoot}

	router := web.NewRouter(web.DefaultConfig(), nil, paths, nil)
	Register(router, NewAppData(Options{Version: testVersion, Paths: paths}))

	server := httptest.NewServer(router)
	defer server.Close()

	body := fetchReport(t, server)

	if count := strings.Count(body, "<div>content-length matches size "); len(integrityAssets) != count {
		t.Errorf("content-length matches = %d, want %d:\n%s", count, len(integrityAssets), body)
	}
	for _, text := range []string{"content-length mismatch", "asset file not found", "request failed: "} {
		if strings.Contains(body, text) {
			t.Errorf("the shipped tree must not report %q:\n%s", text, body)
		}
	}
}
