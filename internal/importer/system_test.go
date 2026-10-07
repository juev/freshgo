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
		// What the config.php of the reference installation says.
		want.Title, want.DefaultUser, want.AuthType, want.APIEnabled = "FreshRSS", "alice", store.AuthForm, true
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
	want.Title, want.DefaultUser, want.APIEnabled = "FreshRSS", "_", false
	want.AuthType, want.AllowAnonymous = store.AuthHTTP, true
	want.Limits.MaxFeeds, want.Limits.MaxRegistrations = 10, 0
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings:\n got %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(unreadable, []string{"reauth_time"}) {
		t.Errorf("unreadable = %v, want only reauth_time", unreadable)
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
