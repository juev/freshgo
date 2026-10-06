package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// source reads the tables of one FreshRSS user. SQLite installations keep a
// database file per user, PostgreSQL ones a set of prefixed tables per user
// in a shared database.
type source struct {
	db *sql.DB
	// prefix goes before a table name: empty for SQLite, "<prefix><user>_"
	// for PostgreSQL.
	prefix   string
	postgres bool
	// owned tells that db belongs to this source and is closed with it.
	owned bool
}

type (
	nullInt    = sql.NullInt64
	nullString = sql.NullString
)

// table returns the quoted name of a FreshRSS table of the user.
func (s *source) table(name string) string {
	return `"` + strings.ReplaceAll(s.prefix+name, `"`, `""`) + `"`
}

func (s *source) query(ctx context.Context, query string) (*sql.Rows, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading FreshRSS tables (is FreshRSS up to date? import expects the schema of 1.29 or later): %w", err)
	}
	return rows, nil
}

func (s *source) queryRow(ctx context.Context, query string) *sql.Row {
	return s.db.QueryRowContext(ctx, query)
}

// counter returns the last identifier FreshRSS issued for a table.
func (s *source) counter(ctx context.Context, name string) (int64, error) {
	var value int64
	var err error
	if s.postgres {
		err = s.db.QueryRowContext(ctx, `SELECT last_value FROM `+s.table(name+"_id_seq")).Scan(&value)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = '`+name+`'`).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
	}
	return value, err
}

func (s *source) close() {
	if s.owned {
		_ = s.db.Close()
	}
}

// sourceOpener returns a function that opens the tables of a user and a
// function that releases what is shared between users.
func sourceOpener(system *systemConfig, opts Options) (open func(user string) (*source, error), closeAll func(), err error) {
	dbType, _ := system.db["type"].(string)
	switch dbType {
	case "sqlite":
		open = func(user string) (*source, error) {
			path := filepath.Join(opts.DataDir, "users", user, "db.sqlite")
			if _, err := os.Stat(path); err != nil {
				return nil, err
			}
			db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			if err != nil {
				return nil, err
			}
			return &source{db: db, owned: true}, nil
		}
		return open, func() {}, nil
	case "pgsql":
		dsn := opts.SourceDatabaseURL
		if dsn == "" {
			if dsn, err = postgresURL(system.db); err != nil {
				return nil, nil, err
			}
		}
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("importer: FreshRSS database: %w", err)
		}
		prefix, _ := system.db["prefix"].(string)
		open = func(user string) (*source, error) {
			return &source{db: db, prefix: prefix + user + "_", postgres: true}, nil
		}
		return open, func() { _ = db.Close() }, nil
	case "mysql":
		return nil, nil, errors.New("importer: MySQL/MariaDB installations are not supported; move FreshRSS to SQLite or PostgreSQL first (cli/export-sqlite-for-user.php)")
	default:
		return nil, nil, fmt.Errorf("importer: unknown database type %q in config.php", dbType)
	}
}

// postgresURL builds a connection URL from the db section of config.php,
// where host may carry a port.
func postgresURL(db map[string]any) (string, error) {
	host, _ := db["host"].(string)
	user, _ := db["user"].(string)
	password, _ := db["password"].(string)
	base, _ := db["base"].(string)
	if host == "" || base == "" {
		return "", errors.New("importer: config.php has no PostgreSQL host or database name; pass the connection URL explicitly")
	}
	u := url.URL{Scheme: "postgres", Host: host, Path: "/" + base}
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	// connection_uri_params is "key=value;key=value" in PDO form.
	if params, _ := db["connection_uri_params"].(string); params != "" {
		q := url.Values{}
		for _, pair := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
				q.Set(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
