package greader

import (
	"context"
	"errors"

	"github.com/juev/freshgo/internal/opml"
)

// subscriptionExport answers with the subscriptions of the user as OPML.
func (h *Handler) subscriptionExport(ctx context.Context, q *request) error {
	now := h.now()
	data, err := opml.Export(ctx, h.db, q.user, now)
	if err != nil {
		return err
	}
	head := q.w.Header()
	head.Set("Content-Type", "application/xml; charset=UTF-8")
	head.Set("Content-Disposition", `attachment; filename="feeds_`+now.Format("2006-01-02")+`.opml.xml"`)
	_, err = q.w.Write(data)
	return err
}

// subscriptionImport adds the subscriptions of the OPML document in the
// body and fetches the entries of the new feeds.
func (h *Handler) subscriptionImport(ctx context.Context, q *request) error {
	added, err := opml.Import(ctx, h.db, h.hooks, q.user, []byte(q.body), opml.Limits{})
	if err != nil && !errors.Is(err, opml.ErrIncomplete) && !errors.Is(err, opml.ErrDocument) {
		return err
	}
	for _, f := range added {
		if f.TTL < 0 {
			// Muted in the document.
			continue
		}
		// A feed that fails is marked as failing and tried again later.
		if err := h.refresher.RefreshFeed(ctx, q.user, f.ID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			h.log.Warn("imported feed failed", "user", q.user.Name, "feed", f.ID, "error", err)
		}
	}
	if err != nil {
		h.log.Warn("OPML import failed", "user", q.user.Name, "error", err)
		return errBadRequest
	}
	done(q.w)
	return nil
}
