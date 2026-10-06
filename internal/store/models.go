package store

import "encoding/json"

// DefaultCategoryID is the category every user has and that cannot be
// deleted; feeds without a category of their own live in it.
const DefaultCategoryID = 1

// DefaultCategoryName is the name the default category gets on creation.
const DefaultCategoryName = "Uncategorized"

// Times are Unix seconds, zero means "never" or "unknown".
// Attributes and Settings are JSON objects; nil is stored as {}.

// User is an account. Identifiers of categories, feeds, tags and entries are
// unique within a user, not across the database.
type User struct {
	ID   int64
	Name string
	// APIPasswordHash is the bcrypt hash of the Google Reader API password,
	// empty while API access is not set up.
	APIPasswordHash string
	Settings        json.RawMessage
}

// Category groups feeds.
type Category struct {
	UserID     int64
	ID         int64
	Name       string
	Kind       int
	LastUpdate int64
	Error      int64
	Attributes json.RawMessage
}

// Feed is a subscription.
type Feed struct {
	UserID      int64
	ID          int64
	URL         string
	Kind        int
	CategoryID  int64
	Name        string
	Website     string
	Description string
	LastUpdate  int64
	Priority    int
	// PathEntries is the CSS selector of the full article text, empty when
	// full-text retrieval is off.
	PathEntries string
	// HTTPAuth is "user:password" for HTTP basic authentication.
	HTTPAuth string
	// Error is the time of the last failed refresh, zero if the last one succeeded.
	Error int64
	// TTL is the refresh period in seconds; zero stands for the user's
	// default. A negative value is a muted feed: it is not refreshed, and
	// the period it had is the absolute value.
	TTL        int
	Attributes json.RawMessage
	// HTTPETag and HTTPLastModified are the validators of the copy fetched
	// last, sent back so that an unchanged feed is answered with 304.
	HTTPETag         string
	HTTPLastModified string
}

// Entry is an article of a feed.
type Entry struct {
	UserID int64
	// ID grows with insertion time: it is a Unix time in microseconds, made
	// strictly increasing within a user.
	ID     int64
	FeedID int64
	// GUID is an opaque key, unique within the feed. It is kept in the form
	// FreshRSS computes it and is never decoded or shown.
	GUID             string
	Title            string
	Authors          []string
	Content          string
	Link             string
	Published        int64
	LastSeen         int64
	LastModified     int64
	LastUserModified int64
	// Hash detects changes of the entry in the feed. Nil means it has not been
	// computed yet: the next refresh fills it in without treating the entry as
	// modified.
	Hash       []byte
	IsRead     bool
	IsFavorite bool
	// Tags are the categories the feed assigned to the entry, not user labels.
	Tags       []string
	Attributes json.RawMessage
}

// EntryState is what a refresh needs to know about a stored entry to decide
// whether the feed has changed it.
type EntryState struct {
	ID int64
	// Hash is nil while it has not been computed, see Entry.Hash.
	Hash             []byte
	IsRead           bool
	IsFavorite       bool
	LastUserModified int64
}

// EntryKey is what tells entries apart for a reader: the identifier within
// the feed and the title.
type EntryKey struct {
	GUID  string
	Title string
}

// Tag is a user label that can be attached to entries.
type Tag struct {
	UserID     int64
	ID         int64
	Name       string
	Attributes json.RawMessage
}

// jsonObject returns the stored form of an attributes value.
func jsonObject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// jsonStrings returns the stored form of a string list.
func jsonStrings(list []string) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(list)
	return string(b), err
}

func parseStrings(stored string) ([]string, error) {
	var list []string
	if err := json.Unmarshal([]byte(stored), &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return list, nil
}
