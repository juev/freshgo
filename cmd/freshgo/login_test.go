package main

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
)

// One count of failed logins serves the web interface and the API: moving
// from one to the other does not start the pause anew.
func TestLoginGuardIsShared(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	db := []string{"-database-url", database}
	if code, _, stderr := runCLIInput(t, "correct horse\n", append([]string{"user", "create"}, append(db, "alice")...)...); code != 0 {
		t.Fatalf("user create: code %d, stderr %q", code, stderr)
	}
	address := serving(t, "listening", db...)

	for i := range 5 {
		if status, body := api(t, address, "/accounts/ClientLogin?Email=alice&Passwd=wrong", ""); status != http.StatusUnauthorized {
			t.Fatalf("failed API login %d: status %d, body %q", i+1, status, body)
		}
	}
	resp, err := http.PostForm("http://"+address+"/login", url.Values{"username": {"alice"}, "password": {"correct horse"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("the web login after five API failures: status %d, Retry-After %q, body\n%s",
			resp.StatusCode, resp.Header.Get("Retry-After"), page)
	}
	if status, body := api(t, address, "/accounts/ClientLogin?Email=alice&Passwd="+url.QueryEscape("correct horse"), ""); status != http.StatusTooManyRequests {
		t.Errorf("the API after the web login was held up: status %d, body %q", status, body)
	}
}
