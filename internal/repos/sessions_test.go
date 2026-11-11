package repos

import (
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	d := testDB(t)
	s := Session{Hash: "h1", Sub: "u1", Name: "Arian", Created: t0, LastSeen: t0, Expires: t0.Add(time.Hour)}
	must(t, AddSession(d, s))

	got, err := SessionByHash(d, "h1")
	must(t, err)
	if got.Sub != "u1" || got.Name != "Arian" || !got.Expires.Equal(t0.Add(time.Hour)) {
		t.Fatalf("by hash: %+v", got)
	}
	if _, err := SessionByHash(d, "nope"); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}

	must(t, TouchSession(d, "h1", t0.Add(time.Minute), t0.Add(2*time.Hour)))
	got, _ = SessionByHash(d, "h1")
	if !got.LastSeen.Equal(t0.Add(time.Minute)) || !got.Expires.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("touch: %+v", got)
	}

	must(t, DeleteExpiredSessions(d, t0.Add(time.Hour)))
	if _, err := SessionByHash(d, "h1"); err != nil {
		t.Fatalf("a running session was dropped: %v", err)
	}
	must(t, DeleteExpiredSessions(d, t0.Add(3*time.Hour)))
	if _, err := SessionByHash(d, "h1"); err != ErrNotFound {
		t.Fatalf("expired session kept: %v", err)
	}

	must(t, AddSession(d, s))
	must(t, DeleteSession(d, "h1"))
	if _, err := SessionByHash(d, "h1"); err != ErrNotFound {
		t.Fatal("not deleted")
	}
}
