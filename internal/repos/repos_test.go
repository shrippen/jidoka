package repos

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/db"
)

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

// testDB opens a fresh migrated database in a temp dir.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
