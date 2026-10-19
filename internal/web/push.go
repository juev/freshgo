package web

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/juev/freshgo/internal/push"
	"github.com/juev/freshgo/internal/store"
)

// serviceWorker is the script a browser runs to show a push message while
// the interface is closed.
//
//go:embed worker.js
var serviceWorker []byte

const (
	// workerPath is where the service worker is served. A worker controls
	// the pages under the directory it is served from, and the interface
	// lives under the same one, so the default scope is the one wanted and
	// no Service-Worker-Allowed header is needed.
	workerPath = "/sw.js"
	// pushPath is where a browser's subscription is posted to, and
	// pushDeletePath where it is taken back.
	pushPath       = "/settings/push"
	pushDeletePath = "/settings/push/delete"
	// maxPushForm bounds the JSON of a subscription; the endpoints of push
	// services are a few hundred bytes long.
	maxPushForm = 4 << 10
)

// pushing says whether the installation offers Web Push: it has the keys.
func (h *Handler) pushing() bool { return h.pushKey != "" }

// worker serves the service worker.
func (h *Handler) worker(w http.ResponseWriter, r *http.Request) {
	if !h.pushing() {
		h.fail(w, r, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// A browser checks a worker for a change itself; the script is not to
	// be kept by anything in between.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(serviceWorker)
}

// subscription is what the browser gives for a PushSubscription, as its
// toJSON writes it.
type subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushForm reads the JSON of a request about a subscription. It answers the
// request itself when there is none to read.
func (h *Handler) pushForm(w http.ResponseWriter, r *http.Request) (subscription, bool) {
	var sub subscription
	if !h.pushing() {
		h.fail(w, r, http.StatusNotFound)
		return sub, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPushForm)
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil || !push.ValidEndpoint(sub.Endpoint) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return sub, false
	}
	return sub, true
}

// subscribePush stores the browser of the user that asked for notifications.
func (h *Handler) subscribePush(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.pushForm(w, r)
	if !ok {
		return
	}
	err := push.Subscribe(r.Context(), h.db, &store.PushSubscription{
		UserID: state(r).who.user.ID, Endpoint: sub.Endpoint, P256DH: sub.Keys.P256DH, Auth: sub.Keys.Auth, Created: h.now().Unix(),
	})
	switch {
	case errors.Is(err, push.ErrInvalid):
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	case errors.Is(err, push.ErrTooMany):
		http.Error(w, http.StatusText(http.StatusConflict), http.StatusConflict)
	case err != nil:
		h.broken(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// unsubscribePush takes back the browser of the user. A browser that is not
// the user's, or not subscribed, is left alone; the answer is the same.
func (h *Handler) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	sub, ok := h.pushForm(w, r)
	if !ok {
		return
	}
	if err := h.db.DeletePush(r.Context(), state(r).who.user.ID, sub.Endpoint); err != nil {
		h.broken(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
