package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/miniflux/minifluxtest"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/ttrss/ttrsstest"
)

// userIn makes a database with the user alice and returns its address.
func userIn(t *testing.T) (database string, db *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "freshgo.sqlite")
	database = "sqlite://" + path
	db, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateUser(context.Background(), &store.User{Name: "alice", Settings: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	return database, db
}

// R7: freshgo import -from miniflux puts one account into an existing user,
// reports what it did and what it could not carry over, and can be run
// again; the server may be at an internal address.
func TestImportMiniflux(t *testing.T) {
	database, db := userIn(t)
	s := minifluxtest.New(t)
	args := []string{"import", "-database-url", database, "-from", "miniflux", "-source-url", s.URL, "-source-token", minifluxtest.Token, "-user", "alice"}

	code, stdout, stderr := runCLI(t, args...)
	if code != 0 || stdout != "alice: 3 feeds, 230 entries added, 0 entries updated\n" {
		t.Fatalf("import: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, want := range []string{"warning: rewrite rules not carried over, set on 1 feeds: Plain\n", "warning: block rules", "warning: keep rules", "warning: fetching through the proxy of Miniflux"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in the warnings:\n%s", want, stderr)
		}
	}
	u, err := db.UserByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := db.Feeds(context.Background(), u.ID)
	if err != nil || len(feeds) != 3 {
		t.Fatalf("%d feeds in the account, %v", len(feeds), err)
	}

	code, stdout, stderr = runCLI(t, args...)
	if code != 0 || stdout != "alice: 0 feeds, 0 entries added, 230 entries updated\n" {
		t.Errorf("second import: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// R8: the token may come from the environment, which keeps it out of the
// list of processes.
func TestImportMinifluxTokenFromEnvironment(t *testing.T) {
	database, _ := userIn(t)
	s := minifluxtest.New(t)
	var out, errOut bytes.Buffer
	e := env{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, getenv: func(name string) string {
		if name == "FRESHGO_SOURCE_TOKEN" {
			return minifluxtest.Token
		}
		return ""
	}}
	code := run(context.Background(), e, []string{"import", "-database-url", database, "-from", "miniflux", "-source-url", s.URL, "-user", "alice"})
	if code != 0 || !strings.HasPrefix(out.String(), "alice: 3 feeds, 230 entries added") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

// R9: flags that do not go together are refused before anything is read or
// written, and a refusal of the server leaves the account as it was.
func TestImportMinifluxUsage(t *testing.T) {
	database, db := userIn(t)
	s := minifluxtest.New(t)
	base := []string{"import", "-database-url", database}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no address", []string{"-from", "miniflux", "-source-token", "x", "-user", "alice"}, "-source-url is required"},
		{"no token", []string{"-from", "miniflux", "-source-url", s.URL, "-user", "alice"}, "-source-token is required"},
		{"no user", []string{"-from", "miniflux", "-source-url", s.URL, "-source-token", "x"}, "-user is required"},
		{"unknown user", []string{"-from", "miniflux", "-source-url", s.URL, "-source-token", "x", "-user", "nobody"}, `no user "nobody"`},
		{"unknown source", []string{"-from", "rss-bandit"}, `unknown -from "rss-bandit": freshrss, miniflux or ttrss`},
		{"FreshRSS flag", []string{"-from", "miniflux", "-source-url", s.URL, "-source-token", "x", "-user", "alice", "-data", "."}, "-data goes with -from freshrss"},
		{"Miniflux flag", []string{"-data", ".", "-source-url", s.URL}, "-source-url goes with -from miniflux"},
		{"wrong token", []string{"-from", "miniflux", "-source-url", s.URL, "-source-token", "wrong", "-user", "alice"}, "does not accept the API token"},
		{"bad address", []string{"-from", "miniflux", "-source-url", "ftp://x", "-source-token", "x", "-user", "alice"}, "not an http(s) address"},
	} {
		code, stdout, stderr := runCLI(t, append(append([]string{}, base...), c.args...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: code %d, stdout %q, stderr %q; want 1 and %q", c.name, code, stdout, stderr, c.want)
		}
	}
	u, err := db.UserByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if feeds, err := db.Feeds(context.Background(), u.ID); err != nil || len(feeds) != 0 {
		t.Errorf("refused imports left %d feeds, %v", len(feeds), err)
	}
}

// R15: freshgo import -from ttrss puts one account of a Tiny Tiny RSS server
// into an existing user with the login and password, reports what it did and
// what it could not carry over, and can be run again.
func TestImportTTRSS(t *testing.T) {
	database, db := userIn(t)
	s := ttrsstest.New(t)
	args := []string{"import", "-database-url", database, "-from", "ttrss", "-source-url", s.URL, "-source-user", ttrsstest.User, "-source-password", ttrsstest.Password, "-user", "alice"}

	code, stdout, stderr := runCLI(t, args...)
	if code != 0 || stdout != "alice: 4 feeds, 230 entries added, 0 entries updated\n" {
		t.Fatalf("import: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, want := range []string{"warning: 1 categories were inside others", "warning: refresh period not carried over, set on 1 feeds: Sub\n", "warning: the filters and the logins of feeds"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in the warnings:\n%s", want, stderr)
		}
	}
	u, err := db.UserByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	feeds, err := db.Feeds(context.Background(), u.ID)
	if err != nil || len(feeds) != 4 {
		t.Fatalf("%d feeds in the account, %v", len(feeds), err)
	}
	if s.Logouts() != 1 {
		t.Errorf("the session was closed %d times, want once", s.Logouts())
	}

	code, stdout, stderr = runCLI(t, args...)
	if code != 0 || stdout != "alice: 0 feeds, 0 entries added, 230 entries updated\n" {
		t.Errorf("second import: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// R16: the password may come from the environment, which keeps it out of the
// list of processes.
func TestImportTTRSSPasswordFromEnvironment(t *testing.T) {
	database, _ := userIn(t)
	s := ttrsstest.New(t)
	var out, errOut bytes.Buffer
	e := env{stdin: strings.NewReader(""), stdout: &out, stderr: &errOut, getenv: func(name string) string {
		if name == "FRESHGO_SOURCE_PASSWORD" {
			return ttrsstest.Password
		}
		return ""
	}}
	code := run(context.Background(), e, []string{"import", "-database-url", database, "-from", "ttrss", "-source-url", s.URL, "-source-user", ttrsstest.User, "-user", "alice"})
	if code != 0 || !strings.HasPrefix(out.String(), "alice: 4 feeds, 230 entries added") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
}

// R17: flags that do not go together are refused before anything is read or
// written, and a refusal of the server leaves the account as it was.
func TestImportTTRSSUsage(t *testing.T) {
	database, db := userIn(t)
	s := ttrsstest.New(t)
	off := ttrsstest.New(t)
	off.DisableAPI()
	base := []string{"import", "-database-url", database}
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no address", []string{"-from", "ttrss", "-source-user", "a", "-source-password", "x", "-user", "alice"}, "-source-url is required"},
		{"no login", []string{"-from", "ttrss", "-source-url", s.URL, "-source-password", "x", "-user", "alice"}, "-source-user is required"},
		{"no password", []string{"-from", "ttrss", "-source-url", s.URL, "-source-user", "a", "-user", "alice"}, "-source-password is required"},
		{"no user", []string{"-from", "ttrss", "-source-url", s.URL, "-source-user", "a", "-source-password", "x"}, "-user is required"},
		{"unknown user", []string{"-from", "ttrss", "-source-url", s.URL, "-source-user", "a", "-source-password", "x", "-user", "nobody"}, `no user "nobody"`},
		{"Miniflux flag", []string{"-from", "ttrss", "-source-url", s.URL, "-source-user", "a", "-source-password", "x", "-user", "alice", "-source-token", "t"}, "-source-token goes with -from miniflux"},
		{"TT-RSS flag with Miniflux", []string{"-from", "miniflux", "-source-url", s.URL, "-source-token", "t", "-user", "alice", "-source-user", "a"}, "-source-user goes with -from ttrss"},
		{"TT-RSS flag with FreshRSS", []string{"-data", ".", "-source-password", "x"}, "-source-password goes with -from ttrss"},
		{"wrong password", []string{"-from", "ttrss", "-source-url", s.URL, "-source-user", ttrsstest.User, "-source-password", "wrong", "-user", "alice"}, "does not accept the login and password"},
		{"API off", []string{"-from", "ttrss", "-source-url", off.URL, "-source-user", ttrsstest.User, "-source-password", ttrsstest.Password, "-user", "alice"}, "API is not enabled"},
		{"bad address", []string{"-from", "ttrss", "-source-url", "ftp://x", "-source-user", "a", "-source-password", "x", "-user", "alice"}, "not an http(s) address"},
	} {
		code, stdout, stderr := runCLI(t, append(append([]string{}, base...), c.args...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: code %d, stdout %q, stderr %q; want 1 and %q", c.name, code, stdout, stderr, c.want)
		}
	}
	u, err := db.UserByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if feeds, err := db.Feeds(context.Background(), u.ID); err != nil || len(feeds) != 0 {
		t.Errorf("refused imports left %d feeds, %v", len(feeds), err)
	}
}
