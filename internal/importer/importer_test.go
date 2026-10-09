package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
)

// The reference data comes from a real FreshRSS, see testdata/reference/generate.sh.
const (
	referenceDir       = "../../testdata/reference"
	sqliteReferenceDir = referenceDir + "/sqlite/data"
	pgsqlReferenceDir  = referenceDir + "/pgsql/data"
)

// eachDestination runs the test with an empty freshgo database on every engine.
func eachDestination(t *testing.T, test func(t *testing.T, dst *store.Store)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run("to-"+e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			dst, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			t.Cleanup(func() { _ = dst.Close() })
			test(t, dst)
		})
	}
}

// reference gives direct access to the FreshRSS tables of a user, bypassing
// the importer, so that the import can be checked against the source.
type reference struct {
	open func(t *testing.T, user string) (db *sql.DB, prefix string)
}

func sqliteReference(dataDir string) reference {
	return reference{open: func(t *testing.T, user string) (*sql.DB, string) {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "users", user, "db.sqlite")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db, ""
	}}
}

func TestImportFromSQLite(t *testing.T) {
	eachDestination(t, func(t *testing.T, dst *store.Store) {
		report, err := Run(context.Background(), dst, Options{DataDir: sqliteReferenceDir})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		checkImport(t, dst, report, sqliteReferenceDir, sqliteReference(sqliteReferenceDir))
	})
}

func TestImportFromPostgres(t *testing.T) {
	sourceURL := storetest.PostgresDatabase(t)
	dump, err := os.ReadFile(referenceDir + "/pgsql/dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The dump empties search_path for its session, so it gets a connection
	// pool of its own that is not used afterwards.
	loader, err := sql.Open("pgx", sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = loader.Exec(string(dump))
	_ = loader.Close()
	if err != nil {
		t.Fatalf("load the FreshRSS dump: %v", err)
	}
	source, err := sql.Open("pgx", sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	ref := reference{open: func(_ *testing.T, user string) (*sql.DB, string) {
		return source, "freshrss_" + user + "_"
	}}

	eachDestination(t, func(t *testing.T, dst *store.Store) {
		// config.php names the host FreshRSS used inside Docker.
		report, err := Run(context.Background(), dst, Options{DataDir: pgsqlReferenceDir, SourceDatabaseURL: sourceURL})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		checkImport(t, dst, report, pgsqlReferenceDir, ref)
	})
}

// checkImport verifies R1: every user of the reference installation has the
// same categories, feeds, labels and entries in dst, with the same
// identifiers, guids and states, and with text decoded.
func checkImport(t *testing.T, dst *store.Store, report *Report, dataDir string, ref reference) {
	t.Helper()
	ctx := context.Background()

	wantReport := []UserReport{
		{Name: "alice", Categories: 3, Feeds: 8, Entries: 22, Tags: 2, TaggedEntries: 3, CustomIcons: 1},
		{Name: "bob", Categories: 2, Feeds: 3, Entries: 11, Tags: 1, TaggedEntries: 1},
	}
	if !reflect.DeepEqual(report.Users, wantReport) {
		t.Errorf("report:\n got %+v\nwant %+v", report.Users, wantReport)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("warnings for a clean installation: %q", report.Warnings)
	}

	system := phpFile(t, filepath.Join(dataDir, "config.php"))
	if salt, err := dst.Setting(ctx, store.SettingSalt); err != nil || salt != system["salt"] {
		t.Errorf("salt = %q, %v; want %q from config.php", salt, err, system["salt"])
	}
	if domains, err := dst.Setting(ctx, store.SettingForceHTTPS); err != nil || domains != "example.net" {
		t.Errorf("force-https domains = %q, %v; want example.net", domains, err)
	}

	users, err := dst.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Name != "alice" || users[1].Name != "bob" {
		t.Fatalf("users = %+v, want alice and bob", users)
	}
	for _, u := range users {
		t.Run(u.Name, func(t *testing.T) {
			conf := phpFile(t, filepath.Join(dataDir, "users", u.Name, "config.php"))
			if u.APIPasswordHash == "" || u.APIPasswordHash != conf["apiPasswordHash"] {
				t.Errorf("API password hash = %q, want %q from config.php", u.APIPasswordHash, conf["apiPasswordHash"])
			}
			var settings map[string]any
			if err := json.Unmarshal(u.Settings, &settings); err != nil {
				t.Fatalf("settings: %v", err)
			}
			if _, kept := settings["apiPasswordHash"]; kept {
				t.Error("settings still hold apiPasswordHash; it has a column of its own")
			}
			archiving, _ := settings["archiving"].(map[string]any)
			if archiving["keep_max"] != float64(200) || archiving["keep_period"] != "P3M" || settings["passwordHash"] != conf["passwordHash"] {
				t.Errorf("settings lost values of config.php: archiving = %v", settings["archiving"])
			}

			db, prefix := ref.open(t, u.Name)
			checkCategories(t, dst, u, db, prefix)
			checkFeeds(t, dst, u, db, prefix)
			checkEntries(t, dst, u, db, prefix)
			checkTags(t, dst, u, db, prefix)
		})
	}
	checkAlice(t, dst, users[0])
}

func phpFile(t *testing.T, path string) map[string]any {
	t.Helper()
	conf, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

func sourceRows(t *testing.T, db *sql.DB, query string) *sql.Rows {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	t.Cleanup(func() { _ = rows.Close() })
	return rows
}

// wantAttributes is the stored form the importer should give to a FreshRSS
// attributes value.
func wantAttributes(stored sql.NullString) string {
	if stored.String == "" || stored.String == "[]" {
		return "{}"
	}
	return stored.String
}

func checkCategories(t *testing.T, dst *store.Store, u *store.User, db *sql.DB, prefix string) {
	t.Helper()
	got, err := dst.Categories(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []*store.Category
	rows := sourceRows(t, db, `SELECT "id", "name", "kind", "lastUpdate", "error", "attributes" FROM "`+prefix+`category" ORDER BY "id"`)
	for rows.Next() {
		c := &store.Category{UserID: u.ID}
		var attributes sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.LastUpdate, &c.Error, &attributes); err != nil {
			t.Fatal(err)
		}
		c.Name = html.UnescapeString(c.Name)
		c.Attributes = json.RawMessage(wantAttributes(attributes))
		want = append(want, c)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("categories:\n got %s\nwant %s", dump(got), dump(want))
	}
}

func checkFeeds(t *testing.T, dst *store.Store, u *store.User, db *sql.DB, prefix string) {
	t.Helper()
	got, err := dst.Feeds(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []*store.Feed
	rows := sourceRows(t, db, `SELECT "id", "url", "kind", "category", "name", "website", "description", "lastUpdate",
		"priority", "pathEntries", "error", "ttl", "attributes" FROM "`+prefix+`feed" ORDER BY "id"`)
	for rows.Next() {
		f := &store.Feed{UserID: u.ID}
		var website, description, pathEntries, attributes sql.NullString
		err := rows.Scan(&f.ID, &f.URL, &f.Kind, &f.CategoryID, &f.Name, &website, &description, &f.LastUpdate,
			&f.Priority, &pathEntries, &f.Error, &f.TTL, &attributes)
		if err != nil {
			t.Fatal(err)
		}
		f.URL, f.Name = html.UnescapeString(f.URL), html.UnescapeString(f.Name)
		f.Website, f.Description = html.UnescapeString(website.String), html.UnescapeString(description.String)
		f.PathEntries = html.UnescapeString(pathEntries.String)
		f.Attributes = json.RawMessage(wantAttributes(attributes))
		want = append(want, f)
	}
	if len(want) == 0 {
		t.Fatal("the reference has no feeds")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("feeds:\n got %s\nwant %s", dump(got), dump(want))
	}
}

func checkEntries(t *testing.T, dst *store.Store, u *store.User, db *sql.DB, prefix string) {
	t.Helper()
	ctx := context.Background()
	rows := sourceRows(t, db, `SELECT "id", "guid", "title", "content", "link", "date", "lastSeen",
		COALESCE("lastModified", 0), COALESCE("lastUserModified", 0), "is_read", "is_favorite", "id_feed", "attributes"
		FROM "`+prefix+`entry" ORDER BY "id"`)
	count := 0
	for rows.Next() {
		want := &store.Entry{UserID: u.ID}
		var (
			isRead, isFavorite int
			attributes         sql.NullString
		)
		err := rows.Scan(&want.ID, &want.GUID, &want.Title, &want.Content, &want.Link, &want.Published, &want.LastSeen,
			&want.LastModified, &want.LastUserModified, &isRead, &isFavorite, &want.FeedID, &attributes)
		if err != nil {
			t.Fatal(err)
		}
		count++
		want.Title, want.Link = html.UnescapeString(want.Title), html.UnescapeString(want.Link)
		want.IsRead, want.IsFavorite = isRead != 0, isFavorite != 0
		want.Attributes = json.RawMessage(wantAttributes(attributes))

		got, err := dst.EntryByID(ctx, u.ID, want.ID)
		if err != nil {
			t.Errorf("entry %d (%s): %v", want.ID, want.GUID, err)
			continue
		}
		if got.Hash != nil {
			t.Errorf("entry %d: hash %x was imported, want none", got.ID, got.Hash)
		}
		// Authors and tags change form; they are checked on known entries.
		want.Authors, want.Tags = got.Authors, got.Tags
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry %d:\n got %s\nwant %s", want.ID, dump(got), dump(want))
		}
	}
	if count == 0 {
		t.Fatal("the reference has no entries")
	}
	if n, err := dst.CountEntries(ctx, u.ID); err != nil || n != count {
		t.Errorf("entries in freshgo = %d (err %v), in FreshRSS = %d", n, err, count)
	}
}

func checkTags(t *testing.T, dst *store.Store, u *store.User, db *sql.DB, prefix string) {
	t.Helper()
	ctx := context.Background()
	got, err := dst.Tags(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var want []*store.Tag
	rows := sourceRows(t, db, `SELECT "id", "name" FROM "`+prefix+`tag" ORDER BY "id"`)
	for rows.Next() {
		tag := &store.Tag{UserID: u.ID, Attributes: json.RawMessage("{}")}
		if err := rows.Scan(&tag.ID, &tag.Name); err != nil {
			t.Fatal(err)
		}
		tag.Name = html.UnescapeString(tag.Name)
		want = append(want, tag)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tags:\n got %s\nwant %s", dump(got), dump(want))
	}

	wantLinks := map[int64][]int64{}
	links := sourceRows(t, db, `SELECT "id_entry", "id_tag" FROM "`+prefix+`entrytag" ORDER BY "id_entry", "id_tag"`)
	for links.Next() {
		var entryID, tagID int64
		if err := links.Scan(&entryID, &tagID); err != nil {
			t.Fatal(err)
		}
		wantLinks[entryID] = append(wantLinks[entryID], tagID)
	}
	if len(wantLinks) == 0 {
		t.Fatal("the reference has no labelled entries")
	}
	for entryID, tagIDs := range wantLinks {
		gotIDs, err := dst.EntryTagIDs(ctx, u.ID, entryID)
		if err != nil || !reflect.DeepEqual(gotIDs, tagIDs) {
			t.Errorf("labels of entry %d = %v (err %v), want %v", entryID, gotIDs, err, tagIDs)
		}
	}
}

// checkAlice pins down values known from the scenario and the corpus,
// independently of how the checks above read the source.
func checkAlice(t *testing.T, dst *store.Store, alice *store.User) {
	t.Helper()
	ctx := context.Background()

	categories, err := dst.Categories(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 3 || categories[2].Name != "Scraped & parsed" {
		t.Errorf("categories = %s, want the third to be %q", dump(categories), "Scraped & parsed")
	}
	tags, err := dst.Tags(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "later" || tags[1].Name != "work & play" {
		t.Errorf("tags = %s, want later and %q", dump(tags), "work & play")
	}

	feeds, err := dst.Feeds(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 8 {
		t.Fatalf("alice has %d feeds, want 8", len(feeds))
	}
	rss, page, jsonFeed, noID := feeds[1], feeds[2], feeds[4], feeds[7]
	if rss.URL != "http://feeds.freshgo.test/rss.xml" || rss.TTL != 7200 || rss.CategoryID != 2 {
		t.Errorf("feed 2 = %s, want the RSS corpus with ttl 7200 in category 2", dump(rss))
	}
	var pageAttrs struct {
		XPath map[string]string `json:"xpath"`
	}
	if err := json.Unmarshal(page.Attributes, &pageAttrs); err != nil || page.Kind != 10 ||
		pageAttrs.XPath["itemContent"] != `descendant::div[@class="body"]` {
		t.Errorf("feed 3 = %s, want kind 10 with its XPath settings", dump(page))
	}
	if jsonFeed.Kind != 25 || string(jsonFeed.Attributes) != "{}" {
		t.Errorf("feed 5 = %s, want kind 25 with empty attributes", dump(jsonFeed))
	}
	if noID.Priority != -5 || noID.CategoryID != store.DefaultCategoryID {
		t.Errorf("feed 8 = %s, want priority -5 in the default category", dump(noID))
	}

	wantIcon, err := os.ReadFile(referenceDir + "/custom-icon.png")
	if err != nil {
		t.Fatal(err)
	}
	salt, err := dst.Setting(ctx, store.SettingSalt)
	if err != nil {
		t.Fatal(err)
	}
	icon, err := dst.CustomIconByHash(ctx, favicon.CustomHash(salt, alice.ID, rss.ID))
	if err != nil || !bytes.Equal(icon.Content, wantIcon) || icon.Modified == 0 {
		t.Errorf("custom icon of feed 2: %+v, err %v; want the %d bytes of custom-icon.png and the time of the file", icon, err, len(wantIcon))
	}
	if _, err := dst.CustomIconByHash(ctx, favicon.CustomHash(salt, alice.ID, feeds[0].ID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("custom icon of feed 1: err %v, want ErrNotFound: icons fetched from sites are not imported", err)
	}

	// The counters continue after the imported identifiers.
	next := &store.Feed{UserID: alice.ID, URL: "https://example.org/new", Name: "new"}
	if err := dst.CreateFeed(ctx, next); err != nil || next.ID != 9 {
		t.Errorf("next feed id = %d (err %v), want 9", next.ID, err)
	}

	byTitle := map[string]*store.Entry{}
	for _, f := range feeds[:8] {
		for _, e := range entriesOfFeed(t, dst, alice.ID, f.ID) {
			byTitle[e.Title] = e
		}
	}
	entry := func(title string) *store.Entry {
		e, ok := byTitle[title]
		if !ok {
			t.Fatalf("no entry titled %q among %d", title, len(byTitle))
		}
		return e
	}

	nonASCII := entry("Identifier with an ampersand & non-ASCII: Привет")
	if want := []string{"Иван Петров", "O'Neil & Sons"}; !reflect.DeepEqual(nonASCII.Authors, want) {
		t.Errorf("authors = %q, want %q", nonASCII.Authors, want)
	}
	if nonASCII.Link != "http://feeds.freshgo.test/atom/query?a=1&b=2" {
		t.Errorf("link = %q, want it decoded", nonASCII.Link)
	}
	if nonASCII.GUID != "http://feeds.freshgo.test/atom/query?a=1&amp;b=2&amp;=" {
		t.Errorf("guid = %q, want the encoded FreshRSS form untouched", nonASCII.GUID)
	}
	if nonASCII.Content != "Summary with &lt;angle brackets&gt; &amp; &quot;quotes&quot;." {
		t.Errorf("content = %q, want the HTML untouched", nonASCII.Content)
	}

	permalink := entry("Permalink guid")
	if want := []string{"news", "tech & science"}; !reflect.DeepEqual(permalink.Tags, want) || !permalink.IsRead || permalink.IsFavorite {
		t.Errorf("Permalink guid: tags %q read %v favorite %v; want %q, read, not favorite", permalink.Tags, permalink.IsRead, permalink.IsFavorite, want)
	}
	plain := entry("Plain entry")
	if want := []string{"go", "rss readers"}; !reflect.DeepEqual(plain.Tags, want) || plain.IsRead || !plain.IsFavorite {
		t.Errorf("Plain entry: tags %q read %v favorite %v; want %q, unread, favorite", plain.Tags, plain.IsRead, plain.IsFavorite, want)
	}
	if ids, err := dst.EntryTagIDs(ctx, alice.ID, plain.ID); err != nil || !reflect.DeepEqual(ids, []int64{tags[0].ID}) {
		t.Errorf("labels of Plain entry = %v (err %v), want [later]", ids, err)
	}
	opaque := entry("Opaque guid")
	if ids, err := dst.EntryTagIDs(ctx, alice.ID, opaque.ID); err != nil || !reflect.DeepEqual(ids, []int64{tags[1].ID}) {
		t.Errorf("labels of Opaque guid = %v (err %v), want [work & play]", ids, err)
	}
	if want := []string{"carol@example.org (Carol)"}; !reflect.DeepEqual(opaque.Authors, want) {
		t.Errorf("authors of Opaque guid = %q, want %q", opaque.Authors, want)
	}
	if e := entry("Second & last"); e.Link != "http://feeds.freshgo.test/page/two?a=1&b=2" {
		t.Errorf("link of a scraped entry = %q, want it decoded", e.Link)
	}
	for _, title := range []string{"First without guid", "Second without guid", "Third on a force-https domain"} {
		if !entry(title).IsRead {
			t.Errorf("%q is unread, want the whole feed read", title)
		}
	}
}

func entriesOfFeed(t *testing.T, dst *store.Store, userID, feedID int64) []*store.Entry {
	t.Helper()
	entries, err := dst.EntriesByFeed(context.Background(), userID, feedID)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func dump(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func TestImportRefusesNonEmptyDatabase(t *testing.T) {
	eachDestination(t, func(t *testing.T, dst *store.Store) {
		ctx := context.Background()
		if _, err := Run(ctx, dst, Options{DataDir: sqliteReferenceDir}); err != nil {
			t.Fatalf("first Run: %v", err)
		}
		before := snapshot(t, dst)

		_, err := Run(ctx, dst, Options{DataDir: sqliteReferenceDir})
		if !errors.Is(err, ErrNotEmpty) {
			t.Fatalf("second Run: error = %v, want ErrNotEmpty", err)
		}
		if after := snapshot(t, dst); after != before {
			t.Errorf("the refused import changed the database:\nbefore %s\n after %s", before, after)
		}
	})
}

// snapshot summarizes the content of a database for before/after comparison.
func snapshot(t *testing.T, dst *store.Store) string {
	t.Helper()
	ctx := context.Background()
	users, err := dst.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, u := range users {
		categories, err := dst.Categories(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		feeds, err := dst.Feeds(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		tags, err := dst.Tags(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := dst.CountEntries(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(dump([]any{u, categories, feeds, tags, entries}))
	}
	salt, _ := dst.Setting(ctx, store.SettingSalt)
	return b.String() + salt
}

// brokenCopy copies the SQLite reference and lets the test damage it.
func brokenCopy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(sqliteReferenceDir)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sourceExec(t *testing.T, dataDir, user, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "users", user, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// readOnly takes the right to write away from the directories of the users,
// as a volume mounted read-only does.
func readOnly(t *testing.T, dataDir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory whatever its mode")
	}
	for _, user := range []string{"alice", "bob"} {
		dir := filepath.Join(dataDir, "users", user)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		// So that the test can remove its directory.
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}
}

// FreshRSS keeps its databases in WAL mode, and the data directory is
// mounted read-only for the import.
func TestImportFromReadOnlyDirectory(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	for _, user := range []string{"alice", "bob"} {
		sourceExec(t, dir, user, `PRAGMA journal_mode=WAL`)
	}
	readOnly(t, dir)

	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	report, err := Run(ctx, dst, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checkImport(t, dst, report, sqliteReferenceDir, sqliteReference(sqliteReferenceDir))
}

// Changes still in the log cannot be read without writing next to the
// database; the import says so instead of leaving them out.
func TestImportRefusesUnreadableLog(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	// A connection that stays open keeps the log from being folded back.
	path := filepath.Join(dir, "users", "alice", "db.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; UPDATE feed SET name = name`); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(path + "-wal")
	if err != nil || len(wal) == 0 {
		t.Fatalf("no log to test with: %d bytes, err %v", len(wal), err)
	}
	// Closing folds the log back; what was in it is put there again, as a
	// FreshRSS that was killed leaves it.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly(t, dir)

	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	if _, err := Run(ctx, dst, Options{DataDir: dir}); err == nil || !strings.Contains(err.Error(), "db.sqlite-wal") {
		t.Fatalf("Run: error = %v, want a failure naming the log", err)
	}
}

func TestImportIsAllOrNothing(t *testing.T) {
	eachDestination(t, func(t *testing.T, dst *store.Store) {
		ctx := context.Background()
		dir := brokenCopy(t)
		// alice imports fine, bob's database is damaged.
		sourceExec(t, dir, "bob", `DROP TABLE entrytag`)

		if _, err := Run(ctx, dst, Options{DataDir: dir}); err == nil || !strings.Contains(err.Error(), "user bob") {
			t.Fatalf("Run: error = %v, want a failure naming bob", err)
		}
		if users, err := dst.Users(ctx); err != nil || len(users) != 0 {
			t.Errorf("users after a failed import = %s (err %v), want none", dump(users), err)
		}
		if _, err := dst.Setting(ctx, store.SettingSalt); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("salt after a failed import: err %v, want ErrNotFound", err)
		}

		// The same database accepts a good installation afterwards.
		if _, err := Run(ctx, dst, Options{DataDir: sqliteReferenceDir}); err != nil {
			t.Errorf("Run after a failed import: %v", err)
		}
	})
}

func TestImportWarnings(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	sourceExec(t, dir, "alice", `UPDATE feed SET attributes = '{"unicityCriteria":"sha1:link_published_title_content"}' WHERE id = 1`)
	sourceExec(t, dir, "alice", `UPDATE feed SET attributes = 'not json' WHERE id = 3`)
	sourceExec(t, dir, "alice", `UPDATE feed SET httpAuth = 'dXNlcjpwQCZhbXA7cyZsdDsmcXVvdDs=' WHERE id = 4`)
	sourceExec(t, dir, "alice", `UPDATE feed SET httpAuth = 'dXNlcjpwYXNz!!!' WHERE id = 5`)
	// A filter with a backreference, which Go's regular expressions lack, next to one that is fine.
	sourceExec(t, dir, "alice", `UPDATE feed SET attributes = '{"filters":[{"search":"intitle:/(a)\\1/ ads","actions":["read"]},{"search":"intitle:ads","actions":["read"]}]}' WHERE id = 6`)
	if err := os.Mkdir(filepath.Join(dir, "users", "leftover"), 0o700); err != nil {
		t.Fatal(err)
	}
	// As if feeds 9 to 20 had existed and were deleted.
	sourceExec(t, dir, "alice", `UPDATE sqlite_sequence SET seq = 20 WHERE name = 'feed'`)
	sourceExec(t, dir, "alice", `INSERT INTO entrytmp (id, guid, title, link, id_feed) VALUES (1, 'pending', 't', 'l', 1)`)
	icons, err := filepath.Glob(filepath.Join(dir, "favicons", "*.ico"))
	if err != nil {
		t.Fatal(err)
	}
	for _, icon := range icons {
		if _, err := os.Stat(strings.TrimSuffix(icon, ".ico") + ".txt"); err != nil {
			if err := os.Remove(icon); err != nil { // the custom icon has no .txt
				t.Fatal(err)
			}
		}
	}
	bobConf := filepath.Join(dir, "users", "bob", "config.php")
	conf, err := os.ReadFile(bobConf)
	if err != nil {
		t.Fatal(err)
	}
	hash := phpFile(t, bobConf)["apiPasswordHash"].(string)
	if err := os.WriteFile(bobConf, bytes.ReplaceAll(conf, []byte(hash), nil), 0o600); err != nil {
		t.Fatal(err)
	}

	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	report, err := Run(ctx, dst, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, want := range []string{
		"user alice: feed 1 (http://feeds.freshgo.test/atom.xml): entries are told apart by a hash of their content",
		"user alice: feed 3: attributes are not a JSON object",
		"user alice: feed 2 (http://feeds.freshgo.test/rss.xml): the custom icon",
		"user alice: 1 entries were still waiting in entrytmp",
		"user bob: no API password is set",
		"user alice: feed 5 (http://feeds.freshgo.test/feed.json): HTTP credentials are not valid base64",
		"users/leftover has no config.php",
		`user alice: feed 6 (http://feeds.freshgo.test/api.json): filter "intitle:/(a)\\1/ ads": search: regular expression is not supported`,
	} {
		found := false
		for _, w := range report.Warnings {
			found = found || strings.HasPrefix(w, want)
		}
		if !found {
			t.Errorf("no warning starting with %q in %q", want, report.Warnings)
		}
	}
	if len(report.Warnings) != 8 {
		t.Errorf("%d warnings, want 8: %q", len(report.Warnings), report.Warnings)
	}

	alice, err := dst.UserByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := dst.FeedByID(ctx, alice.ID, 3); err != nil || string(f.Attributes) != "{}" {
		t.Errorf("feed with broken attributes = %s (err %v), want empty attributes", dump(f), err)
	}
	if f, err := dst.FeedByID(ctx, alice.ID, 4); err != nil || f.HTTPAuth != `user:p@&s<"` {
		t.Errorf("HTTP credentials = %q (err %v), want them decoded from base64 and from HTML entities", f.HTTPAuth, err)
	}
	if f, err := dst.FeedByID(ctx, alice.ID, 5); err != nil || f.HTTPAuth != "" {
		t.Errorf("broken HTTP credentials = %q (err %v), want none", f.HTTPAuth, err)
	}
	next := &store.Feed{UserID: alice.ID, URL: "https://example.org/new", Name: "new"}
	if err := dst.CreateFeed(ctx, next); err != nil || next.ID != 21 {
		t.Errorf("next feed id = %d (err %v), want 21: the FreshRSS counter was at 20", next.ID, err)
	}
	if n, err := dst.CountEntries(ctx, alice.ID); err != nil || n != 22 {
		t.Errorf("entries = %d (err %v), want 22: pending ones are not imported", n, err)
	}
}

func TestImportRejectsUnsupportedSources(t *testing.T) {
	ctx := context.Background()
	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })

	mysql := brokenCopy(t)
	conf, err := os.ReadFile(filepath.Join(mysql, "config.php"))
	if err != nil {
		t.Fatal(err)
	}
	conf = bytes.Replace(conf, []byte(`'type' => 'sqlite'`), []byte(`'type' => 'mysql'`), 1)
	if err := os.WriteFile(filepath.Join(mysql, "config.php"), conf, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, dst, Options{DataDir: mysql}); err == nil || !strings.Contains(err.Error(), "MySQL") {
		t.Errorf("MySQL installation: error = %v, want it refused by name", err)
	}

	if _, err := Run(ctx, dst, Options{DataDir: t.TempDir()}); err == nil {
		t.Error("empty directory: no error")
	}
	if users, err := dst.Users(ctx); err != nil || len(users) != 0 {
		t.Errorf("users after refused imports = %s (err %v), want none", dump(users), err)
	}
}

func TestSplitAuthors(t *testing.T) {
	tests := map[string][]string{
		"":              nil,
		";Alice Author": {"Alice Author"},
		";Иван Петров; O'Neil &amp; Sons": {"Иван Петров", "O'Neil & Sons"},
		";O&#039;Neil; Second":            {"O'Neil", "Second"},
		// An entity brings a semicolon, and FreshRSS then splits on semicolons only.
		"Old, Comma &amp; Form": {"Old, Comma & Form"},
		"Old, Comma Form":       {"Old", "Comma Form"},
		"Single":                {"Single"},
		"; ; ":                  nil,
	}
	for stored, want := range tests {
		if got := splitAuthors(stored); !reflect.DeepEqual(got, want) {
			t.Errorf("splitAuthors(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestSplitTags(t *testing.T) {
	tests := map[string][]string{
		"":                          nil,
		"#go #rss readers":          {"go", "rss readers"},
		"#news #tech &amp; science": {"news", "tech & science"},
		"a, b,c":                    {"a", "b", "c"},
		"# #":                       nil,
		"#&lt;b&gt; #&quot;q&quot;": {"<b>", `"q"`},
	}
	for stored, want := range tests {
		if got := splitTags(stored); !reflect.DeepEqual(got, want) {
			t.Errorf("splitTags(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestDecodeText(t *testing.T) {
	tests := map[string]string{
		"Tom &amp; Jerry":          "Tom & Jerry",
		"&lt;b&gt; &quot;x&quot;":  `<b> "x"`,
		"it&#039;s &#39;ok&#39;":   "it's 'ok'",
		"&amp;lt; stays &amp;amp;": "&lt; stays &amp;",
		"&nbsp;&copy; untouched":   "&nbsp;&copy; untouched",
		"http://x/?a=1&amp;b=2":    "http://x/?a=1&b=2",
	}
	for stored, want := range tests {
		if got := decodeText(stored); got != want {
			t.Errorf("decodeText(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestPostgresURL(t *testing.T) {
	got, err := postgresURL(map[string]any{
		"host": "db.example.org:5433", "user": "fresh", "password": "p@ss/word", "base": "freshrss",
		"connection_uri_params": "sslmode=require;connect_timeout=5",
	})
	want := "postgres://fresh:p%40ss%2Fword@db.example.org:5433/freshrss?connect_timeout=5&sslmode=require"
	if err != nil || got != want {
		t.Errorf("postgresURL = %q, %v; want %q", got, err, want)
	}
	if _, err := postgresURL(map[string]any{"host": "", "base": "x"}); err == nil {
		t.Error("postgresURL without a host: no error")
	}
}

// A user directory may be a symbolic link, for example to another volume.
func TestImportFollowsSymlinkedUser(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	elsewhere := filepath.Join(t.TempDir(), "bob")
	if err := os.Rename(filepath.Join(dir, "users", "bob"), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "users", "bob")); err != nil {
		t.Skipf("symbolic links are not available: %v", err)
	}
	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	report, err := Run(ctx, dst, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Users) != 2 || report.Users[1].Name != "bob" || report.Users[1].Entries != 11 {
		t.Errorf("report = %+v, want bob imported with 11 entries", report.Users)
	}
}

// FreshRSS stores an entry without a title under its guid, which stays
// HTML-encoded while titles are decoded: the import turns it into no title.
func TestImportUntitledEntry(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	sourceExec(t, dir, "alice", `UPDATE entry SET guid = 'http://example.org/?a=1&amp;b=2', title = 'http://example.org/?a=1&amp;b=2'
		WHERE id = (SELECT MIN(id) FROM entry)`)
	sourceExec(t, dir, "alice", `UPDATE entry SET title = 'Tom &amp; Jerry' WHERE id = (SELECT MAX(id) FROM entry)`)

	eachDestination(t, func(t *testing.T, dst *store.Store) {
		if _, err := Run(ctx, dst, Options{DataDir: dir}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		alice, err := dst.UserByName(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		var entries []*store.Entry
		feeds, err := dst.Feeds(ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range feeds {
			entries = append(entries, entriesOfFeed(t, dst, alice.ID, f.ID)...)
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].ID < entries[b].ID })
		first, last := entries[0], entries[len(entries)-1]
		if first.GUID != "http://example.org/?a=1&amp;b=2" || first.Title != "" {
			t.Errorf("untitled entry: guid %q, title %q; want the guid kept and no title", first.GUID, first.Title)
		}
		if last.Title != "Tom & Jerry" {
			t.Errorf("titled entry: title %q", last.Title)
		}
	})
}

// A page shows the text of an entry as it is stored, so the import cleans
// what FreshRSS let through, the text a full text stands in for included.
// The marks of a full text stay, and a text without markup is not touched.
func TestImportCleansTexts(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	sourceExec(t, dir, "alice", `UPDATE entry SET
		content = '<!-- FULLCONTENT start //--><p onclick="x()">page</p><script>x()</script><!-- FULLCONTENT end //--><p>feed</p>',
		attributes = '{"original_content":"<p>feed<script>x()</script></p>"}'
		WHERE id = (SELECT MIN(id) FROM entry)`)
	sourceExec(t, dir, "alice", `UPDATE entry SET content = 'a &quot;b&quot; &amp; c' WHERE id = (SELECT MAX(id) FROM entry)`)

	eachDestination(t, func(t *testing.T, dst *store.Store) {
		if _, err := Run(ctx, dst, Options{DataDir: dir}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		alice, err := dst.UserByName(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		entries, err := dst.ListEntries(ctx, alice.ID, store.EntryQuery{Ascending: true})
		if err != nil {
			t.Fatal(err)
		}
		first, last := entries[0], entries[len(entries)-1]
		if want := `<!-- FULLCONTENT start //--><p>page</p><!-- FULLCONTENT end //--><p>feed</p>`; first.Content != want {
			t.Errorf("content:\n got %s\nwant %s", first.Content, want)
		}
		var attrs map[string]string
		if err := json.Unmarshal(first.Attributes, &attrs); err != nil || attrs["original_content"] != `<p>feed</p>` {
			t.Errorf("attributes: %s (%v)", first.Attributes, err)
		}
		if last.Content != `a &quot;b&quot; &amp; c` {
			t.Errorf("a text without markup: %q, want it as FreshRSS has it", last.Content)
		}
	})
}

// FreshRSS shows the default category, and names it to API clients, in the
// language of the user whatever name is stored; the import stores that name.
func TestImportNamesDefaultCategoryInTheLanguageOfTheUser(t *testing.T) {
	ctx := context.Background()
	dir := brokenCopy(t)
	for user, language := range map[string]string{"alice": "ru", "bob": "fr"} {
		file := filepath.Join(dir, "users", user, "config.php")
		conf, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		changed := bytes.Replace(conf, []byte(`'language' => 'en'`), []byte(`'language' => '`+language+`'`), 1)
		if bytes.Equal(changed, conf) {
			t.Fatalf("%s: no language to replace", file)
		}
		if err := os.WriteFile(file, changed, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// bob already has a category called the way French names the default one.
	sourceExec(t, dir, "bob", `UPDATE category SET name = 'Sans catégorie' WHERE id = 2`)

	driver, dsn := storetest.Engines()[0].New(t)
	dst, err := store.Open(ctx, driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	report, err := Run(ctx, dst, Options{DataDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for user, want := range map[string]string{"alice": "Без категории", "bob": "Uncategorized"} {
		u, err := dst.UserByName(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		categories, err := dst.Categories(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if categories[0].ID != store.DefaultCategoryID || categories[0].Name != want {
			t.Errorf("default category of %s = %s, want the name %q", user, dump(categories[0]), want)
		}
	}
	want := `user bob: the default category keeps the name "Uncategorized": another category is called "Sans catégorie"`
	if len(report.Warnings) != 1 || !strings.HasPrefix(report.Warnings[0], want) {
		t.Errorf("warnings = %q, want one starting with %q", report.Warnings, want)
	}
}
