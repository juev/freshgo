package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/opml"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
)

var (
	// ErrUnknownFile is returned for a file whose name says nothing about
	// what it holds.
	ErrUnknownFile = errors.New("transfer: the name of the file does not say what it holds")
	// ErrDocument is returned for a document of entries that is not JSON.
	ErrDocument = errors.New("transfer: the document cannot be read")
)

// Bounds on what an archive may hold: the files that are looked at, the
// size of one unpacked, and of all of them together.
const (
	maxMembers    = 1000
	maxMemberSize = 64 << 20
	maxTotalSize  = 256 << 20
)

// placeholderFeed is the feed of entries that name none, as in FreshRSS.
const placeholderFeed = "http://import.localhost/import.xml"

// Options are the rules of the installation and of the user an import goes by.
type Options struct {
	// MaxFeeds and MaxCategories bound what the user may have; zero is no bound.
	MaxFeeds      int
	MaxCategories int
	// Now is the time of the import.
	Now time.Time
}

// Report is what an import did.
type Report struct {
	// Feeds counts the feeds that were added, Entries and Updated the
	// entries that were added and rewritten.
	Feeds   int
	Entries int
	Updated int
	// Incomplete says that something of the file was left out: a feed past
	// the limit, an entry without a usable feed, a part of an archive that
	// could not be read.
	Incomplete bool
}

func (r *Report) add(other Report) {
	r.Feeds += other.Feeds
	r.Entries += other.Entries
	r.Updated += other.Updated
	r.Incomplete = r.Incomplete || other.Incomplete
}

// Kinds of files, told by their names the way FreshRSS tells them.
const (
	kindUnknown = iota
	kindZip
	kindText
	kindOPML
	kindStarred
	kindEntries
)

func kindOf(name string) int {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return kindZip
	case strings.HasSuffix(lower, ".txt"):
		return kindText
	case strings.Contains(lower, "opml"):
		return kindOPML
	case strings.HasSuffix(lower, ".json") && strings.Contains(lower, "starred"):
		return kindStarred
	case strings.HasSuffix(lower, ".json"):
		return kindEntries
	case strings.HasSuffix(lower, ".xml"):
		return kindOPML
	}
	return kindUnknown
}

// Import reads a file a user hands over: an OPML document, a list of
// addresses of feeds, a document of entries, or a ZIP archive of those. The
// name of the file says which. Subscriptions come first, so that the entries
// find their feeds.
func Import(ctx context.Context, db *store.Store, registry *hooks.Registry, u *store.User, name string, data []byte, o Options) (Report, error) {
	type file struct {
		kind int
		data []byte
	}
	var (
		files  []file
		report Report
	)
	switch kind := kindOf(name); kind {
	case kindUnknown:
		return report, ErrUnknownFile
	case kindZip:
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return report, fmt.Errorf("%w: %v", ErrDocument, err)
		}
		total := 0
		for _, member := range archive.File {
			kind := kindOf(member.Name)
			if kind == kindUnknown || kind == kindZip || member.FileInfo().IsDir() {
				continue
			}
			if len(files) >= maxMembers || member.UncompressedSize64 > maxMemberSize {
				report.Incomplete = true
				continue
			}
			content, err := readMember(member)
			if err != nil || total+len(content) > maxTotalSize {
				report.Incomplete = true
				continue
			}
			total += len(content)
			files = append(files, file{kind, content})
		}
	default:
		files = append(files, file{kind, data})
	}
	sort.SliceStable(files, func(a, b int) bool { return files[a].kind < files[b].kind })
	for _, f := range files {
		var (
			part Report
			err  error
		)
		switch f.kind {
		case kindText:
			part, err = importOPML(ctx, db, registry, u, addressesAsOPML(f.data), o)
		case kindOPML:
			part, err = importOPML(ctx, db, registry, u, f.data, o)
		case kindStarred:
			part, err = Entries(ctx, db, registry, u, f.data, true, o)
		case kindEntries:
			part, err = Entries(ctx, db, registry, u, f.data, false, o)
		}
		report.add(part)
		switch {
		case errors.Is(err, ErrDocument) || errors.Is(err, opml.ErrDocument):
			// One file that cannot be read does not keep the others out,
			// unless it is the only one.
			if len(files) == 1 {
				return report, err
			}
			report.Incomplete = true
		case err != nil:
			return report, err
		}
	}
	return report, nil
}

// readMember unpacks a file of an archive, up to the size a file may have.
func readMember(member *zip.File) ([]byte, error) {
	r, err := member.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	content, err := io.ReadAll(io.LimitReader(r, maxMemberSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxMemberSize {
		return nil, errors.New("transfer: a file of the archive is too large")
	}
	return content, nil
}

// addressesAsOPML turns a list of addresses, one a line, into an OPML
// document of feeds without a category.
func addressesAsOPML(list []byte) []byte {
	var b bytes.Buffer
	b.WriteString(`<opml version="2.0"><body>`)
	for _, line := range strings.FieldsFunc(string(list), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		b.WriteString(`<outline type="rss" xmlUrl="`)
		_ = xml.EscapeText(&b, []byte(line))
		b.WriteString(`"/>`)
	}
	b.WriteString(`</body></opml>`)
	return b.Bytes()
}

func importOPML(ctx context.Context, db *store.Store, registry *hooks.Registry, u *store.User, data []byte, o Options) (Report, error) {
	added, err := opml.Import(ctx, db, registry, u, data, opml.Limits{Feeds: o.MaxFeeds, Categories: o.MaxCategories})
	report := Report{Feeds: len(added)}
	if errors.Is(err, opml.ErrIncomplete) {
		report.Incomplete, err = true, nil
	}
	return report, err
}

// incoming is an item of a document of entries, read leniently: documents
// of several readers pass for the same format.
type incoming struct {
	GUID       any             `json:"guid"`
	ID         any             `json:"id"`
	Title      any             `json:"title"`
	Author     any             `json:"author"`
	URL        any             `json:"url"`
	Published  any             `json:"published"`
	Timestamp  any             `json:"timestampUsec"`
	Updated    any             `json:"updated"`
	Categories json.RawMessage `json:"categories"`
	Content    json.RawMessage `json:"content"`
	Summary    json.RawMessage `json:"summary"`
	Alternate  json.RawMessage `json:"alternate"`
	Origin     json.RawMessage `json:"origin"`
}

func text(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// contentOf reads the text of an item: an object with the text, or the
// text itself.
func contentOf(raw json.RawMessage) (string, bool) {
	var object struct {
		Content *string `json:"content"`
	}
	if json.Unmarshal(raw, &object) == nil && object.Content != nil {
		return *object.Content, true
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil && len(raw) > 0 {
		return plain, true
	}
	return "", false
}

// decodeChars undoes PHP htmlspecialchars, which FreshRSS applies to the
// titles and authors it writes; text without such characters is unchanged.
var decodeChars = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#039;", "'", "&#39;", "'")

var (
	userTag      = regexp.MustCompile(`^user/[A-Za-z0-9_-]+/`)
	starredTag   = regexp.MustCompile(`^user/[A-Za-z0-9_-]+/state/com\.google/starred$`)
	readTag      = regexp.MustCompile(`^user/[A-Za-z0-9_-]+/state/com\.google/read$`)
	unreadTag    = regexp.MustCompile(`^user/[A-Za-z0-9_-]+/state/com\.google/unread$`)
	labelTag     = regexp.MustCompile(`^user/[A-Za-z0-9_-]+/label/\s*(.+?)\s*$`)
	onlyDigits   = regexp.MustCompile(`^[0-9]+$`)
	authorsSplit = regexp.MustCompile(`\s*;\s*`)
)

// published reads the date of an item: Unix seconds, or milliseconds, as a
// number or a text, or a date written out.
func published(it *incoming, loc *time.Location) int64 {
	var raw string
	switch {
	case scalar(it.Published) != "":
		raw = scalar(it.Published)
	case len(scalar(it.Timestamp)) > 6:
		raw = scalar(it.Timestamp)
		raw = raw[:len(raw)-6]
	case scalar(it.Updated) != "":
		raw = scalar(it.Updated)
	default:
		return 0
	}
	if !onlyDigits.MatchString(raw) {
		seconds, _ := feed.Strtotime(raw, loc)
		return max(seconds, 0)
	}
	if len(raw) > 10 {
		raw = raw[:len(raw)-3]
	}
	seconds, _ := strconv.ParseInt(raw, 10, 64)
	return seconds
}

// scalar spells a number or a text of a document.
func scalar(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	}
	return ""
}

// webAddress reads an address of a document: an http or https one, or none.
func webAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return raw
}

// candidate is an item of a document on its way to become an entry.
type candidate struct {
	entry  *store.Entry
	feed   string
	labels []string
	// read is nil when the document does not say.
	read    *bool
	starred bool
}

// source is what an item says about its feed.
type source struct {
	address, site, title, category string
	muted                          bool
}

// Entries reads a document of entries into the account of the user. An
// entry goes to the feed its item names, which is added without being
// fetched when the user does not have it; an entry the feed has already, by
// its identifier, is rewritten. starred says the document is a list of
// starred entries in the old form, where an item without a label is starred
// without saying so.
func Entries(ctx context.Context, db *store.Store, registry *hooks.Registry, u *store.User, data []byte, starred bool, o Options) (Report, error) {
	var items []json.RawMessage
	var document struct {
		Items *[]json.RawMessage `json:"items"`
	}
	switch {
	case json.Unmarshal(data, &items) == nil:
	case json.Unmarshal(data, &document) == nil && document.Items != nil:
		items = *document.Items
	case json.Valid(data):
		// An object without items holds no entries.
	default:
		return Report{}, ErrDocument
	}

	settings := readSettings(u)
	var (
		report     Report
		candidates []*candidate
		sources    = map[string]source{}
		order      []string
	)
	for _, raw := range items {
		var it incoming
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		guid, ok := text(it.GUID)
		if !ok {
			if guid, ok = text(it.ID); !ok {
				continue
			}
		}
		if guid == "" {
			continue
		}
		src := sourceOf(it.Origin)
		address, err := refresh.CheckURL(src.address)
		if err != nil {
			report.Incomplete = true
			continue
		}
		address = withoutCredentials(address)
		src.address = address
		if _, seen := sources[address]; !seen {
			sources[address] = src
			order = append(order, address)
		}
		candidates = append(candidates, newCandidate(&it, guid, address, starred, settings, o.Now))
	}

	err := db.InTx(ctx, func(tx *store.Store) error {
		report.Feeds, report.Entries, report.Updated = 0, 0, 0
		feeds, err := tx.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		byAddress := make(map[string]*store.Feed, len(feeds))
		for _, f := range feeds {
			// Documents name feeds without the credentials in their addresses.
			byAddress[withoutCredentials(f.URL)] = f
		}
		count := len(feeds)
		for _, address := range order {
			if byAddress[address] != nil {
				continue
			}
			if o.MaxFeeds > 0 && count >= o.MaxFeeds {
				report.Incomplete = true
				continue
			}
			f, err := addFeed(ctx, tx, registry, u, sources[address], settings, o)
			if err != nil {
				return err
			}
			if f == nil {
				report.Incomplete = true
				continue
			}
			byAddress[address] = f
			count++
			report.Feeds++
		}

		// Entries by feed: what is there already is looked up once a feed.
		byFeed := map[int64][]*candidate{}
		var feedOrder []int64
		seen := map[string]bool{}
		for _, c := range candidates {
			f := byAddress[c.feed]
			if f == nil {
				report.Incomplete = true
				continue
			}
			// The first item with an identifier stands for the others.
			key := strconv.FormatInt(f.ID, 10) + "\x00" + c.entry.GUID
			if seen[key] {
				continue
			}
			seen[key] = true
			c.entry.UserID, c.entry.FeedID = u.ID, f.ID
			if _, known := byFeed[f.ID]; !known {
				feedOrder = append(feedOrder, f.ID)
			}
			byFeed[f.ID] = append(byFeed[f.ID], c)
		}
		labelled := map[string][]int64{}
		for _, feedID := range feedOrder {
			list := byFeed[feedID]
			guids := make([]string, len(list))
			for i, c := range list {
				guids[i] = c.entry.GUID
			}
			states, err := tx.EntryStates(ctx, u.ID, feedID, guids)
			if err != nil {
				return err
			}
			var added []*candidate
			for _, c := range list {
				e, ok := registry.EntryBeforeInsert.Call(ctx, c.entry)
				if !ok {
					continue
				}
				c.entry = e
				st, exists := states[e.GUID]
				if !exists {
					if c.entry, ok = registry.EntryBeforeAdd.Call(ctx, c.entry); ok {
						added = append(added, c)
					}
					continue
				}
				stored, err := tx.EntryByID(ctx, u.ID, st.ID)
				if err != nil {
					return err
				}
				// The states the document does not name stay as they are.
				stored.Title, stored.Authors, stored.Content, stored.Link = e.Title, e.Authors, e.Content, e.Link
				stored.Published, stored.Tags = e.Published, e.Tags
				if c.read != nil {
					stored.IsRead = *c.read
				}
				stored.IsFavorite = stored.IsFavorite || c.starred
				if stored, ok = registry.EntryBeforeUpdate.Call(ctx, stored); !ok {
					continue
				}
				if err := tx.UpdateEntry(ctx, stored); err != nil {
					return err
				}
				c.entry = stored
				report.Updated++
				for _, label := range c.labels {
					labelled[label] = append(labelled[label], stored.ID)
				}
			}
			// New entries get identifiers in the order of their dates.
			sort.SliceStable(added, func(a, b int) bool { return added[a].entry.Published < added[b].entry.Published })
			entries := make([]*store.Entry, len(added))
			for i, c := range added {
				entries[i] = c.entry
			}
			if err := tx.InsertEntries(ctx, u.ID, entries); err != nil {
				return err
			}
			report.Entries += len(entries)
			for _, c := range added {
				for _, label := range c.labels {
					labelled[label] = append(labelled[label], c.entry.ID)
				}
			}
		}
		return attachLabels(ctx, tx, u, labelled, &report)
	})
	return report, err
}

// attachLabels gives entries the labels of the given names, making those
// the user does not have. A name a category has gives no label.
func attachLabels(ctx context.Context, tx *store.Store, u *store.User, labelled map[string][]int64, report *Report) error {
	if len(labelled) == 0 {
		return nil
	}
	labels, err := tx.Tags(ctx, u.ID)
	if err != nil {
		return err
	}
	byName := make(map[string]int64, len(labels))
	for _, l := range labels {
		byName[l.Name] = l.ID
	}
	names := make([]string, 0, len(labelled))
	for name := range labelled {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		id, has := byName[name]
		if !has {
			created := &store.Tag{UserID: u.ID, Name: name}
			switch err := tx.CreateTag(ctx, created); {
			case errors.Is(err, store.ErrConflict):
				report.Incomplete = true
				continue
			case err != nil:
				return err
			}
			id = created.ID
		}
		if err := tx.TagEntries(ctx, u.ID, id, labelled[name]); err != nil {
			return err
		}
	}
	return nil
}

// settings are what an import needs to know of the user.
type settings struct {
	// readOnArrival makes read the entries whose state a document does not say.
	readOnArrival bool
	ttlDefault    int
	location      *time.Location
}

func readSettings(u *store.User) settings {
	var stored struct {
		TTLDefault int    `json:"ttl_default"`
		Timezone   string `json:"timezone"`
		MarkWhen   struct {
			Reception bool `json:"reception"`
		} `json:"mark_when"`
	}
	// Settings of another shape are settings nobody made.
	_ = json.Unmarshal(u.Settings, &stored)
	s := settings{readOnArrival: stored.MarkWhen.Reception, ttlDefault: 3600, location: time.Local}
	if stored.TTLDefault > 0 {
		s.ttlDefault = stored.TTLDefault
	}
	if loc, err := time.LoadLocation(stored.Timezone); err == nil && stored.Timezone != "" {
		s.location = loc
	}
	return s
}

// sourceOf reads what an item says about the feed it comes from: the
// address FreshRSS writes, the stream of Google Reader, the site, or
// nothing, in which case the entry goes to a feed that stands for imports
// and is never fetched.
func sourceOf(raw json.RawMessage) source {
	var o map[string]any
	_ = json.Unmarshal(raw, &o)
	get := func(key string) string {
		s, _ := o[key].(string)
		return s
	}
	src := source{site: get("htmlUrl"), title: strings.TrimSpace(get("title")), category: strings.TrimSpace(get("category"))}
	if src.title == "" {
		src.title = "Import"
	}
	stream := get("streamId")
	switch {
	case get("feedUrl") != "":
		src.address = get("feedUrl")
	case strings.HasPrefix(stream, "feed/"):
		src.address = strings.TrimPrefix(stream, "feed/")
	case src.site != "":
		src.address = src.site
	default:
		src.address, src.muted = placeholderFeed, true
	}
	return src
}

func newCandidate(it *incoming, guid, feedAddress string, starredList bool, s settings, now time.Time) *candidate {
	c := &candidate{feed: feedAddress}
	var (
		tags       []string
		categories []any
	)
	// Categories of another shape are no categories.
	_ = json.Unmarshal(it.Categories, &categories)
	for _, v := range categories {
		tag, ok := v.(string)
		if !ok {
			continue
		}
		tag = strings.TrimSpace(tag)
		switch {
		case !userTag.MatchString(tag):
			if tag != "" {
				tags = append(tags, tag)
			}
		case starredTag.MatchString(tag):
			c.starred = true
		case readTag.MatchString(tag):
			yes := true
			c.read = &yes
		case unreadTag.MatchString(tag):
			no := false
			c.read = &no
		default:
			if m := labelTag.FindStringSubmatch(tag); m != nil {
				c.labels = append(c.labels, m[1])
			}
		}
	}
	if starredList && !c.starred {
		c.starred = len(c.labels) == 0
	}

	address := ""
	var alternate []struct {
		Href string `json:"href"`
	}
	if json.Unmarshal(it.Alternate, &alternate) == nil && len(alternate) > 0 && alternate[0].Href != "" {
		address = webAddress(alternate[0].Href)
	} else if s, ok := text(it.URL); ok {
		address = webAddress(s)
	}
	title, hasTitle := text(it.Title)
	if !hasTitle {
		title = address
	}
	// FreshRSS writes an entry without a title under its identifier.
	if strings.TrimSpace(title) == guid {
		title = ""
	}
	body, ok := contentOf(it.Content)
	if !ok {
		body, _ = contentOf(it.Summary)
	}
	author, _ := text(it.Author)
	var authors []string
	for _, name := range authorsSplit.Split(decodeChars.Replace(author), -1) {
		if name = strings.TrimSpace(name); name != "" {
			authors = append(authors, name)
		}
	}
	c.entry = &store.Entry{
		GUID: guid, Title: decodeChars.Replace(title), Authors: authors, Content: sanitize.HTML(body, address, nil),
		Link: address, Published: published(it, s.location), LastSeen: now.Unix(), Tags: tags,
		IsRead: s.readOnArrival, IsFavorite: c.starred,
	}
	if c.read != nil {
		c.entry.IsRead = *c.read
	}
	return c
}

// addFeed stores the feed an item names. The feed is not fetched: a refresh
// does that. nil means an extension refused it.
func addFeed(ctx context.Context, tx *store.Store, registry *hooks.Registry, u *store.User, src source, s settings, o Options) (*store.Feed, error) {
	f := &store.Feed{UserID: u.ID, URL: src.address, Name: src.title, Website: webAddress(src.site), Priority: store.PriorityMain}
	if f.Website == "" {
		f.Website = src.address
	}
	if src.muted {
		f.TTL = -s.ttlDefault
	}
	if src.category != "" {
		categories, err := tx.Categories(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range categories {
			if c.Name == src.category {
				f.CategoryID = c.ID
			}
		}
		if f.CategoryID == 0 && (o.MaxCategories <= 0 || len(categories) < o.MaxCategories) {
			c := &store.Category{UserID: u.ID, Name: src.category}
			switch err := tx.CreateCategory(ctx, c); {
			case errors.Is(err, store.ErrConflict):
				// A label has the name: the feed goes to the default category.
			case err != nil:
				return nil, err
			default:
				f.CategoryID = c.ID
			}
		}
	}
	f, ok := registry.FeedBeforeInsert.Call(ctx, f)
	if !ok {
		return nil, nil
	}
	if err := tx.CreateFeed(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}
