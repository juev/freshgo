package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/juev/freshgo/internal/config"
)

// Log is a warning or an error of the server that names a user.
type Log struct {
	ID   int64
	Time int64
	// Level is "WARN" or "ERROR".
	Level string
	// User is the name of the user the record is about.
	User    string
	Message string
}

// AddLog keeps a record.
func (s *Store) AddLog(ctx context.Context, l *Log) error {
	_, err := s.exec(ctx, `INSERT INTO logs (time, level, user_name, message) VALUES (?, ?, ?, ?)`,
		l.Time, l.Level, l.User, l.Message)
	if err != nil {
		return fmt.Errorf("store: add log: %w", err)
	}
	return nil
}

// LogQuery picks records.
type LogQuery struct {
	// User, when not empty, keeps the records about that user.
	User string
	// Text, when not empty, keeps the records whose message has it, in any
	// case of ASCII letters.
	Text string
	// Before, when not zero, keeps the records with a smaller identifier:
	// where the page before ended.
	Before int64
	// Limit bounds the number of records; zero or less is no limit.
	Limit int
}

// Logs returns records, the newest first.
func (s *Store) Logs(ctx context.Context, q LogQuery) ([]*Log, error) {
	query, args := `SELECT id, time, level, user_name, message FROM logs WHERE 1 = 1`, []any{}
	if q.User != "" {
		query += ` AND user_name = ?`
		args = append(args, q.User)
	}
	if q.Text != "" {
		// The wildcards of LIKE are text here.
		// Only ASCII letters are lowered, as LOWER of SQLite lowers them.
		lowered := strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + 'a' - 'A'
			}
			return r
		}, q.Text)
		pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(lowered)
		// PostgreSQL would lower by the locale of the database.
		column := `message`
		if s.driver == config.DriverPostgres {
			column += ` COLLATE "C"`
		}
		query += ` AND LOWER(` + column + `) LIKE ? ESCAPE '\'`
		args = append(args, "%"+pattern+"%")
	}
	if q.Before != 0 {
		query += ` AND id < ?`
		args = append(args, q.Before)
	}
	query += ` ORDER BY id DESC`
	if q.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, q.Limit)
	}
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: logs: %w", err)
	}
	logs, err := collect(rows, func(sc scanner) (*Log, error) {
		l := &Log{}
		return l, sc.Scan(&l.ID, &l.Time, &l.Level, &l.User, &l.Message)
	})
	if err != nil {
		return nil, fmt.Errorf("store: logs: %w", err)
	}
	return logs, nil
}

// DeleteLogs removes the records about a user, or all of them when the name
// is empty.
func (s *Store) DeleteLogs(ctx context.Context, user string) error {
	query, args := `DELETE FROM logs`, []any{}
	if user != "" {
		query += ` WHERE user_name = ?`
		args = append(args, user)
	}
	if _, err := s.exec(ctx, query, args...); err != nil {
		return fmt.Errorf("store: delete logs: %w", err)
	}
	return nil
}

// DeleteLogsBefore removes the records made before a time.
func (s *Store) DeleteLogsBefore(ctx context.Context, time int64) error {
	if _, err := s.exec(ctx, `DELETE FROM logs WHERE time < ?`, time); err != nil {
		return fmt.Errorf("store: delete old logs: %w", err)
	}
	return nil
}
