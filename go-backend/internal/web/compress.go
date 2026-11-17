package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"

	"github.com/xi0/snappymail/go-backend/internal/httpx"
)

// Content codings of the output compression, in PHP preference order: with the
// php-ext-brotli extension RunResult() uses brotli, else ob_gzhandler.
const (
	encodingBrotli = "br"
	encodingGzip   = "gzip"
)

// compressWriter is the writer handed to the handlers when
// webmail.compress_output is enabled.
type compressWriter struct {
	http.ResponseWriter

	// encoding is the negotiated content coding (br or gzip).
	encoding string
	// codec is the encoder, created by the first WriteHeader() that can be
	// compressed. It is nil for bodies that must stay untouched.
	codec io.WriteCloser
	// wroteHeader guards against the second WriteHeader()/Write() pair.
	wroteHeader bool
}

// compress wraps writer with a br/gzip encoder when webmail.compress_output is
// enabled and the client accepts one of them. The returned function must be
// called once the response is complete.
//
// The actual coding is decided in WriteHeader(), because PHP's output handlers
// run when the body is produced:
//
//   - brotli is preferred over gzip (Service::RunResult() checks for the
//     php-ext-brotli function first);
//   - HEAD and Range requests are left alone, so a partial response never gets
//     a Content-Encoding the client would have to decode before slicing;
//   - already encoded bodies (a handler set Content-Encoding itself) are not
//     encoded twice;
//   - binary media (images, audio, fonts) is not compressed, which matches what
//     the Apache/mod_deflate deployment does for static assets.
func (rt *Router) compress(writer http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if !rt.config.CompressOutput {
		return writer, func() {}
	}

	encoding := ""
	switch {
	case acceptsEncoding(r, encodingBrotli):
		encoding = encodingBrotli
	case acceptsEncoding(r, encodingGzip):
		encoding = encodingGzip
	default:
		return writer, func() {}
	}

	if http.MethodHead == r.Method || httpx.HasHeader(r, "Range") {
		return writer, func() {}
	}

	compressed := &compressWriter{ResponseWriter: writer, encoding: encoding}

	return compressed, compressed.Close
}

// WriteHeader decides whether the response can be encoded, announces the
// encoding and forwards the status.
func (c *compressWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true

	if c.canEncode(status) {
		header := c.Header()
		// The length describes the uncompressed body, so it no longer applies.
		header.Del("Content-Length")
		header.Set("Content-Encoding", c.encoding)
		header.Add("Vary", "Accept-Encoding")

		c.codec = newCodec(c.ResponseWriter, c.encoding)
	}

	c.ResponseWriter.WriteHeader(status)
}

// canEncode reports whether the response may be compressed: it must carry a
// body, must not be encoded already and must be a text-like type.
func (c *compressWriter) canEncode(status int) bool {
	// 204/304/1xx carry no body, so encoding them would be a lie.
	if 200 > status || http.StatusNoContent == status || http.StatusNotModified == status {
		return false
	}

	header := c.Header()
	if "" != header.Get("Content-Encoding") {
		return false
	}

	return compressibleContentType(header.Get("Content-Type"))
}

// Write compresses the body (or passes it through when the response is not
// compressible).
func (c *compressWriter) Write(body []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	if nil == c.codec {
		return c.ResponseWriter.Write(body)
	}

	return c.codec.Write(body)
}

// Flush implements http.Flusher so long streaming responses (RawView, message
// downloads) reach the client before the body is complete.
func (c *compressWriter) Flush() {
	if flusher, ok := c.codec.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}

	if flusher, ok := c.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (c *compressWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// Close finishes the compressed stream. It must be called exactly once.
//
// A handler that wrote neither a status nor a body still gets the announced
// encoding, so the header never promises an encoding the body does not have
// (the encoder then produces a valid empty stream).
func (c *compressWriter) Close() {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}

	if nil != c.codec {
		_ = c.codec.Close()
		c.codec = nil
	}
}

// newCodec builds the encoder of a content coding.
func newCodec(writer io.Writer, encoding string) io.WriteCloser {
	if encodingBrotli == encoding {
		// Deviation from PHP: php-ext-brotli's brotli_compress_init() defaults
		// to quality 11, the Go library defaults to 6. The level only changes
		// the ratio/CPU trade-off, not the semantics of the response.
		return brotli.NewWriter(writer)
	}

	return gzip.NewWriter(writer)
}

// compressibleContentTypes are the content type prefixes worth compressing.
var compressibleContentTypes = []string{
	"text/",
	"application/json",
	"application/javascript",
	"application/x-javascript",
	"application/xml",
	"application/xhtml+xml",
	"application/rss+xml",
	"application/atom+xml",
	"application/ld+json",
	"application/manifest+json",
	"application/wasm",
	"image/svg+xml",
	"font/ttf",
	"font/otf",
	"application/vnd.ms-fontobject",
}

// compressibleContentType reports whether a Content-Type benefits from
// compression. An unset Content-Type counts as compressible: the handler did
// not say what it is, and PHP's output handlers compress those too.
func compressibleContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if "" == contentType {
		return true
	}

	mediaType := contentType
	if semicolon := strings.Index(mediaType, ";"); 0 <= semicolon {
		mediaType = strings.TrimSpace(mediaType[:semicolon])
	}

	for _, compressible := range compressibleContentTypes {
		if mediaType == compressible || strings.HasPrefix(mediaType, compressible) {
			return true
		}
	}

	return false
}

// acceptsEncoding reports whether the Accept-Encoding header allows encoding
// (a q=0 parameter disables it).
//
// Deviation from PHP: ob_gzhandler() and the brotli handler also honour
// q-values, but the RunResult() brotli branch only looks for the "br"
// substring, so `br;q=0, gzip` picks brotli there. Respecting q=0 (and falling
// back to gzip) is the correct reading of RFC 9110.
func acceptsEncoding(r *http.Request, encoding string) bool {
	for _, entry := range strings.Split(httpx.GetHeader(r, "Accept-Encoding"), ",") {
		parts := strings.Split(entry, ";")

		if !strings.EqualFold(strings.TrimSpace(parts[0]), encoding) {
			continue
		}

		allowed := true
		for _, parameter := range parts[1:] {
			parameter = strings.ToLower(strings.TrimSpace(parameter))
			if !strings.HasPrefix(parameter, "q=") {
				continue
			}
			if factor, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64); nil == err && 0 >= factor {
				allowed = false
			}
		}

		return allowed
	}

	return false
}
