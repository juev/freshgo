package opml

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// The reference installation covers the common settings through the API
// (TestReferenceAPI of internal/greader). What is expected here of the rest
// comes from reading the code of FreshRSS.

func eachEngine(t *testing.T, test func(t *testing.T, db *store.Store, u *store.User)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			ctx := context.Background()
			driver, dsn := e.New(t)
			db, err := store.Open(ctx, driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			u := &store.User{Name: "alice", Settings: []byte(`{"language":"en"}`)}
			if err := db.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			test(t, db, u)
		})
	}
}

// every is a feed with every setting OPML carries.
const every = `{
	"unicityCriteria": "sha1:link_published",
	"unicityCriteriaForced": true,
	"xpath": {"item": "//article", "itemTitle": "h2", "itemUid": "@id"},
	"filters": [
		{"search": "intitle:ads", "actions": ["read"]},
		{"search": "author:bot", "actions": ["star"]},
		{"search": "sponsored \"by us\"", "actions": ["read", "star"]}
	],
	"path_entries_conditions": ["intitle:long", "author:x"],
	"path_entries_filter": ".ad, aside",
	"curl_params": {
		"10022": "a=b", "10031": "", "52": false, "68": 3, "47": true, "10015": "q=1",
		"10004": "proxy.example:8080", "101": 3, "10018": "agent/1.0",
		"10023": ["X-One: 1", "X-Two: 2"], "64": false
	},
	"timeout": 30
}`

func TestExportAndImportKeepSettings(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *store.Store, u *store.User) {
		ctx := context.Background()
		// Categories come in the order of their positions, then by name.
		for _, c := range []*store.Category{
			{Name: "beta"}, {Name: "Alpha"}, {Name: "Second", Attributes: []byte(`{"position":1}`)},
			{Name: "First & only", Attributes: []byte(`{"position":0}`)},
			{Name: "Mirror", Kind: kindDynamicOPML, Attributes: []byte(`{"opml_url":"https://example.org/list.opml"}`)},
		} {
			c.UserID = u.ID
			if err := db.CreateCategory(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
		feeds := []*store.Feed{
			{CategoryID: 5, URL: "https://example.org/page", Kind: scrape.KindHTMLXPath, Name: `Page "one" <1>`,
				Website: "https://example.org/", Description: "A page\nin two lines", Priority: -10, TTL: -7200,
				PathEntries: "article .body", Attributes: []byte(every)},
			{CategoryID: 5, URL: "https://www.example.org/unnamed?x=1&y=2"},
			{CategoryID: 2, URL: "https://example.org/json", Kind: scrape.KindHTMLXPathJSON, Name: "zebra", Priority: 20,
				Attributes: []byte(`{"xPathToJson":"//script","json_dotnotation":{"item":"items","itemTitle":"t"}}`)},
			{CategoryID: 2, URL: "https://example.org/forced#force_feed", Kind: 2, Name: "Apple", Priority: 10},
		}
		for _, f := range feeds {
			f.UserID = u.ID
			if err := db.CreateFeed(ctx, f); err != nil {
				t.Fatal(err)
			}
		}

		doc, err := Export(ctx, db, u, time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("", 3*3600)))
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		want := `<?xml version="1.0" encoding="UTF-8"?>
<opml xmlns:frss="https://freshrss.org/opml" version="2.0">
  <head>
    <title>freshgo</title>
    <dateCreated>Tue, 06 Oct 2026 12:00:00 +0300</dateCreated>
  </head>
  <body>
    <outline text="First &amp; only">
      <outline text="example.org/unnamed?x=1&amp;y=2" type="rss" xmlUrl="https://www.example.org/unnamed?x=1&amp;y=2" frss:priority="category"/>
      <outline text="Page &#34;one&#34; &lt;1&gt;" type="HTML+XPath" xmlUrl="https://example.org/page" htmlUrl="https://example.org/" description="A page&#xA;in two lines" frss:priority="hidden" frss:unicityCriteria="sha1:link_published" frss:unicityCriteriaForced="true" frss:ttl="-7200" frss:xPathItem="//article" frss:xPathItemTitle="h2" frss:xPathItemUid="@id" frss:filtersActionRead="intitle:ads&#xA;sponsored &#34;by us&#34;" frss:cssFullContent="article .body" frss:cssFullContentConditions="intitle:long&#xA;author:x" frss:cssContentFilter=".ad, aside" frss:CURLOPT_COOKIE="a=b" frss:CURLOPT_FOLLOWLOCATION="false" frss:CURLOPT_MAXREDIRS="3" frss:CURLOPT_POST="true" frss:CURLOPT_POSTFIELDS="q=1" frss:CURLOPT_PROXY="proxy.example:8080" frss:CURLOPT_PROXYTYPE="-1" frss:CURLOPT_USERAGENT="agent/1.0" frss:CURLOPT_HTTPHEADER="X-One: 1&#xA;X-Two: 2"/>
    </outline>
    <outline text="Second">
    </outline>
    <outline text="Alpha">
    </outline>
    <outline text="beta">
      <outline text="Apple" type="rss" xmlUrl="https://example.org/forced#force_feed"/>
      <outline text="zebra" type="HTML+XPath+JSON+DotNotation" xmlUrl="https://example.org/json" frss:priority="important" frss:jsonItem="items" frss:jsonItemTitle="t" frss:xPathToJson="//script"/>
    </outline>
    <outline text="Mirror" frss:opmlUrl="https://example.org/list.opml">
    </outline>
    <outline text="Uncategorized">
    </outline>
  </body>
</opml>
`
		if string(doc) != want {
			t.Errorf("Export =\n%s\nwant\n%s", doc, want)
		}

		// Another user who imports the document gets the same subscriptions.
		bob := &store.User{Name: "bob"}
		if err := db.CreateUser(ctx, bob); err != nil {
			t.Fatal(err)
		}
		added, err := Import(ctx, db, &hooks.Registry{}, bob, doc, Limits{})
		if err != nil || len(added) != 4 {
			t.Fatalf("Import: %d feeds, %v; want 4", len(added), err)
		}
		again, err := Export(ctx, db, bob, time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("", 3*3600)))
		if err != nil {
			t.Fatal(err)
		}
		// Categories without feeds are not created by an import, the feed
		// without a name got the one it was shown under, and the kind that
		// forces a document to be read as a feed is not told apart in OPML.
		wantAgain := strings.NewReplacer(
			"    <outline text=\"Second\">\n    </outline>\n", "",
			"    <outline text=\"Alpha\">\n    </outline>\n", "",
			"    <outline text=\"Mirror\" frss:opmlUrl=\"https://example.org/list.opml\">\n    </outline>\n", "",
		).Replace(want)
		if string(again) != wantAgain {
			t.Errorf("Export after Import =\n%s\nwant\n%s", again, wantAgain)
		}
		imported, err := db.Feeds(ctx, bob.ID)
		if err != nil {
			t.Fatal(err)
		}
		var page *store.Feed
		for _, f := range imported {
			if f.URL == "https://example.org/page" {
				page = f
			}
		}
		if page == nil || page.Kind != scrape.KindHTMLXPath || page.Priority != -10 || page.TTL != -7200 || page.PathEntries != "article .body" {
			t.Fatalf("imported page feed = %+v", page)
		}
		var got, wantAttrs map[string]any
		if err := json.Unmarshal(page.Attributes, &got); err != nil {
			t.Fatal(err)
		}
		// Of the filters only those that mark read travel; the cookie file is
		// reduced to "use cookies"; settings OPML has no place for are gone.
		_ = json.Unmarshal([]byte(`{
			"unicityCriteria": "sha1:link_published", "unicityCriteriaForced": true,
			"xpath": {"item": "//article", "itemTitle": "h2", "itemUid": "@id"},
			"filters": [{"search": "intitle:ads", "actions": ["read"]}, {"search": "sponsored \"by us\"", "actions": ["read"]}],
			"path_entries_conditions": ["intitle:long", "author:x"],
			"path_entries_filter": ".ad, aside",
			"curl_params": {"10022": "a=b", "52": false, "68": 3, "47": true, "10015": "q=1",
				"10004": "proxy.example:8080", "101": -1, "10018": "agent/1.0", "10023": ["X-One: 1", "X-Two: 2"]}
		}`), &wantAttrs)
		if !reflect.DeepEqual(got, wantAttrs) {
			t.Errorf("attributes of the imported feed = %s\nwant %v", page.Attributes, wantAttrs)
		}
	})
}

func TestImport(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="ISO-8859-1"?>
<OPML version="1.0">
  <body>
    <outline title="Titled">
      <outline title="By title" xmlUrl="example.org/a" type="JSON+DotPath" frss:jsonItem="items"/>
      <outline text="Deep">
        <outline text="Deepest"><outline text="C" xmlUrl="http://example.org/c" frss:priority="FEED"/></outline>
        <outline text="B" xmlUrl="http://example.org/b"/>
      </outline>
    </outline>
    <outline text="Loose" xmlUrl="http://example.org/loose" frss:ttl="600" frss:cssFullContentFilter="nav"/>
    <outline text="Tagged" xmlUrl="http://example.org/tagged" category="One, Two"/>
    <outline text="Titled"><outline text="Late" xmlUrl="http://example.org/late"/></outline>
    <outline text="later"><outline text="Clash" xmlUrl="http://example.org/clash"/></outline>
    <outline text="Bad" xmlUrl="http://"/>
    <outline text="Refused" xmlUrl="http://example.org/refused"/>
    <outline text="Have it" xmlUrl="http://example.org/have" htmlUrl="http://example.org/new" frss:xPathItem="//new" frss:CURLOPT_USERAGENT="new"/>
    <outline text="Empty"/>
  </body>
</OPML>`
	eachEngine(t, func(t *testing.T, db *store.Store, u *store.User) {
		ctx := context.Background()
		if err := db.CreateTag(ctx, &store.Tag{UserID: u.ID, Name: "later"}); err != nil {
			t.Fatal(err)
		}
		existing := &store.Category{UserID: u.ID, Name: "Deep", Attributes: []byte(`{"position":4}`)}
		if err := db.CreateCategory(ctx, existing); err != nil {
			t.Fatal(err)
		}
		// A feed the user has: muted, with settings of its own.
		have := &store.Feed{UserID: u.ID, CategoryID: existing.ID, URL: "http://example.org/have", Name: "Old name",
			Website: "http://example.org/old", Description: "old", Priority: 20, TTL: -900,
			Attributes: []byte(`{"xpath":{"item":"//old","itemTitle":"h1"},"curl_params":{"10018":"old","68":2},"timeout":5}`)}
		if err := db.CreateFeed(ctx, have); err != nil {
			t.Fatal(err)
		}
		registry := &hooks.Registry{}
		registry.FeedBeforeInsert.Add(0, func(_ context.Context, f *store.Feed) (*store.Feed, bool) {
			return f, f.Name != "Refused"
		})

		added, err := Import(ctx, db, registry, u, []byte(doc), Limits{})
		if !errors.Is(err, ErrIncomplete) {
			t.Errorf("Import error = %v, want ErrIncomplete: a feed without address, a refused one, a category named like a label", err)
		}
		var names []string
		for _, f := range added {
			names = append(names, f.Name)
		}
		if want := []string{"By title", "Late", "C", "B", "Loose", "Tagged", "Clash"}; !reflect.DeepEqual(names, want) {
			t.Errorf("added feeds = %v, want %v", names, want)
		}

		categories, err := db.Categories(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		type category struct {
			Name       string
			Attributes string
		}
		var gotCategories []category
		byID := map[int64]string{}
		for _, c := range categories {
			gotCategories = append(gotCategories, category{c.Name, string(c.Attributes)})
			byID[c.ID] = c.Name
		}
		// New categories take the positions after the last one in use, in
		// the order their first feed comes in the document.
		wantCategories := []category{
			{"Uncategorized", "{}"}, {"Deep", `{"position":4}`}, {"Titled", `{"position":5}`},
			{"Deepest", `{"position":6}`}, {"One, Two", `{"position":7}`},
		}
		if !reflect.DeepEqual(gotCategories, wantCategories) {
			t.Errorf("categories = %+v, want %+v", gotCategories, wantCategories)
		}

		feeds, err := db.Feeds(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		type feed struct {
			Category, URL string
			Kind          int
			Priority, TTL int
		}
		got := map[string]feed{}
		for _, f := range feeds {
			got[f.Name] = feed{byID[f.CategoryID], f.URL, f.Kind, f.Priority, f.TTL}
		}
		want := map[string]feed{
			"By title": {"Titled", "https://example.org/a", scrape.KindJSONDotNotation, 10, 0},
			"Late":     {"Titled", "http://example.org/late", 0, 10, 0},
			"C":        {"Deepest", "http://example.org/c", 0, -5, 0},
			"B":        {"Deep", "http://example.org/b", 0, 10, 0},
			"Loose":    {"Uncategorized", "http://example.org/loose", 0, 10, 600},
			"Tagged":   {"One, Two", "http://example.org/tagged", 0, 10, 0},
			"Clash":    {"Uncategorized", "http://example.org/clash", 0, 10, 0},
			// Stays where it was, as important as it was, no longer muted.
			"Have it": {"Deep", "http://example.org/have", 0, 20, 900},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("feeds = %+v\nwant   %+v", got, want)
		}
		updated, err := db.FeedByID(ctx, u.ID, have.ID)
		if err != nil {
			t.Fatal(err)
		}
		var attrs, wantAttrs map[string]any
		_ = json.Unmarshal(updated.Attributes, &attrs)
		_ = json.Unmarshal([]byte(`{"xpath":{"item":"//new","itemTitle":"h1"},"curl_params":{"10018":"new","68":2},"timeout":5}`), &wantAttrs)
		if updated.Website != "http://example.org/new" || updated.Description != "" || !reflect.DeepEqual(attrs, wantAttrs) {
			t.Errorf("feed the user had = site %q, description %q, attributes %s; want the document laid over it", updated.Website, updated.Description, updated.Attributes)
		}

		// The same document again adds nothing.
		added, err = Import(ctx, db, registry, u, []byte(doc), Limits{})
		if !errors.Is(err, ErrIncomplete) || len(added) != 0 {
			t.Errorf("second Import: %d feeds, %v; want none added", len(added), err)
		}
	})
}

func TestImportRefusesWhatIsNotOPML(t *testing.T) {
	eachEngine(t, func(t *testing.T, db *store.Store, u *store.User) {
		for name, doc := range map[string]string{
			"text":       "just text",
			"other XML":  `<rss><channel><outline xmlUrl="http://example.org/"/></channel></rss>`,
			"broken XML": `<opml><body><outline text="a" xmlUrl="http://example.org/"`,
			"empty":      "",
		} {
			added, err := Import(context.Background(), db, &hooks.Registry{}, u, []byte(doc), Limits{})
			if !errors.Is(err, ErrDocument) || len(added) != 0 {
				t.Errorf("%s: %d feeds, error %v; want ErrDocument", name, len(added), err)
			}
		}
	})
}
