// Package repos holds the SQL, one file per table. Every function takes a
// db.Queryer, so it runs on a *sql.DB or inside a db.WithTx transaction.
package repos

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"jidoka/internal/db"
)

// ErrNotFound means no row matches.
var ErrNotFound = errors.New("repos: not found")

// scanner is what *sql.Row and *sql.Rows share.
type scanner interface {
	Scan(dest ...any) error
}

// queryAll runs query and scans every row with scan.
func queryAll[T any](q db.Queryer, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryOne scans the single row of query; ErrNotFound when there is none.
func queryOne[T any](q db.Queryer, scan func(scanner) (T, error), query string, args ...any) (T, error) {
	v, err := scan(q.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// marks returns "?,?,?" for n placeholders.
func marks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// anys converts a string slice for use as query arguments.
func anys(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

// parseTime reads a NOT NULL time column.
func parseTime(s string) (time.Time, error) {
	return db.ParseTime(s)
}

// parseNull reads a nullable time column; nil for NULL.
func parseNull(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := db.ParseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// boolInt stores a bool as 0 or 1.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
