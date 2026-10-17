package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

// doctor runs the command against the database at path and returns what it
// printed, line by line, with its exit code.
func doctor(t *testing.T, path string, args ...string) (int, []string) {
	t.Helper()
	code, stdout, stderr := runCLI(t, append([]string{"doctor", "-database-url", "sqlite://" + path}, args...)...)
	if code != 0 && !strings.Contains(stderr, "of the checks failed") {
		t.Errorf("doctor %v: stderr %q", args, stderr)
	}
	return code, strings.Split(strings.TrimRight(stdout, "\n"), "\n")
}

// line returns the line of the check named, or "".
func line(lines []string, name string) string {
	for _, l := range lines {
		if strings.HasPrefix(l[min(len(l), 6):], name+":") {
			return l
		}
	}
	return ""
}

// The doctor reports a wrong SMTP address, a provider that does not answer,
// a translation service that refuses the key and the feeds that fail, and
// changes nothing.
func TestDoctor(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	db, err := store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	alice := &store.User{Name: "alice"}
	if err := db.CreateUser(ctx, alice); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*store.Feed{
		{UserID: alice.ID, URL: "https://example.org/feed"},
		{UserID: alice.ID, URL: "https://reader:secret@example.org/private", Error: 1760000000},
		{UserID: alice.ID, URL: "https://example.org/behind", Attributes: json.RawMessage(`{"curl_params":{"101":4,"10004":"10.0.0.1:1080"}}`)},
	} {
		if err := db.CreateFeed(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	services := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer right" {
				http.Error(w, "wrong key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "/good/.well-known/openid-configuration":
			issuer := "http://" + r.Host + "/good"
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer services.Close()
	system := store.DefaultSystem()
	system.OIDC = store.OIDC{Issuer: services.URL + "/gone", ClientID: "freshgo"}
	if err := db.SetSystem(ctx, system); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(services.URL, "http://")
	translation := []string{"-fetch-allowlist", host, "-translate-url", services.URL + "/v1", "-translate-model", "m"}

	code, lines := doctor(t, path, append([]string{"-smtp-url", "http://mail.example.org", "-browser-url", "ws://" + host,
		"-oidc-client-secret", "s", "-translate-key", "wrong"}, translation...)...)
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	for name, want := range map[string]string{
		"configuration": "ok    configuration:",
		"smtp":          "FAIL  smtp: mail: not the address of an SMTP server",
		"browser":       "FAIL  browser: ",
		"database":      "ok    database: schema version ",
		"settings":      "ok    settings:",
		"oidc":          "FAIL  oidc: ",
		"translate":     "FAIL  translate: ",
		"proxies":       "warn  proxies: 1 feeds fail until another proxy is chosen",
		"feeds":         "FAIL  feeds: 1 of 3 fail",
	} {
		if got := line(lines, name); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, want %q…", name, got, want)
		}
	}
	output := strings.Join(lines, "\n")
	if !strings.Contains(output, "alice  2  https://example.org/private  since 2025-10-09 08:53:20") {
		t.Errorf("the failing feed is not listed:\n%s", output)
	}
	if !strings.Contains(output, "alice  3  https://example.org/behind  unsupported proxy type: socks4") {
		t.Errorf("the feed behind SOCKS4 is not listed:\n%s", output)
	}
	if strings.Contains(output, "secret") {
		t.Errorf("the output shows a password:\n%s", output)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Errorf("the database changed: %v", err)
	}

	// The same installation set up right.
	db, err = store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	system.OIDC.Issuer = services.URL + "/good"
	if err := db.SetSystem(ctx, system); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, lines = doctor(t, path, append([]string{"-oidc-client-secret", "s", "-translate-key", "right"}, translation...)...)
	for name, want := range map[string]string{
		"smtp":      "skip  smtp: no -smtp-url",
		"oidc":      "ok    oidc: read the discovery document of " + services.URL + "/good",
		"translate": "ok    translate:",
	} {
		if got := line(lines, name); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, want %q…", name, got, want)
		}
	}
}

// Settings that are not JSON are reported; a database that is not there is
// not made.
func TestDoctorBrokenInstallation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	missing := filepath.Join(t.TempDir(), "none.sqlite")
	if code, lines := doctor(t, missing); code != 1 || !strings.HasPrefix(line(lines, "database"), "FAIL  database:") {
		t.Errorf("no database: code %d, %q", code, lines)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("the doctor made a database: %v", err)
	}

	db, err := store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, store.SettingSystem, "{broken"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	code, lines := doctor(t, path)
	if code != 1 || !strings.HasPrefix(line(lines, "settings"), `FAIL  settings: store: setting "system"`) ||
		!strings.HasPrefix(line(lines, "oidc"), "skip  oidc:") {
		t.Errorf("broken settings: code %d,\n%s", code, strings.Join(lines, "\n"))
	}

	if code, lines := doctor(t, path, "-log-format", "xml"); code != 1 || !strings.HasPrefix(line(lines, "configuration"), "FAIL  configuration:") || len(lines) != 1 {
		t.Errorf("wrong flag: code %d, %q", code, lines)
	}
}

// Settings that do not read stop the server and the commands that refresh
// at start, with what to do about them, instead of failing every request.
func TestBrokenSettingsStopTheStart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	db, err := store.Open(ctx, config.DriverSQLite, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, store.SettingSystem, "{broken"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"serve", "refresh", "purge"} {
		code, _, stderr := runCLI(t, command, "-database-url", "sqlite://"+path, "-listen", "127.0.0.1:0")
		if code != 1 || !strings.Contains(stderr, `store: setting "system"`) || !strings.Contains(stderr, "freshgo doctor") ||
			strings.Count(stderr, "\n") > 2 {
			t.Errorf("%s: code %d, stderr %q; want 1 and one error that names freshgo doctor", command, code, stderr)
		}
	}
}
