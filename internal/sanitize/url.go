package sanitize

import (
	"net/url"
	"strings"
)

// Absolutize resolves a reference against a base URL and normalizes the
// result the way the SimplePie IRI class does: lower-case scheme and host, no
// default port, no dot segments, percent-encoding where it is needed and
// only there. It reports false when the reference cannot be resolved; an
// empty base leaves a relative reference as it is.
func Absolutize(reference, base string) (string, bool) {
	reference = strings.TrimSpace(reference)
	ref, err := url.Parse(escape(reference, ""))
	if err != nil {
		return "", false
	}
	if ref.Scheme != "" && (ref.Opaque != "" || ref.Host == "") {
		// mailto:, data:, javascript: and the like: only the scheme is normalized.
		return strings.ToLower(ref.Scheme) + reference[len(ref.Scheme):], true
	}

	scheme, user, host := ref.Scheme, ref.User, ref.Host
	path, query, hasQuery := ref.EscapedPath(), ref.RawQuery, ref.RawQuery != "" || ref.ForceQuery
	if ref.Scheme == "" {
		b, err := url.Parse(escape(strings.TrimSpace(base), ""))
		if err != nil {
			return "", false
		}
		if b.Scheme == "" {
			return reference, true
		}
		// RFC 3986, section 5.2.2.
		scheme = b.Scheme
		if ref.Host == "" {
			user, host = b.User, b.Host
			switch {
			case path == "":
				path = b.EscapedPath()
				if !hasQuery {
					query, hasQuery = b.RawQuery, b.RawQuery != "" || b.ForceQuery
				}
			case !strings.HasPrefix(path, "/"):
				dir := b.EscapedPath()
				path = dir[:strings.LastIndexByte(dir, '/')+1] + path
			}
		}
	}

	var out strings.Builder
	scheme = strings.ToLower(scheme)
	out.WriteString(scheme)
	out.WriteString("://")
	if user != nil {
		out.WriteString(user.String())
		out.WriteByte('@')
	}
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		if port := host[i+1:]; (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			host = host[:i]
		}
	}
	out.WriteString(escape(host, ""))
	path = removeDotSegments(path)
	if path == "" {
		path = "/"
	}
	out.WriteString(escape(path, "[]"))
	if hasQuery {
		out.WriteByte('?')
		out.WriteString(escape(query, "[]"))
	}
	// The fragment is the reference's own, never the base's; an empty one stays.
	if _, fragment, ok := strings.Cut(reference, "#"); ok {
		out.WriteByte('#')
		out.WriteString(escape(fragment, "[]#"))
	}
	return out.String(), true
}

// removeDotSegments is RFC 3986, section 5.2.4.
func removeDotSegments(path string) string {
	var out []string
	in := path
	for in != "" {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[3:]
		case strings.HasPrefix(in, "./"):
			in = in[2:]
		case strings.HasPrefix(in, "/./"):
			in = in[2:]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[3:]
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case in == "/..":
			in = "/"
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		case in == "." || in == "..":
			in = ""
		default:
			end := strings.IndexByte(in[1:], '/') + 1
			if end == 0 {
				end = len(in)
			}
			out = append(out, in[:end])
			in = in[end:]
		}
	}
	return strings.Join(out, "")
}

const upperHex = "0123456789ABCDEF"

// escape brings the percent-encoding of a URL, or of a part of it, to one
// form: escapes of unreserved characters are decoded, the other ones written
// in upper case, a percent sign that starts no escape is escaped itself, and
// so are the characters that may not appear in a URL and those of extra.
func escape(s, extra string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			decoded := unhex(s[i+1])<<4 | unhex(s[i+2])
			if unreserved(decoded) {
				b.WriteByte(decoded)
			} else {
				b.WriteByte('%')
				b.WriteByte(upperHex[decoded>>4])
				b.WriteByte(upperHex[decoded&15])
			}
			i += 2
		case c == '%' || c <= ' ' || c >= 0x7f || strings.IndexByte("\"<>\\^`{|}", c) >= 0 || strings.IndexByte(extra, c) >= 0:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func unreserved(c byte) bool {
	return alphanumeric(c) || c == '-' || c == '.' || c == '_' || c == '~'
}

func alphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}

// AllowedScheme tells whether a URL may be kept: anything but javascript:
// and schemes that are not plain letters and digits.
func AllowedScheme(u string) bool {
	scheme, _, ok := strings.Cut(u, ":")
	if !ok {
		return true
	}
	if scheme == "" {
		return false
	}
	for i := 0; i < len(scheme); i++ {
		if !alphanumeric(scheme[i]) {
			return false
		}
	}
	return !strings.EqualFold(scheme, "javascript")
}
