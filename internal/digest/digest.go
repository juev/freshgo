// Package digest writes the daily letter with the unread entries that came
// since the last one, to the users who asked for it.
package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web/i18n"
)

const (
	// Period is how long a user waits between two digests.
	Period = 24 * time.Hour
	// maxListed bounds the entries a letter names; the rest is counted.
	maxListed = 100
)

// Mailer sends a letter of plain text.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Options are what a digester needs.
type Options struct {
	DB     *store.Store
	Mailer Mailer
	Texts  *i18n.Bundle
	Log    *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Digester sends the digests.
type Digester struct {
	db     *store.Store
	mailer Mailer
	texts  *i18n.Bundle
	log    *slog.Logger
	now    func() time.Time
}

// New returns a digester.
func New(o Options) *Digester {
	d := &Digester{db: o.DB, mailer: o.Mailer, texts: o.Texts, log: o.Log, now: o.Now}
	if d.now == nil {
		d.now = time.Now
	}
	return d
}

// settings are the members of the settings of a user the digest reads. The
// web interface writes digest and mail_login; the digester writes
// digest_sent alone.
type settings struct {
	Enabled  *bool  `json:"enabled"`
	Digest   bool   `json:"digest"`
	Sent     int64  `json:"digest_sent"`
	Mail     string `json:"mail_login"`
	Pending  string `json:"email_validation_token"`
	Language string `json:"language"`
}

// Schedule looks at the users now and then every interval, until ctx is
// done. A pass that fails is logged and the next one still happens.
func (d *Digester) Schedule(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			d.log.Error("digest failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Run sends the digest to every user whose period is over. A user whose
// letter fails is logged and keeps the period, so the next run tries again.
func (d *Digester) Run(ctx context.Context) error {
	users, err := d.db.Users(ctx)
	if err != nil {
		return err
	}
	system, err := d.db.System(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := d.one(ctx, u, system); err != nil {
			d.log.Warn("digest was not sent", "user", u.Name, "error", err)
		}
	}
	return nil
}

func (d *Digester) one(ctx context.Context, u *store.User, system store.System) error {
	var s settings
	// Settings that cannot be read are settings nobody made.
	_ = json.Unmarshal(u.Settings, &s)
	// An address that is yet to be confirmed is not one a letter goes to.
	if !s.Digest || s.Enabled != nil && !*s.Enabled || s.Mail == "" || s.Pending != "" {
		return nil
	}
	now := d.now()
	// The switch records when it was turned on; a user who has none starts
	// now, so that the first digest does not carry the whole backlog.
	if s.Sent == 0 {
		return d.markSent(ctx, u.ID, now)
	}
	since := time.Unix(s.Sent, 0)
	if now.Sub(since) < Period {
		return nil
	}
	entries, more, err := d.unread(ctx, u.ID, since, now)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		feeds, err := d.db.Feeds(ctx, u.ID)
		if err != nil {
			return err
		}
		names := make(map[int64]string, len(feeds))
		for _, f := range feeds {
			names[f.ID] = f.Name
		}
		l := d.texts.Match(s.Language, system.Language)
		subject, body := letter(l, system.Title, entries, more, names)
		if err := d.mailer.Send(ctx, s.Mail, subject, body); err != nil {
			return err
		}
	}
	return d.markSent(ctx, u.ID, now)
}

// unread returns the newest unread entries of the reading list that came in
// [from, to), newest first, at most maxListed, and how many more there are.
func (d *Digester) unread(ctx context.Context, userID int64, from, to time.Time) ([]*store.Entry, int, error) {
	// The reading list is that of the APIs: feeds of priority 0 and above.
	ids, err := d.db.ListEntryIDs(ctx, userID, store.EntryQuery{
		Set: store.EntrySet{MinPriority: new(0)}, Read: new(false), Ascending: true, From: from.UnixMicro(),
	})
	if err != nil {
		return nil, 0, err
	}
	// An identifier is the time the entry was added, in microseconds.
	for len(ids) > 0 && ids[len(ids)-1] >= to.UnixMicro() {
		ids = ids[:len(ids)-1]
	}
	more := max(len(ids)-maxListed, 0)
	entries, err := d.db.EntriesByIDs(ctx, userID, ids[more:], false)
	return entries, more, err
}

func (d *Digester) markSent(ctx context.Context, userID int64, at time.Time) error {
	return d.db.UpdateUserSettings(ctx, userID, func(settings map[string]json.RawMessage) error {
		settings["digest_sent"] = json.RawMessage(fmt.Sprint(at.Unix()))
		return nil
	})
}

// letter writes the subject and the body of a digest.
func letter(l *i18n.Localizer, site string, entries []*store.Entry, more int, feeds map[int64]string) (subject, body string) {
	total := len(entries) + more
	var b strings.Builder
	b.WriteString(l.N("digest.mail.intro", total, site))
	b.WriteString("\n\n")
	for _, e := range entries {
		title := strings.Join(strings.Fields(e.Title), " ")
		if title == "" {
			title = l.T("digest.untitled")
		}
		fmt.Fprintf(&b, "%s: %s\n", strings.Join(strings.Fields(feeds[e.FeedID]), " "), title)
		if e.Link != "" {
			fmt.Fprintf(&b, "%s\n", e.Link)
		}
		b.WriteString("\n")
	}
	if more > 0 {
		b.WriteString(l.N("digest.mail.more", more))
		b.WriteString("\n\n")
	}
	b.WriteString(l.T("digest.mail.footer"))
	b.WriteString("\n")
	return l.N("digest.mail.subject", total, site), b.String()
}
