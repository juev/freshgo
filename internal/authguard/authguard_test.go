package authguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestGuardPauses(t *testing.T) {
	var g Guard
	now := time.Unix(1_000_000, 0)
	for i := 1; i <= 20; i++ {
		if wait := g.Attempt("k", now); wait != 0 {
			t.Fatalf("attempt %d, made after the pause, has to wait %s", i, wait)
		}
		want := time.Duration(0)
		if i >= FreeFailures {
			want = min(FirstPause<<(i-FreeFailures), LongestPause)
		}
		// While the pause lasts, asking again is no attempt.
		for range 3 {
			if want == 0 {
				break
			}
			if got := g.Attempt("k", now); got != want {
				t.Errorf("after %d failures the pause is %s, want %s", i, got, want)
			}
		}
		if got := g.failures["k"].count; got != i {
			t.Errorf("after %d attempts the guard has counted %d", i, got)
		}
		now = now.Add(want)
	}
	// Left alone for longer than the longest pause, a name starts anew.
	now = now.Add(LongestPause + time.Second)
	if g.Attempt("k", now); g.failures["k"].count != 1 {
		t.Errorf("the first failure after a long quiet is counted as number %d", g.failures["k"].count)
	}
	// What the guard remembers is bounded.
	for i := range KnownFailures + 10 {
		g.Attempt(strconv.Itoa(i), now)
	}
	if len(g.failures) > KnownFailures {
		t.Errorf("the guard remembers %d names", len(g.failures))
	}
}

// Attempts made at once are counted like attempts made in a row: no more
// than the free ones get a look at the password.
func TestGuardCountsParallelAttempts(t *testing.T) {
	var (
		g      Guard
		passed atomic.Int32
		wg     sync.WaitGroup
	)
	now := time.Unix(1_000_000, 0)
	for range 200 {
		wg.Go(func() {
			if g.Attempt("k", now) == 0 {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	if n := passed.Load(); n != FreeFailures {
		t.Errorf("%d of 200 attempts made at once were let through, want %d", n, FreeFailures)
	}
}

// A login that worked forgets what came before it.
func TestGuardSucceeded(t *testing.T) {
	var g Guard
	now := time.Unix(1_000_000, 0)
	for range FreeFailures - 1 {
		g.Attempt("k", now)
	}
	g.Succeeded("k")
	if wait := g.Attempt("k", now); wait != 0 {
		t.Fatalf("the attempt after a success has to wait %s", wait)
	}
	if got := len(g.failures); got != 1 {
		t.Errorf("the guard remembers %d keys after a success, want 1", got)
	}
	if got := g.failures["k"].count; got != 1 {
		t.Errorf("the attempt after a success is counted as number %d", got)
	}
}

// A refund takes back the attempt that worked alone: any number of them
// lets a key through, and the failures between them still add up to a
// pause.
func TestGuardRefund(t *testing.T) {
	var g Guard
	now := time.Unix(1_000_000, 0)
	for range 3 * FreeFailures {
		if wait := g.Attempt("k", now); wait != 0 {
			t.Fatalf("an attempt that works has to wait %s", wait)
		}
		g.Refund("k")
	}
	if got := len(g.failures); got != 0 {
		t.Errorf("the guard remembers %d keys after attempts that all worked", got)
	}
	for range FreeFailures - 1 {
		g.Attempt("k", now)
		g.Attempt("k", now)
		g.Refund("k")
	}
	if wait := g.Attempt("k", now); wait != 0 {
		t.Fatalf("the failure number %d has to wait %s", FreeFailures, wait)
	}
	if wait := g.Attempt("k", now); wait == 0 {
		t.Errorf("after %d failures and refunds of the attempts between them, no pause", FreeFailures)
	}
	g.Refund("unknown")
}

func TestKey(t *testing.T) {
	for _, k := range []struct{ name, addr, want string }{
		{"Alice", "192.0.2.1", "alice\x00192.0.2.1"},
		{"alice", "192.0.2.1", "alice\x00192.0.2.1"},
		{"alice", "192.0.2.2", "alice\x00192.0.2.2"},
		{"alice", "", "alice\x00"},
	} {
		if got := Key(k.name, k.addr); got != k.want {
			t.Errorf("Key(%q, %q) = %q, want %q", k.name, k.addr, got, k.want)
		}
	}
}

func TestClientAddr(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}
	request := func(remote string, forwarded ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for _, value := range forwarded {
			r.Header.Add("X-Forwarded-For", value)
		}
		return r
	}
	cases := []struct {
		name    string
		request *http.Request
		proxies []netip.Prefix
		want    string
	}{
		{"the address of the connection", request("192.0.2.9:4000"), proxies, "192.0.2.9"},
		{"an address without a port", request("192.0.2.9"), proxies, "192.0.2.9"},
		{"the last hop of a trusted proxy", request("10.1.0.1:4000", "203.0.113.9, 198.51.100.7"), proxies, "198.51.100.7"},
		{"the last of two headers", request("10.1.0.1:4000", "203.0.113.9", "198.51.100.7"), proxies, "198.51.100.7"},
		{"a header of an untrusted connection", request("192.0.2.9:4000", "198.51.100.7"), proxies, "192.0.2.9"},
		{"a header that is not an address", request("10.1.0.1:4000", "not an address"), proxies, "10.1.0.1"},
		{"an IPv6 proxy", request("[fd00::1]:4000", "fd00::2"), []netip.Prefix{netip.MustParsePrefix("fd00::/8")}, "fd00::2"},
		{"no trusted proxy", request("10.1.0.1:4000", "198.51.100.7"), nil, "10.1.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClientAddr(c.request, c.proxies); got != c.want {
				t.Errorf("ClientAddr = %q, want %q", got, c.want)
			}
		})
	}
}

// A decoy for a cost takes as long as comparing against a hash of that cost,
// and no password matches it.
func TestDecoys(t *testing.T) {
	var d Decoys
	hash := d.withCost(bcrypt.MinCost)
	if hash == nil {
		t.Fatal("no decoy")
	}
	if got, err := bcrypt.Cost(hash); err != nil || got != bcrypt.MinCost {
		t.Fatalf("the decoy has cost %d, %v; want %d", got, err, bcrypt.MinCost)
	}
	if Password(string(hash), "guess") {
		t.Error("a decoy matches another password")
	}
	if again := d.withCost(bcrypt.MinCost); len(again) == 0 || &again[0] != &hash[0] {
		t.Error("the decoy of a cost was not kept")
	}
	if got, err := bcrypt.Cost(d.withCost(bcrypt.MaxCost + 1)); err != nil || got != bcrypt.DefaultCost {
		t.Errorf("a cost out of range made a decoy of cost %d, %v; want %d", got, err, bcrypt.DefaultCost)
	}
	if costs := d.Costs(); len(costs) != 2 || !contains(costs, bcrypt.MinCost) || !contains(costs, bcrypt.DefaultCost) {
		t.Errorf("the decoys are kept for the costs %v, want %d and %d", costs, bcrypt.MinCost, bcrypt.DefaultCost)
	}
}

// Refusing takes the work of the cost it is given.
func TestRefuse(t *testing.T) {
	var d Decoys
	d.Refuse("guess", bcrypt.MinCost)
	if costs := d.Costs(); len(costs) != 1 || costs[0] != bcrypt.MinCost {
		t.Errorf("the refusal used the costs %v, want %d", costs, bcrypt.MinCost)
	}
}

// A hash that is none is compared against the default cost.
func TestCost(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		hash string
		want int
	}{
		{string(hash), bcrypt.MinCost},
		{"", bcrypt.DefaultCost},
		{"$2y$not a hash", bcrypt.DefaultCost},
	} {
		if got := Cost(c.hash); got != c.want {
			t.Errorf("Cost(%q) = %d, want %d", c.hash, got, c.want)
		}
	}
}

func contains(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
