package refresh

import (
	"encoding/json"
	"html"
	"reflect"
	"testing"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

func TestRefreshJSONEnclosures(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		u := w.user("alice", `{}`)
		const audio = `https://example.org/episode.mp3?x=1&y=2`
		const transcript = `https://example.org/transcript.txt`
		const notes = `https://example.org/notes.pdf`
		const invalid = `https://example.org/invalid.pdf`
		const standard = `{"version":"https://jsonfeed.org/version/1.1","title":"Podcast","items":[{"id":"episode","title":"Episode","url":"https://example.org/episode","content_html":"<p>Notes</p>"`
		const dotted = `{"posts":[{"title":"Episode","address":"https://example.org/episode","description":"<p>Notes</p>"`
		w.serveBody("/podcast", "application/json", standard+`}]}`)
		w.serveBody("/posts", "application/json", dotted+`}]}`)
		standardFeed := w.feed(u, "/podcast", func(f *store.Feed) { f.Kind = scrape.KindJSONFeed })
		dottedFeed := w.feed(u, "/posts", func(f *store.Feed) {
			f.Kind = scrape.KindJSONDotNotation
			f.Attributes = json.RawMessage(`{"json_dotnotation":{"item":"posts","itemTitle":"title","itemUri":"address","itemContentHTML":"description","itemAttachment":"media.files","itemAttachmentUrl":"source.href","itemAttachmentType":"format","itemAttachmentLength":"meta.bytes"}}`)
		})
		if st := w.runOne(); st.Failed != 0 || st.NewEntries != 2 {
			t.Fatalf("first refresh: %+v", st)
		}
		before := map[int64]*store.Entry{}
		for _, f := range []*store.Feed{standardFeed, dottedFeed} {
			entries := w.entries(f)
			if len(entries) != 1 {
				t.Fatalf("first refresh: feed %d has %d entries, want 1", f.ID, len(entries))
			}
			before[f.ID] = entries[0]
		}
		w.serveBody("/podcast", "application/json", standard+`,"attachments":[`+
			`{"url":"`+audio+`","mime_type":"audio/mpeg","size_in_bytes":1200},`+
			`{"url":"`+transcript+`","mime_type":"text/plain"},`+
			`{"url":"`+notes+`","size_in_bytes":"unknown"},{"mime_type":"audio/mpeg"},`+
			`{"url":"`+invalid+`","size_in_bytes":true},{"url":"ftp://example.org/ignored"}]}]}`)
		w.serveBody("/posts", "application/json", dotted+`,"media":{"files":{"one":{"source":{"href":"`+audio+`"},"format":"audio/mpeg","meta":{"bytes":"1200"}},`+
			`"two":{"source":{"href":"`+transcript+`"},"format":"text/plain"}}}}]}`)
		w.later()
		st := w.runOne()
		if st.Failed != 0 || st.NewEntries != 0 || st.UpdatedEntries != 2 {
			t.Errorf("second refresh: %+v; want 0 new and 2 updated", st)
		}
		size := 1200
		want := []feed.Enclosure{
			{URL: audio, Type: "audio/mpeg", Length: &size},
			{URL: transcript, Type: "text/plain"},
		}
		for _, f := range []*store.Feed{standardFeed, dottedFeed} {
			entries := w.entries(f)
			if len(entries) != 1 {
				t.Fatalf("second refresh: feed %d has %d entries, want 1", f.ID, len(entries))
			}
			e, old := entries[0], before[f.ID]
			if e.ID != old.ID || e.GUID != old.GUID || e.Content != old.Content || e.Link != old.Link {
				t.Errorf("feed %d: refreshing attachments changed entry identity or content", f.ID)
			}
			var attrs struct {
				Enclosures []feed.Enclosure `json:"enclosures"`
			}
			if err := json.Unmarshal(e.Attributes, &attrs); err != nil {
				t.Fatal(err)
			}
			for i := range attrs.Enclosures {
				attrs.Enclosures[i].URL = html.UnescapeString(attrs.Enclosures[i].URL)
			}
			expected := want
			if f == standardFeed {
				expected = append(append([]feed.Enclosure(nil), want...), feed.Enclosure{URL: notes}, feed.Enclosure{URL: invalid})
			}
			if !reflect.DeepEqual(attrs.Enclosures, expected) {
				t.Errorf("kind %d: enclosures %s, want %v", f.Kind, e.Attributes, expected)
			}
		}
		w.later()
		if st := w.runOne(); st.Failed != 0 || st.NewEntries != 0 || st.UpdatedEntries != 0 {
			t.Errorf("unchanged refresh: %+v", st)
		}
	})
}
