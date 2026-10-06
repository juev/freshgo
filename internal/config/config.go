// Package config reads freshgo settings from flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
)

// Environment variable names. A flag given on the command line wins over the
// variable, the variable wins over the default.
const (
	EnvDatabaseURL = "FRESHGO_DATABASE_URL"
	EnvListen      = "FRESHGO_LISTEN"
	EnvBaseURL     = "FRESHGO_BASE_URL"
)

const (
	defaultDatabaseURL = "sqlite://freshgo.sqlite"
	defaultListen      = "127.0.0.1:8080"
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
	return c
}

// Validate checks the values after flag parsing and normalizes BaseURL.
func (c *Config) Validate() error {
	if _, _, err := c.Database(); err != nil {
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
