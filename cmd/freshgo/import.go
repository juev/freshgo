package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/fetch"
	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/miniflux"
	"github.com/juev/freshgo/internal/store"
	"github.com/juev/freshgo/internal/transfer"
)

// newFlagSet returns the flag set of a subcommand with the shared settings bound.
func newFlagSet(e env, name string) (*flag.FlagSet, *config.Config) {
	fs := flag.NewFlagSet("freshgo "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	conf := config.Bind(fs, e.getenv)
	e.settings.Apply(fs)
	return fs, conf
}

// openStore parses the flags of a subcommand and opens the database they
// name. operands is how many arguments the subcommand takes after its flags;
// they are left in fs.Args.
func openStore(ctx context.Context, fs *flag.FlagSet, conf *config.Config, args []string, operands int) (*store.Store, error) {
	driver, dsn, err := parseDatabase(fs, conf, args, operands)
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, driver, dsn)
}

// parseDatabase is openStore without the opening.
func parseDatabase(fs *flag.FlagSet, conf *config.Config, args []string, operands int) (config.Driver, string, error) {
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() > operands {
		return "", "", fmt.Errorf("unexpected argument %q", fs.Arg(operands))
	}
	if err := conf.Validate(); err != nil {
		return "", "", err
	}
	return conf.Database()
}

// Sources the import command reads from.
const (
	fromFreshRSS = "freshrss"
	fromMiniflux = "miniflux"
)

func runImport(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "import")
	synopsis(fs, e, "import [flags]")
	var opts importer.Options
	from := fs.String("from", fromFreshRSS, "what to import: freshrss, a whole installation into an empty database, "+
		"or miniflux, one account into an existing user")
	fs.StringVar(&opts.DataDir, "data", "", "FreshRSS data directory, the one with config.php and users/ (required for -from freshrss)")
	fs.StringVar(&opts.SourceDatabaseURL, "source-database-url", "",
		"PostgreSQL URL of the FreshRSS database, when it differs from what its config.php says")
	sourceURL := fs.String("source-url", "", "address of the Miniflux server (required for -from miniflux)")
	sourceToken := fs.String("source-token", "", "API token of the Miniflux account; FRESHGO_SOURCE_TOKEN is read when the flag is not given, "+
		"which keeps the token out of the list of processes (required for -from miniflux)")
	user := fs.String("user", "", "the existing user who gets the account (required for -from miniflux)")
	driver, dsn, err := parseDatabase(fs, conf, args, 0)
	if err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if *sourceToken == "" {
		*sourceToken = e.getenv("FRESHGO_SOURCE_TOKEN")
	}
	switch *from {
	case fromFreshRSS:
		for _, name := range []string{"source-url", "source-token", "user"} {
			if given[name] {
				return fmt.Errorf("-%s goes with -from %s", name, fromMiniflux)
			}
		}
		// Opening makes and migrates the database: a mistyped -data would leave
		// an empty one behind, which the next import refuses as not empty.
		if err := checkDataDir(opts.DataDir); err != nil {
			return err
		}
	case fromMiniflux:
		for _, name := range []string{"data", "source-database-url"} {
			if given[name] {
				return fmt.Errorf("-%s goes with -from %s", name, fromFreshRSS)
			}
		}
		switch {
		case *sourceURL == "":
			return errors.New("-source-url is required")
		case *sourceToken == "":
			return errors.New("-source-token is required")
		case *user == "":
			return errors.New("-user is required")
		}
	default:
		return fmt.Errorf("unknown -from %q: %s or %s", *from, fromFreshRSS, fromMiniflux)
	}
	db, err := store.Open(ctx, driver, dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()

	if *from == fromMiniflux {
		return importMiniflux(ctx, e, conf, db, *user, *sourceURL, *sourceToken)
	}
	report, err := importer.Run(ctx, db, opts)
	if err != nil {
		return err
	}
	for _, u := range report.Users {
		if _, err := fmt.Fprintf(e.stdout, "%s: %d categories, %d feeds, %d entries, %d labels on %d entries, %d custom icons\n",
			u.Name, u.Categories, u.Feeds, u.Entries, u.Tags, u.TaggedEntries, u.CustomIcons); err != nil {
			return err
		}
	}
	return printWarnings(e, report.Warnings)
}

// importMiniflux reads the account at the server and writes it into the
// account of the user. The command is the operator's, who names the server:
// it may be at an address of the internal network, so the protection against
// requests there is off, as it is for the feeds of the OPML an operator
// imports by hand.
func importMiniflux(ctx context.Context, e env, conf *config.Config, db *store.Store, name, address, token string) error {
	u, err := findUser(ctx, db, name)
	if err != nil {
		return err
	}
	options := fetchOptions(conf, db)
	options.Allowlist = []string{"*"}
	client, err := fetch.New(options)
	if err != nil {
		return err
	}
	remote, err := miniflux.Read(ctx, client, address, token)
	if err != nil {
		return err
	}
	report, err := transfer.ImportRemote(ctx, db, newHooks(), u, remote, transfer.Options{Now: time.Now()})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(e.stdout, "%s: %d feeds, %d entries added, %d entries updated\n",
		u.Name, report.Feeds, report.Entries, report.Updated); err != nil {
		return err
	}
	return printWarnings(e, report.Warnings)
}

func printWarnings(e env, warnings []string) error {
	for _, w := range warnings {
		if _, err := fmt.Fprintln(e.stderr, "warning:", w); err != nil {
			return err
		}
	}
	return nil
}

// checkDataDir tells whether dir is a FreshRSS data directory that can be
// read: one with a directory users/ in it.
func checkDataDir(dir string) error {
	if dir == "" {
		return errors.New("-data is required")
	}
	users := filepath.Join(dir, "users")
	info, err := os.Stat(users)
	if err != nil {
		return fmt.Errorf("-data %s is not a FreshRSS data directory: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("-data %s is not a FreshRSS data directory: %s is not a directory", dir, users)
	}
	list, err := os.Open(users)
	if err != nil {
		return fmt.Errorf("-data %s: %w", dir, err)
	}
	defer func() { _ = list.Close() }()
	if _, err := list.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("-data %s: %w", dir, err)
	}
	return nil
}
