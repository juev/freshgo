// Package transfer carries the entries of a user out of freshgo and back in:
// it writes them as the JSON documents FreshRSS exports, an extension of the
// item format of Google Reader, and reads such documents, and those of other
// readers that write the same format.
//
// The format follows FreshRSS_Entry::toGReader in its "freshrss" mode, the
// export/articles view and FreshRSS_importExport_Controller::importJson of
// FreshRSS at commit 219eaf58. See docs/specs/transfer.md.
package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/store"
)

const (
	itemPrefix   = "tag:google.com,2005:reader/item/"
	statePrefix  = "user/-/state/"
	labelPrefix  = "user/-/label/"
	stateReading = statePrefix + "com.google/reading-list"
	stateRead    = statePrefix + "com.google/read"
	stateUnread  = statePrefix + "com.google/unread"
	stateStarred = statePrefix + "com.google/starred"
	stateMain    = statePrefix + "org.freshrss/main"
	stateImp     = statePrefix + "org.freshrss/important"
	stateHidden  = statePrefix + "org.freshrss/hidden"
)

// Priorities of feeds the documents tell apart.
const (
	priorityImportant = 20
	priorityHidden    = -10
)

// exportPage is how many entries are read from the database at a time.
const exportPage = 500

type link struct {
	Href string `json:"href"`
	Type string `json:"type,omitempty"`
}

type origin struct {
	StreamID string `json:"streamId"`
	HTMLURL  string `json:"htmlUrl"`
	Title    string `json:"title"`
	FeedURL  string `json:"feedUrl"`
}

type content struct {
	Content string `json:"content"`
}

type media struct {
	Href   string `json:"href"`
	Type   string `json:"type"`
	Length int64  `json:"length,omitempty"`
}

// item is an entry as a document holds it.
type item struct {
	FreshRSSID    int64    `json:"frss:id"`
	ID            string   `json:"id"`
	CrawlTimeMsec string   `json:"crawlTimeMsec"`
	TimestampUsec string   `json:"timestampUsec"`
	Published     int64    `json:"published"`
	Title         string   `json:"title"`
	Canonical     []link   `json:"canonical"`
	Alternate     []link   `json:"alternate"`
	Categories    []string `json:"categories"`
	Origin        origin   `json:"origin"`
	Content       content  `json:"content"`
	GUID          string   `json:"guid"`
	Enclosure     []media  `json:"enclosure,omitempty"`
	Author        string   `json:"author,omitempty"`
}

// specialChars does what PHP htmlspecialchars does: FreshRSS keeps titles
// and authors that way, and writes them out as it keeps them.
var specialChars = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// withoutCredentials returns an address without its user and password.
func withoutCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func newItem(e *store.Entry, f *store.Feed, labels []string) item {
	it := item{
		FreshRSSID: e.ID,
		ID:         itemPrefix + fmt.Sprintf("%016x", e.ID),
		// The identifier is the time the entry was added, in microseconds.
		CrawlTimeMsec: strconv.FormatInt(e.ID/1000, 10),
		TimestampUsec: strconv.FormatInt(e.ID, 10),
		Published:     e.Published,
		Title:         specialChars.Replace(e.Title),
		Canonical:     []link{{Href: e.Link}},
		Alternate:     []link{{Href: e.Link, Type: "text/html"}},
		Categories:    []string{stateReading},
		Origin:        origin{StreamID: "feed/" + strconv.FormatInt(e.FeedID, 10)},
		Content:       content{e.Content},
		GUID:          e.GUID,
		Author:        specialChars.Replace(strings.Join(e.Authors, "; ")),
	}
	if f != nil {
		it.Origin.HTMLURL, it.Origin.Title, it.Origin.FeedURL = f.Website, f.Name, withoutCredentials(f.URL)
		switch {
		case f.Priority >= priorityImportant:
			it.Categories = append(it.Categories, stateMain, stateImp)
		case f.Priority >= store.PriorityMain:
			it.Categories = append(it.Categories, stateMain)
		case f.Priority <= priorityHidden:
			it.Categories = append(it.Categories, stateHidden)
		}
	}
	var attrs struct {
		Enclosures []struct {
			URL    string `json:"url"`
			Type   string `json:"type"`
			Medium string `json:"medium"`
			Length any    `json:"length"`
		} `json:"enclosures"`
	}
	// Attributes of another shape hold no enclosures.
	_ = json.Unmarshal(e.Attributes, &attrs)
	for _, enc := range attrs.Enclosures {
		if enc.URL == "" {
			continue
		}
		m := media{Href: enc.URL, Type: enc.Type}
		if m.Type == "" {
			m.Type = enc.Medium
		}
		switch n := enc.Length.(type) {
		case float64:
			m.Length = int64(n)
		case string:
			m.Length, _ = strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		}
		it.Enclosure = append(it.Enclosure, m)
	}
	if e.IsRead {
		it.Categories = append(it.Categories, stateRead)
	} else {
		it.Categories = append(it.Categories, stateUnread)
	}
	if e.IsFavorite {
		it.Categories = append(it.Categories, stateStarred)
	}
	for _, name := range labels {
		it.Categories = append(it.Categories, labelPrefix+name)
	}
	// What the feed filed the entry under goes in as it is.
	it.Categories = append(it.Categories, e.Tags...)
	return it
}

// Document says which entries a document holds and what it calls itself.
type Document struct {
	// Kind names the list in the identifier of the document: "starred", or
	// "feed/<id>".
	Kind string
	// Title is for whoever opens the document.
	Title string
	// Set picks the entries.
	Set store.EntrySet
}

// Write writes the entries of a document of the user, oldest first. Handlers
// of EntryBeforeDisplay see each entry and may leave it out.
func Write(ctx context.Context, w io.Writer, db *store.Store, registry *hooks.Registry, u *store.User, d Document) error {
	feeds, err := db.Feeds(ctx, u.ID)
	if err != nil {
		return err
	}
	byID := make(map[int64]*store.Feed, len(feeds))
	for _, f := range feeds {
		byID[f.ID] = f
	}
	head, err := json.Marshal(struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Author string `json:"author"`
	}{"user/" + strings.ReplaceAll(u.Name, "/", "") + "/state/org.freshrss/" + d.Kind, d.Title, u.Name})
	if err != nil {
		return err
	}
	// The head is an object the items are added to.
	if _, err := fmt.Fprintf(w, "%s,\"items\":[\n", head[:len(head)-1]); err != nil {
		return err
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	first := true
	for after := ""; ; {
		entries, next, err := db.ListPage(ctx, u.ID, store.Listing{Set: d.Set, Ascending: true, After: after, Limit: exportPage})
		if err != nil {
			return err
		}
		ids := make([]int64, len(entries))
		for i, e := range entries {
			ids[i] = e.ID
		}
		labels, err := db.EntryLabels(ctx, u.ID, ids)
		if err != nil {
			return err
		}
		for _, e := range entries {
			e, ok := registry.EntryBeforeDisplay.Call(ctx, e)
			if !ok {
				continue
			}
			if !first {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			first = false
			// Encode ends every item with a line break.
			if err := encoder.Encode(newItem(e, byID[e.FeedID], labels[e.ID])); err != nil {
				return err
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	_, err = io.WriteString(w, "]}\n")
	return err
}
