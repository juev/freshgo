package greader

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/store"
)

// stream is a set of entries a path or a stream identifier names. One that
// names nothing the user has is empty.
type stream struct {
	set   store.EntrySet
	empty bool
}

func ptr[T any](v T) *T { return &v }

// Streams by the priority of the feed.
var (
	streamReadingList = stream{set: store.EntrySet{MinPriority: ptr(priorityCategory)}}
	streamMain        = stream{set: store.EntrySet{MinPriority: ptr(priorityMain)}}
	streamImportant   = stream{set: store.EntrySet{MinPriority: ptr(priorityImportant)}}
	streamStarred     = stream{set: store.EntrySet{MinPriority: ptr(priorityHidden + 1), OnlyFavorite: true}}
)

// feedStream is the stream of the feed with the given identifier or, when
// that is not a number, with the given address.
func (h *Handler) feedStream(ctx context.Context, u *store.User, feed string) (stream, error) {
	if feed == "" {
		return stream{empty: true}, nil
	}
	if id, ok := numeric(feed); ok {
		if id == 0 {
			return stream{empty: true}, nil
		}
		return stream{set: store.EntrySet{FeedID: id}}, nil
	}
	lib, err := h.library(ctx, u)
	if err != nil {
		return stream{}, err
	}
	if f := lib.feedAt(feed); f != nil {
		return stream{set: store.EntrySet{FeedID: f.ID}}, nil
	}
	return stream{empty: true}, nil
}

// numeric reads what PHP takes for an integer in a stream identifier.
func numeric(s string) (int64, bool) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return int64(n), true
}

// labelStream is the stream of user/-/label/<name>: the category of that
// name or, when there is none, the label of that name.
func (h *Handler) labelStream(ctx context.Context, u *store.User, name string) (stream, error) {
	lib, err := h.library(ctx, u)
	if err != nil {
		return stream{}, err
	}
	if c := lib.categoryNamed(name); c != nil {
		return stream{set: store.EntrySet{CategoryID: c.ID, MinPriority: ptr(priorityCategory)}}, nil
	}
	tags, err := h.db.Tags(ctx, u.ID)
	if err != nil {
		return stream{}, err
	}
	if t := labelNamed(tags, name); t != nil {
		return stream{set: store.EntrySet{LabelID: t.ID}}, nil
	}
	return stream{empty: true}, nil
}

// Entry states of FreshRSS, as bits.
const (
	bitRead        = 1
	bitNotRead     = 2
	bitAll         = bitRead | bitNotRead
	bitFavorite    = 4
	bitNotFavorite = 8
)

// states turns the "it" (include) and "xt" (exclude) targets into
// conditions on the read and starred state. The targets are combined as
// FreshRSS combines them, bit by bit, and a combination that leaves no bit
// set selects everything: "starred, but not read" lists read and unread,
// starred or not.
func states(include, exclude string) (read, favorite *bool) {
	state := bitAll
	switch include {
	case stateRead:
		state = bitRead
	case stateUnread:
		state = bitNotRead
	case stateStarred:
		state = bitFavorite
	}
	switch exclude {
	case stateRead:
		state &= bitNotRead
	case stateUnread:
		state &= bitRead
	case stateStarred:
		state &= bitNotFavorite
	}
	switch state & bitAll {
	case bitRead:
		read = ptr(true)
	case bitNotRead:
		read = ptr(false)
	}
	switch state & (bitFavorite | bitNotFavorite) {
	case bitFavorite:
		favorite = ptr(true)
	case bitNotFavorite:
		favorite = ptr(false)
	}
	return read, favorite
}

// page is the part of a stream a request asks for.
type page struct {
	query store.EntryQuery
	// count is how many entries were asked for; continued says the request
	// goes on from where another one stopped.
	count     int
	continued bool
}

// page reads the parameters all listings share: n, r, ot, nt, c, it, xt.
func (q *request) page(s stream, include string) page {
	p := page{count: int(q.number("n", 20))}
	p.query.Set = s.set
	p.query.Read, p.query.Favorite = states(include, q.get("xt"))
	p.query.Since, p.query.Until = q.number("ot", 0), q.number("nt", 0)
	p.query.Ascending = q.get("r") == "o"
	// The continuation is the last identifier of the page before: the next
	// page starts with that entry once more, and drops it.
	if c := strings.TrimSpace(q.get("c")); c != "" && strings.Trim(c, "0123456789") == "" {
		p.query.From, _ = strconv.ParseInt(c, 10, 64)
	}
	p.continued = p.query.From != 0
	p.query.Limit = p.count
	if p.continued {
		p.query.Limit++
	}
	return p
}

// Addresses of feeds and names of labels may contain slashes, so they are
// taken from the path as it was sent, where a slash of theirs is encoded.
var (
	feedInPath  = regexp.MustCompile(`/reader/api/0/stream/contents/feed/([A-Za-z0-9'!*()%$_.~+-]+)`)
	labelInPath = regexp.MustCompile(`/reader/api/0/stream/contents/user/[^/+]/label/([A-Za-z0-9'!*()%$_.~+-]+)`)
)

func unescapedMatch(re *regexp.Regexp, uri string) (string, bool) {
	m := re.FindStringSubmatch(uri)
	if m == nil {
		return "", false
	}
	value, err := url.QueryUnescape(m[1])
	if err != nil {
		return m[1], true
	}
	return value, true
}

// stream answers the endpoints under /reader/api/0/stream.
func (h *Handler) stream(ctx context.Context, q *request) error {
	parts := q.parts
	part := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	switch part(5) {
	case "contents":
		// BazQux names the stream in a parameter instead of the path.
		if len(parts) == 6 && q.has("s") {
			parts = append(parts[:6:6], strings.Split(q.get("s"), "/")...)
		}
		if len(parts) < 8 {
			// EasyRSS and FeedMe ask for no stream at all.
			return h.streamContents(ctx, q, streamReadingList)
		}
		switch {
		case part(6) == "feed":
			feed := part(7)
			if _, isNumber := numeric(feed); feed != "" && !isNumber {
				feed, _ = unescapedMatch(feedInPath, q.r.RequestURI)
			}
			s, err := h.feedStream(ctx, q.user, feed)
			if err != nil {
				return err
			}
			return h.streamContents(ctx, q, s)
		case part(6) == "user" && len(parts) >= 10 && part(8) == "state":
			if (part(9) != "com.google" && part(9) != "org.freshrss") || len(parts) < 11 {
				return errBadRequest
			}
			switch part(10) {
			case "reading-list":
				return h.streamContents(ctx, q, streamReadingList)
			case "starred":
				return h.streamContents(ctx, q, streamStarred)
			case "main":
				return h.streamContents(ctx, q, streamMain)
			case "important":
				return h.streamContents(ctx, q, streamImportant)
			}
		case part(6) == "user" && len(parts) >= 10 && part(8) == "label":
			name, ok := unescapedMatch(labelInPath, q.r.RequestURI)
			if !ok {
				name = part(9)
			}
			s, err := h.labelStream(ctx, q.user, name)
			if err != nil {
				return err
			}
			return h.streamContents(ctx, q, s)
		}
	case "items":
		switch {
		case part(6) == "ids" && q.has("s"):
			return h.streamItemIDs(ctx, q)
		case part(6) == "contents" && len(q.form["i"]) > 0: // FeedMe
			return h.streamItems(ctx, q)
		}
	}
	return errBadRequest
}

func (h *Handler) streamContents(ctx context.Context, q *request, s stream) error {
	p := q.page(s, q.get("it"))
	var entries []*store.Entry
	if !s.empty {
		var err error
		if entries, err = h.db.ListEntries(ctx, q.user.ID, p.query); err != nil {
			return err
		}
	}
	items, err := h.items(ctx, q.user, entries)
	if err != nil {
		return err
	}
	if p.continued && len(items) > 0 {
		items = items[1:]
	}
	continuation := ""
	if len(items) >= p.count && len(items) > 0 {
		continuation = items[len(items)-1].TimestampUsec
	}
	return h.writeItems(q, items, continuation)
}

// items shows entries, leaving out those an extension holds back.
func (h *Handler) items(ctx context.Context, u *store.User, entries []*store.Entry) ([]item, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	lib, err := h.library(ctx, u)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := h.db.EntryLabels(ctx, u.ID, ids)
	if err != nil {
		return nil, err
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		e, ok := h.hooks.EntryBeforeDisplay.Call(ctx, e)
		if !ok {
			continue
		}
		f := lib.byID[e.FeedID]
		if f == nil {
			continue
		}
		items = append(items, newItem(e, f, lib.category[f.CategoryID].Name, labels[e.ID]))
	}
	return items, nil
}

// writeItems answers with a list of items in the layout of FreshRSS: one
// item per line. Every list calls itself the reading list.
func (h *Handler) writeItems(q *request, items []item, continuation string) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "{\n\t\"id\": %q,\n\t\"updated\": %d,\n\t\"items\": [\n", stateReadingList, h.now().Unix())
	for i, it := range items {
		if i > 0 {
			b.WriteString(",\n")
		}
		if err := encode(&b, it); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // the line break of the encoder
	}
	b.WriteString("\n\t]")
	if continuation != "" {
		fmt.Fprintf(&b, ",\n\t\"continuation\": %q", continuation)
	}
	b.WriteString("\n}\n")
	q.w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_, err := q.w.Write(b.Bytes())
	return err
}

func (h *Handler) streamItemIDs(ctx context.Context, q *request) error {
	var (
		s       = streamReadingList
		include = q.get("it")
		err     error
	)
	switch id := q.get("s"); {
	case id == stateStarred:
		s = streamStarred
	case id == stateMain:
		s = streamMain
	case id == stateImportant:
		s = streamImportant
	case id == stateRead, id == stateUnread:
		include = id
	case strings.HasPrefix(id, feedPrefix):
		s, err = h.feedStream(ctx, q.user, strings.TrimPrefix(id, feedPrefix))
	case strings.HasPrefix(id, labelPrefix):
		s, err = h.labelStream(ctx, q.user, strings.TrimPrefix(id, labelPrefix))
	}
	if err != nil {
		return err
	}
	p := q.page(s, include)
	var ids []int64
	if !s.empty {
		if ids, err = h.db.ListEntryIDs(ctx, q.user.ID, p.query); err != nil {
			return err
		}
	}
	if p.continued && len(ids) > 0 {
		ids = ids[1:]
	}
	// News+ takes an empty list for a failure.
	if len(ids) == 0 && q.get("client") == "newsplus" {
		ids = []int64{0}
	}

	type ref struct {
		ID string `json:"id"`
	}
	response := struct {
		ItemRefs     []ref  `json:"itemRefs"`
		Continuation string `json:"continuation,omitempty"`
	}{ItemRefs: []ref{}}
	for _, id := range ids {
		response.ItemRefs = append(response.ItemRefs, ref{strconv.FormatInt(id, 10)})
	}
	if len(ids) >= p.count && len(ids) > 0 && ids[len(ids)-1] != 0 {
		response.Continuation = strconv.FormatInt(ids[len(ids)-1], 10)
	}
	return writeJSON(q.w, response)
}

func (h *Handler) streamItems(ctx context.Context, q *request) error {
	entries, err := h.db.EntriesByIDs(ctx, q.user.ID, entryIDs(q.posted("i")), q.get("r") == "o")
	if err != nil {
		return err
	}
	items, err := h.items(ctx, q.user, entries)
	if err != nil {
		return err
	}
	return h.writeItems(q, items, "")
}

// entryIDs reads item identifiers in either form: decimal, or hexadecimal
// with or without the tag:google.com prefix. A string of digits that does
// not start with a zero is decimal. What cannot be read is identifier 0,
// which no entry has.
func entryIDs(raw []string) []int64 {
	ids := make([]int64, len(raw))
	for i, s := range raw {
		if s != "" && s[0] != '0' && strings.Trim(s, "0123456789") == "" {
			ids[i], _ = strconv.ParseInt(s, 10, 64)
			continue
		}
		hex := s[strings.LastIndexByte(s, '/')+1:]
		if n, err := strconv.ParseUint(hex, 16, 63); err == nil {
			ids[i] = int64(n)
		}
	}
	return ids
}
