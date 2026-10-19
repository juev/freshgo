package web

import (
	"io"
	"net/http"
	"time"

	"github.com/juev/freshgo/internal/events"
)

// eventsPath is where an open page listens for changes of the entries of its
// user, as server-sent events.
const eventsPath = "/events"

// heartbeat is how often a stream with nothing to tell says it is there,
// so that a proxy does not take it for idle and close it.
const heartbeat = 30 * time.Second

// eventStream tells the page of a logged-in user that the counts of the
// tree changed ("tree") or that entries came ("entries"), and then the page
// asks for what it shows again. Nothing of the entries goes over it. It ends
// with the login it was opened under, which it looks at with every event
// and every heartbeat.
func (h *Handler) eventStream(w http.ResponseWriter, r *http.Request) {
	s := state(r)
	who := s.who
	if who == nil || who.anonymous || unconfirmed(s) {
		h.fail(w, r, http.StatusForbidden)
		return
	}
	sub := h.events.Subscribe(r.Context(), who.user.ID)
	defer sub.Cancel()
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	// nginx would hold the events back to send them in a batch.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)
	send := func(text string) bool {
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		return flusher.Flush() == nil
	}
	// The browser takes the stream for open once something comes.
	if !send(": open\n\n") {
		return
	}
	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()
	for {
		var text string
		select {
		case <-sub.Done():
			return
		case <-beat.C:
			text = ": heartbeat\n\n"
		case <-sub.Ready():
			kind := sub.Take()
			switch {
			case kind&events.Entries != 0:
				text = "event: entries\ndata: entries\n\n"
			case kind&events.Tree != 0:
				text = "event: tree\ndata: tree\n\n"
			}
		}
		if !h.stillLoggedIn(r, who) {
			return
		}
		if text != "" && !send(text) {
			return
		}
	}
}

// stillLoggedIn reports whether the request is still made as who: the same
// user, enabled, under the same login.
func (h *Handler) stillLoggedIn(r *http.Request, who *identity) bool {
	system, err := h.db.System(r.Context())
	if err != nil {
		return false
	}
	now, err := h.identify(r, system)
	if err != nil || now == nil || now.anonymous || now.user.ID != who.user.ID {
		return false
	}
	if who.session == nil {
		return now.session == nil
	}
	return now.session != nil && now.session.TokenHash == who.session.TokenHash
}

// loginsEnded tells the streams of a user that one or more of their logins
// ended, so that the streams opened under them end now.
func (h *Handler) loginsEnded(userID int64) {
	if h.events != nil {
		h.events.Publish(userID, events.Session)
	}
}
