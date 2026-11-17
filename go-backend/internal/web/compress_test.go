package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

// readBody decodes a response body according to its Content-Encoding.
func readBody(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	switch encoding := recorder.Header().Get("Content-Encoding"); encoding {
	case encodingBrotli:
		body, err := io.ReadAll(brotli.NewReader(recorder.Body))
		if nil != err {
			t.Fatalf("decompress brotli body: %v", err)
		}
		return string(body)

	case encodingGzip:
		reader, err := gzip.NewReader(recorder.Body)
		if nil != err {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		body, err := io.ReadAll(reader)
		if nil != err {
			t.Fatalf("decompress gzip body: %v", err)
		}
		return string(body)
	}

	return recorder.Body.String()
}

func TestCompressionPrefersBrotli(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true
	router, _ := newTestRouter(t, config)

	request := get("/whatever")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	recorder := serve(router, request)

	if http.StatusOK != recorder.Code {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if encodingBrotli != recorder.Header().Get("Content-Encoding") {
		t.Fatalf("Content-Encoding = %q, want br (PHP prefers php-ext-brotli)",
			recorder.Header().Get("Content-Encoding"))
	}
	if got := recorder.Header().Get("Content-Length"); "" != got {
		t.Errorf("Content-Length = %q, want it dropped for the encoded body", got)
	}
	if !strings.Contains(recorder.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding", recorder.Header().Get("Vary"))
	}
	if body := readBody(t, recorder); !strings.Contains(body, "SnappyMail") {
		t.Errorf("decompressed body = %q, want the index page", body)
	}
}

func TestCompressionFallsBackToGzip(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true
	router, _ := newTestRouter(t, config)

	request := get("/whatever")
	request.Header.Set("Accept-Encoding", "gzip, deflate")
	recorder := serve(router, request)

	if encodingGzip != recorder.Header().Get("Content-Encoding") {
		t.Fatalf("Content-Encoding = %q, want gzip", recorder.Header().Get("Content-Encoding"))
	}
	if body := readBody(t, recorder); !strings.Contains(body, "SnappyMail") {
		t.Errorf("decompressed body = %q, want the index page", body)
	}
}

func TestCompressionNegotiation(t *testing.T) {
	tests := []struct {
		name           string
		header         string
		wantEncoding   string
		compressOutput bool
	}{
		{name: "no accept-encoding", header: "", compressOutput: true},
		{name: "identity only", header: "identity", compressOutput: true},
		{name: "br only", header: "br", wantEncoding: encodingBrotli, compressOutput: true},
		{name: "br upper case", header: "BR", wantEncoding: encodingBrotli, compressOutput: true},
		{name: "gzip only", header: "gzip", wantEncoding: encodingGzip, compressOutput: true},
		{name: "br and gzip", header: "gzip, br", wantEncoding: encodingBrotli, compressOutput: true},
		// q=0 removes a coding from the list, so gzip is used instead of br.
		{name: "br with q=0", header: "br;q=0, gzip", wantEncoding: encodingGzip, compressOutput: true},
		{name: "gzip with q=0", header: "gzip;q=0", wantEncoding: "", compressOutput: true},
		{name: "br with q=0.5", header: "br;q=0.5", wantEncoding: encodingBrotli, compressOutput: true},
		{name: "compression disabled", header: "gzip, br", wantEncoding: "", compressOutput: false},
		// Accept-Encoding only carries a token for encodings we do not have.
		{name: "unsupported only", header: "deflate, zstd", compressOutput: true},
	}

	for _, test := range tests {
		config := DefaultConfig()
		config.CompressOutput = test.compressOutput
		router, _ := newTestRouter(t, config)

		request := get("/whatever")
		if "" != test.header {
			request.Header.Set("Accept-Encoding", test.header)
		}

		recorder := serve(router, request)
		if got := recorder.Header().Get("Content-Encoding"); test.wantEncoding != got {
			t.Errorf("%s: Content-Encoding = %q, want %q", test.name, got, test.wantEncoding)
		}
		if "" != test.wantEncoding && !strings.Contains(recorder.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s: Vary = %q, want Accept-Encoding", test.name, recorder.Header().Get("Vary"))
		}
		if "" == test.wantEncoding && "" != recorder.Header().Get("Vary") {
			t.Errorf("%s: Vary = %q, want nothing", test.name, recorder.Header().Get("Vary"))
		}
	}
}

func TestCompressionSkipsUncompressibleResponses(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true
	router, _ := newTestRouter(t, config)

	// Static binaries are not compressed (images/audio/fonts), text assets are.
	staticTargets := []struct {
		target   string
		encoding string
	}{
		{"/snappymail/v/0.0.0/static/js/min/app.min.js", encodingBrotli},
		{"/snappymail/v/0.0.0/static/css/app.min.css", encodingBrotli},
		{"/snappymail/v/0.0.0/static/manifest.json", encodingBrotli},
		{"/snappymail/v/0.0.0/static/images/snappymail-logo.png", ""},
		{"/snappymail/v/0.0.0/static/sounds/new-mail.mp3", ""},
		{"/snappymail/v/0.0.0/static/css/fonts/snappymail.woff2", ""},
	}

	for _, test := range staticTargets {
		request := get(test.target)
		request.Header.Set("Accept-Encoding", "gzip, br")

		recorder := serve(router, request)
		if http.StatusOK != recorder.Code {
			t.Fatalf("GET %s = %d, want 200", test.target, recorder.Code)
		}
		if got := recorder.Header().Get("Content-Encoding"); test.encoding != got {
			t.Errorf("GET %s Content-Encoding = %q, want %q", test.target, got, test.encoding)
		}
	}

	// HEAD responses carry no body and stay unencoded.
	request := httptest.NewRequest(http.MethodHead, "/whatever", nil)
	request.Header.Set("Accept-Encoding", "gzip, br")
	if got := serve(router, request).Header().Get("Content-Encoding"); "" != got {
		t.Errorf("HEAD Content-Encoding = %q, want nothing", got)
	}

	// Range requests must not be encoded: the client slices the raw bytes.
	request = get("/snappymail/v/0.0.0/static/js/min/app.min.js")
	request.Header.Set("Accept-Encoding", "gzip, br")
	request.Header.Set("Range", "bytes=0-3")
	recorder := serve(router, request)
	if http.StatusPartialContent != recorder.Code {
		t.Fatalf("Range status = %d, want 206", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Encoding"); "" != got {
		t.Errorf("Range Content-Encoding = %q, want nothing", got)
	}
	if 4 != recorder.Body.Len() {
		t.Errorf("Range body = %q, want 4 raw bytes", recorder.Body.String())
	}
}

func TestCompressionLeavesPreEncodedBodiesAlone(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true

	paths := testTree(t)
	router := NewRouter(config, nil, paths, nil)
	router.Register("PreEncoded", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		w.Header().Set("Content-Encoding", "identity")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "plain")
		return true
	})
	router.Register("NoContent", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		w.WriteHeader(http.StatusNoContent)
		return true
	})

	request := get("/?/PreEncoded/0/")
	request.Header.Set("Accept-Encoding", "gzip, br")
	if got := serve(router, request).Header().Get("Content-Encoding"); "identity" != got {
		t.Errorf("Content-Encoding = %q, want the handler value", got)
	}

	request = get("/?/NoContent/0/")
	request.Header.Set("Accept-Encoding", "gzip, br")
	recorder := serve(router, request)
	if http.StatusNoContent != recorder.Code {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Encoding"); "" != got {
		t.Errorf("204 Content-Encoding = %q, want nothing", got)
	}
}

func TestCompressibleContentType(t *testing.T) {
	compressible := []string{
		"",
		"text/html; charset=utf-8",
		"text/css",
		"application/javascript",
		"application/json; charset=utf-8",
		"application/xml",
		"image/svg+xml",
		"text/plain",
	}

	notCompressible := []string{
		"image/png",
		"image/jpeg",
		"audio/mpeg",
		"font/woff2",
		"application/zip",
		"application/octet-stream",
	}

	for _, value := range compressible {
		if !compressibleContentType(value) {
			t.Errorf("compressibleContentType(%q) = false, want true", value)
		}
	}

	for _, value := range notCompressible {
		if compressibleContentType(value) {
			t.Errorf("compressibleContentType(%q) = true, want false", value)
		}
	}
}

func TestAcceptsEncoding(t *testing.T) {
	tests := []struct {
		header   string
		encoding string
		want     bool
	}{
		{"", encodingGzip, false},
		{"gzip", encodingGzip, true},
		{"GZIP", encodingGzip, true},
		{" gzip ", encodingGzip, true},
		{"gzip, br", encodingBrotli, true},
		{"gzip, br", encodingGzip, true},
		{"br", encodingGzip, false},
		{"gzip;q=0", encodingGzip, false},
		{"gzip;q=0.001", encodingGzip, true},
		{"gzip;q=0, br", encodingBrotli, true},
		{"gzip; q=0", encodingGzip, false},
		{"deflate", encodingGzip, false},
		{"*", encodingGzip, false}, // the wildcard is not expanded
	}

	for _, test := range tests {
		request := get("/")
		if "" != test.header {
			request.Header.Set("Accept-Encoding", test.header)
		}

		if got := acceptsEncoding(request, test.encoding); got != test.want {
			t.Errorf("acceptsEncoding(%q, %q) = %v, want %v",
				test.header, test.encoding, got, test.want)
		}
	}
}

func TestCompressWriterFlushesAndUnwraps(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true

	paths := testTree(t)
	router := NewRouter(config, nil, paths, nil)

	var compressed http.ResponseWriter

	router.Register("Stream", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		compressed = w
		_, _ = io.WriteString(w, "chunk")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "-two")
		return true
	})

	request := get("/?/Stream/0/")
	request.Header.Set("Accept-Encoding", "br")
	recorder := serve(router, request)

	if body := readBody(t, recorder); "chunk-two" != body {
		t.Errorf("body = %q, want chunk-two", body)
	}

	controller := http.NewResponseController(compressed)
	if err := controller.Flush(); nil != err {
		t.Errorf("ResponseController.Flush: %v", err)
	}
}

func TestCompressWriterHandlesEmptyBody(t *testing.T) {
	config := DefaultConfig()
	config.CompressOutput = true

	paths := testTree(t)
	router := NewRouter(config, nil, paths, nil)
	router.Register("Empty", func(w http.ResponseWriter, r *http.Request, route *Route) bool {
		return true
	})

	request := get("/?/Empty/0/")
	request.Header.Set("Accept-Encoding", "br")
	recorder := serve(router, request)

	if encodingBrotli != recorder.Header().Get("Content-Encoding") {
		t.Fatalf("Content-Encoding = %q, want br", recorder.Header().Get("Content-Encoding"))
	}

	// An announced brotli body must at least be a decodable empty stream.
	body, err := io.ReadAll(brotli.NewReader(recorder.Body))
	if nil != err {
		t.Fatalf("decompress brotli body: %v", err)
	}
	if 0 != len(body) {
		t.Errorf("body = %q, want nothing", body)
	}
}
