package repos

import (
	"testing"
	"time"

	"jidoka/internal/model"
)

func TestAlerts(t *testing.T) {
	d := testDB(t)
	if _, err := GetAlert(d, "a"); err != ErrNotFound {
		t.Fatalf("none: %v", err)
	}

	a := Alert{Job: "a", Since: t0, Streak: 1, Event: model.EvFail}
	must(t, OpenAlert(d, a))
	got, err := GetAlert(d, "a")
	must(t, err)
	if got.Streak != 1 || len(got.Sent) != 0 || got.Sent == nil || got.Event != model.EvFail ||
		!got.Since.Equal(t0) || !got.LastSlot.IsZero() || got.RunID != 0 {
		t.Fatalf("open: %+v", got)
	}

	sent := t0.Add(time.Minute)
	a.Streak, a.Event, a.LastSlot, a.RunID = 3, model.EvTimeout, sent, 7
	a.Name, a.Detail, a.Sent = "Job A", "line 3", map[int]time.Time{0: sent, 2: t0}
	must(t, OpenAlert(d, a))
	got, _ = GetAlert(d, "a")
	if got.Streak != 3 || got.Event != model.EvTimeout || !got.LastSlot.Equal(sent) || got.RunID != 7 ||
		got.Name != "Job A" || got.Detail != "line 3" ||
		len(got.Sent) != 2 || !got.Sent[0].Equal(sent) || !got.Sent[2].Equal(t0) {
		t.Fatalf("replace: %+v", got)
	}

	a.Prev = &Alert{Job: "a", Event: model.EvFail, Since: t0, Streak: 2, Sent: map[int]time.Time{1: t0}}
	must(t, OpenAlert(d, a))
	got, _ = GetAlert(d, "a")
	if got.Prev == nil || got.Prev.Event != model.EvFail || got.Prev.Streak != 2 || !got.Prev.Sent[1].Equal(t0) {
		t.Fatalf("prev: %+v", got.Prev)
	}

	if all, _ := AllAlerts(d); len(all) != 1 {
		t.Fatalf("all: %+v", all)
	}
	must(t, CloseAlert(d, "a"))
	if _, err := GetAlert(d, "a"); err != ErrNotFound {
		t.Fatalf("closed: %v", err)
	}
}
