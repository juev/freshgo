package greader

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/hooks"
	"github.com/juev/freshgo/internal/scrape"
	"github.com/juev/freshgo/internal/store"
)

// maxNameBytes is how long the name of a category or of a label may be:
// FreshRSS cuts names to the length its indexes take.
const maxNameBytes = 191

// cleanName is a name as it is stored.
func cleanName(name string) string {
	return cutBytes(phpTrim(name), maxNameBytes)
}

// labelName returns the name in user/-/label/<name>, or in
// user/<user>/label/<name> when own is set and the user is the one asking.
func (q *request) labelName(stream string, own bool) (string, bool) {
	if name, ok := strings.CutPrefix(stream, labelPrefix); ok {
		return name, true
	}
	if own {
		return strings.CutPrefix(stream, "user/"+q.user.Name+"/label/")
	}
	return "", false
}

// editTag marks entries read or starred and puts labels on them, or the
// reverse: "a" adds and "r" removes, both for all the entries of "i".
func (h *Handler) editTag(ctx context.Context, q *request) error {
	ids := entryIDs(q.posted("i"))
	now := h.now().Unix()
	setRead := func(read bool) error {
		n, err := h.db.SetEntriesRead(ctx, q.user.ID, ids, read, now)
		if err == nil && n > 0 {
			h.hooks.EntriesRead.Call(ctx, hooks.EntriesRead{UserID: q.user.ID, IDs: ids, IsRead: read})
		}
		return err
	}
	setFavorite := func(favorite bool) error {
		err := h.db.SetEntriesFavorite(ctx, q.user.ID, ids, favorite, now)
		if err == nil {
			h.hooks.EntriesFavorite.Call(ctx, hooks.EntriesFavorite{UserID: q.user.ID, IDs: ids, IsFavorite: favorite})
		}
		return err
	}

	for _, add := range q.posted("a") {
		var err error
		switch add {
		case stateRead:
			err = setRead(true)
		case stateStarred:
			err = setFavorite(true)
		default:
			// Other states of Google Reader have no counterpart here.
			if name, ok := q.labelName(add, true); ok && name != "" {
				err = h.addLabel(ctx, q.user.ID, cleanName(name), ids)
			}
		}
		if err != nil {
			return err
		}
	}
	for _, remove := range q.posted("r") {
		var err error
		switch remove {
		case stateRead:
			err = setRead(false)
		case stateStarred:
			err = setFavorite(false)
		default:
			if name, ok := q.labelName(remove, false); ok {
				err = h.removeLabel(ctx, q.user.ID, name, ids)
			}
		}
		if err != nil {
			return err
		}
	}
	done(q.w)
	return nil
}

// addLabel puts the label on the entries, creating it first when the user
// has none of that name. A name a category has is left alone: the label
// cannot exist.
func (h *Handler) addLabel(ctx context.Context, userID int64, name string, ids []int64) error {
	return h.db.InTx(ctx, func(tx *store.Store) error {
		tags, err := tx.Tags(ctx, userID)
		if err != nil {
			return err
		}
		tag := labelNamed(tags, name)
		if tag == nil {
			tag = &store.Tag{UserID: userID, Name: name}
			if err := tx.CreateTag(ctx, tag); errors.Is(err, store.ErrConflict) {
				return nil
			} else if err != nil {
				return err
			}
		}
		return tx.TagEntries(ctx, userID, tag.ID, ids)
	})
}

func (h *Handler) removeLabel(ctx context.Context, userID int64, name string, ids []int64) error {
	tags, err := h.db.Tags(ctx, userID)
	if err != nil {
		return err
	}
	if tag := labelNamed(tags, name); tag != nil {
		return h.db.UntagEntries(ctx, userID, tag.ID, ids)
	}
	return nil
}

// markAllAsRead marks a whole stream read, up to the entry "ts" names.
func (h *Handler) markAllAsRead(ctx context.Context, q *request) error {
	// "ts" is compared with entry identifiers; without it everything there
	// is now counts.
	maxID := h.now().UnixMicro()
	if _, sent := q.form["ts"]; sent {
		ts := strings.TrimSpace(q.post("ts"))
		if ts == "" || strings.Trim(ts, "0123456789") != "" {
			return errBadRequest
		}
		if n, err := strconv.ParseInt(ts, 10, 64); err != nil {
			maxID = math.MaxInt64
		} else if n != 0 {
			maxID = n
		}
	}

	var set store.EntrySet
	switch id := strings.TrimSpace(q.post("s")); {
	case strings.HasPrefix(id, feedPrefix):
		feed, ok := numeric(id[strings.LastIndexByte(id, '/')+1:])
		if !ok {
			return errBadRequest
		}
		if feed == 0 {
			done(q.w)
			return nil
		}
		set.FeedID = feed
	case strings.HasPrefix(id, labelPrefix):
		s, err := h.labelStream(ctx, q.user, strings.TrimPrefix(id, labelPrefix))
		if err != nil {
			return err
		}
		if s.empty {
			return errBadRequest
		}
		set = s.set
		if set.CategoryID != 0 {
			// FreshRSS leaves the important feeds of a category out: they
			// have a view of their own to be read in.
			set.BelowPriority = ptr(priorityImportant)
		}
	case id == stateReadingList, id == stateUnread:
		set.MinPriority = ptr(priorityHidden + 1)
	case id == stateStarred:
		set = streamStarred.set
	case id == stateMain:
		set = streamMain.set
	case id == stateImportant:
		set = streamImportant.set
	case id == stateRead:
		// Nothing read is left to be marked.
		done(q.w)
		return nil
	default:
		return errBadRequest
	}
	if _, err := h.db.MarkSetRead(ctx, q.user.ID, set, maxID, h.now().Unix()); err != nil {
		return err
	}
	done(q.w)
	return nil
}

// renameTag renames user/-/label/<name>: the category of that name or, when
// there is none, the label. A name that is taken changes nothing, and the
// answer is still OK, as in FreshRSS.
func (h *Handler) renameTag(ctx context.Context, q *request) error {
	from, okFrom := q.labelName(strings.TrimSpace(q.post("s")), false)
	to, okTo := q.labelName(strings.TrimSpace(q.post("dest")), false)
	if !okFrom || !okTo {
		return errBadRequest
	}
	// FreshRSS would store an empty name; nothing could address it afterwards.
	if to = cleanName(to); to == "" {
		return errBadRequest
	}
	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}
	if c := lib.categoryNamed(from); c != nil {
		renamed := *c
		renamed.Name = to
		err = h.db.UpdateCategory(ctx, &renamed)
	} else {
		var tags []*store.Tag
		if tags, err = h.db.Tags(ctx, q.user.ID); err != nil {
			return err
		}
		tag := labelNamed(tags, from)
		if tag == nil {
			return errBadRequest
		}
		err = h.db.RenameTag(ctx, q.user.ID, tag.ID, to)
	}
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	done(q.w)
	return nil
}

// disableTag deletes user/-/label/<name>: a category gives its feeds to the
// default one, a label comes off its entries. Only the first "s" counts.
func (h *Handler) disableTag(ctx context.Context, q *request) error {
	streams := q.posted("s")
	if len(streams) == 0 {
		return errBadRequest
	}
	name, ok := q.labelName(streams[0], false)
	if !ok {
		return errBadRequest
	}
	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}
	if c := lib.categoryNamed(name); c != nil {
		err = h.db.DeleteCategory(ctx, q.user.ID, c.ID)
	} else {
		var tags []*store.Tag
		if tags, err = h.db.Tags(ctx, q.user.ID); err != nil {
			return err
		}
		tag := labelNamed(tags, name)
		if tag == nil {
			return errBadRequest
		}
		err = h.db.DeleteTag(ctx, q.user.ID, tag.ID)
	}
	if err != nil {
		return err
	}
	done(q.w)
	return nil
}

// jsonInAddress is how FreshRSS tells a JSON Feed from RSS and Atom when a
// feed is added: the API has no way to say which it is.
var jsonInAddress = regexp.MustCompile(`(?i)(?:\b|_)json(?:\b|_)`)

// KindOf returns the kind a feed added by its address alone is taken for.
func KindOf(address string) int {
	if jsonInAddress.MatchString(address) {
		return scrape.KindJSONFeed
	}
	return 0
}

var feedPrefixes = regexp.MustCompile(`^(feed/)+`)

// subscriptionEdit subscribes to, unsubscribes from and edits the feeds of
// "s": "ac" is the action, "t" the titles, "a" the category to move to.
func (h *Handler) subscriptionEdit(ctx context.Context, q *request) error {
	// A single stream and title may come in the query, several in the body.
	streams, titles := q.posted("s"), q.posted("t")
	if q.post("s") == "" && q.get("s") != "" {
		streams = []string{q.get("s")}
	}
	if q.post("t") == "" && q.get("t") != "" {
		titles = []string{q.get("t")}
	}
	action := q.either("ac")
	if len(streams) == 0 || (action != "subscribe" && action != "unsubscribe" && action != "edit") {
		return errBadRequest
	}
	lib, err := h.library(ctx, q.user)
	if err != nil {
		return err
	}

	// A feed lives in one category: the one to add it to or, when it is
	// only taken out of one, the default.
	var categoryID int64
	if add := q.either("a"); strings.HasPrefix(add, "user/") {
		name, _ := q.labelName(add, true)
		name = cleanName(name)
		switch c := lib.categoryNamed(name); {
		case name == "" || name == store.DefaultCategoryName:
			categoryID = store.DefaultCategoryID
		case c != nil:
			categoryID = c.ID
		case q.system.Limits.MaxCategories > 0 && len(lib.categories) >= q.system.Limits.MaxCategories:
			// One category too many: the feed goes to the default one.
			categoryID = store.DefaultCategoryID
		default:
			created := &store.Category{UserID: q.user.ID, Name: name}
			switch err := h.db.CreateCategory(ctx, created); {
			case errors.Is(err, store.ErrConflict):
				categoryID = store.DefaultCategoryID
			case err != nil:
				return err
			default:
				categoryID = created.ID
			}
		}
	} else if strings.HasPrefix(q.either("r"), labelPrefix) {
		categoryID = store.DefaultCategoryID
	}

	for i := len(streams) - 1; i >= 0; i-- {
		if !strings.HasPrefix(streams[i], feedPrefix) {
			continue
		}
		address := feedPrefixes.ReplaceAllString(streams[i], "")
		var feed *store.Feed
		if id, ok := numeric(address); ok {
			if action == "subscribe" {
				continue
			}
			feed = lib.byID[id]
		} else {
			feed = lib.feedAt(address)
		}
		title := ""
		if i < len(titles) {
			title = titles[i]
		}
		switch action {
		case "subscribe":
			if feed != nil {
				return errBadRequest
			}
			added := &store.Feed{
				URL: address, Name: phpTrim(title), CategoryID: categoryID, Kind: KindOf(address), Priority: priorityMain,
			}
			if err := h.refresher.AddFeed(ctx, q.user, added); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				h.log.Warn("subscription failed", "user", q.user.Name, "error", err)
				return errBadRequest
			}
		case "unsubscribe":
			if feed == nil {
				return errBadRequest
			}
			// Gone already when the request names the feed twice.
			if err := h.db.DeleteFeed(ctx, q.user.ID, feed.ID); errors.Is(err, store.ErrNotFound) {
				return errBadRequest
			} else if err != nil {
				return err
			}
		case "edit":
			if feed == nil {
				// FreshRSS looks a feed up by its address, and takes a
				// number for a feed without looking.
				if _, byNumber := numeric(address); byNumber {
					continue
				}
				return errBadRequest
			}
			edited := *feed
			if categoryID > 0 {
				edited.CategoryID = categoryID
			}
			if title != "" {
				edited.Name = title
			}
			if err := h.editFeed(ctx, &edited); errors.Is(err, store.ErrNotFound) {
				return errBadRequest
			} else if err != nil {
				return err
			}
		}
	}
	done(q.w)
	return nil
}

// editFeed stores the name and the category of a feed, and nothing else: a
// refresh may be writing the rest at the same time.
func (h *Handler) editFeed(ctx context.Context, edited *store.Feed) error {
	return h.db.InTx(ctx, func(tx *store.Store) error {
		fresh, err := tx.LockFeed(ctx, edited.UserID, edited.ID)
		if err != nil {
			return err
		}
		fresh.Name, fresh.CategoryID = edited.Name, edited.CategoryID
		return tx.UpdateFeed(ctx, fresh)
	})
}

// quickAdd subscribes to the feed at the address of "quickadd".
func (h *Handler) quickAdd(ctx context.Context, q *request) error {
	address := strings.TrimPrefix(q.either("quickadd"), feedPrefix)
	feed := &store.Feed{URL: address, Kind: KindOf(address), Priority: priorityMain}
	if err := h.refresher.AddFeed(ctx, q.user, feed); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h.log.Warn("quickadd failed", "user", q.user.Name, "error", err)
		return writeJSON(q.w, struct {
			NumResults int    `json:"numResults"`
			Error      string `json:"error"`
		}{0, err.Error()})
	}
	return writeJSON(q.w, struct {
		NumResults int    `json:"numResults"`
		Query      string `json:"query"`
		StreamID   string `json:"streamId"`
		StreamName string `json:"streamName"`
	}{1, feed.URL, feedPrefix + strconv.FormatInt(feed.ID, 10), feedName(feed)})
}
