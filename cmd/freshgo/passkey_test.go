package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/store"
)

// With logins by password switched off the web interface takes no password,
// and the apps keep theirs: the Google Reader API and the Fever API take the
// password set with the user, and `user passwd` sets one that works for
// them.
func TestPasswordLoginOffLeavesAppsAlone(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	db := []string{"-database-url", database}
	if code, _, stderr := runCLIInput(t, "correct horse\n", append([]string{"user", "create"}, append(db, "alice")...)...); code != 0 {
		t.Fatalf("user create: code %d, stderr %q", code, stderr)
	}
	direct, err := store.Open(context.Background(), config.DriverSQLite, strings.TrimPrefix(database, "sqlite://"))
	if err != nil {
		t.Fatal(err)
	}
	system, err := direct.System(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	system.PasswordLoginOff, system.FeverEnabled = true, true
	if err := direct.SetSystem(context.Background(), system); err != nil {
		t.Fatal(err)
	}
	if err := direct.Close(); err != nil {
		t.Fatal(err)
	}
	address := serving(t, "listening", db...)

	resp, err := http.PostForm("http://"+address+"/login", url.Values{"username": {"alice"}, "password": {"correct horse"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(resp.Cookies()) != 0 {
		t.Errorf("the web password: status %d, cookies %v; want 403 and none", resp.StatusCode, resp.Cookies())
	}

	fever := func(password string) string {
		t.Helper()
		resp, err := http.PostForm("http://"+address+"/api/fever.php?api", url.Values{"api_key": {store.FeverKey("alice", password)}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if login(t, address, "alice", "correct horse") == "" {
		t.Error("the Google Reader API turned down the password of alice")
	}
	if body := fever("correct horse"); !strings.Contains(body, `"auth":1`) {
		t.Errorf("the Fever API turned down the password of alice: %s", body)
	}

	// The command line still sets a password, and the apps take it. For a
	// plain user it leaves logins by password switched off.
	webLogin := func(user, password string) int {
		t.Helper()
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noFollow.PostForm("http://"+address+"/login", url.Values{"username": {user}, "password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code, _, stderr := runCLIInput(t, "pl4in user\n", append([]string{"user", "create"}, append(db, "plain")...)...); code != 0 {
		t.Fatalf("user create: code %d, stderr %q", code, stderr)
	}
	code, stdout, stderr := runCLIInput(t, "tr0ub4dor&3\n", append([]string{"user", "passwd"}, append(db, "plain")...)...)
	if code != 0 || strings.Contains(stdout, "switched on") {
		t.Fatalf("user passwd of a plain user: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if login(t, address, "plain", "tr0ub4dor&3") == "" || login(t, address, "plain", "pl4in user") != "" {
		t.Error("after user passwd the Google Reader API takes the old password of a plain user or not the new one")
	}
	// The server reads the settings again within a few seconds; it still
	// refuses, however long it is given.
	time.Sleep(6 * time.Second)
	if status := webLogin("plain", "tr0ub4dor&3"); status != http.StatusForbidden {
		t.Errorf("the web password of a plain user: status %d, want 403", status)
	}

	// An administrator is let back in: the command switches the logins on.
	code, stdout, stderr = runCLIInput(t, "tr0ub4dor&3\n", append([]string{"user", "passwd"}, append(db, "alice")...)...)
	if code != 0 || !strings.Contains(stdout, "logins by password were switched off; alice is an administrator, so they are switched on again") {
		t.Fatalf("user passwd of the administrator: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if login(t, address, "alice", "tr0ub4dor&3") == "" || login(t, address, "alice", "correct horse") != "" {
		t.Error("after user passwd the Google Reader API takes the old password or not the new one")
	}
	if body := fever("tr0ub4dor&3"); !strings.Contains(body, `"auth":1`) {
		t.Errorf("after user passwd the Fever API turned down the new password: %s", body)
	}
	deadline := time.Now().Add(15 * time.Second)
	for webLogin("alice", "tr0ub4dor&3") != http.StatusSeeOther {
		if time.Now().After(deadline) {
			t.Fatal("the administrator does not get back in with the new password")
		}
		time.Sleep(200 * time.Millisecond)
	}
}
