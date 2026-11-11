package repos

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	d := testDB(t)

	must(t, EnsureSchedule(d, "a", t0))
	must(t, EnsureSchedule(d, "a", t0.Add(time.Hour))) // keeps the first date
	if _, ok, err := LastSlot(d, "a"); err != nil || ok {
		t.Fatalf("no slot yet: %v %v", ok, err)
	}
	if _, ok, _ := LastSlot(d, "none"); ok {
		t.Fatal("unknown job has a slot")
	}

	slot := t0.Add(5 * time.Minute)
	must(t, SetLastSlot(d, "a", slot))
	must(t, SetLastSlot(d, "b", slot)) // no row yet
	got, ok, err := LastSlot(d, "a")
	if err != nil || !ok || !got.Equal(slot) {
		t.Fatalf("slot: %v %v %v", got, ok, err)
	}

	all, err := AllSchedules(d)
	must(t, err)
	if len(all) != 2 || !all["a"].FirstSeen.Equal(t0) || !all["b"].FirstSeen.Equal(slot) {
		t.Fatalf("all: %+v", all)
	}
}
