package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "freshgo.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A flag wins over the file, the file over the environment, the
// environment over the default.
func TestFilePrecedence(t *testing.T) {
	settings, err := ReadFile(writeFile(t, `
listen: 0.0.0.0:9000
refresh-interval: 30m
websub: true
trusted-proxies: ""
base-url:
`))
	if err != nil {
		t.Fatal(err)
	}
	parse := func(env map[string]string, args ...string) *Config {
		t.Helper()
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		c := Bind(fs, func(k string) string { return env[k] })
		settings.Apply(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		return c
	}
	env := map[string]string{EnvListen: "10.0.0.1:1", EnvTrustedProxies: "10.0.0.0/8", EnvLogLevel: "warn", EnvBaseURL: "https://rss.example.org"}
	c := parse(env)
	if c.Listen != "0.0.0.0:9000" || c.RefreshInterval != 30*time.Minute || !c.WebSub || c.TrustedProxies != "" || c.BaseURL != "" {
		t.Errorf("the file over the environment: %+v", c)
	}
	if c.LogLevel != "warn" || c.DatabaseURL != defaultDatabaseURL {
		t.Errorf("what the file leaves out: log level %q, database %q", c.LogLevel, c.DatabaseURL)
	}
	if c := parse(env, "-listen", ":1234", "-websub=false"); c.Listen != ":1234" || c.WebSub {
		t.Errorf("flags over the file: listen %q, websub %v", c.Listen, c.WebSub)
	}
}

func TestFileRefused(t *testing.T) {
	for content, want := range map[string]string{
		"listn: :8080\n":                    `unknown setting "listn"`,
		"refresh-interval: soon\n":          "refresh-interval: ",
		"websub: maybe\n":                   "websub: ",
		"fetch-allowlist: [a, b]\n":         "cannot unmarshal",
		"- listen\n":                        "cannot unmarshal",
		"listen: :8080\n  base-url: x: y\n": "yaml",
	} {
		if _, err := ReadFile(writeFile(t, content)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want one with %q", content, err, want)
		}
	}
	if _, err := ReadFile(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a file that is not there is taken")
	}
	if s, err := ReadFile(writeFile(t, "")); err != nil || len(s) != 0 {
		t.Errorf("an empty file: %v, %v", s, err)
	}
}
