// Package push tells the users who asked for it, through the push service of
// their browser, that entries came: one short message per browser after a
// refresh brought unread entries to the reading list. See docs/specs/push.md.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/web/i18n"
)

const (
	// sendTimeout bounds one message to a push service, so that a service
	// that does not answer holds nothing back but its own message.
	sendTimeout = 10 * time.Second
	// ttl is how long, in seconds, a push service keeps a message for a
	// browser that is off: the news of a day is stale after it.
	ttl = 24 * 60 * 60
	// topic makes a push service replace a message that waits for a browser
	// with the next one, so that a browser that was off is told once.
	topic = "new-entries"
	// maxTitle bounds the title of an entry in a message, in runes: the
	// message is one record of the encryption and a notification shows a
	// line or two.
	maxTitle = 100
)

// Fetcher makes the request to a push service. The address is the browser's
// to name, so it goes through the client that refuses internal destinations.
type Fetcher interface {
	Fetch(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// Options are what a notifier needs.
type Options struct {
	DB      *store.Store
	Fetcher Fetcher
	Texts   *i18n.Bundle
	Log     *slog.Logger
	// PublicKey and PrivateKey are the VAPID keys; Subject is the mailto:
	// or https: address the push services may contact the operator at.
	PublicKey, PrivateKey, Subject string
}

// Notifier sends the messages.
type Notifier struct {
	db         *store.Store
	client     viaFetcher
	texts      *i18n.Bundle
	log        *slog.Logger
	public     string
	private    string
	subscriber string
	// sending are the messages on their way.
	sending sync.WaitGroup
}

// New returns a notifier.
func New(o Options) *Notifier {
	return &Notifier{
		db: o.DB, client: viaFetcher{o.Fetcher}, texts: o.Texts, log: o.Log, public: o.PublicKey, private: o.PrivateKey,
		// The library writes "mailto:" before a subject that is not an
		// https address.
		subscriber: strings.TrimPrefix(o.Subject, "mailto:"),
	}
}

// Wait returns when the messages sent so far have been answered or have
// timed out.
func (n *Notifier) Wait() { n.sending.Wait() }

// settings are the members of the settings of a user a message reads.
type settings struct {
	Language string `json:"language"`
}

// message is what the service worker of the browser shows.
type message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Notify tells the browsers of the user that unread entries came to their
// reading list since the time. It returns once the messages are on their way:
// they are sent in the background, each with a time limit, and a failure is
// logged, never returned.
func (n *Notifier) Notify(ctx context.Context, u *store.User, since time.Time) {
	subs, err := n.db.PushSubscriptions(ctx, u.ID)
	if err != nil {
		n.log.Warn("push subscriptions cannot be read", "user", u.Name, "error", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	payload, err := n.payload(ctx, u, since)
	if err != nil {
		n.log.Warn("push message cannot be made", "user", u.Name, "error", err)
		return
	}
	if payload == nil {
		return
	}
	// The message goes on when the run that asked for it, or the request of
	// a reader that started the run, is over.
	ctx = context.WithoutCancel(ctx)
	for _, sub := range subs {
		n.sending.Go(func() { n.send(ctx, u, sub, payload) })
	}
}

// payload writes the message about the unread entries that came to the
// reading list since the time, nil when there are none.
func (n *Notifier) payload(ctx context.Context, u *store.User, since time.Time) ([]byte, error) {
	// The reading list is that of the digest and the APIs: feeds of priority
	// 0 and above. An entry a rule has marked read is not news.
	ids, err := n.db.ListEntryIDs(ctx, u.ID, store.EntryQuery{
		Set: store.EntrySet{MinPriority: new(0)}, Read: new(false), Ascending: true, From: since.UnixMicro(),
	})
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	newest, err := n.db.EntriesByIDs(ctx, u.ID, ids[len(ids)-1:], false)
	if err != nil || len(newest) == 0 {
		return nil, err
	}
	system, err := n.db.System(ctx)
	if err != nil {
		return nil, err
	}
	var s settings
	// Settings that cannot be read are settings nobody made.
	_ = json.Unmarshal(u.Settings, &s)
	l := n.texts.Match(s.Language, system.Language)
	title := strings.Join(strings.Fields(newest[0].Title), " ")
	if title == "" {
		title = l.T("push.untitled")
	}
	if runes := []rune(title); len(runes) > maxTitle {
		title = string(runes[:maxTitle-1]) + "…"
	}
	return json.Marshal(message{Title: system.Title, Body: l.N("push.body", len(ids), title)})
}

// send delivers the payload to one browser. A push service that no longer
// knows the browser (404 or 410) loses its subscription.
func (n *Notifier) send(ctx context.Context, u *store.User, sub *store.PushSubscription, payload []byte) {
	sent, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	// The library pads the encrypted record in the spare capacity of the
	// message, so each send gets a copy of its own.
	resp, err := webpush.SendNotificationWithContext(sent, slices.Clone(payload),
		&webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{Auth: sub.Auth, P256dh: sub.P256DH}},
		&webpush.Options{
			HTTPClient: n.client, Subscriber: n.subscriber, VAPIDPublicKey: n.public, VAPIDPrivateKey: n.private,
			TTL: ttl, Topic: topic, Urgency: webpush.UrgencyNormal,
		})
	// The endpoint is a secret of the browser; the log names its host only.
	host := ""
	if e, perr := url.Parse(sub.Endpoint); perr == nil {
		host = e.Host
	}
	if err != nil {
		n.log.Warn("push message was not sent", "user", u.Name, "service", host, "error", strings.ReplaceAll(err.Error(), sub.Endpoint, host))
		return
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		if err := n.db.DeletePush(ctx, u.ID, sub.Endpoint); err != nil {
			n.log.Warn("push subscription cannot be removed", "user", u.Name, "service", host, "error", err)
			return
		}
		n.log.Info("push subscription is gone and was removed", "user", u.Name, "service", host, "status", resp.StatusCode)
	case resp.StatusCode >= 300:
		n.log.Warn("push service refused the message", "user", u.Name, "service", host, "status", resp.StatusCode)
	}
}

// viaFetcher lets the library send its request through a Fetcher.
type viaFetcher struct{ fetcher Fetcher }

// Do sends the request as a POST that follows no redirect and returns the
// status of the answer. A push service answers 201, which a Fetcher reports
// with the other statuses but 200.
func (v viaFetcher) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_, err = v.fetcher.Fetch(req.Context(), fetch.Request{
		URL:    req.URL.String(),
		Params: fetch.Params{Post: true, PostBody: string(body), Header: req.Header, NoRedirects: true},
	})
	status := http.StatusOK
	var refused *fetch.StatusError
	switch {
	case errors.As(err, &refused):
		status = refused.Code
	case err != nil:
		return nil, err
	}
	return &http.Response{StatusCode: status, Body: http.NoBody}, nil
}
