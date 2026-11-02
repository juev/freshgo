package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

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

	// The command line still sets a password, and the apps take it.
	if code, _, stderr := runCLIInput(t, "tr0ub4dor&3\n", append([]string{"user", "passwd"}, append(db, "alice")...)...); code != 0 {
		t.Fatalf("user passwd: code %d, stderr %q", code, stderr)
	}
	if login(t, address, "alice", "tr0ub4dor&3") == "" || login(t, address, "alice", "correct horse") != "" {
		t.Error("after user passwd the Google Reader API takes the old password or not the new one")
	}
	if body := fever("tr0ub4dor&3"); !strings.Contains(body, `"auth":1`) {
		t.Errorf("after user passwd the Fever API turned down the new password: %s", body)
	}
	// The new password is still no way into the web interface.
	resp, err = http.PostForm("http://"+address+"/login", url.Values{"username": {"alice"}, "password": {"tr0ub4dor&3"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("the new web password: status %d, want 403", resp.StatusCode)
	}
}
