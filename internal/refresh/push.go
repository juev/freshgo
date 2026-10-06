package refresh

import (
	"context"
	"net/http"
	"strings"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// Push stores a document a WebSub hub sent about a topic into every feed
// that announces the topic, whoever reads it, and returns the number of
// feeds it was stored for. Muted feeds and feeds of disabled users are
// passed over. A feed the document cannot be stored for is logged and not
// counted; the error is for what stops the push as a whole.
func (r *Refresher) Push(ctx context.Context, topic string, document []byte, contentType string) (int, error) {
	feeds, err := r.db.FeedsByTopic(ctx, topic)
	if err != nil || len(feeds) == 0 {
		return 0, err
	}
	users, err := r.db.Users(ctx)
	if err != nil {
		return 0, err
	}
	byID := map[int64]*store.User{}
	for _, u := range users {
		byID[u.ID] = u
	}
	stored := 0
	jobs := map[int64]*job{}
	for _, f := range feeds {
		u := byID[f.UserID]
		if u == nil || f.TTL < 0 {
			continue
		}
		j := jobs[u.ID]
		if j == nil {
			if !readUserSettings(u.Settings).enabled {
				continue
			}
			if j, err = r.userJob(ctx, u); err != nil {
				return stored, err
			}
			jobs[u.ID] = j
		}
		params, err := fetch.FeedParams(f.HTTPAuth, f.Attributes)
		if err != nil {
			r.log.Warn("pushed document is not stored", "user", u.Name, "feed", f.ID, "error", err)
			continue
		}
		// What a request for the feed would have answered.
		resp := &fetch.Response{
			URL:    strings.TrimSuffix(f.URL, forceFeedSuffix),
			Header: http.Header{"Content-Type": {contentType}},
			Body:   document,
		}
		if _, err := r.storeFetched(ctx, j, f, params, resp, r.now().Unix(), true); err != nil {
			if ctx.Err() != nil {
				return stored, ctx.Err()
			}
			r.log.Warn("pushed document is not stored", "user", u.Name, "feed", f.ID, "error", err)
			continue
		}
		stored++
	}
	return stored, nil
}
