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
	if got.Streak != 1 || got.FirstSent != nil || got.Event != model.EvFail || !got.Since.Equal(t0) {
		t.Fatalf("open: %+v", got)
	}

	sent := t0.Add(time.Minute)
	a.Streak, a.FirstSent, a.LastSent, a.Event = 3, &sent, &sent, model.EvTimeout
	must(t, UpdateAlert(d, a))
	got, _ = GetAlert(d, "a")
	if got.Streak != 3 || got.FirstSent == nil || !got.LastSent.Equal(sent) || got.Event != model.EvTimeout {
		t.Fatalf("update: %+v", got)
	}

	if all, _ := AllAlerts(d); len(all) != 1 {
		t.Fatalf("all: %+v", all)
	}
	must(t, CloseAlert(d, "a"))
	if _, err := GetAlert(d, "a"); err != ErrNotFound {
		t.Fatalf("closed: %v", err)
	}
}
