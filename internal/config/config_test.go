package config

import (
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func parse(t *testing.T, env map[string]string, args ...string) *Config {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := Bind(fs, func(k string) string { return env[k] })
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return c
}

func TestPrecedence(t *testing.T) {
	env := map[string]string{EnvListen: "0.0.0.0:9000"}

	if got := parse(t, nil).Listen; got != defaultListen {
		t.Errorf("no env, no flag: Listen = %q, want default %q", got, defaultListen)
	}
	if got := parse(t, env).Listen; got != "0.0.0.0:9000" {
		t.Errorf("env only: Listen = %q, want value from environment", got)
	}
	if got := parse(t, env, "-listen", ":1234").Listen; got != ":1234" {
		t.Errorf("env and flag: Listen = %q, want value from flag", got)
	}
}

func TestDatabase(t *testing.T) {
	tests := []struct {
		url     string
		driver  Driver
		dsn     string
		wantErr bool
	}{
		{url: "sqlite://data/freshgo.sqlite", driver: DriverSQLite, dsn: "data/freshgo.sqlite"},
		{url: "sqlite:///var/lib/freshgo/db.sqlite", driver: DriverSQLite, dsn: "/var/lib/freshgo/db.sqlite"},
		{url: "postgres://u:p@localhost:5432/freshgo", driver: DriverPostgres, dsn: "postgres://u:p@localhost:5432/freshgo"},
		{url: "postgresql://localhost/freshgo", driver: DriverPostgres, dsn: "postgresql://localhost/freshgo"},
		{url: "sqlite://", wantErr: true},
		{url: "mysql://localhost/freshgo", wantErr: true},
		{url: "freshgo.sqlite", wantErr: true},
	}
	for _, tt := range tests {
		c := &Config{DatabaseURL: tt.url}
		driver, dsn, err := c.Database()
		if (err != nil) != tt.wantErr {
			t.Errorf("Database(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			continue
		}
		if driver != tt.driver || dsn != tt.dsn {
			t.Errorf("Database(%q) = %q, %q; want %q, %q", tt.url, driver, dsn, tt.driver, tt.dsn)
		}
	}
}

func TestValidateBaseURL(t *testing.T) {
	c := &Config{DatabaseURL: defaultDatabaseURL, BaseURL: "https://rss.example.org/reader/", RefreshInterval: time.Minute}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if c.BaseURL != "https://rss.example.org/reader" {
		t.Errorf("BaseURL = %q, want trailing slash removed", c.BaseURL)
	}

	for _, bad := range []string{"rss.example.org", "ftp://rss.example.org", "https://"} {
		c := &Config{DatabaseURL: defaultDatabaseURL, BaseURL: bad, RefreshInterval: time.Minute}
		if err := c.Validate(); err == nil {
			t.Errorf("Validate with BaseURL %q: no error", bad)
		}
	}
}

func TestRefreshSettings(t *testing.T) {
	c := parse(t, nil)
	if c.RefreshInterval != defaultRefreshInterval || c.Allowlist() != nil {
		t.Errorf("defaults: interval %s, allowlist %q", c.RefreshInterval, c.Allowlist())
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate of the defaults: %v", err)
	}

	env := map[string]string{EnvRefreshInterval: "30m", EnvFetchAllowlist: "feeds.lan:8080, 10.0.0.0/8,"}
	c = parse(t, env)
	if c.RefreshInterval != 30*time.Minute || !reflect.DeepEqual(c.Allowlist(), []string{"feeds.lan:8080", "10.0.0.0/8"}) {
		t.Errorf("from environment: interval %s, allowlist %q", c.RefreshInterval, c.Allowlist())
	}
	c = parse(t, env, "-refresh-interval", "5m", "-fetch-allowlist", "*")
	if c.RefreshInterval != 5*time.Minute || !reflect.DeepEqual(c.Allowlist(), []string{"*"}) {
		t.Errorf("flags over environment: interval %s, allowlist %q", c.RefreshInterval, c.Allowlist())
	}

	if err := parse(t, map[string]string{EnvRefreshInterval: "soon"}).Validate(); err == nil {
		t.Error("Validate accepted a refresh interval that is not a duration")
	}
	if err := parse(t, nil, "-refresh-interval", "0s").Validate(); err == nil {
		t.Error("Validate accepted a zero refresh interval")
	}
}

func TestWebSubSetting(t *testing.T) {
	if parse(t, nil).WebSub {
		t.Error("WebSub is on by default")
	}
	if !parse(t, map[string]string{EnvWebSub: "true"}).WebSub || !parse(t, nil, "-websub").WebSub {
		t.Error("WebSub is not switched on by the environment or by the flag")
	}
	if parse(t, map[string]string{EnvWebSub: "1"}, "-websub=false").WebSub {
		t.Error("the flag does not win over the environment")
	}
	if err := parse(t, map[string]string{EnvWebSub: "maybe"}).Validate(); err == nil {
		t.Error("Validate accepted a WebSub value that is not a boolean")
	}
}

func TestOIDCClientSecret(t *testing.T) {
	if got := parse(t, nil).OIDCClientSecret; got != "" {
		t.Errorf("the secret by default = %q", got)
	}
	if got := parse(t, map[string]string{EnvOIDCClientSecret: "from the environment"}).OIDCClientSecret; got != "from the environment" {
		t.Errorf("the secret of the environment = %q", got)
	}
	if got := parse(t, map[string]string{EnvOIDCClientSecret: "from the environment"}, "-oidc-client-secret", "from the flag").OIDCClientSecret; got != "from the flag" {
		t.Errorf("the secret with a flag = %q, want that of the flag", got)
	}
}

func TestHelpKeepsSecrets(t *testing.T) {
	env := map[string]string{
		EnvDatabaseURL:      "postgres://freshgo:database-password@db/freshgo",
		EnvSMTPURL:          "smtp://freshgo:smtp-password@mail:587?from=a@example.org",
		EnvOIDCClientSecret: "oidc-password",
		EnvListen:           "0.0.0.0:9000",
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var help strings.Builder
	fs.SetOutput(&help)
	c := Bind(fs, func(k string) string { return env[k] })
	if err := fs.Parse([]string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parse -h: %v", err)
	}
	for _, secret := range []string{"database-password", "smtp-password", "oidc-password"} {
		if strings.Contains(help.String(), secret) {
			t.Errorf("the help shows %q:\n%s", secret, help.String())
		}
	}
	for _, shown := range []string{defaultDatabaseURL, "0.0.0.0:9000"} {
		if !strings.Contains(help.String(), shown) {
			t.Errorf("the help does not show %q:\n%s", shown, help.String())
		}
	}
	if c.DatabaseURL != env[EnvDatabaseURL] || c.SMTPURL != env[EnvSMTPURL] || c.OIDCClientSecret != env[EnvOIDCClientSecret] {
		t.Errorf("the values of the environment are not taken: %+v", c)
	}
}

func TestTrustedProxies(t *testing.T) {
	prefixes := func(c *Config) []string {
		t.Helper()
		ranges, err := c.Proxies()
		if err != nil {
			t.Fatalf("Proxies of %q: %v", c.TrustedProxies, err)
		}
		var out []string
		for _, r := range ranges {
			out = append(out, r.String())
		}
		return out
	}
	if got := prefixes(parse(t, nil)); !reflect.DeepEqual(got, []string{"127.0.0.0/8", "::1/128"}) {
		t.Errorf("default proxies = %v, want this machine", got)
	}
	c := parse(t, map[string]string{EnvTrustedProxies: "10.1.2.3, 192.168.5.9/16,fd00::1,"})
	if got := prefixes(c); !reflect.DeepEqual(got, []string{"10.1.2.3/32", "192.168.0.0/16", "fd00::1/128"}) {
		t.Errorf("proxies from the environment = %v", got)
	}
	if got := prefixes(parse(t, nil, "-trusted-proxies", "")); got != nil {
		t.Errorf("no proxies = %v, want none", got)
	}
	if err := parse(t, nil, "-trusted-proxies", "proxy.lan").Validate(); err == nil {
		t.Error("Validate accepted a proxy given by name")
	}
}
