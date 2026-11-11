// Adapted from andon internal/db/db.go (468474a).

// Package db opens the SQLite database and applies migrations.
//
// The file is plain SQLite, not encrypted as a whole: Jidoka starts without
// MASTER_KEY (Z7). Secrets sit in `*_enc` columns, sealed by package crypto.
//
// SQLite runs in WAL mode: readers never wait for the writer. Write
// transactions begin IMMEDIATE, so writers queue on the lock (busy
// timeout) instead of failing on a late lock upgrade; WithRead opens a
// read-only transaction next to them. The driver is ncruces/go-sqlite3
// (SQLite compiled to WebAssembly, no cgo), so the binary cross-compiles.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"sort"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const (
	driverName    = "sqlite3"
	busyTimeoutMS = 5000
	// maxConns bounds the pool: one writer at a time plus concurrent readers.
	maxConns = 4
)

// Open opens (and creates if needed) the database at path, sets the
// required pragmas and applies pending migrations.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_txlock=immediate&_pragma=busy_timeout(%d)&_pragma=foreign_keys(on)&_pragma=temp_store(memory)",
		url.PathEscape(path), busyTimeoutMS)

	d, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(maxConns)

	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil {
		d.Close()
		return nil, err
	}
	if err := migrate(d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// migrate applies migrations/*.sql in name order, each in one transaction,
// and records it in schema_migrations.
func migrate(d *sql.DB) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`); err != nil {
		return err
	}

	names, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	list := make([]string, 0, len(names))
	for _, n := range names {
		list = append(list, n.Name())
	}
	sort.Strings(list)

	for _, name := range list {
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		if err := applyOne(d, name); err != nil {
			return err
		}
	}
	return nil
}

// applyOne runs one migration unless it is already recorded.
func applyOne(d *sql.DB, name string) error {
	var applied int
	row := d.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE name = ?", name)
	if err := row.Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}

	body, err := migrationFiles.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}
	return WithTx(d, func(tx *sql.Tx) error {
		if _, err := tx.Exec(string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		_, err := tx.Exec("INSERT INTO schema_migrations (name) VALUES (?)", name)
		return err
	})
}
