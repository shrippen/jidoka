package repos

import (
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	d := testDB(t)
	id, err := AddToken(d, "andon", "hash1", []string{"read", "run"}, t0)
	must(t, err)
	if _, err := AddToken(d, "dup", "hash1", nil, t0); err == nil {
		t.Fatal("duplicate hash accepted")
	}

	got, err := TokenByHash(d, "hash1")
	must(t, err)
	if got.ID != id || got.Name != "andon" || len(got.Rights) != 2 || got.Rights[1] != "run" || got.LastUsed != nil {
		t.Fatalf("by hash: %+v", got)
	}
	if _, err := TokenByHash(d, "nope"); err != ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}

	must(t, TouchToken(d, id, t0.Add(time.Hour)))
	list, err := ListTokens(d)
	must(t, err)
	if len(list) != 1 || list[0].LastUsed == nil || !list[0].LastUsed.Equal(t0.Add(time.Hour)) {
		t.Fatalf("list: %+v", list)
	}

	must(t, DeleteToken(d, id))
	if list, _ = ListTokens(d); len(list) != 0 {
		t.Fatal("not deleted")
	}
}
