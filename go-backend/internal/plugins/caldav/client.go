package caldav

// The CalDAV transport: a port of CaldavPlugin::makeCalDAVRequest(), which uses
// curl in PHP.
//
// The observable parts are kept:
//
//   - Basic authentication with the CalDAV user name and the (decrypted) account
//     password, and certificate verification against the system trust store;
//   - an *explicit* header list per request: the PROPFIND defaults
//     ("Content-Type: application/xml; charset=utf-8", "Depth: 1") are only used
//     when the caller passes none, so MKCALENDAR/PROPPATCH/PUT/DELETE never
//     inherit a "Depth" header some servers reject;
//   - redirects are not followed (curl's default without CURLOPT_FOLLOWLOCATION),
//     so a 301/302 to a login page is reported as the status it is instead of
//     being turned into a GET of the target;
//   - no timeout, like curl without CURLOPT_TIMEOUT: a CalDAV request may
//     legitimately take long, and the request handler is bounded by the client's
//     own timeout.
//
// The transport failure of curl ($result['error']) is reported in Error with the
// same "empty on success" contract; the HTTP status is 0 then, which no caller
// treats as success.

import (
	"io"
	"net/http"
	"strings"
)

// calDAVDefaultHeaders is the fallback header list of makeCalDAVRequest().
var calDAVDefaultHeaders = []string{
	"Content-Type: application/xml; charset=utf-8",
	"Depth: 1",
}

// defaultClient is the *http.Client of a production request.
//
// Redirects are not followed (http.ErrUseLastResponse), which mirrors curl
// without CURLOPT_FOLLOWLOCATION.
var defaultClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// calDAVResponse is the array makeCalDAVRequest() returns: the HTTP status, the
// response body and the transport error.
type calDAVResponse struct {
	// Code is the HTTP status code (0 when the request never completed).
	Code int
	// Body is the response body.
	Body string
	// Error is the curl_error() equivalent: empty on success.
	Error string
}

// client is the HTTP client of the plugin.
func (p *Plugin) client() *http.Client {
	if nil != p && nil != p.HTTP {
		return p.HTTP
	}
	return defaultClient
}

// makeCalDAVRequest is CaldavPlugin::makeCalDAVRequest(): one CalDAV request
// with Basic authentication.
//
// body is the request payload (PHP's $body, empty for none) and headers the
// explicit header list ("Name: value"); an empty list selects
// calDAVDefaultHeaders, like PHP's "$headers ?: [...]".
func (p *Plugin) makeCalDAVRequest(
	uri, method, username, password, body string,
	headers ...string,
) calDAVResponse {
	list := headers
	if 0 == len(list) {
		list = calDAVDefaultHeaders
	}

	var payload io.Reader
	if "" != body {
		payload = strings.NewReader(body)
	}

	request, err := http.NewRequest(method, uri, payload)
	if nil != err {
		return calDAVResponse{Error: err.Error()}
	}

	for _, header := range list {
		name, value, found := strings.Cut(header, ":")
		if !found {
			continue
		}
		request.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	// CURLOPT_HTTPAUTH = CURLAUTH_BASIC with CURLOPT_USERPWD.
	request.SetBasicAuth(username, password)

	response, err := p.client().Do(request)
	if nil != err {
		return calDAVResponse{Error: err.Error()}
	}
	defer func() {
		_ = response.Body.Close()
	}()

	content, err := io.ReadAll(response.Body)
	if nil != err {
		return calDAVResponse{Code: response.StatusCode, Error: err.Error()}
	}

	return calDAVResponse{Code: response.StatusCode, Body: string(content)}
}
