package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Params are the request settings of a feed.
type Params struct {
	// BasicAuth is "user:password".
	BasicAuth string
	// Proxy is the proxy to go through: http, https, socks5 or socks5h.
	Proxy *url.URL
	// Insecure skips the verification of the TLS certificate.
	Insecure bool
	// Timeout replaces the client's timeout when positive.
	Timeout   time.Duration
	UserAgent string
	// Header holds extra request headers; they replace the default ones.
	Header http.Header
	// Cookie is the value of the Cookie header.
	Cookie string
	// CookieEngine keeps cookies set by responses for the rest of a redirect chain.
	CookieEngine bool
	// Post sends PostBody with POST instead of a GET.
	Post     bool
	PostBody string
	// MaxRedirects is the redirect limit: zero for the default of 4, negative
	// for as many as the client's hard cap allows.
	MaxRedirects int
	// NoRedirects makes any redirect an error.
	NoRedirects bool
}

// Keys of feed.attributes.curl_params: the numeric values of the PHP
// constants CURLOPT_*, which is how FreshRSS stores them.
const (
	curlPost           = "47"
	curlFollowLocation = "52"
	curlMaxRedirs      = "68"
	curlProxyType      = "101"
	curlProxy          = "10004"
	curlPostFields     = "10015"
	curlUserAgent      = "10018"
	curlCookie         = "10022"
	curlHTTPHeader     = "10023"
	curlCookieFile     = "10031"
)

// Values of CURLOPT_PROXYTYPE. SOCKS4 (4) and SOCKS4A (6) have no
// implementation here; a negative value and 3 mean "no proxy".
var proxySchemes = map[int]string{
	0: "http",
	2: "https",
	4: "socks4",
	5: "socks5",
	6: "socks4a",
	7: "socks5h",
}

const proxyTypeNoneLegacy = 3

// Headers that would let a feed setting impersonate a user behind a reverse
// proxy with HTTP authentication; FreshRSS drops them too.
var forbiddenHeader = regexp.MustCompile(`(?i)^(Remote[-_\s]*User|X[-_\s]*WebAuth[-_\s]*User)$`)

var proxyScheme = regexp.MustCompile(`^.*://`)

// headerName is an HTTP token: a name net/http agrees to send.
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// FeedParams reads the request settings of a feed: its HTTP credentials and
// the keys curl_params, ssl_verify and timeout of its attributes.
func FeedParams(httpAuth string, attributes json.RawMessage) (Params, error) {
	p := Params{BasicAuth: httpAuth}
	if len(attributes) == 0 {
		return p, nil
	}
	var a struct {
		CurlParams json.RawMessage `json:"curl_params"`
		SSLVerify  json.RawMessage `json:"ssl_verify"`
		Timeout    json.RawMessage `json:"timeout"`
	}
	if err := json.Unmarshal(attributes, &a); err != nil {
		return Params{}, fmt.Errorf("feed attributes: %w", err)
	}
	// ssl_verify is three-valued: absent or null keeps verification on.
	p.Insecure = present(a.SSLVerify) && !truthy(a.SSLVerify)
	if seconds, ok := integer(a.Timeout); ok && seconds > 0 {
		p.Timeout = time.Duration(seconds) * time.Second
	}

	// PHP writes an empty array as [], not as an object.
	var curl map[string]json.RawMessage
	if err := json.Unmarshal(a.CurlParams, &curl); err != nil {
		return p, nil
	}
	p.UserAgent = text(curl[curlUserAgent])
	p.Cookie = text(curl[curlCookie])
	_, p.CookieEngine = curl[curlCookieFile]
	p.Header = headers(curl[curlHTTPHeader])
	if truthy(curl[curlPost]) {
		p.Post = true
		p.PostBody = text(curl[curlPostFields])
	}
	if n, ok := integer(curl[curlMaxRedirs]); ok {
		p.MaxRedirects = n
		p.NoRedirects = n == 0
	}
	if raw := curl[curlFollowLocation]; present(raw) && !truthy(raw) {
		p.NoRedirects = true
	}

	proxyType, _ := integer(curl[curlProxyType])
	address := proxyScheme.ReplaceAllString(text(curl[curlProxy]), "")
	if address == "" || proxyType < 0 || proxyType == proxyTypeNoneLegacy {
		return p, nil
	}
	scheme, ok := proxySchemes[proxyType]
	if !ok {
		return Params{}, fmt.Errorf("%w: %d", ErrUnsupportedProxy, proxyType)
	}
	proxy, err := url.Parse(scheme + "://" + address)
	if err != nil || proxy.Host == "" {
		return Params{}, fmt.Errorf("feed proxy address is malformed")
	}
	// Without a port curl goes to 1080, and to 443 for an HTTPS proxy.
	if proxy.Port() == "" {
		port := "1080"
		if scheme == "https" {
			port = "443"
		}
		proxy.Host = net.JoinHostPort(proxy.Hostname(), port)
	}
	p.Proxy = proxy
	return p, nil
}

// headers reads a list of "Name: value" lines. After FreshRSS has filtered
// the list, PHP may write it as an object with numeric keys.
func headers(raw json.RawMessage) http.Header {
	var lines []string
	if err := json.Unmarshal(raw, &lines); err != nil {
		var keyed map[string]string
		if err := json.Unmarshal(raw, &keyed); err != nil {
			return nil
		}
		keys := make([]string, 0, len(keyed))
		for k := range keyed {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, _ := strconv.Atoi(keys[i])
			b, _ := strconv.Atoi(keys[j])
			return a < b
		})
		for _, k := range keys {
			lines = append(lines, keyed[k])
		}
	}
	h := http.Header{}
	for _, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || !headerName.MatchString(name) || forbiddenHeader.MatchString(name) {
			continue
		}
		h.Add(name, strings.TrimSpace(value))
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

func present(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}

// truthy tells whether PHP would take the value for true.
func truthy(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "false", "0", `""`, `"0"`, "[]", "{}":
		return false
	}
	return true
}

// integer reads a number, or a string holding one.
func integer(raw json.RawMessage) (int, bool) {
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	return n, err == nil
}

func text(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
