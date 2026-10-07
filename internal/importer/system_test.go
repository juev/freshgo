package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/juev/freshgo/internal/store"
)

// The settings of the installation come over, the unset ones with the
// defaults of FreshRSS.
func TestImportSystemSettings(t *testing.T) {
	eachDestination(t, func(t *testing.T, dst *store.Store) {
		ctx := context.Background()
		if _, err := Run(ctx, dst, Options{DataDir: sqliteReferenceDir}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		got, err := dst.System(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := store.DefaultSystem()
		// What the config.php of the reference installation says, but for
		// the title, which there is the one FreshRSS gives itself.
		want.DefaultUser, want.AuthType, want.APIEnabled = "alice", store.AuthForm, true
		if !reflect.DeepEqual(got, want) {
			t.Errorf("System after the import:\n got %+v\nwant %+v", got, want)
		}
	})
}

func TestReadSystemSettings(t *testing.T) {
	got, unreadable := readSystemSettings(map[string]any{
		"auth_type":       "http_auth",
		"allow_anonymous": true,
		"limits":          map[string]any{"max_feeds": 10, "max_registrations": 0},
		"reauth_time":     "soon",
		"base_url":        "https://rss.example.org",
	})
	want := store.DefaultSystem()
	want.DefaultUser, want.APIEnabled = "_", false
	want.AuthType, want.AllowAnonymous = store.AuthHTTP, true
	want.Limits.MaxFeeds, want.Limits.MaxRegistrations = 10, 0
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(unreadable, []string{"reauth_time"}) {
		t.Errorf("unreadable = %v, want only reauth_time", unreadable)
	}
}

// A title somebody chose comes over; the name FreshRSS gives itself does not.
func TestImportedTitle(t *testing.T) {
	for _, tc := range []struct {
		name string
		conf map[string]any
		want string
	}{
		{"not set", map[string]any{}, "freshgo"},
		{"the default of FreshRSS", map[string]any{"title": "FreshRSS"}, "freshgo"},
		{"chosen", map[string]any{"title": "News of the family"}, "News of the family"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := readSystemSettings(tc.conf); got.Title != tc.want {
				t.Errorf("title = %q, want %q", got.Title, tc.want)
			}
		})
	}
}

// The proxy FreshRSS sends every feed through becomes that of the
// installation.
func TestImportedProxy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options any
		want    string
		fails   bool
	}{
		{"no options", []any{}, "", false},
		{"options without a proxy", map[string]any{"64": false}, "", false},
		{"address with a port", map[string]any{"10004": "proxy.example:3128"}, "http://proxy.example:3128", false},
		{"port of its own", map[string]any{"101": 0, "10004": "127.0.0.1", "59": 8080}, "http://127.0.0.1:8080", false},
		{"the port of the address wins", map[string]any{"10004": "127.0.0.1:3128", "59": 8080}, "http://127.0.0.1:3128", false},
		{"socks5h with credentials", map[string]any{"101": 7, "10004": "socks.example:1080", "10006": "bob:secret"}, "socks5h://bob:secret@socks.example:1080", false},
		{"a scheme in the address", map[string]any{"101": 5, "10004": "socks5://10.0.0.1:1080"}, "socks5://10.0.0.1:1080", false},
		{"switched off", map[string]any{"101": -1, "10004": "proxy.example:3128"}, "", false},
		{"a kind freshgo has not", map[string]any{"101": 4, "10004": "proxy.example:1080"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, unreadable := readSystemSettings(map[string]any{"curl_options": tc.options})
			if got.Proxy != tc.want {
				t.Errorf("proxy = %q, want %q", got.Proxy, tc.want)
			}
			if failed := len(unreadable) == 1 && unreadable[0] == "curl_options"; failed != tc.fails || len(unreadable) > 1 {
				t.Errorf("unreadable = %v, want curl_options there: %v", unreadable, tc.fails)
			}
		})
	}
}

// The terms of use FreshRSS keeps in a file come along.
func TestTermsAreImported(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../testdata/reference/sqlite/data")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tos.html"), []byte("<p>Be kind.</p>\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf, err := readSystemConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if conf.settings.TOS != "<p>Be kind.</p>" {
		t.Errorf("terms = %q", conf.settings.TOS)
	}
	if plain, err := readSystemConfig("../../testdata/reference/sqlite/data"); err != nil || plain.settings.TOS != "" {
		t.Errorf("terms of an installation without any = %q, %v", plain.settings.TOS, err)
	}
}
