package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// site serves a feed at /feed and at /news.xml, and a page at / that
// announces the first.
func site(t *testing.T) (address, host string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed", "/news.xml":
			_, _ = io.WriteString(w, testFeed)
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, `<html><head><link rel="alternate" type="application/rss+xml" href="/feed"></head></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, strings.TrimPrefix(server.URL, "http://")
}

// api sends a request to the Google Reader API of a running server.
func api(t *testing.T, address, path, token string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "GoogleLogin auth="+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp.StatusCode, string(data)
}

// login returns the token ClientLogin gives for the password, empty when it
// is turned down.
func login(t *testing.T, address, user, password string) string {
	t.Helper()
	status, body := api(t, address, "/accounts/ClientLogin?Email="+url.QueryEscape(user)+"&Passwd="+url.QueryEscape(password), "")
	if status != http.StatusOK {
		return ""
	}
	token := regexp.MustCompile(`(?m)^Auth=(\S+)$`).FindStringSubmatch(body)
	if token == nil {
		t.Fatalf("ClientLogin: no token in %q", body)
	}
	return token[1]
}

// An installation set up from the command line alone, without an import,
// serves API clients.
func TestUsersFromTheCommandLine(t *testing.T) {
	database := "sqlite://" + filepath.Join(t.TempDir(), "freshgo.sqlite")
	db := []string{"-database-url", database}
	feeds, host := site(t)

	code, stdout, stderr := runCLIInput(t, "correct horse\n", append([]string{"user", "create"}, append(db, "alice")...)...)
	if code != 0 || stdout != "user alice created\n" || stderr != "" {
		t.Fatalf("user create: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// The password is the first line as typed, without the line end of any system.
	if code, _, stderr := runCLIInput(t, "battery staple\r\nignored\n", append([]string{"user", "create"}, append(db, "bob")...)...); code != 0 {
		t.Fatalf("user create bob: code %d, stderr %q", code, stderr)
	}
	for _, bad := range []struct{ name, input, want string }{
		{"alice", "another password\n", `user "alice" already exists`},
		{"carol", "short\n", "at least 7 characters"},
		{"carol", "", "at least 7 characters"},
		{"no/slash", "long enough\n", "cannot be a user name"},
		{"", "long enough\n", "cannot be a user name"},
	} {
		code, stdout, stderr := runCLIInput(t, bad.input, append([]string{"user", "create"}, append(db, bad.name)...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, bad.want) {
			t.Errorf("user create %q with password %q: code %d, stdout %q, stderr %q; want 1 and %q",
				bad.name, bad.input, code, stdout, stderr, bad.want)
		}
	}
	if code, _, stderr := runCLI(t, append([]string{"user", "create"}, db...)...); code != 1 || !strings.Contains(stderr, "a user name is required") {
		t.Errorf("user create without a name: code %d, stderr %q", code, stderr)
	}

	// The address of the site is enough: the feed it announces is found.
	code, stdout, stderr = runCLI(t, append([]string{"feed", "add"}, append(db, "-fetch-allowlist", host, "-user", "alice", "-category", "News", feeds+"/")...)...)
	if want := "feed 1: Blog (" + feeds + "/feed), 2 entries\n"; code != 0 || stdout != want {
		t.Fatalf("feed add: code %d, stdout %q, stderr %q; want %q", code, stdout, stderr, want)
	}
	code, _, stderr = runCLI(t, append([]string{"feed", "add"}, append(db, "-fetch-allowlist", host, "-user", "alice", feeds+"/feed")...)...)
	if code != 1 || !strings.Contains(stderr, "already subscribed") {
		t.Errorf("feed add twice: code %d, stderr %q", code, stderr)
	}
	for _, bad := range []struct {
		args []string
		want string
	}{
		{[]string{"-user", "nobody", feeds + "/feed"}, `no user "nobody"`},
		{[]string{feeds + "/feed"}, "a user name is required"},
		{[]string{"-user", "alice"}, "the address of a feed is required"},
		{[]string{"-user", "alice", feeds + "/missing"}, "404"},
		{[]string{"-user", "alice", feeds + "/feed", "extra"}, `unexpected argument "extra"`},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"feed", "add"}, append(append(db, "-fetch-allowlist", host), bad.args...)...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, bad.want) {
			t.Errorf("feed add %v: code %d, stdout %q, stderr %q; want 1 and %q", bad.args, code, stdout, stderr, bad.want)
		}
	}

	code, stdout, _ = runCLI(t, append([]string{"user", "list"}, db...)...)
	if want := "alice: 1 feeds, 2 entries\nbob: 0 feeds, 0 entries\n"; code != 0 || stdout != want {
		t.Errorf("user list: code %d, stdout %q; want %q", code, stdout, want)
	}

	// The feed was refreshed a moment ago, so the server only answers.
	address := serving(t, "listening", db...)
	if login(t, address, "alice", "wrong password") != "" || login(t, address, "alice", "battery staple") != "" {
		t.Error("ClientLogin took a password that is not the one of alice")
	}
	if login(t, address, "bob", "battery staple") == "" {
		t.Error("ClientLogin turned down the password of bob, typed with a CRLF line end")
	}
	token := login(t, address, "alice", "correct horse")
	if token == "" {
		t.Fatal("ClientLogin turned down the password given to user create")
	}
	status, body := api(t, address, "/reader/api/0/subscription/list?output=json", token)
	var list struct {
		Subscriptions []struct {
			Title      string `json:"title"`
			URL        string `json:"url"`
			Categories []struct {
				Label string `json:"label"`
			} `json:"categories"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal([]byte(body), &list); status != http.StatusOK || err != nil {
		t.Fatalf("subscription/list: status %d, body %q, %v", status, body, err)
	}
	if len(list.Subscriptions) != 1 || list.Subscriptions[0].Title != "Blog" || list.Subscriptions[0].URL != feeds+"/feed" ||
		len(list.Subscriptions[0].Categories) != 1 || list.Subscriptions[0].Categories[0].Label != "News" {
		t.Errorf("subscription/list = %+v; want the feed added from the command line, in News", list.Subscriptions)
	}
	// Added to the main stream, not to its category only.
	if status, body := api(t, address, "/reader/api/0/stream/items/ids?output=json&s=user/-/state/org.freshrss/main&n=10", token); status != http.StatusOK || strings.Count(body, `"id"`) != 2 {
		t.Errorf("main stream: status %d, body %q; want the 2 entries of the feed", status, body)
	}

	code, stdout, stderr = runCLIInput(t, "tr0ub4dor&3\n", append([]string{"user", "passwd"}, append(db, "alice")...)...)
	if code != 0 || !strings.HasPrefix(stdout, "password of alice changed") {
		t.Fatalf("user passwd: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if status, _ := api(t, address, "/reader/api/0/subscription/list?output=json", token); status != http.StatusUnauthorized {
		t.Errorf("request with the token of the old password: status %d, want 401", status)
	}
	if login(t, address, "alice", "correct horse") != "" || login(t, address, "alice", "tr0ub4dor&3") == "" {
		t.Error("after user passwd the old password works or the new one does not")
	}
	if code, _, stderr := runCLIInput(t, "short\n", append([]string{"user", "passwd"}, append(db, "alice")...)...); code != 1 || !strings.Contains(stderr, "at least 7") {
		t.Errorf("user passwd with a short password: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLIInput(t, "long enough\n", append([]string{"user", "passwd"}, append(db, "nobody")...)...); code != 1 || !strings.Contains(stderr, `no user "nobody"`) {
		t.Errorf("user passwd of an unknown user: code %d, stderr %q", code, stderr)
	}

	code, stdout, stderr = runCLI(t, append([]string{"user", "delete"}, append(db, "alice")...)...)
	if code != 0 || stdout != "user alice deleted\n" {
		t.Fatalf("user delete: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if login(t, address, "alice", "tr0ub4dor&3") != "" {
		t.Error("ClientLogin still takes the password of a deleted user")
	}
	if code, _, stderr := runCLI(t, append([]string{"user", "delete"}, append(db, "alice")...)...); code != 1 || !strings.Contains(stderr, `no user "alice"`) {
		t.Errorf("user delete twice: code %d, stderr %q", code, stderr)
	}
	code, stdout, _ = runCLI(t, append([]string{"user", "list"}, db...)...)
	if code != 0 || stdout != "bob: 0 feeds, 0 entries\n" {
		t.Errorf("user list after the deletion: code %d, stdout %q", code, stdout)
	}
}

// Subscriptions written by one installation are taken by another, from a
// file and from standard input.
func TestOPMLFromTheCommandLine(t *testing.T) {
	dir := t.TempDir()
	feeds, host := site(t)
	source := []string{"-database-url", "sqlite://" + filepath.Join(dir, "source.sqlite"), "-fetch-allowlist", host}
	target := []string{"-database-url", "sqlite://" + filepath.Join(dir, "target.sqlite"), "-fetch-allowlist", host}
	for _, db := range [][]string{source, target} {
		if code, _, stderr := runCLIInput(t, "correct horse\n", append([]string{"user", "create"}, append(db, "alice")...)...); code != 0 {
			t.Fatalf("user create: code %d, stderr %q", code, stderr)
		}
	}
	for category, address := range map[string]string{"News": feeds + "/feed", "": feeds + "/news.xml"} {
		if code, _, stderr := runCLI(t, append([]string{"feed", "add"}, append(source, "-user", "alice", "-category", category, address)...)...); code != 0 {
			t.Fatalf("feed add %s: code %d, stderr %q", address, code, stderr)
		}
	}

	code, exported, stderr := runCLI(t, append([]string{"opml", "export"}, append(source, "-user", "alice")...)...)
	if code != 0 || !strings.Contains(exported, `xmlUrl="`+feeds+`/feed"`) || !strings.Contains(exported, `xmlUrl="`+feeds+`/news.xml"`) ||
		!strings.Contains(exported, `<outline text="News">`) {
		t.Fatalf("opml export: code %d, stderr %q, document:\n%s", code, stderr, exported)
	}
	if code, stdout, stderr := runCLI(t, append([]string{"opml", "export"}, source...)...); code != 1 || stdout != "" || !strings.Contains(stderr, "a user name is required") {
		t.Errorf("opml export without a user: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	// One feed comes from a file; the whole document then adds the other.
	// A feed the user has stays in its category, so the one taken first is
	// the one without a category of its own.
	one := filepath.Join(dir, "one.opml")
	document := `<opml version="2.0"><body><outline text="Blog" type="rss" xmlUrl="` + feeds + `/news.xml"/></body></opml>`
	if err := os.WriteFile(one, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, append([]string{"opml", "import"}, append(target, "-user", "alice", one)...)...)
	if code != 0 || stdout != "alice: 1 feeds added, 0 of them failed to refresh\n" {
		t.Fatalf("opml import of a file: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = runCLIInput(t, exported, append([]string{"opml", "import"}, append(target, "-user", "alice")...)...)
	if code != 0 || stdout != "alice: 1 feeds added, 0 of them failed to refresh\n" {
		t.Fatalf("opml import from standard input: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, stdout, _ = runCLI(t, append([]string{"user", "list"}, target...)...)
	if code != 0 || stdout != "alice: 2 feeds, 4 entries\n" {
		t.Errorf("user list after the imports: code %d, stdout %q; want both feeds with their entries", code, stdout)
	}
	// What the second installation writes is what the first one wrote,
	// apart from the time of writing.
	created := regexp.MustCompile(`<dateCreated>[^<]*</dateCreated>`)
	code, again, _ := runCLI(t, append([]string{"opml", "export"}, append(target, "-user", "alice")...)...)
	if code != 0 || created.ReplaceAllString(again, "") != created.ReplaceAllString(exported, "") {
		t.Errorf("opml export of the importing installation differs:\n%s\nwant:\n%s", again, exported)
	}

	// A feed that does not answer is added all the same and reported.
	broken := `<opml version="2.0"><body><outline text="Gone" type="rss" xmlUrl="` + feeds + `/missing"/></body></opml>`
	code, stdout, stderr = runCLIInput(t, broken, append([]string{"opml", "import"}, append(target, "-user", "alice")...)...)
	if code != 0 || stdout != "alice: 1 feeds added, 1 of them failed to refresh\n" || !strings.Contains(stderr, "imported feed failed") {
		t.Errorf("opml import of a dead feed: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, bad := range []struct {
		input string
		args  []string
		want  string
	}{
		{"this is not OPML", []string{"-user", "alice"}, "cannot be read"},
		{exported, []string{"-user", "nobody"}, `no user "nobody"`},
		{"", []string{"-user", "alice", filepath.Join(dir, "missing.opml")}, "no such file"},
	} {
		code, stdout, stderr := runCLIInput(t, bad.input, append([]string{"opml", "import"}, append(target, bad.args...)...)...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, bad.want) {
			t.Errorf("opml import %v: code %d, stdout %q, stderr %q; want 1 and %q", bad.args, code, stdout, stderr, bad.want)
		}
	}
}

func TestCommandGroups(t *testing.T) {
	for _, group := range []string{"user", "feed", "opml"} {
		if code, stdout, stderr := runCLI(t, group); code != 1 || stdout != "" || !strings.Contains(stderr, "no action given") || !strings.Contains(stderr, "Actions:") {
			t.Errorf("%s: code %d, stdout %q, stderr %q; want 1 and the list of actions", group, code, stdout, stderr)
		}
		if code, _, stderr := runCLI(t, group, "frobnicate"); code != 1 || !strings.Contains(stderr, `unknown action "frobnicate"`) {
			t.Errorf("%s frobnicate: code %d, stderr %q", group, code, stderr)
		}
		if code, stdout, stderr := runCLI(t, group, "-h"); code != 0 || stderr != "" || !strings.Contains(stdout, "Usage: freshgo "+group+" <action>") {
			t.Errorf("%s -h: code %d, stdout %q, stderr %q", group, code, stdout, stderr)
		}
	}
	if code, stdout, stderr := runCLI(t, "feed", "add", "-h"); code != 0 || stdout != "" ||
		!strings.Contains(stderr, "Usage: freshgo feed add [flags] <address>") || !strings.Contains(stderr, "-category") {
		t.Errorf("feed add -h: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}
