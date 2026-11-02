package fetch

import (
	"net/netip"
	"net/url"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// OwnedBy returns the settings for requests of a feed whose website is at
// site. The cookie, the basic auth and the extra headers go only to that
// website: the same host or a host with the same registrable domain. Every
// other request keeps the proxy, the user agent and the TLS settings only.
func (p Params) OwnedBy(site string) Params {
	p.Site = site
	return p
}

// SameSite reports whether address is on the website at site (see sameSite);
// false for an address that cannot be parsed.
func SameSite(address, site string) bool {
	u, err := url.Parse(address)
	return err == nil && sameSite(u, site)
}

// sameSite reports whether the host of u belongs to the website at site:
// the same host, or the same registrable domain (eTLD+1). A site that cannot
// be parsed owns nothing.
func sameSite(u *url.URL, site string) bool {
	s, err := url.Parse(site)
	if err != nil {
		return false
	}
	a, b := normalHost(u.Hostname()), normalHost(s.Hostname())
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// An address or a name with no registrable domain ("localhost", a public
	// suffix itself) is its own site only.
	if _, err := netip.ParseAddr(a); err == nil {
		return false
	}
	da, err := publicsuffix.EffectiveTLDPlusOne(a)
	if err != nil {
		return false
	}
	db, err := publicsuffix.EffectiveTLDPlusOne(b)
	return err == nil && da == db
}

// normalHost lowers a host, converts it to ASCII and drops the dot of an
// absolute name; "" for a host that cannot be converted.
func normalHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return ""
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return ""
	}
	return ascii
}
