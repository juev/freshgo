package refresh

import (
	"context"
	"time"

	"github.com/juev/freshgo/internal/store"
)

const (
	// summaryConcurrency is how many batches of entries ask the model at
	// once: it answers slowly, and the service of an installation is one.
	summaryConcurrency = 2
	// summaryBudget bounds the time of one batch, so that a model that does
	// not answer cannot keep a process from ending.
	summaryBudget = 10 * time.Minute
)

// summarise has the new entries of a feed summarised when the feed asks
// for it. The batch runs on its own, past the end of the run that brought
// the entries: a failure is logged and the entries stay as they are.
func (r *Refresher) summarise(j *job, attrs attributes, entries []*store.Entry) {
	if r.Summarise == nil || len(entries) == 0 {
		return
	}
	if on, _ := get[bool](attrs, "summarise_long"); !on {
		return
	}
	r.background(j.user, "entries were not summarised", func(ctx context.Context) error {
		return r.Summarise(ctx, j.user, entries)
	})
}

// background runs a batch of work for the model on its own, one of a few at
// a time and for no longer than summaryBudget, and ends it with Lifetime.
// A failure is logged under the message.
func (r *Refresher) background(user *store.User, failure string, run func(ctx context.Context) error) {
	r.summaries.Go(func() {
		select {
		case r.summarySlots <- struct{}{}:
		case <-r.Lifetime.Done():
			return
		}
		defer func() { <-r.summarySlots }()
		ctx, cancel := context.WithTimeout(r.Lifetime, summaryBudget)
		defer cancel()
		if err := run(ctx); err != nil && r.Lifetime.Err() == nil {
			r.log.Warn(failure, "user", user.Name, "error", err)
		}
	})
}

// Wait returns when the summaries and the suggestions under way are done.
func (r *Refresher) Wait() { r.summaries.Wait() }
