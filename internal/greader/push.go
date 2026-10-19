package greader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/juev/freshgo/internal/push"
	"github.com/juev/freshgo/internal/store"
)

// PushPath is where an app learns whether the installation pushes, and
// subscribes to its messages with the token of the API. It is not part of
// the Google Reader API, which has no word for it. See docs/specs/push.md.
const PushPath = "/api/freshgo/push"

// pushRequest says whether the path of the request is under PushPath.
func pushRequest(q *request) bool {
	return len(q.parts) >= 4 && q.parts[1] == "api" && q.parts[2] == "freshgo" && q.parts[3] == "push"
}

// appSubscription is what an app gives for its subscription, in the shape
// of a PushSubscription of a browser.
type appSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// push answers a request under PushPath: the public VAPID key, or a
// subscription made or taken back. While the installation does not push, there
// is nothing there.
func (h *Handler) push(ctx context.Context, q *request) error {
	if h.pushKey == "" {
		text(q.w, http.StatusNotFound, "Not Found!")
		return nil
	}
	action := ""
	switch len(q.parts) {
	case 4:
	case 5:
		action = q.parts[4]
	default:
		return errBadRequest
	}
	switch {
	case action == "" && q.r.Method == http.MethodGet:
		return writeJSON(q.w, struct {
			Key string `json:"vapidPublicKey"`
		}{h.pushKey})
	case q.r.Method != http.MethodPost:
		return errBadRequest
	}
	var sub appSubscription
	if err := json.Unmarshal([]byte(q.body), &sub); err != nil {
		return errBadRequest
	}
	switch action {
	case "subscribe":
		err := push.Subscribe(ctx, h.db, &store.PushSubscription{
			UserID: q.user.ID, Endpoint: sub.Endpoint, P256DH: sub.Keys.P256DH, Auth: sub.Keys.Auth, Created: h.now().Unix(), App: true,
		})
		switch {
		case errors.Is(err, push.ErrInvalid):
			return errBadRequest
		case errors.Is(err, push.ErrTooMany):
			text(q.w, http.StatusConflict, "Conflict!")
			return nil
		case err != nil:
			return err
		}
	case "unsubscribe":
		if !push.ValidEndpoint(sub.Endpoint) {
			return errBadRequest
		}
		// Another user's endpoint, or none, is left alone; the answer is the
		// same.
		if err := h.db.DeletePush(ctx, q.user.ID, sub.Endpoint); err != nil {
			return err
		}
	default:
		return errBadRequest
	}
	q.w.WriteHeader(http.StatusNoContent)
	return nil
}
