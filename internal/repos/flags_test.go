package repos

import (
	"testing"
	"time"
)

func TestFlags(t *testing.T) {
	d := testDB(t)

	f, err := GetFlags(d, "a")
	must(t, err)
	if f.Job != "a" || f.PausedAt != nil || f.AckedAt != nil || f.MutedUntil != nil {
		t.Fatalf("empty: %+v", f)
	}

	must(t, SetPaused(d, "a", "arian", t0))
	must(t, SetAcked(d, "a", t0.Add(time.Minute)))
	until := t0.Add(time.Hour)
	must(t, SetMuted(d, "a", &until, false))
	must(t, SetMuted(d, "b", nil, true))

	f, _ = GetFlags(d, "a")
	if f.PausedAt == nil || !f.PausedAt.Equal(t0) || f.PausedBy != "arian" ||
		f.AckedAt == nil || f.MutedUntil == nil || !f.MutedUntil.Equal(until) || f.MuteRecover {
		t.Fatalf("set: %+v", f)
	}
	if b, _ := GetFlags(d, "b"); b.MutedUntil != nil || !b.MuteRecover {
		t.Fatalf("recover mute: %+v", b)
	}

	all, err := AllFlags(d)
	must(t, err)
	if len(all) != 2 {
		t.Fatalf("all: %+v", all)
	}

	must(t, ClearPaused(d, "a"))
	must(t, ClearAcked(d, "a"))
	must(t, ClearMuted(d, "a"))
	must(t, ClearMuted(d, "nobody"))
	f, _ = GetFlags(d, "a")
	if f.PausedAt != nil || f.PausedBy != "" || f.AckedAt != nil || f.MutedUntil != nil || f.MuteRecover {
		t.Fatalf("cleared: %+v", f)
	}
}

func TestResetRecover(t *testing.T) {
	d := testDB(t)
	until := t0.Add(time.Hour)
	must(t, SetPaused(d, "a", "x", t0))
	must(t, SetAcked(d, "a", t0))
	must(t, SetMuted(d, "a", &until, true))
	must(t, ResetRecover(d, "a"))

	f, _ := GetFlags(d, "a")
	if f.AckedAt != nil || f.MuteRecover || f.MutedUntil == nil || f.PausedAt == nil {
		t.Fatalf("reset: %+v", f)
	}
}
