// Package push tells the users who asked for it, through the push service of
// their browser or app, that entries came: one short message per browser and
// app after a refresh brought unread entries to the reading list. Apps are
// also told, a while after, that entries were read or starred. See
// docs/specs/push.md.
package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
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
	// topicEntries and topicChanges make a push service replace a message
	// that waits for a browser with the next one of its kind, so that a
	// browser that was off is told once.
	topicEntries = "new-entries"
	topicChanges = "changes"
	// changeDelay is how long after a change of entries the apps of the
	// user are told: the changes of a reader going through a list are told
	// in one message.
	changeDelay = 30 * time.Second
	// maxTitle bounds the title of an entry in a message, in runes: the
	// message is one record of the encryption and a notification shows a
	// line or two.
	maxTitle = 100
	// MaxSubscriptions is how many browsers and apps a user may have
	// subscribed: a message is sent to each of them.
	MaxSubscriptions = 10
	// maxEndpoint bounds the address of a push service.
	maxEndpoint = 1024
)

// Why Subscribe refuses a subscription.
var (
	ErrInvalid = errors.New("push: not a subscription")
	ErrTooMany = errors.New("push: too many subscriptions")
)

// ValidEndpoint reports whether the address is one a push service takes
// messages at. Browsers and UnifiedPush distributors hand out https
// addresses.
func ValidEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && len(endpoint) <= maxEndpoint && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// validKey reports whether the text is base64url of n bytes.
func validKey(text string, n int) bool {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(text, "="))
	return err == nil && len(raw) == n
}

// Subscribe stores the subscription of a browser or an app of a user. The
// public key of the subscriber is a point of the curve P-256, 65 bytes, and
// its secret is 16; the user has fewer than MaxSubscriptions, or this one
// among them.
func Subscribe(ctx context.Context, db *store.Store, sub *store.PushSubscription) error {
	if !ValidEndpoint(sub.Endpoint) || !validKey(sub.P256DH, 65) || !validKey(sub.Auth, 16) {
		return ErrInvalid
	}
	own, err := db.PushSubscriptions(ctx, sub.UserID)
	if err != nil {
		return err
	}
	if len(own) >= MaxSubscriptions && !slices.ContainsFunc(own, func(p *store.PushSubscription) bool { return p.Endpoint == sub.Endpoint }) {
		return ErrTooMany
	}
	sub.P256DH, sub.Auth = strings.TrimRight(sub.P256DH, "="), strings.TrimRight(sub.Auth, "=")
	return db.PutPush(ctx, sub)
}

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
	// delay is changeDelay but in tests.
	delay time.Duration
	// sending are the messages on their way, and those of changes that
	// wait for their time.
	sending sync.WaitGroup
	// mu guards changes: the users whose apps are to be told of changes,
	// with the timer that tells them.
	mu      sync.Mutex
	changes map[int64]*time.Timer
}

// New returns a notifier.
func New(o Options) *Notifier {
	return &Notifier{
		db: o.DB, client: viaFetcher{o.Fetcher}, texts: o.Texts, log: o.Log, public: o.PublicKey, private: o.PrivateKey,
		// The library writes "mailto:" before a subject that is not an
		// https address.
		subscriber: strings.TrimPrefix(o.Subject, "mailto:"),
		delay:      changeDelay, changes: map[int64]*time.Timer{},
	}
}

// Wait sends the messages of changes that wait for their time and returns
// when the messages sent so far have been answered or have timed out.
func (n *Notifier) Wait() {
	n.mu.Lock()
	for user, timer := range n.changes {
		// A timer that has fired is telling the apps already.
		if timer.Stop() {
			go n.tell(user)
		}
	}
	n.mu.Unlock()
	n.sending.Wait()
}

// settings are the members of the settings of a user a message reads.
type settings struct {
	Language string `json:"language"`
}

// message is what a browser or an app is told. Of entries that came, Title
// and Body are what the service worker of a browser shows, Count and Newest
// what an app reads: the number of the entries and the identifier of the
// newest, in the decimal form of the Google Reader API. Of changes, there is
// the kind alone.
type message struct {
	Kind   string `json:"kind"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Count  int    `json:"count,omitempty"`
	Newest string `json:"newest,omitempty"`
}

// The kinds of message.
const (
	kindEntries = "entries"
	kindChanges = "changes"
)

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
		n.sending.Go(func() { n.send(ctx, u, sub, payload, topicEntries) })
	}
}

// Changed tells the apps of the user, after a while, that entries of theirs
// were read, starred or otherwise changed, so that they sync. Changes that
// come meanwhile are told in the same message. Browsers are not told: each
// message to a browser shows a notification.
func (n *Notifier) Changed(userID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, waiting := n.changes[userID]; waiting {
		return
	}
	n.sending.Add(1)
	n.changes[userID] = time.AfterFunc(n.delay, func() { n.tell(userID) })
}

// tell sends the message of changes to the apps of the user.
func (n *Notifier) tell(userID int64) {
	defer n.sending.Done()
	n.mu.Lock()
	delete(n.changes, userID)
	n.mu.Unlock()
	ctx := context.Background()
	subs, err := n.db.PushSubscriptions(ctx, userID)
	if err != nil {
		n.log.Warn("push subscriptions cannot be read", "user", userID, "error", err)
		return
	}
	subs = slices.DeleteFunc(subs, func(sub *store.PushSubscription) bool { return !sub.App })
	if len(subs) == 0 {
		return
	}
	u, err := n.db.UserByID(ctx, userID)
	if err != nil {
		n.log.Warn("push message cannot be made", "user", userID, "error", err)
		return
	}
	payload, err := json.Marshal(message{Kind: kindChanges})
	if err != nil {
		n.log.Warn("push message cannot be made", "user", u.Name, "error", err)
		return
	}
	for _, sub := range subs {
		n.sending.Go(func() { n.send(ctx, u, sub, payload, topicChanges) })
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
	return json.Marshal(message{
		Kind: kindEntries, Title: system.Title, Body: l.N("push.body", len(ids), title),
		Count: len(ids), Newest: strconv.FormatInt(newest[0].ID, 10),
	})
}

// send delivers the payload to one browser or app under the topic. A push
// service that no longer knows the browser (404 or 410) loses its
// subscription.
func (n *Notifier) send(ctx context.Context, u *store.User, sub *store.PushSubscription, payload []byte, topic string) {
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
