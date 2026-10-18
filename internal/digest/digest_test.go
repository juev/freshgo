package digest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/mail/mailtest"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/storetest"
	"github.com/juev/freshgo/internal/web/i18n"
)

// start is when the digest of alice was switched on.
var start = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// world is a database with alice, who has the digest on, an SMTP server and
// a digester over them whose clock the test sets.
type world struct {
	t     *testing.T
	db    *store.Store
	smtp  *mailtest.Server
	d     *Digester
	now   time.Time
	alice *store.User
	// fail, while set, is what sending a letter answers.
	fail  error
	feeds map[string]*store.Feed
}

// flaky sends through the SMTP server unless the test made it fail.
type flaky struct {
	w      *world
	sender *mail.Sender
}

func (f flaky) Send(ctx context.Context, to, subject, body string) error {
	if f.w.fail != nil {
		return f.w.fail
	}
	return f.sender.Send(ctx, to, subject, body)
}

func eachEngine(t *testing.T, test func(t *testing.T, w *world)) {
	t.Helper()
	for _, e := range storetest.Engines() {
		t.Run(e.Name, func(t *testing.T) {
			driver, dsn := e.New(t)
			db, err := store.Open(context.Background(), driver, dsn)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			texts, err := i18n.Load()
			if err != nil {
				t.Fatal(err)
			}
			smtp := mailtest.New(t)
			sender, err := mail.New(smtp.URL)
			if err != nil {
				t.Fatal(err)
			}
			w := &world{t: t, db: db, smtp: smtp, now: start, feeds: map[string]*store.Feed{}}
			w.d = New(Options{
				DB: db, Mailer: flaky{w, sender}, Texts: texts, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Now: func() time.Time { return w.now },
			})
			w.alice = w.user("alice", map[string]any{"digest": true, "digest_sent": start.Unix(), "mail_login": "alice@example.org"})
			w.feeds["news"] = w.feed(w.alice, &store.Feed{Name: "News", Priority: 10})
			w.feeds["hidden"] = w.feed(w.alice, &store.Feed{Name: "Hidden", Priority: -10})
			test(t, w)
		})
	}
}

func (w *world) user(name string, settings map[string]any) *store.User {
	w.t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		w.t.Fatal(err)
	}
	u := &store.User{Name: name, Settings: raw}
	if err := w.db.CreateUser(context.Background(), u); err != nil {
		w.t.Fatal(err)
	}
	return u
}

func (w *world) feed(u *store.User, f *store.Feed) *store.Feed {
	w.t.Helper()
	f.UserID = u.ID
	f.URL = "https://example.org/" + strconv.Itoa(len(w.feeds)) + "/" + u.Name
	if err := w.db.CreateFeed(context.Background(), f); err != nil {
		w.t.Fatal(err)
	}
	return f
}

// entry stores an entry that arrived at the time and returns its title.
func (w *world) entry(u *store.User, f *store.Feed, at time.Time, title string, read bool) string {
	w.t.Helper()
	e := &store.Entry{
		ID: at.UnixMicro(), FeedID: f.ID, GUID: title, Title: title, Link: "https://example.org/" + title, IsRead: read,
	}
	if err := w.db.InsertEntries(context.Background(), u.ID, []*store.Entry{e}); err != nil {
		w.t.Fatal(err)
	}
	return title
}

// run lets the digester look at the users at the time.
func (w *world) run(at time.Time) {
	w.t.Helper()
	w.now = at
	if err := w.d.Run(context.Background()); err != nil {
		w.t.Fatalf("Run: %v", err)
	}
}

// sent is when the digest of the user last went out, in Unix seconds.
func (w *world) sent(u *store.User) int64 {
	w.t.Helper()
	got, err := w.db.UserByName(context.Background(), u.Name)
	if err != nil {
		w.t.Fatal(err)
	}
	var s settings
	if err := json.Unmarshal(got.Settings, &s); err != nil {
		w.t.Fatal(err)
	}
	return s.Sent
}

func (w *world) setting(u *store.User, key string, value any) {
	w.t.Helper()
	err := w.db.UpdateUserSettings(context.Background(), u.ID, func(settings map[string]json.RawMessage) error {
		raw, err := json.Marshal(value)
		settings[key] = raw
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) letters(count int) []mailtest.Letter {
	w.t.Helper()
	letters := w.smtp.Letters()
	if len(letters) != count {
		w.t.Fatalf("%d letters, want %d: %+v", len(letters), count, letters)
	}
	return letters
}

func mentions(t *testing.T, body string, present, absent []string) {
	t.Helper()
	for _, title := range present {
		if !strings.Contains(body, title) {
			t.Errorf("the letter does not name %q:\n%s", title, body)
		}
	}
	for _, title := range absent {
		if strings.Contains(body, title) {
			t.Errorf("the letter names %q:\n%s", title, body)
		}
	}
}

// One letter carries the unread entries of the reading list that came in
// the period, with the feed, the title and the link; read entries, entries
// of another user, those of a hidden feed and those from before the period
// are not in it. The next letter holds only what came after the first.
func TestOneLetterForThePeriod(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		bob := w.user("bob", map[string]any{"digest": true, "digest_sent": start.Unix()})
		bobFeed := w.feed(bob, &store.Feed{Name: "Bob feed"})
		news := w.feeds["news"]
		w.entry(w.alice, news, start.Add(-time.Hour), "before", false)
		fresh := []string{
			w.entry(w.alice, news, start.Add(2*time.Hour), "first", false),
			w.entry(w.alice, news, start.Add(5*time.Hour), "second", false),
		}
		w.entry(w.alice, news, start.Add(3*time.Hour), "seen", true)
		w.entry(w.alice, w.feeds["hidden"], start.Add(4*time.Hour), "secret", false)
		w.entry(bob, bobFeed, start.Add(4*time.Hour), "bobs", false)

		// Nothing goes out before a day has passed.
		w.run(start.Add(23 * time.Hour))
		w.letters(0)

		due := start.Add(24 * time.Hour)
		w.run(due)
		letter := w.letters(1)[0]
		if letter.To != "alice@example.org" || letter.Subject != "2 new unread entries — freshgo" {
			t.Errorf("letter to %q, subject %q", letter.To, letter.Subject)
		}
		mentions(t, letter.Body, append(fresh, "News: first", "https://example.org/first"), []string{"before", "seen", "secret", "bobs"})
		if strings.Index(letter.Body, "second") > strings.Index(letter.Body, "first") {
			t.Errorf("the newest entry is not first:\n%s", letter.Body)
		}
		if got := w.sent(w.alice); got != due.Unix() {
			t.Errorf("the period of alice ends at %d, want %d", got, due.Unix())
		}

		// A second look within the day sends nothing.
		w.run(due.Add(time.Hour))
		w.letters(1)

		// After the day only what is newer comes.
		w.entry(w.alice, news, due.Add(3*time.Hour), "third", false)
		w.run(due.Add(24 * time.Hour))
		letter = w.letters(2)[1]
		mentions(t, letter.Body, []string{"third"}, []string{"first", "second", "before"})
		if letter.Subject != "1 new unread entry — freshgo" {
			t.Errorf("subject %q", letter.Subject)
		}
	})
}

// With nothing new no letter goes out, and the period still moves on, so
// that an entry of an earlier day does not turn up later.
func TestNoLetterWithoutNewEntries(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		due := start.Add(24 * time.Hour)
		w.entry(w.alice, w.feeds["news"], start.Add(time.Hour), "read in time", true)
		w.run(due)
		w.letters(0)
		if got := w.sent(w.alice); got != due.Unix() {
			t.Errorf("the period ends at %d, want %d", got, due.Unix())
		}
		w.entry(w.alice, w.feeds["news"], due.Add(time.Hour), "later", false)
		w.run(due.Add(24 * time.Hour))
		mentions(t, w.letters(1)[0].Body, []string{"later"}, []string{"read in time"})
	})
}

// A letter that cannot be sent leaves the period as it was; the next look
// sends it with the same entries.
func TestFailedLetterIsRetried(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.entry(w.alice, w.feeds["news"], start.Add(time.Hour), "kept", false)
		w.fail = errors.New("the server is down")
		w.run(start.Add(24 * time.Hour))
		w.letters(0)
		if got := w.sent(w.alice); got != start.Unix() {
			t.Errorf("the period moved to %d after a failure, want %d", got, start.Unix())
		}
		w.fail = nil
		w.run(start.Add(25 * time.Hour))
		mentions(t, w.letters(1)[0].Body, []string{"kept"}, nil)
	})
}

// A user whose switch is off, who has no address, whose address is not
// confirmed or who is kept out gets nothing. A user whose switch has no
// start yet gets the start, and no letter with the whole backlog.
func TestWhoGetsNothing(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.entry(w.alice, w.feeds["news"], start.Add(time.Hour), "news", false)
		for name, change := range map[string]func(){
			"switch off":  func() { w.setting(w.alice, "digest", false) },
			"no address":  func() { w.setting(w.alice, "mail_login", "") },
			"unconfirmed": func() { w.setting(w.alice, "email_validation_token", "secret") },
			"kept out":    func() { w.setting(w.alice, "enabled", false) },
		} {
			change()
			w.run(start.Add(48 * time.Hour))
			if letters := w.smtp.Letters(); len(letters) != 0 {
				t.Fatalf("%s: letters %+v", name, letters)
			}
			w.setting(w.alice, "digest", true)
			w.setting(w.alice, "mail_login", "alice@example.org")
			w.setting(w.alice, "email_validation_token", "")
			w.setting(w.alice, "enabled", true)
			w.setting(w.alice, "digest_sent", start.Unix())
		}

		w.setting(w.alice, "digest_sent", 0)
		w.run(start.Add(48 * time.Hour))
		w.letters(0)
		if got := w.sent(w.alice); got != start.Add(48*time.Hour).Unix() {
			t.Errorf("the period starts at %d, want %d", got, start.Add(48*time.Hour).Unix())
		}
	})
}

// A letter names at most a hundred entries, the newest, and says how many
// more there are.
func TestLongList(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		const total = 105
		for i := range total {
			w.entry(w.alice, w.feeds["news"], start.Add(time.Duration(i+1)*time.Minute), fmt.Sprintf("entry-%03d", i), false)
		}
		w.run(start.Add(24 * time.Hour))
		letter := w.letters(1)[0]
		if got := strings.Count(letter.Body, "https://example.org/entry-"); got != 100 {
			t.Errorf("%d entries named, want 100", got)
		}
		mentions(t, letter.Body, []string{"entry-104", "entry-005", "… and 5 more."}, []string{"entry-004", "entry-000"})
		if letter.Subject != "105 new unread entries — freshgo" {
			t.Errorf("subject %q", letter.Subject)
		}
	})
}

// The letter speaks the language of the user.
func TestLetterInTheLanguageOfTheUser(t *testing.T) {
	eachEngine(t, func(t *testing.T, w *world) {
		w.setting(w.alice, "language", "ru")
		w.entry(w.alice, w.feeds["news"], start.Add(time.Hour), "one", false)
		w.run(start.Add(24 * time.Hour))
		letter := w.letters(1)[0]
		if letter.Subject != "1 новая непрочитанная статья — freshgo" || !strings.Contains(letter.Body, "1 непрочитанная статья пришла в freshgo с прошлой сводки:") {
			t.Errorf("subject %q, body\n%s", letter.Subject, letter.Body)
		}
	})
}
