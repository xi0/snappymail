// Package httpclient ports SnappyMail\HTTP\Request (the pure PHP "socket"
// driver) and SnappyMail\HTTP\Response: the small outbound HTTP client the
// ?/Test report (step 13b) and the external image proxy (step 13c) share.
//
// The parts that are a contract for the call sites are ported one-to-one:
//
//   - only http and https URIs may be fetched (Request::URIHasAllowedScheme()),
//   - a redirect is only followed for GET and HEAD and only on
//     301/302/303/307, at most MaxRedirects times (Request::doRequest()),
//   - Location resolution of root-relative, relative and protocol-relative
//     targets (Response::getRedirectLocation()),
//   - the body is a single string capped at MaxResponseKB, or it is streamed to
//     a caller supplied writer (Request::streamBodyTo()),
//   - a gzip encoded body is decoded (Response::__construct()).
//
// Deviations, each documented where it matters:
//
//   - the client never announces an Accept-Encoding itself, exactly like PHP;
//     a caller that wants gzip/br has to add the header,
//   - PHP's Digest authentication branch calls \split(), which was removed in
//     PHP 7, so it can never run; only Basic and Bearer are implemented,
//   - a streaming response keeps the raw (possibly gzip encoded) bytes instead
//     of letting the body decoder throw on the empty string,
//   - a redirect without a Location header ends the chain instead of turning
//     the next attempt into an un-fetchable "null" URL,
//   - the header map cannot keep the order the headers arrived in.
package httpclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The authentication options of SnappyMail\HTTP\Request; they are bitwise.
const (
	AuthBasic  = 1
	AuthDigest = 2
	AuthBearer = 4
)

// The Request property defaults.
const (
	// DefaultTimeout is the $timeout default (5 seconds).
	DefaultTimeout = 5 * time.Second
	// DefaultMaxResponseKB is the $max_response_kb default (1 MB).
	DefaultMaxResponseKB = 1024
	// DefaultMaxRedirects is the $max_redirects default (no redirect).
	DefaultMaxRedirects = 0
	// UserAgentPrefix is prefixed to APP_VERSION by New().
	UserAgentPrefix = "SnappyMail/"
)

// Errors of the client.
var (
	// ErrScheme is the URIHasAllowedScheme() refusal.
	ErrScheme = errors.New("httpclient: uri scheme not allowed")
	// ErrTimeout is the "return null" of Request::doRequest() when the time
	// budget is spent before a response arrived.
	ErrTimeout = errors.New("httpclient: request timed out")
)

var (
	// schemePattern is Request::URIHasAllowedScheme(): '#^https?://#i'.
	schemePattern = regexp.MustCompile(`(?i)^https?://`)

	// The two patterns of Response::getRedirectLocation(): a URI with a host,
	// and the origin plus path of the URI that produced the response.
	absoluteURIPattern = regexp.MustCompile(`(?i)^([a-z][a-z0-9+\-.]+:)?//[^/]+`)
	originPathPattern  = regexp.MustCompile(`(?i)^((?:[a-z][a-z0-9+\-.]+:)?//[^/]+)(/[^?#]*)`)
)

// URIHasAllowedScheme mirrors Request::URIHasAllowedScheme(): by default any
// http or https URI may be fetched.
func URIHasAllowedScheme(uri string) bool { return schemePattern.MatchString(uri) }

// Response is SnappyMail\HTTP\Response: the final response of a request chain.
type Response struct {
	// RequestURI is the URI that was passed to Do().
	RequestURI string
	// FinalURI is the URI of the response after following redirects.
	FinalURI string
	// Status is the HTTP status code of the response of FinalURI.
	Status int
	// Headers are the response headers of FinalURI. Go canonicalises the
	// names, so a lookup is case-insensitive just like the lower cased PHP map.
	Headers http.Header
	// Body is the response body. It is empty when the body was streamed.
	Body []byte
}

// GetHeader is Response::getHeader(): the value of the first name that is
// present, or "" when none is.
//
// PHP returns an array for a repeated header; HeaderValues() is that variant.
func (r *Response) GetHeader(names ...string) string {
	if nil == r {
		return ""
	}

	for _, name := range names {
		if values := r.Headers.Values(name); 0 < len(values) {
			return values[0]
		}
	}

	return ""
}

// HeaderValues is the array form of Response::getHeader(): every value of a
// repeated header.
func (r *Response) HeaderValues(name string) []string {
	if nil == r {
		return nil
	}
	return r.Headers.Values(name)
}

// RedirectLocation is Response::getRedirectLocation(): the absolute target of
// the Location header, or "" when the response carries none.
//
// A relative Location can only be resolved when the URI of the response has a
// path; against a bare "http://host" the PHP pattern does not match and the
// target is returned unchanged, so the next fetch fails the scheme check. That
// quirk is kept.
func (r *Response) RedirectLocation() string {
	uri := r.GetHeader("location")
	if "" == uri {
		return ""
	}

	if !absoluteURIPattern.MatchString(uri) {
		// No host: resolve the target against the URI of the response.
		origin := originPathPattern.FindStringSubmatch(r.FinalURI)
		if nil == origin {
			// PHP builds an undefined $match and returns the URI unchanged.
			return uri
		}

		path := origin[2]
		slash := strings.LastIndex(path, "/")
		if 0 > slash {
			// The pattern guarantees a leading '/', so this is defensive.
			slash = 0
		}

		if '/' == uri[0] {
			// Absolute path.
			uri = origin[1] + uri
		} else {
			// Relative path: the directory of the current path.
			uri = origin[1] + path[:slash+1] + uri
		}
	}

	if strings.HasPrefix(uri, "//") {
		// Protocol relative: the scheme of the original request decides.
		if scheme, _, found := strings.Cut(r.RequestURI, ":"); found {
			uri = scheme + ":" + uri
		}
	}

	return uri
}

// credentials is the $auth property of SnappyMail\HTTP\Request.
type credentials struct {
	kind int
	user string
	pass string
}

// Client is SnappyMail\HTTP\Request with the socket driver as backend.
//
// The zero value has no timeout, no response cap and no user agent; New()
// holds the PHP defaults and is what the endpoints use.
type Client struct {
	// Timeout is $timeout: the connect/read budget of one request and of the
	// whole redirect chain. Zero means "no timeout".
	Timeout time.Duration
	// MaxResponseKB is $max_response_kb; <= 0 reads the whole body.
	MaxResponseKB int
	// UserAgent is $user_agent ("SnappyMail/<APP_VERSION>" by default).
	UserAgent string
	// MaxRedirects is $max_redirects; negative values count as zero.
	MaxRedirects int
	// VerifyPeer is $verify_peer. When it and CABundle are both unset the
	// certificate is not verified, which is what PHP's allow_self_signed
	// amounts to.
	VerifyPeer bool
	// CABundle is $ca_bundle, set through SetCABundleFile().
	CABundle string

	// headers are the lines added with AddHeader() (PHP: $this->headers).
	headers []string
	// stream is the body target of StreamBodyTo() (PHP: $this->stream).
	stream io.Writer
	// auth is $this->auth.
	auth credentials
}

// New builds a client with the Request defaults: a 5 second timeout, a 1 MB
// response cap, no redirect and the versioned user agent.
func New(version string) *Client {
	return &Client{
		Timeout:       DefaultTimeout,
		MaxResponseKB: DefaultMaxResponseKB,
		UserAgent:     UserAgentPrefix + version,
		MaxRedirects:  DefaultMaxRedirects,
	}
}

// AddHeader is Request::addHeader(): queue a raw "Name: value" header line.
func (c *Client) AddHeader(header string) *Client {
	c.headers = append(c.headers, header)
	return c
}

// SetAuthBasic is Request::setAuth(self::AUTH_BASIC, …).
func (c *Client) SetAuthBasic(user, pass string) *Client {
	c.auth = credentials{kind: AuthBasic, user: user, pass: pass}
	return c
}

// SetAuthBearer is Request::setAuth(self::AUTH_BEARER, …); the password field
// carries the token, like in PHP.
func (c *Client) SetAuthBearer(user, token string) *Client {
	c.auth = credentials{kind: AuthBearer, user: user, pass: token}
	return c
}

// StreamBodyTo is Request::streamBodyTo(): write the body to stream instead of
// keeping it in Response.Body. A nil stream restores the buffered mode.
func (c *Client) StreamBodyTo(stream io.Writer) {
	c.stream = stream
}

// SetCABundleFile is Request::setCABundleFile().
func (c *Client) SetCABundleFile(file string) {
	c.CABundle = file
}

// CanFetchURI is Request::canFetchURI(): the scheme allowlist plus the SSL
// support, which a Go binary always has.
func (c *Client) CanFetchURI(uri string) bool { return URIHasAllowedScheme(uri) }

// Do is Request::doRequest(): perform the request and return the response of
// the end of the redirect chain.
//
// body may be nil, a string, []byte, url.Values or map[string]string (both are
// http_build_query()'d). A GET body is appended to the query string, like PHP.
// extraHeaders are merged over the AddHeader() lines.
func (c *Client) Do(method, requestURI string, body any, extraHeaders http.Header) (*Response, error) {
	verb := strings.ToUpper(method)

	payload, err := encodeBody(body)
	if nil != err {
		return nil, err
	}

	uri := requestURI
	if 0 < len(payload) && http.MethodGet == verb {
		separator := "?"
		if strings.Contains(uri, "?") {
			separator = "&"
		}
		uri += separator + string(payload)
		payload = nil
	}

	timeout := c.timeout()
	deadline := time.Now().Add(timeout)
	redirects := c.maxRedirects()

	var response *Response

	for {
		if !URIHasAllowedScheme(uri) {
			return nil, fmt.Errorf("%w: %s", ErrScheme, uri)
		}

		if nil != response {
			if 0 < timeout && !time.Now().Before(deadline) {
				return nil, ErrTimeout
			}
		}

		// PHP rewinds the body target before every attempt ($this->stream &&
		// \rewind($this->stream)); a redirect re-uses the same target.
		if seeker, ok := c.stream.(io.Seeker); ok {
			if _, err := seeker.Seek(0, io.SeekStart); nil != err {
				return nil, err
			}
		}

		response, err = c.doRequest(verb, uri, payload, extraHeaders)
		if nil != err {
			return nil, err
		}

		// RFC 2616 10.3: a redirect is only followed automatically for GET
		// and HEAD (PHP: in_array($method, ['GET','HEAD'])), and only while
		// the redirect budget lasts (PHP: if ($redirects-- && …)).
		follow := 0 < redirects && isRedirectStatus(response.Status) &&
			(http.MethodGet == verb || http.MethodHead == verb)
		if !follow {
			break
		}

		location := response.RedirectLocation()
		if "" == location {
			// Nothing to follow; PHP would retry with a null URL.
			break
		}

		redirects--
		uri = location
	}

	// PHP sets final_uri to the URI of the response and request_uri to the
	// original argument.
	response.RequestURI = requestURI
	response.FinalURI = uri

	return response, nil
}

// doRequest is one attempt of the Socket driver, including its 401 handling.
func (c *Client) doRequest(method, uri string, payload []byte, extraHeaders http.Header) (*Response, error) {
	authorization := c.authorizationHeader(extraHeaders)

	response, err := c.send(method, uri, payload, extraHeaders, authorization)
	if nil != err {
		return nil, err
	}

	// The Socket driver only sends the credentials after a challenge.
	if http.StatusUnauthorized == response.Status && "" == authorization {
		if header := c.challengeAuthorization(response); "" != header {
			return c.send(method, uri, payload, extraHeaders, header)
		}
	}

	return response, nil
}

// authorizationHeader is the Authorization header the caller supplied itself.
func (c *Client) authorizationHeader(extraHeaders http.Header) string {
	if value := extraHeaders.Get("Authorization"); "" != value {
		return value
	}

	for _, line := range c.headers {
		if name, value, found := splitHeader(line); found && strings.EqualFold("Authorization", name) {
			return value
		}
	}

	return ""
}

// challengeAuthorization is the WWW-Authenticate branch of the Socket driver.
//
// PHP requires a non-empty user for all three schemes and prefers Basic over
// Bearer; Digest is not ported (see the package comment).
func (c *Client) challengeAuthorization(response *Response) string {
	if "" == c.auth.user {
		return ""
	}

	challenge := strings.ToLower(response.GetHeader("www-authenticate"))

	if 0 != c.auth.kind&AuthBasic && strings.Contains(challenge, "basic") {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.auth.user+":"+c.auth.pass))
	}

	if 0 != c.auth.kind&AuthBearer {
		return "Bearer " + c.auth.pass
	}

	return ""
}

// send performs a single HTTP request/response round trip.
func (c *Client) send(method, uri string, payload []byte, extraHeaders http.Header, authorization string) (*Response, error) {
	var body io.Reader
	if nil != payload {
		body = bytes.NewReader(payload)
	}

	request, err := http.NewRequest(method, uri, body)
	if nil != err {
		return nil, err
	}

	header := request.Header
	if "" != c.UserAgent {
		header.Set("User-Agent", c.UserAgent)
	}

	for _, line := range c.headers {
		if name, value, found := splitHeader(line); found {
			header.Add(name, value)
		}
	}
	for name, values := range extraHeaders {
		for _, value := range values {
			header.Add(name, value)
		}
	}

	if nil != payload {
		// PHP: a request with a body defaults to a form encoded content type.
		if "" == header.Get("Content-Type") {
			header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		request.ContentLength = int64(len(payload))
	}

	if "" != authorization {
		header.Set("Authorization", authorization)
	}

	transport, err := c.transport()
	if nil != err {
		return nil, err
	}

	client := &http.Client{
		Transport: transport,
		// Do() follows redirects itself, with PHP's method restriction.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if timeout := c.timeout(); 0 < timeout {
		client.Timeout = timeout
	}

	httpResponse, err := client.Do(request)
	if nil != err {
		if isTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, err
	}
	defer func() {
		_ = httpResponse.Body.Close()
	}()

	return c.read(uri, httpResponse)
}

// transport builds the round trip configuration of one request.
//
// PHP opens a fresh socket with "Connection: Close" for every request, so
// keep-alives are disabled and no proxy is used (the Socket driver ignores
// $proxy as well).
func (c *Client) transport() (*http.Transport, error) {
	tlsConfig, err := c.tlsConfig()
	if nil != err {
		return nil, err
	}

	timeout := c.timeout()

	return &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: timeout,
		}).DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
		// The caller decides about Accept-Encoding (PHP sends none unless a
		// header was added), so net/http must not negotiate gzip itself.
		DisableCompression: true,
		TLSClientConfig:    tlsConfig,
	}, nil
}

// tlsConfig is the stream context setup of the Socket driver.
func (c *Client) tlsConfig() (*tls.Config, error) {
	config := &tls.Config{
		// PHP defaults to verify_peer = false with allow_self_signed.
		InsecureSkipVerify: !c.VerifyPeer && "" == c.CABundle,
	}

	if "" != c.CABundle {
		pool, err := loadCertPool(c.CABundle)
		if nil != err {
			return nil, err
		}
		config.RootCAs = pool
	}

	return config, nil
}

// loadCertPool reads a PEM bundle, or every PEM file of a directory (PHP's
// capath).
//
// Deviation from PHP: OpenSSL looks a hashed path directory up by certificate
// fingerprint, which crypto/x509 cannot do, so the directory entries are read
// as ordinary PEM files.
func loadCertPool(file string) (*x509.CertPool, error) {
	info, err := os.Stat(file)
	if nil != err {
		return nil, err
	}

	pool := x509.NewCertPool()

	if !info.IsDir() {
		if err := appendCertFile(pool, file); nil != err {
			return nil, err
		}
		return pool, nil
	}

	entries, err := os.ReadDir(file)
	if nil != err {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := appendCertFile(pool, filepath.Join(file, entry.Name())); nil != err {
			return nil, err
		}
	}

	return pool, nil
}

// appendCertFile adds the certificates of one PEM file to the pool.
func appendCertFile(pool *x509.CertPool, file string) error {
	content, err := os.ReadFile(file)
	if nil != err {
		return err
	}

	pool.AppendCertsFromPEM(content)

	return nil
}

// read turns the net/http response into a Response: it streams or buffers the
// body and decodes a gzip content coding.
func (c *Client) read(uri string, httpResponse *http.Response) (*Response, error) {
	response := &Response{
		RequestURI: uri,
		FinalURI:   uri,
		Status:     httpResponse.StatusCode,
		Headers:    httpResponse.Header,
	}

	if nil != c.stream {
		// Streaming keeps the raw bytes: PHP writes them straight to the
		// caller's stream and only then looks at the (empty) body.
		if _, err := io.Copy(c.stream, httpResponse.Body); nil != err {
			if isTimeout(err) {
				return nil, ErrTimeout
			}
			return nil, err
		}
		return response, nil
	}

	var reader io.Reader = httpResponse.Body
	if limit := int64(c.MaxResponseKB) * 1024; 0 < limit {
		reader = io.LimitReader(reader, limit)
	}

	body, err := io.ReadAll(reader)
	if nil != err {
		if isTimeout(err) {
			return nil, ErrTimeout
		}
		return nil, err
	}

	if strings.Contains(strings.ToLower(response.GetHeader("content-encoding")), "gzip") {
		decoded, err := gunzip(body)
		if nil != err {
			// Response::__construct() throws when gzinflate() fails.
			return nil, fmt.Errorf("httpclient: %w for %s", err, uri)
		}
		body = decoded
	}

	response.Body = body

	return response, nil
}

// gunzip decodes a gzip encoded body.
//
// PHP slices the header and trailer off before gzinflate(); the Go reader
// validates them instead, which yields the same payload for a valid stream.
func gunzip(body []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if nil != err {
		return nil, err
	}

	decoded, err := io.ReadAll(reader)
	if closeErr := reader.Close(); nil == err {
		err = closeErr
	}

	return decoded, err
}

// encodeBody is http_build_query() plus the string/[]byte passthrough.
func encodeBody(body any) ([]byte, error) {
	switch typed := body.(type) {
	case nil:
		return nil, nil
	case []byte:
		return typed, nil
	case string:
		return []byte(typed), nil
	case url.Values:
		return []byte(typed.Encode()), nil
	case map[string]string:
		values := make(url.Values, len(typed))
		for key, value := range typed {
			values.Set(key, value)
		}
		return []byte(values.Encode()), nil
	}

	return nil, fmt.Errorf("httpclient: unsupported body type %T", body)
}

// splitHeader splits a raw "Name: value" header line.
func splitHeader(line string) (name, value string, found bool) {
	position := strings.Index(line, ":")
	if 0 > position {
		return "", "", false
	}

	return strings.TrimSpace(line[:position]), strings.TrimSpace(line[position+1:]), true
}

// isRedirectStatus reports whether status is one of the redirects the client
// follows (PHP: 301, 302, 303, 307).
func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
		return true
	}
	return false
}

// timeout is $timeout, with negative values treated as zero.
func (c *Client) timeout() time.Duration {
	if 0 > c.Timeout {
		return 0
	}
	return c.Timeout
}

// maxRedirects is max(0, $max_redirects).
func (c *Client) maxRedirects() int {
	if 0 > c.MaxRedirects {
		return 0
	}
	return c.MaxRedirects
}

// isTimeout reports whether err is a deadline of the transport or the client.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return true
	}

	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Timeout()
	}

	return false
}
