// Package httpx ports the HTTP request/response helpers of
// \MailSo\Base\Http, the URL path helpers of \RainLoop\Utils and the small
// string/format helpers the handlers and services share.
//
// Everything here is deliberately a one-to-one port: the PHP helpers are a
// compatibility contract (redirect targets, host validation, client IP
// selection, cache headers, Content-Disposition layout), so the quirks are
// kept instead of "improved".
package httpx

import (
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// bracketedIPv6 matches a bracketed IPv6 address with an optional port,
	// like PHP's '/^\[([0-9a-f:.]+)\](?::\d{1,5})?$/iD'.
	bracketedIPv6 = regexp.MustCompile(`(?i)^\[([0-9a-f:.]+)\](?::\d{1,5})?$`)
	// hostnamePattern matches a hostname with an optional port, like PHP's
	// '/^[a-z0-9_](?:[a-z0-9_.-]*[a-z0-9_])?\.?(?::\d{1,5})?$/iD'.
	hostnamePattern = regexp.MustCompile(`(?i)^[a-z0-9_](?:[a-z0-9_.-]*[a-z0-9_])?\.?(?::\d{1,5})?$`)
)

const (
	// dateRFC822 is PHP's DATE_RFC822 ("D, d M y H:i:s O").
	dateRFC822 = "Mon, 02 Jan 06 15:04:05 -0700"
	// maxPortLength is the maximum number of digits PHP accepts in ":<port>".
	maxPortLength = 5
)

// Method returns the request method (PHP: $_SERVER['REQUEST_METHOD']).
func Method(r *http.Request) string {
	if nil == r {
		return ""
	}
	return r.Method
}

// IsPost mirrors \MailSo\Base\Http::IsPost().
func IsPost(r *http.Request) bool { return http.MethodPost == Method(r) }

// IsGet mirrors \MailSo\Base\Http::IsGet().
func IsGet(r *http.Request) bool { return http.MethodGet == Method(r) }

// GetHeader mirrors \MailSo\Base\Http::GetHeader().
//
// PHP looks at $_SERVER['HTTP_<NAME>'] and falls back to
// apache_request_headers(); Go normalises both into http.Header, so a single
// lookup is equivalent.
func GetHeader(r *http.Request, name string) string {
	if nil == r {
		return ""
	}
	return r.Header.Get(name)
}

// HasHeader reports whether the request carried the header at all, which some
// PHP checks need to distinguish from an empty value (isset() vs. empty()).
func HasHeader(r *http.Request, name string) bool {
	if nil == r {
		return false
	}
	_, ok := r.Header[http.CanonicalHeaderKey(name)]
	return ok
}

// RequestURI mirrors $_SERVER['REQUEST_URI'] (Http::GetUrl()).
//
// The origin-form target is built from the URL rather than from
// http.Request.RequestURI, which holds the raw request line (and therefore the
// absolute URL for proxy style requests).
func RequestURI(r *http.Request) string {
	if nil == r {
		return ""
	}
	if nil != r.URL {
		return r.URL.RequestURI()
	}
	return r.RequestURI
}

// IsSecure mirrors \MailSo\Base\Http::IsSecure($bCheckProxy = true).
func IsSecure(r *http.Request) bool {
	if nil == r {
		return false
	}
	if nil != r.TLS {
		return true
	}
	if 443 == localPort(r) {
		return true
	}
	if "https" == strings.ToLower(GetHeader(r, "X-Forwarded-Proto")) {
		return true
	}
	return "on" == strings.ToLower(GetHeader(r, "X-Forwarded-Ssl"))
}

// Scheme mirrors \MailSo\Base\Http::GetScheme().
func Scheme(r *http.Request) string {
	if IsSecure(r) {
		return "https"
	}
	return "http"
}

// localPort is $_SERVER['SERVER_PORT'] for the connection the request arrived
// on (not the Host header, which is client controlled).
func localPort(r *http.Request) int {
	if nil == r {
		return 0
	}
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || nil == addr {
		return 0
	}
	_, port, err := net.SplitHostPort(addr.String())
	if nil != err {
		return 0
	}
	value, err := strconv.Atoi(port)
	if nil != err {
		return 0
	}
	return value
}

// Host mirrors \MailSo\Base\Http::GetHost().
//
// HTTP_HOST is client controlled, so only a syntactically valid hostname or IP
// (with an optional port) is accepted. Everything else yields "" instead of
// leaking into logs, redirects and outgoing requests.
func Host(r *http.Request, withoutWWW bool, withoutPort bool) string {
	host := ""
	if nil != r {
		host = r.Host
	}

	if "" == strings.TrimSpace(host) {
		// PHP falls back to SERVER_NAME:port; Go has no server name, only the
		// local port, so the fallback is empty.
		return ""
	}

	host = ValidHost(host)
	if withoutWWW {
		host = strings.TrimPrefix(host, "www.")
	}
	if withoutPort {
		host = stripPort(host)
	}

	return host
}

// ValidHost returns value when it is a valid hostname or IP with an optional
// port, else "". Port of \MailSo\Base\Http::getValidHost().
func ValidHost(value string) string {
	host := strings.TrimSpace(value)
	if "" == host || strings.Contains(host, "..") {
		return ""
	}

	if IsValidIP(host) {
		return host
	}

	if matches := bracketedIPv6.FindStringSubmatch(host); nil != matches && IsValidIP(matches[1]) {
		return host
	}

	if hostnamePattern.MatchString(host) {
		return host
	}

	return ""
}

// stripPort removes a trailing ":<port>" from a host, keeping bracketed IPv6
// addresses intact (PHP: preg_replace('/^(\[[0-9a-f:.]+\]|[^:]+):\d+$/i', '$1')).
func stripPort(host string) string {
	if strings.HasPrefix(host, "[") {
		if end := strings.LastIndex(host, "]"); 0 < end {
			return host[:end+1]
		}
		return host
	}

	colon := strings.Index(host, ":")
	if 0 > colon || colon != strings.LastIndex(host, ":") {
		// No port, or more than one colon (an unbracketed IPv6 address).
		return host
	}

	port := host[colon+1:]
	if "" != port && len(port) <= maxPortLength && isDigits(port) {
		return host[:colon]
	}

	return host
}

// isDigits reports whether value consists of ASCII digits only.
func isDigits(value string) bool {
	if "" == value {
		return false
	}
	for i := 0; i < len(value); i++ {
		if '0' > value[i] || '9' < value[i] {
			return false
		}
	}
	return true
}

// IsValidIP mirrors \MailSo\Base\Utils::ValidateIP() (filter_var with
// FILTER_VALIDATE_IP: IPv4 or IPv6, nothing else).
func IsValidIP(value string) bool {
	return "" != value && nil != net.ParseIP(value)
}

// ClientIP mirrors \MailSo\Base\Http::GetClientIp().
//
// The proxy headers are only honoured when the caller asks for it, because
// they are client controlled unless every direct request comes from a trusted
// reverse proxy (labs.http_client_ip_check_proxy).
func ClientIP(r *http.Request, checkProxy bool) string {
	if checkProxy {
		for _, name := range []string{"Client-IP", "X-Forwarded-For"} {
			if ip := firstValidIP(GetHeader(r, name)); "" != ip {
				return ip
			}
		}
	}

	return firstValidIP(remoteAddr(r))
}

// remoteAddr is $_SERVER['REMOTE_ADDR'].
func remoteAddr(r *http.Request) string {
	if nil == r {
		return ""
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); nil == err {
		return host
	}
	return r.RemoteAddr
}

// firstValidIP returns the first valid IP of a comma separated header value.
func firstValidIP(value string) string {
	for _, candidate := range strings.Split(value, ",") {
		if candidate = strings.TrimSpace(candidate); IsValidIP(candidate) {
			return candidate
		}
	}
	return ""
}

// statusTexts is the status map of \MailSo\Base\Http::StatusHeader().
var statusTexts = map[int]string{
	200: "OK",
	206: "Partial Content",
	301: "Moved Permanently",
	302: "Found",
	304: "Not Modified",
	400: "Bad Request",
	401: "Unauthorized",
	403: "Forbidden",
	404: "Not Found",
	405: "Method Not Allowed",
	412: "Precondition Failed",
	416: "Requested range not satisfiable",
	500: "Internal Server Error",
}

// StatusText returns PHP's reason phrase for a status code, else "".
func StatusText(code int) string { return statusTexts[code] }

// Location mirrors \MailSo\Base\Http::Location() (status defaults to 302).
func Location(w http.ResponseWriter, target string, status int) {
	if 100 > status {
		status = http.StatusFound
	}
	w.Header().Set("Location", target)
	w.WriteHeader(status)
}

// NoStore mirrors \MailSo\Base\Http::ServerNoCache().
func NoStore(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Expires", "Mon, 26 Jul 1997 05:00:00 GMT")
	header.Set("Last-Modified", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05")+" GMT")
	header.Set("Cache-Control", "no-store")
	header.Set("Pragma", "no-cache")
}

// Disposition is the parameter set of
// \MailSo\Base\Http::setContentDisposition().
type Disposition struct {
	// Type is "inline" or "attachment".
	Type string
	// Filename is sent as filename="..." for ASCII names and as an RFC 5987
	// filename*=utf-8''... attribute otherwise.
	Filename string
	// CreationDate, ModificationDate and ReadDate are omitted when zero.
	CreationDate     time.Time
	ModificationDate time.Time
	ReadDate         time.Time
	// Size is only sent when it is non-zero.
	Size int64
}

// SetContentDisposition mirrors
// \MailSo\Base\Http::setContentDisposition().
func SetContentDisposition(w http.ResponseWriter, disposition Disposition) {
	params := []string{disposition.Type}

	if "" != disposition.Filename {
		if isAsciiFilename(disposition.Filename) {
			params = append(params, `filename="`+disposition.Filename+`"`)
		} else {
			// PHP: rawurlencode(), no charset/language, RFC 5987.
			params = append(params, "filename*=utf-8''"+RawURLEncode(disposition.Filename))
		}
	}

	for _, date := range []struct {
		key   string
		value time.Time
	}{
		{"creation-date", disposition.CreationDate},
		{"modification-date", disposition.ModificationDate},
		{"read-date", disposition.ReadDate},
	} {
		if !date.value.IsZero() {
			params = append(params, date.key+"="+date.value.UTC().Format(dateRFC822))
		}
	}

	if 0 != disposition.Size {
		params = append(params, "size="+strconv.FormatInt(disposition.Size, 10))
	}

	w.Header().Set("Content-Disposition", strings.Join(params, "; "))
}

// isAsciiFilename mirrors PHP's preg_match('#^[\x01-\x7F]*$#D').
func isAsciiFilename(name string) bool {
	for i := 0; i < len(name); i++ {
		if 1 > name[i] || 0x7F < name[i] {
			return false
		}
	}
	return true
}

// RawURLEncode mirrors PHP's rawurlencode(): every byte outside the unreserved
// set is percent encoded with upper case hex digits.
func RawURLEncode(value string) string {
	const unreserved = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_.~"
	const hexDigits = "0123456789ABCDEF"

	var builder strings.Builder
	builder.Grow(len(value))

	for i := 0; i < len(value); i++ {
		c := value[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			builder.WriteByte(c)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[c>>4])
		builder.WriteByte(hexDigits[c&0x0F])
	}

	return builder.String()
}

// FormatFileSize is \MailSo\Base\Utils::FormatFileSize().
func FormatFileSize(size int64, round int) string {
	units := []string{"B", "KB", "MB"}

	index := 0
	value := float64(size)
	for value > 1024 && index+1 < len(units) {
		value /= 1024
		index++
	}

	return FormatFloat(Round(value, round)) + units[index]
}

// Round mirrors PHP's round($value, $precision) (half away from zero).
func Round(value float64, precision int) float64 {
	factor := 1.0
	for i := 0; i < precision; i++ {
		factor *= 10
	}

	if 0 > value {
		return -float64(int64(-value*factor+0.5)) / factor
	}

	return float64(int64(value*factor+0.5)) / factor
}

// FormatFloat formats a float the way PHP casts it to string: no exponent, no
// trailing zeros ("1.5", "2", "-0.25").
func FormatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
