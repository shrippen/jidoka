package repos

import (
	"testing"
	"time"

	"jidoka/internal/model"
)

func TestStatusQueries(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

	add := func(job string, state model.RunState, out string, dry bool, n int) int64 {
		id, err := InsertRun(d, model.Run{Job: job, Trigger: model.TrigSchedule, State: model.RunRunning,
			Slot: at, Attempt: 1, Started: at.Add(time.Duration(n) * time.Minute), Dry: dry})
		if err != nil {
			t.Fatal(err)
		}
		err = FinishRun(d, model.Run{ID: id, State: state, Ended: at.Add(time.Duration(n) * time.Minute), Output: out})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	a1 := add("a", model.RunOK, "old", false, 1)
	a2 := add("a", model.RunFail, "0123456789", false, 2)
	a3 := add("a", model.RunOK, "dry output", true, 3)
	b1 := add("b", model.RunOK, "b out", false, 4)

	tails, err := LatestTails(d, 4)
	if err != nil || tails["a"] != "6789" || tails["b"] != " out" {
		t.Errorf("tails: %v %v (dry run %d must not count)", tails, err, a3)
	}

	oks, err := LastOKRuns(d)
	if err != nil || oks["a"].ID != a1 || oks["b"].ID != b1 {
		t.Errorf("last ok: %v %v", oks, err)
	}

	older, newer, err := RunNeighbours(d, "a", a2)
	if err != nil || older != a1 || newer != a3 {
		t.Errorf("neighbours: %d %d %v", older, newer, err)
	}

	jobs, err := RunJobs(d)
	if err != nil || len(jobs) != 2 || jobs[0].Job != "a" || jobs[0].Count != 3 {
		t.Errorf("run jobs: %+v %v", jobs, err)
	}
}

func TestDeliveryCounts(t *testing.T) {
	d := testDB(t)
	at := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

	for _, ok := range []bool{true, true, false} {
		if _, err := AddDelivery(d, model.Delivery{Event: model.EvFail, Channel: "mail", At: at, OK: ok}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AddDelivery(d, model.Delivery{Event: model.EvFail, Channel: "mail", At: at.AddDate(0, 0, -40), OK: true}); err != nil {
		t.Fatal(err)
	}

	got, err := DeliveryCounts(d, at.AddDate(0, 0, -30))
	if err != nil || got["mail"] != (DeliveryCount{OK: 2, Failed: 1}) {
		t.Errorf("counts: %+v %v", got, err)
	}
}
