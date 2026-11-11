package alerts

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

func newDBStore(t *testing.T) (Store, func(job string)) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	ack := func(job string) {
		until := time.Now().Add(time.Hour)
		if err := repos.SetAcked(d, job, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := repos.SetMuted(d, job, &until, true); err != nil {
			t.Fatal(err)
		}
	}
	return NewDBStore(d), ack
}

func TestDBStoreDisturbance(t *testing.T) {
	s, _ := newDBStore(t)

	if d, err := s.Disturbance("a"); d != nil || err != nil {
		t.Fatalf("missing: %v %v", d, err)
	}

	t0 := time.Date(2026, 11, 11, 12, 0, 0, 5, time.UTC)
	in := Disturbance{
		Job: "a", Event: model.EvFail, Since: t0, Streak: 2, LastSlot: t0.Add(time.Hour),
		RunID: 9, Name: "Job A", Detail: "x.toml:3", Sent: map[int]time.Time{0: t0, 1: t0.Add(time.Minute)},
	}
	if err := s.SaveDisturbance(in); err != nil {
		t.Fatal(err)
	}

	got, err := s.Disturbance("a")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if !reflect.DeepEqual(*got, in) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", *got, in)
	}

	in.Streak = 3
	in.Sent = map[int]time.Time{}
	if err := s.SaveDisturbance(in); err != nil {
		t.Fatal(err)
	}
	all, err := s.Disturbances()
	if err != nil || len(all) != 1 || all[0].Streak != 3 || len(all[0].Sent) != 0 {
		t.Fatalf("all: %+v %v", all, err)
	}

	if err := s.CloseDisturbance("a"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Disturbance("a"); d != nil {
		t.Fatalf("closed: %+v", d)
	}
}

func TestDBStoreFlags(t *testing.T) {
	s, ack := newDBStore(t)

	if f, err := s.Flags("a"); err != nil || f.AckedAt != nil {
		t.Fatalf("none: %+v %v", f, err)
	}
	ack("a")
	if err := s.ResetFlags("a"); err != nil {
		t.Fatal(err)
	}

	f, _ := s.Flags("a")
	if f.AckedAt != nil || f.MuteRecover || f.MutedUntil == nil {
		t.Fatalf("reset: %+v", f)
	}
}

func TestDBStoreOutbox(t *testing.T) {
	s, _ := newDBStore(t)
	t0 := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	run := int64(4)
	id, err := s.OutboxAdd(Queued{Created: t0, Due: t0, Job: "a", RunID: &run, Event: model.EvFail,
		Channel: "apprise", Payload: "{}"})
	if err != nil || id == 0 {
		t.Fatalf("add: %d %v", id, err)
	}

	list, err := s.OutboxList()
	if err != nil || len(list) != 1 || list[0].ID != id || *list[0].RunID != 4 {
		t.Fatalf("list: %+v %v", list, err)
	}

	q := list[0]
	q.Attempts, q.LastError, q.Due = 2, "boom", t0.Add(time.Hour)
	if err := s.OutboxSave(q); err != nil {
		t.Fatal(err)
	}
	list, _ = s.OutboxList()
	if list[0].Attempts != 2 || list[0].LastError != "boom" || !list[0].Due.Equal(t0.Add(time.Hour)) {
		t.Fatalf("save: %+v", list[0])
	}

	if err := s.OutboxDelete(id); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.OutboxList(); len(list) != 0 {
		t.Fatalf("deleted: %+v", list)
	}

	if err := s.AddDelivery(model.Delivery{Job: "a", Event: model.EvFail, Channel: "apprise", At: t0, OK: true}); err != nil {
		t.Fatal(err)
	}
}

// Fail, message, recover through the real store.
func TestEngineOnDBStore(t *testing.T) {
	h := newHarness(t)
	h.db, _ = newDBStore(t)
	h.eng = h.newEngine()
	j := h.job("a", nil)

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)

	if d, err := h.db.Disturbance("a"); err != nil || d == nil || len(d.Sent) == 0 {
		t.Fatalf("persisted: %+v %v", d, err)
	}

	// Restart: a new engine on the same store sends nothing again.
	h.eng = h.newEngine()
	h.advance(time.Hour)
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.advance(time.Second)
	h.wantApprise(1)

	h.advance(time.Hour)
	h.done(j, model.RunOK, day(3), AttemptFinal, nil)
	h.advance(time.Second)
	h.wantApprise(2)
	if d, _ := h.db.Disturbance("a"); d != nil {
		t.Fatalf("not closed: %+v", d)
	}
}
