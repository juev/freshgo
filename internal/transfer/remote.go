package transfer

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/refresh"
	"github.com/juev/freshgo/internal/sanitize"
	"github.com/juev/freshgo/internal/store"
)

// Remote is the account of a user on another reader's server, as the
// sources that read one (Miniflux, Tiny Tiny RSS) hand it over. Reading it
// takes the network and writing it takes the database: the two never overlap,
// so a source that fails half way has changed nothing.
type Remote struct {
	// Categories are the names of all categories, also those without feeds.
	Categories []string
	Feeds      []RemoteFeed
	Entries    []RemoteEntry
	// Warnings are what the source holds and freshgo cannot carry over.
	Warnings []string
}

// RemoteFeed is a subscription of the account.
type RemoteFeed struct {
	// Key is what the entries name their feed by.
	Key      string
	URL      string
	Site     string
	Title    string
	Category string
	// Muted feeds are not refreshed.
	Muted bool
	// CategoryOnly keeps the feed out of the main stream.
	CategoryOnly bool
	// HTTPAuth is "user:password" for the feed's own server.
	HTTPAuth  string
	UserAgent string
	Cookie    string
	// Selector picks the article on its page; Automatic finds it without one.
	Selector  string
	Automatic bool
}

// RemoteEntry is an article of the account.
type RemoteEntry struct {
	// Feed is the Key of its feed.
	Feed string
	// ID is the identifier the source keeps. The entry is told by its link
	// and the feed is set to be told so by a refresh as well, since the
	// identifiers of a source are not those a refresh computes; ID stands
	// for the link of an entry that has none.
	ID        string
	Title     string
	Author    string
	URL       string
	Content   string
	Published int64
	Read      bool
	Starred   bool
	// Labels become labels of the user.
	Labels []string
}

// Names lists up to a few names, with how many more there are.
func Names(names []string) string {
	const few = 5
	if len(names) <= few {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:few], ", "), len(names)-few)
}

// ImportRemote writes the account into the account of the user, in one
// transaction: on an error nothing of it is kept. A feed the user has
// already, by its address, keeps its settings and takes the entries; the
// entries it has are rewritten and get the read and starred states of the
// source, so the import can be run again to catch up.
func ImportRemote(ctx context.Context, db *store.Store, registry *hooks.Registry, u *store.User, remote *Remote, o Options) (Report, error) {
	var report Report
	err := db.InTx(ctx, func(tx *store.Store) error {
		report = Report{Warnings: slices.Clone(remote.Warnings)}
		existing, err := tx.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		byURL := make(map[string]*store.Feed, len(existing))
		for _, f := range existing {
			byURL[withoutCredentials(f.URL)] = f
		}
		count := len(existing)

		var overLimit []string
		for _, name := range remote.Categories {
			_, limited, err := categoryNamed(ctx, tx, u, name, o.MaxCategories)
			if err != nil {
				return err
			}
			if limited {
				report.Incomplete = true
				report.Warnings = append(report.Warnings, fmt.Sprintf("category %s: not made, the limit of categories is reached", name))
			}
		}
		byKey := make(map[string]*store.Feed, len(remote.Feeds))
		for _, rf := range remote.Feeds {
			address, err := refresh.CheckURL(rf.URL)
			if err != nil {
				report.Incomplete = true
				report.Warnings = append(report.Warnings, fmt.Sprintf("feed %s: the address is not usable, so the feed and its entries were skipped", rf.Title))
				continue
			}
			address = withoutCredentials(address)
			if f := byURL[address]; f != nil {
				byKey[rf.Key] = f
				var a struct {
					Unicity string `json:"unicityCriteria"`
				}
				_ = json.Unmarshal(f.Attributes, &a)
				if a.Unicity != "link" {
					report.Warnings = append(report.Warnings, fmt.Sprintf("feed %s: already subscribed with entries told by the identifier the feed gives, so a refresh may add the imported entries again", f.Name))
				}
				continue
			}
			if o.MaxFeeds > 0 && count >= o.MaxFeeds {
				overLimit = append(overLimit, rf.Title)
				continue
			}
			f, warning, err := addRemoteFeed(ctx, tx, registry, u, rf, address, o)
			if err != nil {
				return err
			}
			if warning != "" {
				report.Incomplete = true
				report.Warnings = append(report.Warnings, warning)
			}
			if f == nil {
				report.Warnings = append(report.Warnings, fmt.Sprintf("feed %s: refused by an extension", rf.Title))
				continue
			}
			byKey[rf.Key] = f
			byURL[address] = f
			count++
			report.Feeds++
		}
		if len(overLimit) > 0 {
			report.Incomplete = true
			report.Warnings = append(report.Warnings, fmt.Sprintf("%d feeds not imported, the limit of feeds is reached: %s", len(overLimit), Names(overLimit)))
		}

		var (
			candidates []*candidate
			skipped    int
		)
		for _, re := range remote.Entries {
			if byKey[re.Feed] == nil {
				skipped++
				continue
			}
			c := remoteCandidate(&re, o)
			if c == nil {
				skipped++
				continue
			}
			candidates = append(candidates, c)
		}
		if skipped > 0 {
			report.Incomplete = true
			report.Warnings = append(report.Warnings, fmt.Sprintf("%d entries not imported: their feeds were not imported or they have no identifier", skipped))
		}
		return writeEntries(ctx, tx, registry, u, byKey, candidates, &report)
	})
	return report, err
}

// remoteCandidate turns an entry of the account into a candidate. nil
// means the entry has no identifier to be told by.
func remoteCandidate(re *RemoteEntry, o Options) *candidate {
	address := webAddress(re.URL)
	guid := address
	if guid == "" {
		guid = strings.Trim(re.ID, " \n\r\t\v")
	}
	if len(guid) > maxGUID {
		guid = strings.ToValidUTF8(guid[:maxGUID], "")
	}
	if guid == "" {
		return nil
	}
	read, starred := re.Read, re.Starred
	c := &candidate{feed: re.Feed, read: &read, starred: starred, labels: re.Labels}
	var authors []string
	for _, name := range authorsSplit.Split(re.Author, -1) {
		if name = strings.TrimSpace(name); name != "" {
			authors = append(authors, name)
		}
	}
	c.entry = &store.Entry{
		GUID: guid, Title: re.Title, Authors: authors, Content: sanitize.HTML(re.Content, address, nil),
		Link: address, Published: re.Published, LastSeen: o.Now.Unix(),
		IsRead: read, IsFavorite: starred,
	}
	return c
}

// addRemoteFeed stores a feed of the account with the settings freshgo has
// for it. The feed is not fetched: a refresh does that. A nil feed with no
// error means an extension refused it; warning is set when the feed went to
// the default category because the limit of categories was reached.
func addRemoteFeed(ctx context.Context, tx *store.Store, registry *hooks.Registry, u *store.User, rf RemoteFeed, address string, o Options) (f *store.Feed, warning string, err error) {
	site := webAddress(rf.Site)
	if site == "" {
		site = address
	}
	name := strings.TrimSpace(rf.Title)
	if name == "" {
		name = address
	}
	f = &store.Feed{
		UserID: u.ID, URL: address, Name: name, Website: site, Priority: store.PriorityMain,
		PathEntries: rf.Selector, HTTPAuth: rf.HTTPAuth,
	}
	if rf.CategoryOnly {
		f.Priority = 0
	}
	if rf.Muted {
		f.TTL = store.MutedTTL
	}
	if rf.Category != "" {
		id, limited, err := categoryNamed(ctx, tx, u, rf.Category, o.MaxCategories)
		if err != nil {
			return nil, "", err
		}
		if limited {
			warning = fmt.Sprintf("feed %s: category %s not made, the limit of categories is reached; the feed is in the default category", name, rf.Category)
		}
		f.CategoryID = id
	}
	attrs := map[string]any{}
	if rf.Automatic {
		attrs["path_entries_auto"] = true
	}
	attrs["unicityCriteria"] = "link"
	attrs["unicityCriteriaForced"] = true
	curl := map[string]any{}
	if rf.UserAgent != "" {
		curl["10018"] = rf.UserAgent
	}
	if rf.Cookie != "" {
		curl["10022"] = rf.Cookie
	}
	if len(curl) > 0 {
		attrs["curl_params"] = curl
	}
	if len(attrs) > 0 {
		if f.Attributes, err = json.Marshal(attrs); err != nil {
			return nil, "", err
		}
	}
	f, ok := registry.FeedBeforeInsert.Call(ctx, f)
	if !ok {
		return nil, warning, nil
	}
	if err := tx.CreateFeed(ctx, f); err != nil {
		return nil, "", err
	}
	return f, warning, nil
}
