// Package authguard slows down guessing of passwords and keeps the time a
// refusal takes from telling which user names exist: the pause after a run
// of failed logins, the address an attempt is counted from, and the hashes
// that stand in for the password of an account that has none to compare.
package authguard

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Failed logins that are let through before a pause is imposed, and the
// pauses: the first, then twice as long with every further failure, up to
// the longest.
const (
	FreeFailures = 5
	FirstPause   = 30 * time.Second
	LongestPause = 15 * time.Minute
	// KnownFailures bounds what the guard remembers.
	KnownFailures = 10000
)

// Guard slows down guessing of passwords: after a run of failed logins for
// a name from an address, further ones from there have to wait.
//
// One Guard is shared by every way into an installation, so that changing
// from one to another does not start the count anew. Its zero value is
// ready.
type Guard struct {
	mu       sync.Mutex
	failures map[string]*failure
}

type failure struct {
	count int
	// until is when the next attempt is heard; last when the last failed.
	until, last time.Time
}

// Attempt asks whether a login for the name from the address may be tried
// now. It returns how long the attempt has to wait, or zero after counting
// it as failed: the count is taken before the password is looked at, so
// that attempts made at once cannot all slip through, and is dropped by
// Succeeded.
func (g *Guard) Attempt(key string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures == nil {
		g.failures = map[string]*failure{}
	}
	f := g.failures[key]
	if f != nil && now.Before(f.until) {
		return f.until.Sub(now)
	}
	if f == nil && len(g.failures) >= KnownFailures {
		// Forget what no longer holds anybody back, and everything if that
		// is not enough: memory is not what an attacker gets to fill.
		for k, old := range g.failures {
			if now.Sub(old.last) > LongestPause {
				delete(g.failures, k)
			}
		}
		if len(g.failures) >= KnownFailures {
			clear(g.failures)
		}
	}
	if f == nil || now.Sub(f.last) > LongestPause {
		f = &failure{}
		g.failures[key] = f
	}
	f.count++
	f.last = now
	if f.count >= FreeFailures {
		pause := FirstPause << min(f.count-FreeFailures, 10)
		f.until = now.Add(min(pause, LongestPause))
	}
	return 0
}

// Succeeded forgets the attempts before a login that worked, that one
// included.
func (g *Guard) Succeeded(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, key)
}

// Key tells attempts apart by name and by where they come from. The name is
// one a user can have, so the key is short.
func Key(name, addr string) string {
	return strings.ToLower(name) + "\x00" + addr
}

// RetryAfter is the value of the Retry-After header of a pause: the seconds
// left, as HTTP counts them in whole seconds, the second under way included.
func RetryAfter(wait time.Duration) string {
	return strconv.Itoa(int(wait/time.Second) + 1)
}

// FromProxy reports whether the request was made by a reverse proxy whose
// word is taken.
func FromProxy(r *http.Request, proxies []netip.Prefix) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, proxy := range proxies {
		if proxy.Contains(addr) {
			return true
		}
	}
	return false
}

// ClientAddr returns the address a request comes from: behind a trusted
// reverse proxy the one the proxy says it took the request from, which is
// the last it added to X-Forwarded-For.
func ClientAddr(r *http.Request, proxies []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !FromProxy(r, proxies) {
		return host
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return host
	}
	hops := strings.Split(forwarded[len(forwarded)-1], ",")
	if client, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
		return client.Unmap().String()
	}
	return host
}
