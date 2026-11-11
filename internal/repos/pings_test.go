package repos

import "testing"

func TestPings(t *testing.T) {
	d := testDB(t)
	must(t, PutPing(d, "feed", "h1", []byte("e1"), t0))
	must(t, PutPing(d, "feed", "h2", []byte("e2"), t0)) // renew

	if _, err := JobByPing(d, "h1"); err != ErrNotFound {
		t.Fatalf("old hash: %v", err)
	}
	job, err := JobByPing(d, "h2")
	must(t, err)
	if job != "feed" {
		t.Fatalf("job: %q", job)
	}

	p, err := GetPing(d, "feed")
	must(t, err)
	if p.Hash != "h2" || string(p.Enc) != "e2" {
		t.Fatalf("get: %+v", p)
	}

	must(t, ReplacePingEnc(d, "feed", []byte("e3")))
	if all, _ := AllPings(d); len(all) != 1 || string(all[0].Enc) != "e3" {
		t.Fatalf("all: %+v", all)
	}

	must(t, DeletePing(d, "feed"))
	if _, err := GetPing(d, "feed"); err != ErrNotFound {
		t.Fatalf("deleted: %v", err)
	}
}
