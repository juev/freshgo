package fetch

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// internalRanges are the addresses a feed URL must not lead to: the list of
// FreshRSS_http_Util::PRIVATE_SUBNETS plus multicast and the special-purpose
// ranges of the IANA registries that are not reachable on the internet.
// IPv4-mapped IPv6 addresses are checked as IPv4, NAT64 and 6to4 ones by the
// IPv4 address they embed. Teredo is refused whole: the IPv4 address of its
// client is obfuscated and that of its server is another one.
var internalRanges = prefixes(
	"0.0.0.0/8",       // this network
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // carrier-grade NAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, broadcast
	"::/128",          // unspecified
	"::1/128",         // loopback
	"64:ff9b:1::/48",  // local-use NAT64
	"2001::/32",       // Teredo
	"2001:db8::/32",   // documentation
	"fc00::/7",        // unique local
	"fe80::/10",       // link-local
	"ff00::/8",        // multicast
)

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func prefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(list))
	for i, s := range list {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// guard refuses connections to internal addresses that are not allowlisted.
// The check runs on the address the socket actually connects to, after name
// resolution, so a name cannot pass the check and then resolve elsewhere.
type guard struct {
	all      bool
	literals map[string]bool
	ranges   []netip.Prefix
}

func newGuard(allowlist []string) (*guard, error) {
	g := &guard{literals: map[string]bool{}}
	for _, entry := range allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		switch {
		case entry == "":
		case entry == "*":
			g.all = true
		case strings.Contains(entry, "/"):
			p, err := netip.ParsePrefix(entry)
			if err != nil {
				return nil, fmt.Errorf("allowlist entry %q: %w", entry, err)
			}
			g.ranges = append(g.ranges, p.Masked())
		default:
			g.literals[entry] = true
		}
	}
	return g, nil
}

// dialContext is the transport's dialer. With a proxy the address is the
// proxy's: what the proxy then connects to is not checked.
func (g *guard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	// A "host:port" entry allows the name whatever it resolves to.
	if !g.all && !g.literals[strings.ToLower(address)] {
		d.Control = g.control
	}
	return d.DialContext(ctx, network, address)
}

func (g *guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !g.allowed(ap) {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, address)
	}
	return nil
}

func (g *guard) allowed(ap netip.AddrPort) bool {
	ip := ap.Addr().WithZone("").Unmap()
	for _, r := range g.ranges {
		if r.Contains(ip) {
			return true
		}
	}
	if g.literals[netip.AddrPortFrom(ip, ap.Port()).String()] {
		return true
	}
	return !internal(ip)
}

func internal(ip netip.Addr) bool {
	switch b := ip.As16(); {
	case nat64.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[12:]))
	case sixToFour.Contains(ip):
		ip = netip.AddrFrom4([4]byte(b[2:6]))
	}
	for _, r := range internalRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// Allowed reports whether address is one a request may go to: an http or
// https address whose host resolves to nothing internal that the allowlist
// does not name. It is for a page that something other than this client
// requests, a browser. Unlike a request of the client, it looks at what the
// name resolves to now, not at what is then connected to.
func (c *Client) Allowed(ctx context.Context, address string) error {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, redacted(address))
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, redacted(address))
	}
	g := c.guard
	if g.all || g.literals[strings.ToLower(net.JoinHostPort(u.Hostname(), port))] {
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("%s: %w", redacted(address), err)
	}
	for _, ip := range ips {
		if !g.allowed(netip.AddrPortFrom(ip, uint16(number))) {
			return fmt.Errorf("%w: %s", ErrForbiddenAddress, redacted(address))
		}
	}
	return nil
}
