package greader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/juev/freshgo/internal/mediaproxy"
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

// streamChunk is how many entries a listing reads at once. The answer is
// written as the chunks come, so neither the number of entries nor the size
// of their texts decides the memory a request takes.
const streamChunk = 100

func (h *Handler) streamContents(ctx context.Context, q *request, s stream) error {
	p := q.page(s, q.get("it"))
	var (
		list   *itemList
		source *itemSource
	)
	if !s.empty {
		var (
			limited = p.query.Limit > 0
			cursor  = p.query.From
			left    = p.query.Limit // zero or less: as many as there are
			drop    = p.continued
		)
		for {
			query := p.query
			query.From = cursor
			query.Limit = streamChunk
			if limited && left < query.Limit {
				query.Limit = left
			}
			entries, err := h.db.ListEntries(ctx, q.user.ID, query)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				break
			}
			// What an item needs besides its entry is asked for once, and
			// the answer begins only when the first chunk is ready.
			if source == nil {
				if source, err = h.itemSource(q); err != nil {
					return err
				}
			}
			items, err := source.items(entries)
			if err != nil {
				return err
			}
			if list == nil {
				if list, err = h.newItemList(q); err != nil {
					return err
				}
			}
			for _, it := range items {
				if drop {
					drop = false
					continue
				}
				if err := list.add(it); err != nil {
					return err
				}
			}
			last := entries[len(entries)-1].ID
			if p.query.Ascending {
				cursor = last + 1
			} else {
				if last <= 1 {
					// No entry has a smaller identifier, and zero would
					// mean "from the beginning" to the query.
					break
				}
				cursor = last - 1
			}
			if limited {
				left -= len(entries)
				if left <= 0 {
					break
				}
			}
			if len(entries) < query.Limit {
				break
			}
			// A client that has gone away is seen before more entries are
			// read for it.
			if err := flush(q.w); err != nil {
				return err
			}
		}
	}
	if list == nil {
		var err error
		if list, err = h.newItemList(q); err != nil {
			return err
		}
	}
	return list.finish(continuationOf(list, p))
}

// continuationOf returns the identifier of the last item of a full page.
func continuationOf(list *itemList, p page) string {
	if list.count >= p.count && list.count > 0 {
		return list.last
	}
	return ""
}

// itemSource is what every chunk of a listing shares: the library of the
// user and the rewrite of images through the server.
type itemSource struct {
	h       *Handler
	q       *request
	lib     *library
	through func(content string) string
}

// itemSource asks for what the items need besides the entries themselves.
func (h *Handler) itemSource(q *request) (*itemSource, error) {
	through, err := h.throughServer(q)
	if err != nil {
		return nil, err
	}
	lib, err := h.library(q.r.Context(), q.user)
	if err != nil {
		return nil, err
	}
	return &itemSource{h: h, q: q, lib: lib, through: through}, nil
}

// items shows the entries of one chunk, leaving out those an extension holds
// back.
func (src *itemSource) items(entries []*store.Entry) ([]item, error) {
	ctx, u := src.q.r.Context(), src.q.user
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := src.h.db.EntryLabels(ctx, u.ID, ids)
	if err != nil {
		return nil, err
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		e, ok := src.h.hooks.EntryBeforeDisplay.Call(ctx, e)
		if !ok {
			continue
		}
		f := src.lib.byID[e.FeedID]
		if f == nil {
			continue
		}
		it := newItem(e, f, src.lib.category[f.CategoryID].Name, labels[e.ID])
		it.Summary.Content = src.through(it.Summary.Content)
		items = append(items, it)
	}
	return items, nil
}

// items shows entries, leaving out those an extension holds back.
func (h *Handler) items(q *request, entries []*store.Entry) ([]item, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	source, err := h.itemSource(q)
	if err != nil {
		return nil, err
	}
	return source.items(entries)
}

// throughServer returns what puts the images of an entry behind the public
// address of the server, as the installation is set.
func (h *Handler) throughServer(q *request) (func(content string) string, error) {
	ctx := q.r.Context()
	asIs := func(content string) string { return content }
	if !h.media {
		return asIs, nil
	}
	system, err := h.db.System(ctx)
	if err != nil || (system.MediaProxy != mediaproxy.ModeHTTPOnly && system.MediaProxy != mediaproxy.ModeAll) {
		return asIs, err
	}
	salt, err := h.db.Salt(ctx)
	if err != nil {
		return nil, err
	}
	key, base := mediaproxy.Key(salt), h.base(q.r)
	return func(content string) string {
		return mediaproxy.Rewrite(content, system.MediaProxy, func(target string) string { return mediaproxy.Address(key, base, target) })
	}, nil
}

// writeItems answers with a list of items known whole. Every list calls
// itself the reading list.
func (h *Handler) writeItems(q *request, items []item, continuation string) error {
	list, err := h.newItemList(q)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := list.add(it); err != nil {
			return err
		}
	}
	return list.finish(continuation)
}

// itemList writes a list of items in the layout of FreshRSS: one item per
// line. Each item goes out as it is added, so the answer is never held whole.
type itemList struct {
	w     io.Writer
	enc   *json.Encoder
	buf   bytes.Buffer
	count int
	// last is the timestamp of the last item written: the continuation of a
	// full page.
	last string
}

// newItemList starts the answer and returns what writes its items.
func (h *Handler) newItemList(q *request) (*itemList, error) {
	list := &itemList{w: q.w}
	list.enc = json.NewEncoder(&list.buf)
	list.enc.SetEscapeHTML(false)
	q.wrote = true
	q.w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	if _, err := fmt.Fprintf(list.w, "{\n\t\"id\": %q,\n\t\"updated\": %d,\n\t\"items\": [\n", stateReadingList, h.now().Unix()); err != nil {
		return nil, err
	}
	return list, nil
}

// add writes one item.
func (l *itemList) add(it item) error {
	l.buf.Reset()
	if err := l.enc.Encode(it); err != nil {
		return err
	}
	if l.count > 0 {
		if _, err := io.WriteString(l.w, ",\n"); err != nil {
			return err
		}
	}
	line := l.buf.Bytes()
	if _, err := l.w.Write(line[:len(line)-1]); err != nil { // the line break of the encoder
		return err
	}
	l.count++
	l.last = it.TimestampUsec
	return nil
}

// finish writes the end of the answer.
func (l *itemList) finish(continuation string) error {
	end := "\n\t]"
	if continuation != "" {
		end += fmt.Sprintf(",\n\t\"continuation\": %q", continuation)
	}
	_, err := io.WriteString(l.w, end+"\n}\n")
	return err
}

// flush sends what has been written so far, so that a client that has gone
// away is seen before more work is done for it.
func flush(w http.ResponseWriter) error {
	err := http.NewResponseController(w).Flush()
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
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
	items, err := h.items(q, entries)
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
