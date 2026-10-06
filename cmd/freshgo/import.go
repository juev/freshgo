package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/juev/freshgo/internal/config"
	"github.com/juev/freshgo/internal/importer"
	"github.com/juev/freshgo/internal/store"
)

// newFlagSet returns the flag set of a subcommand with the shared settings bound.
func newFlagSet(e env, name string) (*flag.FlagSet, *config.Config) {
	fs := flag.NewFlagSet("freshgo "+name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	return fs, config.Bind(fs, e.getenv)
}

// openStore parses the flags of a subcommand and opens the database they name.
func openStore(ctx context.Context, fs *flag.FlagSet, conf *config.Config, args []string) (*store.Store, error) {
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}
	driver, dsn, err := conf.Database()
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, driver, dsn)
}

func runImport(ctx context.Context, e env, args []string) (err error) {
	fs, conf := newFlagSet(e, "import")
	var opts importer.Options
	fs.StringVar(&opts.DataDir, "data", "", "FreshRSS data directory, the one with config.php and users/ (required)")
	fs.StringVar(&opts.SourceDatabaseURL, "source-database-url", "",
		"PostgreSQL URL of the FreshRSS database, when it differs from what its config.php says")
	db, err := openStore(ctx, fs, conf, args)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if opts.DataDir == "" {
		return errors.New("-data is required")
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
	for _, w := range report.Warnings {
		if _, err := fmt.Fprintln(e.stderr, "warning:", w); err != nil {
			return err
		}
	}
	return nil
}
