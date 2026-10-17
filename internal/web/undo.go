package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/juev/freshgo/internal/store"
)

// undoLife is how long the last destructive action of a user can be taken
// back.
const undoLife = 10 * time.Minute

// undoing keeps, by user, the last action that can be taken back. It is kept
// in memory: a restart forgets it, and the next such action of the user
// replaces it.
type undoing struct {
	mu   sync.Mutex
	last map[int64]*undoable
	// seq numbers the actions, so that a notice offers to take back its own
	// action and no other.
	seq uint64
}

// undoable is an action that can be taken back until expires.
type undoable struct {
	id      uint64
	expires time.Time
	undo    func(ctx context.Context) error
}

// put keeps undo as what takes back the last action of the user and returns
// its number.
func (u *undoing) put(userID int64, undo func(ctx context.Context) error, now time.Time) uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.last == nil {
		u.last = map[int64]*undoable{}
	}
	for id, a := range u.last {
		if !now.Before(a.expires) {
			delete(u.last, id)
		}
	}
	u.seq++
	u.last[userID] = &undoable{id: u.seq, expires: now.Add(undoLife), undo: undo}
	return u.seq
}

// offers reports whether the action numbered id can still be taken back.
func (u *undoing) offers(userID int64, id uint64, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	a := u.last[userID]
	return a != nil && a.id == id && now.Before(a.expires)
}

// take returns what takes back the last action of the user and forgets it;
// nil when there is none or its time is over.
func (u *undoing) take(userID int64, now time.Time) func(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	a := u.last[userID]
	delete(u.last, userID)
	if a == nil || !now.Before(a.expires) {
		return nil
	}
	return a.undo
}

// notifyUndo leaves a notice for the next page, as notify does, that offers
// to take back the action it is about with undo. A nil undo offers nothing.
func (h *Handler) notifyUndo(w http.ResponseWriter, r *http.Request, key string, n int, undo func(ctx context.Context) error) {
	if undo == nil {
		h.notify(w, r, key, n)
		return
	}
	id := h.undos.put(state(r).who.user.ID, undo, h.now())
	h.setNotice(w, r, key+":"+strconv.Itoa(n)+":"+strconv.FormatUint(id, 10))
}

// undo takes back the last destructive action of the user.
func (h *Handler) undo(w http.ResponseWriter, r *http.Request) {
	if !h.form(w, r) {
		return
	}
	ctx := r.Context()
	notice := "notice.undo-gone"
	if undo := h.undos.take(state(r).who.user.ID, h.now()); undo != nil {
		err := undo(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, store.ErrConflict):
			notice = "notice.undo-conflict"
		case err != nil:
			h.broken(w, r, err)
			return
		default:
			notice = "notice.undone"
		}
	}
	h.notify(w, r, notice, 0)
	http.Redirect(w, r, h.localTarget(r.PostForm.Get("next")), http.StatusSeeOther)
}
