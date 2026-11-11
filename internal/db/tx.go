// Adapted from andon internal/db/tx.go (468474a).

package db

import (
	"context"
	"database/sql"
)

// Queryer is satisfied by both *sql.DB and *sql.Tx, so repo functions take
// it and work inside or outside an explicit transaction.
type Queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// WithTx runs fn as one unit of work: commit on success, roll back on error
// or panic.
func WithTx(d *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// readOnly begins a deferred, read-only transaction (the driver sets
// query_only): it never takes the write lock, so it runs next to a writer
// (WAL).
var readOnly = &sql.TxOptions{ReadOnly: true}

// WithRead runs fn on one consistent read-only snapshot; any write fails.
func WithRead(d *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := d.BeginTx(context.Background(), readOnly)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	return fn(tx)
}
