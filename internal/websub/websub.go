// Package websub subscribes to the WebSub hubs feeds announce and takes the
// documents the hubs push, so that new entries arrive without waiting for
// the next poll.
//
// When to subscribe, renew and distrust a hub follows
// FreshRSS_Feed::pubSubHubbubPrepare and p/api/pshb.php of FreshRSS at
// commit 219eaf58; the callback address and the signature are freshgo's own.
// See docs/specs/websub.md.
package websub

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // one of the signature methods hubs use
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/juev/freshgo/internal/feed"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/store"
)

// Path is where hubs call back: Path followed by the key of the subscription.
const Path = "/websub/"

const (
	// renewBefore is how long before its end a subscription is renewed, and
	// retryAfter how long a hub that failed is left alone.
	renewBefore = 23 * time.Hour
	retryAfter  = 23 * time.Hour
	// renewPause is how long a hub is given to confirm a renewal.
	renewPause = time.Hour
	// maxPayload is the largest document a hub may push.
	maxPayload = 3 << 20
	// minLease is the shortest lease a hub is believed.
	minLease = 60
)

// Pusher stores a document a hub pushed for one subscription. It returns 1
// when the feed the subscription belongs to took it, and 0 when the feed is
// gone, muted or its user is disabled.
type Pusher interface {
	Push(ctx context.Context, sub *store.WebSub, document []byte, contentType string) (int, error)
}

// Service keeps the subscriptions and answers the hubs.
type Service struct {
	db       *store.Store
	client   *fetch.Client
	log      *slog.Logger
	callback string

	// Pusher gets the documents hubs push, and Now tells the time: time.Now
	// unless a test has another clock. Both are set before the first
	// request and not changed afterwards.
	Pusher Pusher
	Now    func() time.Time
}

// ErrNotPublic is returned by New for a base URL hubs cannot call back.
var ErrNotPublic = errors.New("websub: the public URL of the server is not reachable from outside")

// New returns a Service whose callbacks live under baseURL, the public
// address of the server. An address that is missing, or names this machine
// or a private network, gives ErrNotPublic: no hub could reach it.
func New(db *store.Store, client *fetch.Client, log *slog.Logger, baseURL string) (*Service, error) {
	if !public(baseURL) {
		return nil, ErrNotPublic
	}
	return &Service{db: db, client: client, log: log, callback: strings.TrimRight(baseURL, "/") + Path, Now: time.Now}, nil
}

// public reports whether an address can be reached from the Internet, as
// far as its host tells.
func public(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || !strings.Contains(host, ".") && net.ParseIP(host) == nil {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
	return true
}

// Working returns the feeds whose hub can be relied on at the moment: it has
// pushed since the feed was subscribed, and the lease has not run out.
func (s *Service) Working(ctx context.Context, userID int64) (map[int64]bool, error) {
	subs, err := s.db.WebSubsOfUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := s.Now().Unix()
	working := map[int64]bool{}
	for _, sub := range subs {
		if !sub.Error && (sub.LeaseEnd == 0 || sub.LeaseEnd > now) {
			working[sub.FeedID] = true
		}
	}
	return working, nil
}

// Distrust records that the hub of a feed missed an entry: the feed goes
// back to being polled until a push arrives again.
func (s *Service) Distrust(ctx context.Context, userID, feedID int64) {
	sub, err := s.db.WebSubByFeed(ctx, userID, feedID)
	if err != nil || sub.Error {
		return
	}
	sub.Error = true
	if err := s.db.PutWebSub(ctx, sub); err != nil {
		s.log.Warn("WebSub subscription is not updated", "user", userID, "feed", feedID, "error", err)
		return
	}
	s.log.Warn("an entry was found by polling although the hub should have pushed it", "user", userID, "feed", feedID)
}

// Ensure makes sure the hub of a feed has been asked to push: the first time
// the feed announces the topic, when the lease is about to run out, and
// again a day after a failure. A hub that fails is logged; the feed is
// polled as before.
func (s *Service) Ensure(ctx context.Context, userID, feedID int64, topic, hub string) {
	if err := s.ensure(ctx, userID, feedID, topic, hub); err != nil && ctx.Err() == nil {
		s.log.Warn("WebSub subscription failed", "user", userID, "feed", feedID, "topic", topic, "hub", hub, "error", err)
	}
}

func (s *Service) ensure(ctx context.Context, userID, feedID int64, topic, hub string) error {
	now := s.Now()
	sub, err := s.db.WebSubByFeed(ctx, userID, feedID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		sub = &store.WebSub{UserID: userID, FeedID: feedID, Topic: topic, Hub: hub, Error: true}
		if sub.Key, sub.Secret, err = newKeySecret(); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		sinceAsked := now.Sub(time.Unix(sub.LeaseStart, 0))
		rested := sinceAsked >= retryAfter
		expiring := sub.LeaseEnd != 0 && time.Unix(sub.LeaseEnd, 0).Sub(now) < renewBefore
		unconfirmed := sub.Error || sub.LeaseEnd == 0
		// A feed that names another topic or hub is asked at the new one, no
		// more often than a hub that failed.
		moved := sub.Topic != topic || sub.Hub != hub
		// A hub in good standing gets a moment to confirm a renewal before
		// it is asked again; FreshRSS asks on every refresh.
		renew := expiring && (rested || !sub.Error && sinceAsked >= renewPause)
		retry := rested && (unconfirmed || moved)
		if !renew && !retry {
			return nil
		}
		if moved {
			// The new hub gets a secret of its own, and the old key stops
			// working, so an old hub cannot keep pushing.
			if sub.Key, sub.Secret, err = newKeySecret(); err != nil {
				return err
			}
			sub.Topic, sub.Hub = topic, hub
			sub.Error, sub.LeaseEnd = true, 0
		}
	}
	// Recorded before asking, so that a hub that does not answer is not
	// asked again by every refresh.
	sub.LeaseStart = now.Unix()
	if err := s.db.PutWebSub(ctx, sub); err != nil {
		return err
	}
	form := url.Values{
		"hub.mode":     {"subscribe"},
		"hub.topic":    {topic},
		"hub.callback": {s.callback + sub.Key},
		"hub.secret":   {sub.Secret},
		"hub.verify":   {"sync"}, // for hubs that speak PubSubHubbub 0.3
	}
	_, err = s.client.Fetch(ctx, fetch.Request{URL: hub, Params: fetch.Params{Post: true, PostBody: form.Encode()}})
	// Hubs accept with 202 and confirm later, or with 204 after confirming.
	var status *fetch.StatusError
	if errors.As(err, &status) && status.Code/100 == 2 {
		err = nil
	}
	if err != nil {
		// The hub may have confirmed and then failed: read before writing.
		if fresh, readErr := s.db.WebSubByFeed(ctx, userID, feedID); readErr == nil {
			fresh.Error = true
			_ = s.db.PutWebSub(ctx, fresh)
		}
		return err
	}
	s.log.Info("asked the WebSub hub to push", "user", userID, "feed", feedID, "topic", topic, "hub", hub)
	return nil
}

// newKeySecret returns the callback key and the signing secret of a new
// subscription.
func newKeySecret() (key, secret string, err error) {
	if key, err = random(); err != nil {
		return "", "", err
	}
	if secret, err = random(); err != nil {
		return "", "", err
	}
	return key, secret, nil
}

// random returns 32 random bytes as hexadecimal digits.
func random() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ServeHTTP answers a hub at Path<key>: a GET that asks whether a
// subscription is wanted, and a POST that pushes a document.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=UTF-8")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	ctx := r.Context()
	sub, err := s.db.WebSubByKey(ctx, strings.TrimPrefix(r.URL.Path, Path))
	switch {
	case errors.Is(err, store.ErrNotFound) && r.Method == http.MethodPost:
		// Tells the hub to stop sending.
		http.Error(w, "Unknown subscription", http.StatusGone)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "Unknown subscription", http.StatusNotFound)
	case err != nil:
		s.fail(w, err)
	case r.Method == http.MethodGet:
		s.verify(ctx, w, r, sub)
	case r.Method == http.MethodPost:
		s.push(ctx, w, r, sub)
	default:
		h.Set("Allow", "GET, POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Service) fail(w http.ResponseWriter, err error) {
	s.log.Error("WebSub request failed", "error", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

// verify answers the hub's question whether the subscription, or its end,
// is wanted: the challenge is echoed for yes, 404 is no.
func (s *Service) verify(ctx context.Context, w http.ResponseWriter, r *http.Request, sub *store.WebSub) {
	q := r.URL.Query()
	if topic := q.Get("hub.topic"); topic != "" && !sameTopic(topic, sub.Topic) {
		http.Error(w, "Not the topic of this subscription", http.StatusNotFound)
		return
	}
	feed, err := s.db.FeedByID(ctx, sub.UserID, sub.FeedID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	// A muted feed is nobody's to read, and a feed that is gone is not read.
	reads := err == nil && feed.TTL >= 0
	switch q.Get("hub.mode") {
	case "subscribe":
		if !reads {
			http.Error(w, "Nobody reads this feed", http.StatusNotFound)
			return
		}
		now := s.Now().Unix()
		sub.LeaseStart, sub.LeaseEnd = now, 0
		if lease, err := strconv.ParseInt(q.Get("hub.lease_seconds"), 10, 64); err == nil && lease > minLease {
			sub.LeaseEnd = now + lease
		}
		// Error stays as it is: a hub is trusted from its first push on.
		if err := s.db.PutWebSub(ctx, sub); err != nil {
			s.fail(w, err)
			return
		}
	case "unsubscribe":
		// Wanted only when the feed is gone or muted.
		if reads {
			http.Error(w, "The subscription is in use", http.StatusNotFound)
			return
		}
		if err := s.db.DeleteWebSub(ctx, sub.UserID, sub.FeedID); err != nil {
			s.fail(w, err)
			return
		}
	case "denied":
		sub.Error, sub.LeaseStart = true, s.Now().Unix()
		if err := s.db.PutWebSub(ctx, sub); err != nil {
			s.fail(w, err)
			return
		}
		s.log.Warn("WebSub hub denied the subscription", "user", sub.UserID, "feed", sub.FeedID,
			"topic", sub.Topic, "reason", q.Get("hub.reason"))
	default:
		http.Error(w, "Unknown hub.mode", http.StatusBadRequest)
		return
	}
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
}

// push takes a document from the hub. It has to carry the signature made
// with the secret of the subscription, be about its topic, and is stored in
// the feed the subscription belongs to.
func (s *Service) push(ctx context.Context, w http.ResponseWriter, r *http.Request, sub *store.WebSub) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayload+1))
	if err != nil {
		http.Error(w, "Unreadable payload", http.StatusBadRequest)
		return
	}
	if len(body) > maxPayload {
		http.Error(w, "Payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !signed(body, sub.Secret, r.Header.Get("X-Hub-Signature")) {
		s.log.Warn("WebSub push without a valid signature", "user", sub.UserID, "feed", sub.FeedID, "topic", sub.Topic)
		http.Error(w, "Invalid signature", http.StatusForbidden)
		return
	}
	if len(body) == 0 {
		http.Error(w, "Missing payload", http.StatusUnprocessableEntity)
		return
	}
	contentType := r.Header.Get("Content-Type")
	// The address the document gives as its own; the Link header overrules it.
	self := ""
	if doc, err := feed.Parse(body, feed.Options{ContentType: contentType}); err == nil {
		self = doc.SelfURL
	}
	if fromHeader := Link(r.Header.Values("Link"), "self"); fromHeader != "" {
		self = fromHeader
	}
	if self != "" && !sameTopic(self, sub.Topic) {
		s.log.Warn("WebSub push for another address", "user", sub.UserID, "feed", sub.FeedID, "topic", sub.Topic, "self", self)
		http.Error(w, "Self URL does not match the subscription", http.StatusUnprocessableEntity)
		return
	}
	n, err := s.Pusher.Push(ctx, sub, body, contentType)
	if err != nil {
		s.fail(w, err)
		return
	}
	if n == 0 {
		// The feed is gone or muted: the hub may stop, and so may we.
		if err := s.db.DeleteWebSub(ctx, sub.UserID, sub.FeedID); err != nil {
			s.log.Warn("WebSub subscription is not deleted", "user", sub.UserID, "feed", sub.FeedID, "error", err)
		}
		http.Error(w, "Nobody reads this feed any more", http.StatusGone)
		return
	}
	if sub.Error {
		// Read again: storing the entries took time.
		if fresh, err := s.db.WebSubByFeed(ctx, sub.UserID, sub.FeedID); err == nil {
			fresh.Error = false
			if err := s.db.PutWebSub(ctx, fresh); err != nil {
				s.log.Warn("WebSub subscription is not updated", "user", sub.UserID, "feed", sub.FeedID, "error", err)
			}
		}
	}
	_, _ = io.WriteString(w, "Done: "+strconv.Itoa(n)+"\n")
}

// Signature methods a hub may name in X-Hub-Signature.
var methods = map[string]func() hash.Hash{
	"sha1": sha1.New, "sha256": sha256.New, "sha384": sha512.New384, "sha512": sha512.New,
}

// signed reports whether the header is "<method>=<hexadecimal HMAC>" of
// the body under the secret.
func signed(body []byte, secret, header string) bool {
	method, digest, ok := strings.Cut(strings.TrimSpace(header), "=")
	newHash := methods[strings.ToLower(method)]
	if !ok || newHash == nil {
		return false
	}
	sent, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	mac := hmac.New(newHash, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sent, mac.Sum(nil))
}

// A link of a Link header with its parameters, and the relations among them.
var (
	linkValue = regexp.MustCompile(`<([^>]*)>([^<]*)`)
	linkRel   = regexp.MustCompile(`(?i);\s*rel\s*=\s*(?:"([^"]*)"|([^\s";,]+))`)
)

// Link returns the first address Link headers give with the relation, such
// as "hub" or "self", or "".
func Link(headers []string, rel string) string {
	for _, header := range headers {
		for _, link := range linkValue.FindAllStringSubmatch(header, -1) {
			m := linkRel.FindStringSubmatch(link[2])
			if m == nil {
				continue
			}
			for _, r := range strings.Fields(m[1] + m[2]) {
				if strings.EqualFold(r, rel) {
					return strings.TrimSpace(link[1])
				}
			}
		}
	}
	return ""
}

var scheme = regexp.MustCompile(`(?i)^https?://`)

// sameTopic compares addresses the way FreshRSS does for WebSub: http and
// https are the same place.
func sameTopic(a, b string) bool {
	return scheme.ReplaceAllString(strings.TrimSpace(a), "//") == scheme.ReplaceAllString(strings.TrimSpace(b), "//")
}
