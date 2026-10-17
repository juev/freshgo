package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/juev/freshgo/internal/browser"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/mail"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/translate"
)

// doctorTimeout bounds each check of doctor that asks another server.
const doctorTimeout = 15 * time.Second

// checkup writes the outcome of each check on a line of its own and counts
// those that failed.
type checkup struct {
	out    io.Writer
	failed int
	err    error
}

func (c *checkup) line(format string, args ...any) {
	if c.err == nil {
		_, c.err = fmt.Fprintf(c.out, format+"\n", args...)
	}
}

func (c *checkup) ok(name, text string) {
	c.line("ok    %s: %s", name, text)
}

func (c *checkup) fail(name string, err error) {
	c.failed++
	c.line("FAIL  %s: %v", name, err)
}

func (c *checkup) skip(name, why string) {
	c.line("skip  %s: %s", name, why)
}

// warn reports what works, but not as the administrator may think.
func (c *checkup) warn(name, text string) {
	c.line("warn  %s: %s", name, text)
}

// check runs one check against another server, within doctorTimeout.
func (c *checkup) check(ctx context.Context, name, text string, run func(context.Context) error) {
	ctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	if err := run(ctx); err != nil {
		c.fail(name, err)
		return
	}
	c.ok(name, text)
}

// runDoctor checks the configuration and what it names, and changes
// nothing: the database is opened as it is, a letter is not sent.
func runDoctor(ctx context.Context, e env, args []string) error {
	fs, conf := newFlagSet(e, "doctor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	c := &checkup{out: e.stdout}
	if err := conf.Validate(); err != nil {
		c.fail("configuration", err)
		return c.result()
	}
	c.ok("configuration", "the flags and the environment are valid")

	if conf.SMTPURL == "" {
		c.skip("smtp", "no -smtp-url")
	} else if sender, err := mail.New(conf.SMTPURL); err != nil {
		c.fail("smtp", err)
	} else {
		c.check(ctx, "smtp", "logged in to the server", sender.Check)
	}
	if conf.BrowserURL == "" {
		c.skip("browser", "no -browser-url")
	} else if pages, err := browser.New(conf.BrowserURL); err != nil {
		c.fail("browser", err)
	} else {
		c.check(ctx, "browser", "the browser answers", pages.Check)
	}

	driver, dsn, err := conf.Database()
	if err != nil {
		c.fail("database", err)
		return c.result()
	}
	db, err := store.OpenAsIs(ctx, driver, dsn)
	if err != nil {
		c.fail("database", err)
		c.skip("translate", "the requests go through the proxy set in the database")
		return c.result()
	}
	defer func() { _ = db.Close() }()
	c.databaseChecks(ctx, db, conf.OIDCClientSecret != "")

	if conf.TranslateURL == "" {
		c.skip("translate", "no -translate-url")
	} else if client, err := fetch.New(fetchOptions(conf, db)); err != nil {
		c.fail("translate", err)
	} else {
		model := &translate.Client{URL: conf.TranslateURL, Key: conf.TranslateKey, Model: conf.TranslateModel, HTTP: client}
		c.check(ctx, "translate", "the service lists its models", model.Check)
	}
	return c.result()
}

// databaseChecks look at the schema, the settings of the installation, the
// provider they name and the feeds that fail.
func (c *checkup) databaseChecks(ctx context.Context, db *store.Store, oidcSecret bool) {
	current, latest, err := db.SchemaVersion(ctx)
	switch {
	case err != nil:
		c.fail("database", err)
		return
	case current == 0:
		c.ok("database", "empty: the server makes its tables at start")
		return
	case current > latest:
		c.fail("database", fmt.Errorf("schema version %d is newer than this binary supports (%d)", current, latest))
		return
	case current < latest:
		c.ok("database", fmt.Sprintf("schema version %d: the server brings it to %d at start", current, latest))
	default:
		c.ok("database", fmt.Sprintf("schema version %d", current))
	}

	system, err := db.System(ctx)
	if err != nil {
		c.fail("settings", err)
		c.skip("oidc", "the settings are not read")
	} else {
		c.ok("settings", "the settings of the installation are read")
		switch {
		case system.OIDC.Issuer == "":
			c.skip("oidc", "no provider in the settings")
		case !oidcSecret:
			c.skip("oidc", "no -oidc-client-secret: nobody signs in through "+system.OIDC.Issuer)
		default:
			c.check(ctx, "oidc", "read the discovery document of "+system.OIDC.Issuer, func(ctx context.Context) error {
				_, err := oidc.NewProvider(ctx, system.OIDC.Issuer)
				return err
			})
		}
	}

	c.feedProxies(ctx, db)
	failing, err := db.FailingFeeds(ctx)
	if err != nil {
		c.fail("feeds", err)
		return
	}
	total, err := db.CountFeeds(ctx)
	if err != nil {
		c.fail("feeds", err)
		return
	}
	if len(failing) == 0 {
		c.ok("feeds", fmt.Sprintf("none of %d fails", total))
		return
	}
	c.fail("feeds", fmt.Errorf("%d of %d fail", len(failing), total))
	for _, f := range failing {
		c.line("      %s  %d  %s  since %s", f.User, f.ID, withoutCredentials(f.URL), time.Unix(f.Since, 0).UTC().Format(time.DateTime))
	}
}

// feedProxies lists the feeds set to a proxy no request goes through, as
// FreshRSS lets SOCKS4 be: they fail at every refresh until another is
// chosen.
func (c *checkup) feedProxies(ctx context.Context, db *store.Store) {
	users, err := db.Users(ctx)
	if err != nil {
		c.fail("proxies", err)
		return
	}
	var unusable []string
	for _, u := range users {
		feeds, err := db.Feeds(ctx, u.ID)
		if err != nil {
			c.fail("proxies", err)
			return
		}
		for _, f := range feeds {
			if err := fetch.FeedProxy(f.Attributes); errors.Is(err, fetch.ErrUnsupportedProxy) {
				unusable = append(unusable, fmt.Sprintf("      %s  %d  %s  %v", u.Name, f.ID, withoutCredentials(f.URL), err))
			}
		}
	}
	if len(unusable) == 0 {
		c.ok("proxies", "every proxy of a feed is one freshgo goes through")
		return
	}
	c.warn("proxies", fmt.Sprintf("%d feeds fail until another proxy is chosen in their settings", len(unusable)))
	for _, l := range unusable {
		c.line("%s", l)
	}
}

// result is the error of the command: what went wrong in writing, or how
// many checks failed.
func (c *checkup) result() error {
	if c.err != nil {
		return c.err
	}
	if c.failed > 0 {
		return fmt.Errorf("%d of the checks failed", c.failed)
	}
	return nil
}

// withoutCredentials removes the user and the password from an address.
func withoutCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	return u.String()
}
