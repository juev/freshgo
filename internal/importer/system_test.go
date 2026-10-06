package importer

import (
	"context"
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
