package db

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jidoka.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}

func TestOpenMigratesOnce(t *testing.T) {
	d, path := open(t)
	d.Close()

	d, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()

	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n != 1 {
		t.Fatalf("migrations recorded: %d, %v", n, err)
	}
	if err := d.QueryRow("SELECT COUNT(*) FROM runs").Scan(&n); err != nil {
		t.Fatalf("runs table: %v", err)
	}
}

func TestWAL(t *testing.T) {
	d, _ := open(t)
	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}

func TestWithTxRollsBack(t *testing.T) {
	d, _ := open(t)
	boom := errors.New("boom")
	err := WithTx(d, func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO kv (key, value) VALUES ('a', 'b')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	var n int
	_ = d.QueryRow("SELECT COUNT(*) FROM kv").Scan(&n)
	if n != 0 {
		t.Fatal("rolled back write is visible")
	}
}

func TestWithReadRefusesWrites(t *testing.T) {
	d, _ := open(t)
	err := WithRead(d, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO kv (key, value) VALUES ('a', 'b')")
		return err
	})
	if err == nil {
		t.Fatal("write in a read transaction succeeded")
	}
}

func TestTimeRoundtripAndOrder(t *testing.T) {
	whole := time.Date(2026, 11, 11, 10, 0, 5, 0, time.UTC)
	frac := whole.Add(500 * time.Millisecond)

	// Sorted as text, a whole second must come before the fraction after it.
	if !(TimeStr(whole) < TimeStr(frac)) {
		t.Fatalf("not sortable: %s %s", TimeStr(whole), TimeStr(frac))
	}

	got, err := ParseTime(TimeStr(frac))
	if err != nil || !got.Equal(frac) {
		t.Fatalf("roundtrip: %v %v", got, err)
	}
	zone := time.FixedZone("x", 3600)
	if TimeStr(whole.In(zone)) != TimeStr(whole) {
		t.Fatal("not UTC")
	}
}

func TestSalt(t *testing.T) {
	_, path := open(t)
	if s, err := ReadSalt(path); err != nil || s != nil {
		t.Fatalf("fresh: %v %v", s, err)
	}
	want := []byte("0123456789abcdef")
	if err := WriteSalt(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSalt(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("got %q, %v", got, err)
	}
}
