package greader

import (
	"crypto/sha256"
	"fmt"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
	"strconv"
	"strings"
	"testing"
)

func TestTranslationTruncatedReaderSourceIsRefused(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.handler.translator = translate.New(w.db, nil)
		f := w.feed(w.alice, &store.Feed{Name: "Feed"})
		e := &store.Entry{FeedID: f.ID, Title: "Long", Content: strings.Repeat("x", maxContent+1)}
		w.entries(w.alice, e)
		code, _ := w.get(w.auth, "/api/freshgo/translation/entry?id="+strconv.FormatInt(e.ID, 10)+"&language=ru")
		if code != 409 {
			t.Fatalf("truncated source = %d, want 409 before any paid work", code)
		}
	})
}

func TestTranslationSourceHashUsesDecodedReaderTitle(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.handler.translator = translate.New(w.db, nil)
		f := w.feed(w.alice, &store.Feed{Name: "Feed"})
		e := &store.Entry{FeedID: f.ID, Title: "Hello & < reader", Content: "<p>body</p>"}
		w.entries(w.alice, e)
		var got clientTranslation
		w.json("/api/freshgo/translation/entry?id="+strconv.FormatInt(e.ID, 10)+"&language=ru", &got)
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(e.Title+"\x00"+e.Content)))
		if got.SourceHash != want {
			t.Fatalf("source fingerprint = %s, want decoded-title fingerprint %s", got.SourceHash, want)
		}
	})
}
