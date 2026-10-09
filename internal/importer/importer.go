// Package importer moves a FreshRSS installation into a freshgo database.
//
// The import is one-way and all-or-nothing: it needs an empty database and
// either carries over every user or changes nothing. Identifiers, guids,
// read and favorite states, labels and API passwords are kept, so API
// clients keep working against freshgo without being set up again.
package importer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/favicon"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/search"
	"github.com/juev/freshgo/internal/store"
)

// ErrNotEmpty is returned when the destination database already has users.
var ErrNotEmpty = errors.New("importer: the database is not empty")

// Options describe the installation to import.
type Options struct {
	// DataDir is the FreshRSS data directory: the one with config.php and users/.
	DataDir string
	// SourceDatabaseURL replaces the PostgreSQL connection settings found in
	// config.php, for a database reachable under another address than
	// FreshRSS used. Ignored for SQLite installations.
	SourceDatabaseURL string
}

// Report tells what was imported.
type Report struct {
	Users []UserReport
	// Warnings list what could not be carried over exactly.
	Warnings []string
}

// UserReport counts the objects imported for one user.
type UserReport struct {
	Name       string
	Categories int
	Feeds      int
	Entries    int
	Tags       int
	// TaggedEntries is the number of label-to-entry links.
	TaggedEntries int
	CustomIcons   int
}

func (r *Report) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Run imports the installation into dst. On error dst is left unchanged.
func Run(ctx context.Context, dst *store.Store, opts Options) (*Report, error) {
	system, err := readSystemConfig(opts.DataDir)
	if err != nil {
		return nil, err
	}
	names, skipped, err := userNames(opts.DataDir)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("importer: no users in %s", filepath.Join(opts.DataDir, "users"))
	}
	open, closeSource, err := sourceOpener(system, opts)
	if err != nil {
		return nil, err
	}
	defer closeSource()

	report := &Report{}
	for _, name := range skipped {
		report.warnf("users/%s has no config.php and was not imported", name)
	}
	for _, key := range system.unreadable {
		report.warnf("the system setting %s has a value freshgo cannot read and keeps its default", key)
	}
	err = dst.InTx(ctx, func(tx *store.Store) error {
		existing, err := tx.Users(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return ErrNotEmpty
		}
		if err := tx.SetSetting(ctx, store.SettingSalt, system.salt); err != nil {
			return err
		}
		if err := tx.SetSystem(ctx, system.settings); err != nil {
			return err
		}
		domains, err := readForceHTTPS(opts.DataDir)
		if err != nil {
			return err
		}
		if domains != "" {
			if err := tx.SetSetting(ctx, store.SettingForceHTTPS, domains); err != nil {
				return err
			}
		}
		for _, name := range names {
			src, err := open(name)
			if err != nil {
				return fmt.Errorf("importer: user %s: %w", name, err)
			}
			u := &userImport{ctx: ctx, dst: tx, src: src, name: name, dataDir: opts.DataDir, salt: system.salt, report: report}
			err = u.run()
			src.close()
			if err != nil {
				return fmt.Errorf("importer: user %s: %w", name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

type systemConfig struct {
	salt string
	db   map[string]any
	// settings is what the administrator has set, over the defaults of
	// FreshRSS; unreadable names the settings that could not be taken over.
	settings   store.System
	unreadable []string
}

// systemKeys are the settings of the system config.php freshgo has a use for.
var systemKeys = []string{
	"title", "language", "default_user", "auth_type", "allow_anonymous", "allow_anonymous_refresh",
	"api_enabled", "force_email_validation", "http_auth_auto_register", "reauth_time",
	"closed_registration_message", "limits",
}

// freshRSSTitle is what FreshRSS calls an installation nobody has named.
const freshRSSTitle = "FreshRSS"

// readSystemSettings takes the settings of an installation from its
// config.php. A file written by FreshRSS holds only what differs from the
// defaults, so those come first. The title is the exception: the name of
// the program that is gone is not carried over, only a title somebody chose.
func readSystemSettings(conf map[string]any) (settings store.System, unreadable []string) {
	settings = store.DefaultSystem()
	settings.DefaultUser = "_"
	settings.APIEnabled = false
	for _, key := range systemKeys {
		value, set := conf[key]
		if !set {
			continue
		}
		// Each on its own: one value of the wrong type costs one setting.
		raw, err := json.Marshal(map[string]any{key: value})
		if err == nil {
			merged := settings
			if err = json.Unmarshal(raw, &merged); err == nil {
				settings = merged
				continue
			}
		}
		unreadable = append(unreadable, key)
	}
	if settings.Title == freshRSSTitle {
		settings.Title = store.DefaultSystem().Title
	}
	if options, set := conf["curl_options"]; set {
		proxy, err := systemProxy(options)
		if err != nil {
			unreadable = append(unreadable, "curl_options")
		}
		settings.Proxy = proxy
	}
	return settings, unreadable
}

// Keys of curl_options a proxy is set with, the numbers of CURLOPT_PROXYPORT,
// CURLOPT_PROXY and CURLOPT_PROXYUSERPWD.
const (
	curlProxyPort    = "59"
	curlProxyType    = "101"
	curlProxy        = "10004"
	curlProxyUserPwd = "10006"
)

// curlProxyKinds are the values of CURLOPT_PROXYTYPE by the scheme that
// spells them in the address of a proxy.
var curlProxyKinds = map[string]int{"http": 0, "https": 2, "socks4": 4, "socks5": 5, "socks4a": 6, "socks5h": 7}

// systemProxy returns the proxy the curl_options of an installation send
// every feed through, as System.Proxy has it; empty when they name none.
// The other options, which FreshRSS hands to cURL as they are, have no
// counterpart.
func systemProxy(options any) (string, error) {
	// PHP writes an empty array as a list.
	curl, _ := options.(map[string]any)
	address, _ := curl[curlProxy].(string)
	scheme, rest, found := strings.Cut(address, "://")
	if found {
		address = rest
	}
	if address == "" {
		return "", nil
	}
	curl = maps.Clone(curl)
	// Without a kind of its own, cURL takes the one the address spells.
	if kind, spelled := curlProxyKinds[strings.ToLower(scheme)]; found && spelled && curl[curlProxyType] == nil {
		curl[curlProxyType] = kind
	}
	// The port may be an option of its own.
	if port, set := curl[curlProxyPort]; set {
		if at, err := url.Parse("//" + address); err == nil && at.Port() == "" {
			address = net.JoinHostPort(at.Hostname(), fmt.Sprint(port))
			if at.User != nil {
				address = at.User.String() + "@" + address
			}
		}
	}
	curl[curlProxy] = address
	raw, err := json.Marshal(map[string]any{"curl_params": curl})
	if err != nil {
		return "", err
	}
	params, err := fetch.FeedParams("", raw)
	if err != nil || params.Proxy == nil {
		return "", err
	}
	proxy := params.Proxy
	if credentials, _ := curl[curlProxyUserPwd].(string); credentials != "" && proxy.User == nil {
		name, password, _ := strings.Cut(credentials, ":")
		proxy.User = url.UserPassword(name, password)
	}
	// SOCKS4 is a kind a feed may keep and freshgo cannot go through.
	if _, err := fetch.ParseProxy(proxy.String()); err != nil {
		return "", err
	}
	return proxy.String(), nil
}

func readSystemConfig(dataDir string) (*systemConfig, error) {
	conf, err := readConfig(filepath.Join(dataDir, "config.php"))
	if err != nil {
		return nil, err
	}
	c := &systemConfig{}
	c.salt, _ = conf["salt"].(string)
	if c.salt == "" {
		return nil, fmt.Errorf("importer: %s: no salt; is it the data directory of an installed FreshRSS?", filepath.Join(dataDir, "config.php"))
	}
	c.db, _ = conf["db"].(map[string]any)
	c.settings, c.unreadable = readSystemSettings(conf)
	// The terms of use are a file of their own; most installations have none.
	if terms, err := os.ReadFile(filepath.Join(dataDir, "tos.html")); err == nil {
		c.settings.TOS = strings.ToValidUTF8(strings.ReplaceAll(string(terms), "\x00", ""), "\uFFFD")
	}
	return c, nil
}

func readConfig(path string) (map[string]any, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("importer: %w", err)
	}
	v, err := parsePHPConfig(string(src))
	if err != nil {
		return nil, fmt.Errorf("importer: %s: %w", path, err)
	}
	conf, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("importer: %s: want an array with named keys", path)
	}
	return conf, nil
}

// userNames lists the users of the installation: the directories in users/
// that have a config.php, a symbolic link to such a directory included.
// FreshRSS keeps a template user in "_". Directories without a config.php
// are returned as skipped so that no user is left behind silently.
func userNames(dataDir string) (names, skipped []string, err error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "users"))
	if err != nil {
		return nil, nil, fmt.Errorf("importer: %w", err)
	}
	for _, e := range entries {
		if e.Name() == "_" {
			continue
		}
		dir := filepath.Join(dataDir, "users", e.Name())
		// Stat follows symbolic links, unlike the directory entry.
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "config.php")); err != nil {
			skipped = append(skipped, e.Name())
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, skipped, nil
}

// readForceHTTPS returns the domains of force-https.txt, one per line,
// without comments. A missing file is an empty list.
func readForceHTTPS(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "force-https.txt"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("importer: %w", err)
	}
	var domains []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		domains = append(domains, line)
	}
	return strings.Join(domains, "\n"), nil
}

type userImport struct {
	ctx     context.Context
	dst     *store.Store
	src     *source
	name    string
	dataDir string
	salt    string
	report  *Report

	user  *store.User
	stats UserReport
	// language is the language of the user's interface.
	language string
	// queries are the saved searches of the user, which filters may refer to.
	queries []search.SavedQuery
}

func (u *userImport) run() error {
	if err := u.createUser(); err != nil {
		return err
	}
	u.stats.Name = u.name
	steps := []func() error{u.categories, u.feeds, u.entries, u.tags, u.counters}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	u.report.Users = append(u.report.Users, u.stats)
	return nil
}

func (u *userImport) warnf(format string, args ...any) {
	u.report.warnf("user %s: %s", u.name, fmt.Sprintf(format, args...))
}

func (u *userImport) createUser() error {
	conf, err := readConfig(filepath.Join(u.dataDir, "users", u.name, "config.php"))
	if err != nil {
		return err
	}
	u.language, _ = conf["language"].(string)
	hash, _ := conf["apiPasswordHash"].(string)
	if hash == "" {
		u.warnf("no API password is set; API clients cannot log in until one is")
	}
	// The hash gets a column of its own; everything else is kept as it is.
	delete(conf, "apiPasswordHash")
	settings, err := json.Marshal(conf)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	u.user = &store.User{Name: u.name, APIPasswordHash: hash, Settings: settings}
	if err := u.dst.CreateUser(u.ctx, u.user); err != nil {
		return err
	}
	var kept struct {
		Queries json.RawMessage `json:"queries"`
		Filters json.RawMessage `json:"filters"`
	}
	if err := json.Unmarshal(settings, &kept); err == nil {
		u.queries = search.ParseSavedQueries(kept.Queries)
		u.checkFilters(kept.Filters, "settings")
	}
	return nil
}

// checkFilters warns about the filter actions freshgo will not apply: those
// whose query it cannot read. owner says whose filters they are.
func (u *userImport) checkFilters(filters json.RawMessage, owner string) {
	_, problems := search.ParseRules(filters, search.Options{Queries: u.queries})
	for _, problem := range problems {
		u.warnf("%s: %v; the filter will be skipped", owner, problem)
	}
}

// checkAttributeFilters is checkFilters for the "filters" of an attributes value.
func (u *userImport) checkAttributeFilters(attributes json.RawMessage, owner string) {
	var kept struct {
		Filters json.RawMessage `json:"filters"`
	}
	if json.Unmarshal(attributes, &kept) == nil {
		u.checkFilters(kept.Filters, owner)
	}
}

// defaultCategoryNames is what FreshRSS calls the default category in each
// language of its interface (gen.short.default_category).
var defaultCategoryNames = map[string]string{
	"az": "Kateqoriyasız", "be": "Без катэгорыі", "cs": "Nezařazeno", "de": "Unkategorisiert",
	"el": "Μη κατηγοριοποιημένα", "en": "Uncategorized", "en-US": "Uncategorized", "es": "Sin categorizar",
	"fa": "دسته\u200cبندی\u200cنشده", "fi": "Luokittelematon", "fr": "Sans catégorie", "he": "ללא קטגוריה",
	"hu": "Kategória nélküli", "id": "Tidak ada kategori", "it": "Senza categoria", "ja": "未分類",
	"ko": "분류 없음", "lt": "Be kategorijos", "lv": "Neklasificēts", "nl": "Niet ingedeeld", "oc": "Pas triat",
	"pl": "Brak kategorii", "pt-BR": "Sem categoria", "pt-PT": "Sem categoria", "ru": "Без категории",
	"sk": "Bez kategórie", "tr": "Kategorisiz", "uk": "Без категорії", "zh-CN": "未分类", "zh-TW": "未分類",
}

func (u *userImport) categories() error {
	rows, err := u.src.query(u.ctx, `SELECT "id", "name", "kind", "lastUpdate", "error", "attributes" FROM `+u.src.table("category")+` ORDER BY "id"`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var (
		categories []*store.Category
		names      = map[string]bool{}
	)
	for rows.Next() {
		var (
			c                        = &store.Category{UserID: u.user.ID}
			kind, lastUpdate, failed nullInt
			attributes               nullString
		)
		if err := rows.Scan(&c.ID, &c.Name, &kind, &lastUpdate, &failed, &attributes); err != nil {
			return fmt.Errorf("category: %w", err)
		}
		c.Name = decodeText(c.Name)
		c.Kind, c.LastUpdate, c.Error = int(kind.Int64), lastUpdate.Int64, failed.Int64
		c.Attributes = u.attributes(attributes.String, "category %d", c.ID)
		u.checkAttributeFilters(c.Attributes, fmt.Sprintf("category %d", c.ID))
		categories = append(categories, c)
		if c.ID != store.DefaultCategoryID {
			names[c.Name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range categories {
		// The default category was created together with the user.
		if c.ID == store.DefaultCategoryID {
			// FreshRSS shows it, and names it to API clients, in the language
			// of the user, whatever name is stored.
			shown, known := defaultCategoryNames[u.language]
			if !known {
				shown = store.DefaultCategoryName
			}
			if names[shown] {
				u.warnf("the default category keeps the name %q: another category is called %q, as FreshRSS shows the default one", c.Name, shown)
			} else {
				c.Name = shown
			}
			err = u.dst.UpdateCategory(u.ctx, c)
		} else {
			err = u.dst.CreateCategory(u.ctx, c)
		}
		if err != nil {
			return err
		}
		u.stats.Categories++
	}
	return nil
}

func (u *userImport) feeds() error {
	rows, err := u.src.query(u.ctx, `
		SELECT "id", "url", "kind", "category", "name", "website", "description", "lastUpdate", "priority",
			"pathEntries", "httpAuth", "error", "ttl", "attributes"
		FROM `+u.src.table("feed")+` ORDER BY "id"`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			f                                           = &store.Feed{UserID: u.user.ID}
			kind, category, lastUpdate, failed          nullInt
			website, description, pathEntries, httpAuth nullString
			attributes                                  nullString
		)
		err := rows.Scan(&f.ID, &f.URL, &kind, &category, &f.Name, &website, &description, &lastUpdate, &f.Priority,
			&pathEntries, &httpAuth, &failed, &f.TTL, &attributes)
		if err != nil {
			return fmt.Errorf("feed: %w", err)
		}
		f.URL = decodeText(f.URL)
		f.Name = decodeText(f.Name)
		f.Website = decodeText(website.String)
		f.Description = decodeText(description.String)
		f.PathEntries = decodeText(pathEntries.String)
		f.Kind, f.LastUpdate, f.Error = int(kind.Int64), lastUpdate.Int64, failed.Int64
		// FreshRSS lets the category be NULL or 0; both mean the default one.
		f.CategoryID = category.Int64
		if httpAuth.String != "" {
			auth, err := base64.StdEncoding.DecodeString(httpAuth.String)
			if err != nil {
				u.warnf("feed %d (%s): HTTP credentials are not valid base64 and were dropped", f.ID, f.URL)
				auth = nil
			}
			// FreshRSS encodes the form input before the base64 and decodes it on use.
			f.HTTPAuth = decodeText(string(auth))
		}
		f.Attributes = u.attributes(attributes.String, "feed %d", f.ID)
		u.checkAttributeFilters(f.Attributes, fmt.Sprintf("feed %d (%s)", f.ID, f.URL))
		if err := u.dst.CreateFeed(u.ctx, f); err != nil {
			return err
		}
		u.stats.Feeds++
		if err := u.feedExtras(f); err != nil {
			return err
		}
	}
	return rows.Err()
}

// feedExtras handles what hangs off the feed attributes: the custom icon and
// the settings freshgo cannot reproduce exactly.
func (u *userImport) feedExtras(f *store.Feed) error {
	var attrs struct {
		CustomFavicon   bool   `json:"customFavicon"`
		UnicityCriteria string `json:"unicityCriteria"`
	}
	// Attributes of other shapes are kept as they are and not interpreted here.
	_ = json.Unmarshal(f.Attributes, &attrs)

	if strings.Contains(attrs.UnicityCriteria, "content") {
		u.warnf("feed %d (%s): entries are told apart by a hash of their content (%s), which freshgo computes differently; the first refresh may add each current entry once more",
			f.ID, f.URL, attrs.UnicityCriteria)
	}
	if !attrs.CustomFavicon {
		return nil
	}
	// FreshRSS names the file of a custom icon after crc32b(salt . feed id . user name).
	hash := crc32.ChecksumIEEE([]byte(u.salt + strconv.FormatInt(f.ID, 10) + u.name))
	path := filepath.Join(u.dataDir, "favicons", fmt.Sprintf("%08x.ico", hash))
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(content) == 0 {
		u.warnf("feed %d (%s): the custom icon %s is missing", f.ID, f.URL, path)
		return nil
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	icon := store.CustomIcon{
		Hash: favicon.CustomHash(u.salt, u.user.ID, f.ID), Content: content, Modified: info.ModTime().Unix(),
	}
	if err := u.dst.SetCustomIcon(u.ctx, u.user.ID, f.ID, icon); err != nil {
		return err
	}
	u.stats.CustomIcons++
	return nil
}

// entryBatch bounds the memory used while copying entries.
const entryBatch = 500

func (u *userImport) entries() error {
	var pending int
	if err := u.src.queryRow(u.ctx, `SELECT COUNT(*) FROM `+u.src.table("entrytmp")).Scan(&pending); err != nil {
		return fmt.Errorf("entrytmp: %w", err)
	}
	if pending > 0 {
		u.warnf("%d entries were still waiting in entrytmp (a refresh was interrupted) and were not imported; the next refresh fetches them again", pending)
	}

	rows, err := u.src.query(u.ctx, `
		SELECT "id", "guid", "title", "author", "content", "link", "date", "lastSeen", "lastModified",
			"lastUserModified", "is_read", "is_favorite", "id_feed", "tags", "attributes"
		FROM `+u.src.table("entry")+` ORDER BY "id"`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	batch := make([]*store.Entry, 0, entryBatch)
	flush := func() error {
		if err := u.dst.InsertEntries(u.ctx, u.user.ID, batch); err != nil {
			return err
		}
		u.stats.Entries += len(batch)
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		var (
			e                                              = &store.Entry{}
			author, content, tags, attributes              nullString
			date, lastSeen, lastModified, lastUserModified nullInt
			isRead, isFavorite, feedID                     nullInt
		)
		err := rows.Scan(&e.ID, &e.GUID, &e.Title, &author, &content, &e.Link, &date, &lastSeen, &lastModified,
			&lastUserModified, &isRead, &isFavorite, &feedID, &tags, &attributes)
		if err != nil {
			return fmt.Errorf("entry: %w", err)
		}
		// guid stays as stored: it is a key, not text. The FreshRSS hash is not
		// carried over, see docs/specs/storage.md.
		e.FeedID = feedID.Int64
		// FreshRSS stores an entry without a title under its guid and takes
		// the two being equal for "no title"; here that is an empty title.
		if strings.TrimSpace(e.Title) == e.GUID {
			e.Title = ""
		}
		e.Title = decodeText(e.Title)
		e.Authors = splitAuthors(author.String)
		e.Content = content.String
		e.Link = strings.TrimSpace(decodeText(e.Link))
		e.Published, e.LastSeen = date.Int64, lastSeen.Int64
		e.LastModified, e.LastUserModified = lastModified.Int64, lastUserModified.Int64
		e.IsRead, e.IsFavorite = isRead.Int64 != 0, isFavorite.Int64 != 0
		e.Tags = splitTags(tags.String)
		e.Attributes = u.attributes(attributes.String, "entry %d", e.ID)
		// FreshRSS cleaned the text by its own rules. A page shows what is
		// stored, so the text is cleaned by the rules of this server too.
		var attrs []byte
		e.Content, attrs, _ = sanitize.Stored(e.Content, e.Attributes, e.Link)
		e.Attributes = attrs
		batch = append(batch, e)
		if len(batch) == entryBatch {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

func (u *userImport) tags() error {
	rows, err := u.src.query(u.ctx, `SELECT "id", "name", "attributes" FROM `+u.src.table("tag")+` ORDER BY "id"`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		t := &store.Tag{UserID: u.user.ID}
		var attributes nullString
		if err := rows.Scan(&t.ID, &t.Name, &attributes); err != nil {
			return fmt.Errorf("tag: %w", err)
		}
		t.Name = decodeText(t.Name)
		t.Attributes = u.attributes(attributes.String, "tag %d", t.ID)
		u.checkAttributeFilters(t.Attributes, fmt.Sprintf("label %d", t.ID))
		if err := u.dst.CreateTag(u.ctx, t); err != nil {
			return err
		}
		u.stats.Tags++
	}
	if err := rows.Err(); err != nil {
		return err
	}

	links, err := u.src.query(u.ctx, `SELECT "id_tag", "id_entry" FROM `+u.src.table("entrytag")+` ORDER BY "id_tag", "id_entry"`)
	if err != nil {
		return err
	}
	defer func() { _ = links.Close() }()
	for links.Next() {
		var tagID, entryID int64
		if err := links.Scan(&tagID, &entryID); err != nil {
			return fmt.Errorf("entrytag: %w", err)
		}
		if err := u.dst.TagEntry(u.ctx, u.user.ID, tagID, entryID); err != nil {
			return err
		}
		u.stats.TaggedEntries++
	}
	return links.Err()
}

// counters carries over the identifier counters, which may be ahead of the
// largest identifier in use when the newest objects were deleted.
func (u *userImport) counters() error {
	var c store.Counters
	for name, dest := range map[string]*int64{"category": &c.Category, "feed": &c.Feed, "tag": &c.Tag} {
		value, err := u.src.counter(u.ctx, name)
		if err != nil {
			u.warnf("the %s counter could not be read (%v); new identifiers continue after the largest one in use", name, err)
			continue
		}
		*dest = value
	}
	return u.dst.RaiseCounters(u.ctx, u.user.ID, c)
}

// attributes returns a stored attributes value as a JSON object. FreshRSS
// writes an empty set as "[]", an empty string or NULL.
func (u *userImport) attributes(stored, what string, id int64) json.RawMessage {
	stored = strings.TrimSpace(stored)
	if stored == "" || stored == "[]" || stored == "null" {
		return json.RawMessage("{}")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &object); err != nil {
		u.warnf(what+": attributes are not a JSON object and were dropped", id)
		return json.RawMessage("{}")
	}
	return json.RawMessage(stored)
}
