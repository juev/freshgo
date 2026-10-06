// Package config reads freshgo settings from flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Environment variable names. A flag given on the command line wins over the
// variable, the variable wins over the default.
const (
	EnvDatabaseURL = "FRESHGO_DATABASE_URL"
	EnvListen      = "FRESHGO_LISTEN"
	EnvBaseURL     = "FRESHGO_BASE_URL"
	// EnvFetchAllowlist and EnvRefreshInterval are read by the commands that
	// refresh feeds.
	EnvFetchAllowlist  = "FRESHGO_FETCH_ALLOWLIST"
	EnvRefreshInterval = "FRESHGO_REFRESH_INTERVAL"
	// EnvWebSub switches WebSub on with a value strconv.ParseBool takes for true.
	EnvWebSub = "FRESHGO_WEBSUB"
	// EnvTrustedProxies is read by the web interface.
	EnvTrustedProxies = "FRESHGO_TRUSTED_PROXIES"
)

const (
	defaultDatabaseURL = "sqlite://freshgo.sqlite"
	defaultListen      = "127.0.0.1:8080"
	// defaultRefreshInterval is on the order of the cron period FreshRSS is
	// usually run with.
	defaultRefreshInterval = 10 * time.Minute
	// defaultTrustedProxies is a reverse proxy on the same machine, as in
	// FreshRSS.
	defaultTrustedProxies = "127.0.0.0/8,::1/128"
)

// Driver identifies a database engine.
type Driver string

const (
	DriverSQLite   Driver = "sqlite"
	DriverPostgres Driver = "postgres"
)

// Config holds the settings shared by all subcommands.
type Config struct {
	// DatabaseURL is sqlite://<path> or postgres://<dsn>.
	DatabaseURL string
	// Listen is the address the HTTP server binds to.
	Listen string
	// BaseURL is the public address of the server, without a trailing slash.
	// Empty means it is unknown: links are built from the request, WebSub is off.
	BaseURL string
	// FetchAllowlist names, separated by commas, the internal destinations
	// feeds may be fetched from: "host:port", "ip:port", a CIDR range, or
	// "*" for all of them. Without it requests to private and loopback
	// addresses are refused.
	FetchAllowlist string
	// RefreshInterval is how often the server looks for feeds that are due.
	RefreshInterval time.Duration
	// WebSub makes the server subscribe to the hubs feeds announce, so that
	// new entries are pushed to it. It needs a BaseURL hubs can reach.
	WebSub bool
	// TrustedProxies names, separated by commas, the addresses and CIDR
	// ranges of the reverse proxies whose word is taken for who the user
	// is, when users are told apart by the proxy.
	TrustedProxies string

	// invalid is what was wrong with the environment, reported by Validate.
	invalid error
}

// Bind registers the shared flags on fs. Defaults come from getenv, so the
// precedence flag > environment > built-in default falls out of flag parsing.
func Bind(fs *flag.FlagSet, getenv func(string) string) *Config {
	c := &Config{}
	fs.StringVar(&c.DatabaseURL, "database-url", envOr(getenv, EnvDatabaseURL, defaultDatabaseURL),
		"database: sqlite://<path> or postgres://<dsn> ($"+EnvDatabaseURL+")")
	fs.StringVar(&c.Listen, "listen", envOr(getenv, EnvListen, defaultListen),
		"HTTP listen address ($"+EnvListen+")")
	fs.StringVar(&c.BaseURL, "base-url", getenv(EnvBaseURL),
		"public URL of the server ($"+EnvBaseURL+")")
	fs.StringVar(&c.FetchAllowlist, "fetch-allowlist", getenv(EnvFetchAllowlist),
		"internal addresses feeds may be fetched from, comma-separated: host:port, CIDR or * ($"+EnvFetchAllowlist+")")
	interval := defaultRefreshInterval
	if v := getenv(EnvRefreshInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			c.invalid = fmt.Errorf("$%s: %w", EnvRefreshInterval, err)
		} else {
			interval = d
		}
	}
	fs.DurationVar(&c.RefreshInterval, "refresh-interval", interval,
		"how often the server looks for feeds to refresh ($"+EnvRefreshInterval+")")
	webSub := false
	if v := getenv(EnvWebSub); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			c.invalid = errors.Join(c.invalid, fmt.Errorf("$%s: %w", EnvWebSub, err))
		}
		webSub = on
	}
	fs.BoolVar(&c.WebSub, "websub", webSub,
		"subscribe to the WebSub hubs of feeds; needs a public -base-url ($"+EnvWebSub+")")
	fs.StringVar(&c.TrustedProxies, "trusted-proxies", envOr(getenv, EnvTrustedProxies, defaultTrustedProxies),
		"reverse proxies that may name the user, comma-separated addresses or CIDR ranges ($"+EnvTrustedProxies+")")
	return c
}

// Proxies returns the entries of TrustedProxies as address ranges.
func (c *Config) Proxies() ([]netip.Prefix, error) {
	var ranges []netip.Prefix
	for _, entry := range strings.Split(c.TrustedProxies, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			ranges = append(ranges, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: want an address or a CIDR range", entry)
		}
		ranges = append(ranges, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
	}
	return ranges, nil
}

// Allowlist returns the entries of FetchAllowlist.
func (c *Config) Allowlist() []string {
	var list []string
	for _, entry := range strings.Split(c.FetchAllowlist, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			list = append(list, entry)
		}
	}
	return list
}

// Validate checks the values after flag parsing and normalizes BaseURL.
func (c *Config) Validate() error {
	if c.invalid != nil {
		return c.invalid
	}
	if _, _, err := c.Database(); err != nil {
		return err
	}
	if c.RefreshInterval <= 0 {
		return fmt.Errorf("refresh interval %s: want a positive duration", c.RefreshInterval)
	}
	if _, err := c.Proxies(); err != nil {
		return err
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("base URL %q: want http(s)://host[/path]", c.BaseURL)
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	}
	return nil
}

// Database splits DatabaseURL into the engine and the value its driver wants:
// a file path for SQLite, the URL itself for PostgreSQL.
func (c *Config) Database() (Driver, string, error) {
	scheme, rest, ok := strings.Cut(c.DatabaseURL, "://")
	if !ok {
		return "", "", fmt.Errorf("database URL %q: want sqlite://<path> or postgres://<dsn>", c.DatabaseURL)
	}
	switch scheme {
	case "sqlite":
		if rest == "" {
			return "", "", errors.New("database URL: empty SQLite path")
		}
		return DriverSQLite, rest, nil
	case "postgres", "postgresql":
		return DriverPostgres, c.DatabaseURL, nil
	default:
		return "", "", fmt.Errorf("database URL %q: unknown scheme %q", c.DatabaseURL, scheme)
	}
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
