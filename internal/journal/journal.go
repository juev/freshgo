// Package journal keeps what went wrong for a user where the user can read
// it: warnings and errors the server logs with the attribute "user" go into
// the database as well, for the page of the web interface that shows them.
// See docs/specs/web.md.
package journal

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/juev/freshgo/internal/store"
)

const (
	// Kept is how long a record stays.
	Kept = 30 * 24 * time.Hour
	// pruneEvery is how often old records are cleared away.
	pruneEvery = time.Hour
	// writeTimeout bounds the write of one record.
	writeTimeout = 5 * time.Second
	// maxMessage bounds a record, in bytes.
	maxMessage = 4000
)

// userKey is the attribute that names the user a record is about.
const userKey = "user"

// Handler passes records on to another handler and keeps those that are
// warnings or errors about a user.
type Handler struct {
	next slog.Handler
	db   *store.Store
	now  func() time.Time
	// attrs are the attributes records get from With, as text, and user the
	// user among them.
	attrs string
	user  string
	// pruned is when old records were last cleared away, in Unix seconds;
	// handlers made by With share it.
	pruned *atomic.Int64
}

// New returns a handler in front of next.
func New(next slog.Handler, db *store.Store) *Handler {
	return &Handler{next: next, db: db, now: time.Now, pruned: &atomic.Int64{}}
}

// Enabled reports whether the handler in front of which this one stands
// takes records of the level.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// value spells an attribute the way the text log does.
func value(a slog.Attr) string {
	text := strings.ReplaceAll(a.Value.Resolve().String(), "\n", " ")
	if strings.ContainsAny(text, " \t\"=") {
		return a.Key + "=" + strconv.Quote(text)
	}
	return a.Key + "=" + text
}

// Handle passes the record on and keeps it when it is about a user.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	err := h.next.Handle(ctx, r)
	if r.Level < slog.LevelWarn {
		return err
	}
	user, parts := h.user, []string{r.Message}
	if h.attrs != "" {
		parts = append(parts, h.attrs)
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == userKey {
			user = a.Value.Resolve().String()
			return true
		}
		parts = append(parts, value(a))
		return true
	})
	if user == "" {
		return err
	}
	message := strings.Join(parts, " ")
	if len(message) > maxMessage {
		message = strings.ToValidUTF8(message[:maxMessage], "")
	}
	// PostgreSQL keeps no NUL, and text that is not UTF-8 neither.
	message = strings.ToValidUTF8(strings.ReplaceAll(message, "\x00", ""), "�")
	level := "WARN"
	if r.Level >= slog.LevelError {
		level = "ERROR"
	}
	// The request the record is about may be over: the record is kept all
	// the same. A record that cannot be kept is in the log of the server.
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	now := h.now()
	_ = h.db.AddLog(write, &store.Log{Time: now.Unix(), Level: level, User: user, Message: message})
	if last := h.pruned.Load(); now.Unix()-last >= int64(pruneEvery/time.Second) && h.pruned.CompareAndSwap(last, now.Unix()) {
		_ = h.db.DeleteLogsBefore(write, now.Add(-Kept).Unix())
	}
	return err
}

// WithAttrs returns a handler whose records have the attributes as well.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.next = h.next.WithAttrs(attrs)
	for _, a := range attrs {
		if a.Key == userKey {
			next.user = a.Value.Resolve().String()
			continue
		}
		next.attrs = strings.TrimSpace(next.attrs + " " + value(a))
	}
	return &next
}

// WithGroup returns a handler whose next one groups what follows; records
// are kept without the grouping.
func (h *Handler) WithGroup(name string) slog.Handler {
	next := *h
	next.next = h.next.WithGroup(name)
	return &next
}
