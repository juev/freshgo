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
	"sync"
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
	// attrs are the attributes records get from With, as text, and user the
	// user among them.
	attrs string
	user  string
	// out is where records are kept; handlers made by With share it.
	out *writer
}

// writer keeps records behind the back of whoever logs them: a record is
// often logged inside a transaction, and one more write to the database
// from there would wait for that transaction to end.
type writer struct {
	db  *store.Store
	now func() time.Time
	// records are the records waiting to be kept; one that finds the queue
	// full is dropped, and is still in the log of the server.
	records chan store.Log
	done    chan struct{}
	mu      sync.RWMutex
	closed  bool
}

// queued is how many records may wait to be kept.
const queued = 256

// New returns a handler in front of next. Close has to be called for the
// records still waiting to be kept.
func New(next slog.Handler, db *store.Store) *Handler {
	w := &writer{db: db, now: time.Now, records: make(chan store.Log, queued), done: make(chan struct{})}
	go w.run()
	return &Handler{next: next, out: w}
}

// run keeps the records as they come, until the queue is closed.
func (w *writer) run() {
	defer close(w.done)
	var pruned time.Time
	for record := range w.records {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		// A record that cannot be kept is in the log of the server.
		_ = w.db.AddLog(ctx, &record)
		if now := w.now(); now.Sub(pruned) >= pruneEvery {
			pruned = now
			_ = w.db.DeleteLogsBefore(ctx, now.Add(-Kept).Unix())
		}
		cancel()
	}
}

// Close keeps the records that are waiting and stops keeping new ones.
func (h *Handler) Close() {
	w := h.out
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.records)
	}
	w.mu.Unlock()
	<-w.done
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
	w := h.out
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.closed {
		select {
		case w.records <- store.Log{Time: w.now().Unix(), Level: level, User: user, Message: message}:
		default:
		}
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
