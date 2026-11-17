package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// TrimPath mirrors the trim(\trim($s), ' /') the path vector applies.
func TrimPath(value string) string {
	return strings.Trim(strings.Trim(value, " \t\n\r\x00\x0B"), " /")
}

// QueryPath returns the string Service::RunResult() explodes into the path
// vector:
//
//   - QUERY_STRING up to the first "&",
//   - plus every element of $_GET['q'] (each trimmed of " /"),
//   - plus the PATH_INFO remainder of "/index.php/..." requests, which Apache
//     currently rewrites into the query string.
//
// The result is not trimmed again, so the explode() semantics (including the
// single empty element of an empty string) are preserved by Paths().
func QueryPath(r *http.Request) string {
	if nil == r {
		return ""
	}

	query := ""
	if nil != r.URL {
		query = strings.TrimSpace(r.URL.RawQuery)
	}
	if position := strings.Index(query, "&"); 0 < position {
		query = query[:position]
	}
	query = TrimPath(query)

	if sub := subQuery(r); 0 < len(sub) {
		query += "/" + strings.Join(sub, "/")
	}

	if info := pathInfo(r); "" != info {
		if "" == query {
			// Unlike the $_GET['q'] append above, PATH_INFO always starts at the
			// first segment (Apache hands over "Raw/0/..." without a leading
			// slash), so the vector must not start with an empty element.
			query = info
		} else {
			query += "/" + info
		}
	}

	return query
}

// Paths returns the path vector: PHP's explode('/', $sQuery).
//
// Note that an empty query yields one empty element, exactly like explode().
func Paths(r *http.Request) []string {
	return strings.Split(QueryPath(r), "/")
}

// subQuery returns the trimmed elements of $_GET['q'] when it is a flat array,
// mirroring PHP's is_array($_GET['q']) check plus the array_map(trim) around
// it. A scalar "q=" is ignored (it is not an array in PHP either), and the
// order of the parameters in the query string is kept because it decides the
// final path vector.
func subQuery(r *http.Request) []string {
	if nil == r || nil == r.URL || "" == r.URL.RawQuery {
		return nil
	}

	var result []string

	for _, pair := range strings.Split(r.URL.RawQuery, "&") {
		if "" == pair {
			continue
		}

		key, value := pair, ""
		if position := strings.Index(pair, "="); 0 <= position {
			key, value = pair[:position], pair[position+1:]
		}

		if decoded, err := url.QueryUnescape(key); nil == err {
			key = decoded
		}
		if !isSubQueryKey(key) {
			continue
		}

		if decoded, err := url.QueryUnescape(value); nil == err {
			value = decoded
		}

		result = append(result, TrimPath(value))
	}

	return result
}

// isSubQueryKey reports whether key addresses a single element of $_GET['q'],
// i.e. "q[]" or "q[<key>]" (but not the scalar "q" and not a nested array).
func isSubQueryKey(key string) bool {
	if !strings.HasPrefix(key, "q[") || !strings.HasSuffix(key, "]") {
		return false
	}

	inner := key[len("q["):]

	return "]" == inner || !strings.Contains(inner[:len(inner)-1], "[")
}

// pathInfo returns the PATH_INFO part of "/index.php/..." style requests.
//
// index.php in the install root is the PHP front controller; Apache's
// "RewriteRule ^(.+)$ index.php/$1" together with "AcceptPathInfo On" hands
// everything after it over as PATH_INFO, which the Go backend has to fold back
// into the path vector because it serves the request itself.
func pathInfo(r *http.Request) string {
	if nil == r || nil == r.URL {
		return ""
	}

	path := r.URL.Path
	position := strings.Index(strings.ToLower(path), "/index.php")
	if 0 > position {
		return ""
	}

	rest := path[position+len("/index.php"):]
	if "" == rest || !strings.HasPrefix(rest, "/") {
		// "/index.php" is the front controller itself, "/index.php.bak" is not
		// a front controller request at all.
		return ""
	}

	return TrimPath(rest)
}

// SplitSegment splits a path vector element into the service name and its
// "@extra" suffix, mirroring
//
//	$sMethodName = 'Service'.\preg_replace('/@.+$/', '', $aPaths[0]);
//	$sMethodExtra = \strpos($aPaths[0], '@') ? \preg_replace('/^[^@]+@/', '', $aPaths[0]) : '';
//
// Note the PHP quirks that are kept: a trailing "@" stays part of the name (the
// regex needs at least one character after it) and a segment starting with "@"
// has no extra part (strpos() returning 0 is falsey).
func SplitSegment(segment string) (name string, extra string) {
	name = segment

	position := strings.Index(segment, "@")
	if 0 > position {
		return name, ""
	}

	if position+1 < len(segment) {
		name = segment[:position]
		if 0 < position {
			extra = segment[position+1:]
		}
	}

	return name, extra
}
