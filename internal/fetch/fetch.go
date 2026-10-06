// Package fetch downloads feeds and pages over HTTP the way FreshRSS does:
// conditional requests, redirects followed by hand, a body size limit,
// protection against requests to internal addresses, Retry-After and the
// per-feed request settings.
//
// The behaviour follows FreshRSS_http_Util::httpGet and the SimplePie File
// class of FreshRSS at commit 219eaf58.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Accept headers by kind of document, as FreshRSS sends them.
const (
	AcceptFeed = "application/atom+xml, application/rss+xml, application/rdf+xml;q=0.9, application/xml;q=0.8, text/xml;q=0.8, text/html;q=0.7, */*;q=0.1"
	AcceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	AcceptXML  = "application/xml,application/xhtml+xml,text/xml;q=0.9,*/*;q=0.8"
	AcceptJSON = "application/json,application/feed+json,application/javascript;q=0.9,text/javascript;q=0.8,*/*;q=0.7"
	AcceptIcon = "image/x-icon,image/vnd.microsoft.icon,image/ico,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.1"
)

const (
	defaultTimeout         = 20 * time.Second
	defaultMaxBodySize     = 16 << 20
	defaultHostConcurrency = 2
	defaultRedirects       = 4
	// redirectsCap bounds a feed configured for unlimited redirects.
	redirectsCap = 20

	// A 429 or 503 without a usable Retry-After blocks the host for
	// retryAfterDefault; no Retry-After blocks it for longer than retryAfterMax.
	retryAfterDefault = 1500 * time.Second
	retryAfterMax     = 48 * time.Hour
)

var (
	// ErrBadURL is returned for a URL, or a redirect target, that is not http(s).
	ErrBadURL = errors.New("not an http(s) URL")
	// ErrForbiddenAddress is returned when the host, or the proxy, resolves
	// only to internal addresses that are not in the allowlist.
	ErrForbiddenAddress = errors.New("address is not allowed")
	// ErrTooManyRedirects is returned when the redirect limit is exceeded.
	ErrTooManyRedirects = errors.New("too many redirects")
	// ErrBodyTooLarge is returned when the response body exceeds the limit.
	ErrBodyTooLarge = errors.New("response body is too large")
	// ErrUnsupportedProxy is returned for a proxy type that cannot be used.
	ErrUnsupportedProxy = errors.New("unsupported proxy type")
)

// StatusError is a response with a status other than 200 and 304.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.Code, http.StatusText(e.Code))
}

// RetryAfterError is returned without a request being made while the host is
// in the pause it asked for with a 429 or 503 response.
type RetryAfterError struct {
	Host  string
	Until time.Time
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("%s asked not to be requested before %s", e.Host, e.Until.Format(time.RFC3339))
}

// Options configure a Client. Zero values select the defaults.
type Options struct {
	// UserAgent is sent unless the request settings carry their own.
	UserAgent string
	// Allowlist names internal destinations that may be requested: "host:port",
	// "ip:port", a CIDR range, or "*" to switch the protection off.
	Allowlist []string
	// Timeout bounds one request of a redirect chain, body included.
	Timeout time.Duration
	// MaxBodySize is the largest accepted body after decompression, in bytes.
	MaxBodySize int64
	// HostConcurrency is the number of simultaneous requests to one host.
	HostConcurrency int
}

// Client fetches documents. It is safe for concurrent use and keeps
// connections, the Retry-After pauses and the per-host limits between calls.
type Client struct {
	userAgent string
	timeout   time.Duration
	maxBody   int64
	perHost   int
	guard     *guard
	now       func() time.Time

	mu         sync.Mutex
	transports map[transportKey]*http.Transport
	hosts      map[string]chan struct{}
	retryAfter map[string]time.Time
}

type transportKey struct {
	proxy    string
	insecure bool
}

// New returns a Client. It fails on an allowlist entry that looks like a CIDR
// range and is not one.
func New(o Options) (*Client, error) {
	g, err := newGuard(o.Allowlist)
	if err != nil {
		return nil, err
	}
	c := &Client{
		userAgent:  o.UserAgent,
		timeout:    o.Timeout,
		maxBody:    o.MaxBodySize,
		perHost:    o.HostConcurrency,
		guard:      g,
		now:        time.Now,
		transports: map[transportKey]*http.Transport{},
		hosts:      map[string]chan struct{}{},
		retryAfter: map[string]time.Time{},
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	if c.maxBody <= 0 {
		c.maxBody = defaultMaxBodySize
	}
	if c.perHost <= 0 {
		c.perHost = defaultHostConcurrency
	}
	return c, nil
}

// Request describes one document to fetch.
type Request struct {
	URL string
	// Accept is the Accept header; one of the Accept constants.
	Accept string
	// ETag and LastModified are the validators of the copy the caller already
	// has. With either set, an unchanged document comes back as NotModified.
	ETag         string
	LastModified string
	Params       Params
}

// Response is a fetched document.
type Response struct {
	// URL is the address the document finally came from.
	URL string
	// PermanentURL is where the requested URL has moved for good: the end of
	// the unbroken chain of 301 and 308 redirects starting at the requested
	// URL. It is empty when the first response was not such a redirect.
	PermanentURL string
	// NotModified reports a 304: the caller's copy is current and Body is empty.
	NotModified bool
	Header      http.Header
	Body        []byte
}

// Fetch downloads the document. Any status other than 200 and 304 is a
// *StatusError.
func (c *Client) Fetch(ctx context.Context, req Request) (*Response, error) {
	u, err := parseURL(req.URL)
	if err != nil {
		return nil, err
	}
	p := req.Params
	transport, err := c.transport(p)
	if err != nil {
		return nil, err
	}
	h := hop{
		client: &http.Client{
			Transport: transport,
			// Redirects are followed here, one checked request at a time.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		req:     req,
		post:    p.Post,
		auth:    p.BasicAuth,
		cookie:  p.Cookie,
		header:  p.Header.Clone(),
		timeout: c.timeout,
	}
	if p.Timeout > 0 {
		h.timeout = p.Timeout
	}
	if p.CookieEngine {
		h.jar, _ = cookiejar.New(nil)
	}
	if p.Proxy != nil {
		h.proxy = p.Proxy.Host
	}

	limit := defaultRedirects
	switch {
	case p.NoRedirects:
		limit = 0
	case p.MaxRedirects < 0 || p.MaxRedirects > redirectsCap:
		limit = redirectsCap
	case p.MaxRedirects > 0:
		limit = p.MaxRedirects
	}

	res := &Response{}
	permanent := true
	for redirects := 0; ; redirects++ {
		status, header, body, err := c.do(ctx, u, &h)
		if err != nil {
			return nil, err
		}
		location := header.Get("Location")
		if isRedirect(status) && location != "" {
			if redirects >= limit {
				return nil, fmt.Errorf("%w: more than %d from %s", ErrTooManyRedirects, limit, redacted(req.URL))
			}
			next, err := u.Parse(location)
			if err != nil || (next.Scheme != "http" && next.Scheme != "https") || next.Host == "" {
				return nil, fmt.Errorf("%w: redirect to %q", ErrBadURL, location)
			}
			// 307 and 308 must not change the method.
			if h.post && status != http.StatusTemporaryRedirect && status != http.StatusPermanentRedirect {
				h.post = false
				h.header.Del("Content-Type")
			}
			if !sameOrigin(u, next) {
				h.auth, h.cookie = "", ""
				h.header.Del("Cookie")
				h.header.Del("Authorization")
			}
			permanent = permanent && (status == http.StatusMovedPermanently || status == http.StatusPermanentRedirect)
			if permanent {
				res.PermanentURL = next.String()
			}
			u = next
			continue
		}
		switch status {
		case http.StatusOK:
			res.URL, res.Header, res.Body = u.String(), header, body
			return res, nil
		case http.StatusNotModified:
			res.URL, res.Header, res.NotModified = u.String(), header, true
			return res, nil
		default:
			return nil, fmt.Errorf("%s: %w", redacted(u.String()), &StatusError{Code: status})
		}
	}
}

// hop is the state of a redirect chain that changes from request to request.
type hop struct {
	client  *http.Client
	req     Request
	jar     http.CookieJar
	proxy   string
	post    bool
	auth    string
	cookie  string
	header  http.Header
	timeout time.Duration
}

// do sends one request and reads the body of a 200 response.
func (c *Client) do(ctx context.Context, u *url.URL, h *hop) (int, http.Header, []byte, error) {
	host := strings.ToLower(u.Host)
	pauseKey := host + " " + h.proxy
	if until := c.pausedUntil(pauseKey); !until.IsZero() {
		return 0, nil, nil, &RetryAfterError{Host: host, Until: until}
	}
	release, err := c.acquire(ctx, host)
	if err != nil {
		return 0, nil, nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	method, body := http.MethodGet, io.Reader(nil)
	if h.post {
		method, body = http.MethodPost, strings.NewReader(h.req.Params.PostBody)
	}
	r, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %s", ErrBadURL, redacted(u.String()))
	}
	accept := h.req.Accept
	if accept == "" {
		accept = "*/*"
	}
	r.Header.Set("Accept", accept)
	r.Header.Set("User-Agent", c.userAgent)
	if ua := h.req.Params.UserAgent; ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	if h.req.ETag != "" {
		r.Header.Set("If-None-Match", h.req.ETag)
	}
	if h.req.LastModified != "" {
		r.Header.Set("If-Modified-Since", h.req.LastModified)
	}
	if h.post {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if user, password, ok := strings.Cut(h.auth, ":"); ok {
		r.SetBasicAuth(user, password)
	}
	// The feed's own headers win over everything set above.
	for name, values := range h.header {
		r.Header[name] = slices.Clone(values)
	}
	// The transport decompresses only what it asked for itself.
	r.Header.Del("Accept-Encoding")
	if h.cookie != "" {
		r.Header.Add("Cookie", h.cookie)
	}
	if h.jar != nil {
		for _, ck := range h.jar.Cookies(u) {
			r.AddCookie(ck)
		}
	}

	resp, err := h.client.Do(r)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if h.jar != nil {
		h.jar.SetCookies(u, resp.Cookies())
	}

	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBody+1))
		if err != nil {
			return 0, nil, nil, fmt.Errorf("%s: %w", redacted(u.String()), err)
		}
		if int64(len(data)) > c.maxBody {
			return 0, nil, nil, fmt.Errorf("%w: %s is over %d bytes", ErrBodyTooLarge, redacted(u.String()), c.maxBody)
		}
		return resp.StatusCode, resp.Header, data, nil
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		c.pause(pauseKey, resp.Header.Get("Retry-After"))
	}
	return resp.StatusCode, resp.Header, nil, nil
}

// transport returns the connection pool for the proxy and TLS settings.
func (c *Client) transport(p Params) (*http.Transport, error) {
	key := transportKey{insecure: p.Insecure}
	if p.Proxy != nil {
		switch p.Proxy.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedProxy, p.Proxy.Scheme)
		}
		key.proxy = p.Proxy.String()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.transports[key]; ok {
		return t, nil
	}
	t := &http.Transport{
		// The proxy of the environment is not used: only the feed's own.
		Proxy:               http.ProxyURL(p.Proxy),
		DialContext:         c.guard.dialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: c.perHost,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if p.Insecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the feed's ssl_verify setting
	}
	c.transports[key] = t
	return t, nil
}

// acquire takes one of the host's request slots, waiting for a free one.
func (c *Client) acquire(ctx context.Context, host string) (func(), error) {
	c.mu.Lock()
	slots, ok := c.hosts[host]
	if !ok {
		slots = make(chan struct{}, c.perHost)
		c.hosts[host] = slots
	}
	c.mu.Unlock()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) pausedUntil(key string) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.retryAfter[key]
	if !ok {
		return time.Time{}
	}
	if !until.After(c.now()) {
		delete(c.retryAfter, key)
		return time.Time{}
	}
	return until
}

// pause records the Retry-After of a 429 or 503 response: a number of
// seconds or an HTTP date.
func (c *Client) pause(key, retryAfter string) {
	now := c.now()
	until := now.Add(retryAfterDefault)
	if seconds, err := strconv.ParseUint(strings.TrimSpace(retryAfter), 10, 63); err == nil {
		seconds = min(seconds, uint64(retryAfterMax/time.Second))
		until = now.Add(time.Duration(seconds) * time.Second)
	} else if date, err := http.ParseTime(retryAfter); err == nil {
		until = date
	}
	if latest := now.Add(retryAfterMax); until.After(latest) {
		until = latest
	}
	c.mu.Lock()
	c.retryAfter[key] = until
	c.mu.Unlock()
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%w: %s", ErrBadURL, redacted(raw))
	}
	return u, nil
}

func isRedirect(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// redacted is the URL without its password, for error messages.
func redacted(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(malformed URL)"
	}
	return u.Redacted()
}
