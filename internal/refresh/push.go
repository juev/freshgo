package refresh

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// Push stores a document a WebSub hub sent for one subscription into the
// feed the subscription belongs to, and returns 1 when the feed took it.
// A feed that is gone, muted, or belongs to a disabled user takes nothing
// and returns 0. The error is for a document that cannot be read or stored:
// the subscription stays, and the hub may try again.
func (r *Refresher) Push(ctx context.Context, sub *store.WebSub, document []byte, contentType string) (int, error) {
	f, err := r.db.FeedByID(ctx, sub.UserID, sub.FeedID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	case f.TTL < 0:
		return 0, nil
	}
	u, err := r.db.UserByID(ctx, f.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return 0, nil
	case err != nil:
		return 0, err
	case !readUserSettings(u.Settings).enabled:
		return 0, nil
	}
	j, err := r.userJob(ctx, u)
	if err != nil {
		return 0, err
	}
	params, err := fetch.FeedParams(f.HTTPAuth, f.Attributes)
	if err != nil {
		return 0, err
	}
	// What a request for the feed would have answered.
	resp := &fetch.Response{
		URL:    strings.TrimSuffix(f.URL, forceFeedSuffix),
		Header: http.Header{"Content-Type": {contentType}},
		Body:   document,
	}
	began := r.now()
	res, err := r.storeFetched(ctx, j, f, params, resp, began.Unix(), true)
	if err != nil {
		return 0, err
	}
	if r.Notify != nil && res.added > 0 {
		r.Notify(ctx, u, began)
	}
	return 1, nil
}
