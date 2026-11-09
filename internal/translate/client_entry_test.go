package translate

import (
	"context"
	"encoding/json"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"golang.org/x/text/language"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientStepPreservesConcurrentWebOriginalChoice(t *testing.T) {
	for _, engine := range storetest.Engines() {
		t.Run(engine.Name, func(t *testing.T) {
			ctx := context.Background()
			driver, dsn := engine.New(t)
			db, err := store.Open(ctx, driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			u := &store.User{Name: "alice", Settings: json.RawMessage(`{}`)}
			if err := db.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			f := &store.Feed{UserID: u.ID, URL: "https://example.org/feed"}
			if err := db.CreateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
			e := &store.Entry{FeedID: f.ID, Title: "Hello", Content: strings.Repeat("<p>Hello "+strings.Repeat("x", 1000)+"</p>", 5)}
			if err := db.InsertEntries(ctx, u.ID, []*store.Entry{e}); err != nil {
				t.Fatal(err)
			}
			var pause atomic.Bool
			var once sync.Once
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			s := newService(t, func(system, user string) string {
				if pause.Load() {
					once.Do(func() { close(entered) })
					<-release
				}
				return translator(system, user)
			})
			tr := New(db, s.Client)
			first, err := tr.Step(ctx, u.ID, e.ID, language.Russian)
			if err != nil || first.Complete() {
				t.Fatalf("first step %+v %v", first, err)
			}
			pause.Store(true)
			finished := make(chan error, 1)
			go func() {
				_, _, err := tr.ForClient(ctx, u.ID, e.ID, language.Russian, Fingerprint(e.Title, e.Content), first.Done, true)
				finished <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("client step never reached model")
			}
			if _, err := tr.Show(ctx, u.ID, e.ID, language.Russian, false); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client step did not finish")
			}
			fresh, err := db.EntryByID(ctx, u.ID, e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if Of(fresh, "ru").Shown {
				t.Fatal("client step overwrote the web reader's newer Show original choice")
			}
		})
	}
}
