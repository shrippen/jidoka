package repos

import (
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

func newRun(job string, at time.Time) model.Run {
	return model.Run{Job: job, Trigger: model.TrigSchedule, State: model.RunRunning,
		Slot: at, Attempt: 1, Started: at, Commit: "abc"}
}

// addDone inserts a run of job at at and finishes it in state.
func addDone(t *testing.T, q db.Queryer, job string, at time.Time, state model.RunState) int64 {
	t.Helper()
	r := newRun(job, at)
	id, err := InsertRun(q, r)
	must(t, err)

	exit := 0
	if state != model.RunOK {
		exit = 1
	}
	r.ID, r.State, r.Ended, r.Exit = id, state, at.Add(time.Second), &exit
	r.Summary, r.Output = "sum", "out"
	must(t, FinishRun(q, r))
	return id
}

func TestRunInsertFinishGet(t *testing.T) {
	d := testDB(t)
	r := newRun("backup", t0.Add(1500*time.Millisecond))
	id, err := InsertRun(d, r)
	must(t, err)

	got, err := GetRun(d, id)
	must(t, err)
	if got.State != model.RunRunning || !got.Ended.IsZero() || got.Exit != nil || !got.Started.Equal(r.Started) {
		t.Fatalf("running run: %+v", got)
	}

	exit := 3
	got.State, got.Ended, got.Exit = model.RunFail, t0.Add(time.Minute), &exit
	got.Summary, got.Message, got.Output, got.Error = "s", "m", "o", "e"
	must(t, FinishRun(d, got))

	done, err := GetRun(d, id)
	must(t, err)
	if done.State != model.RunFail || done.Exit == nil || *done.Exit != 3 ||
		done.Summary != "s" || done.Message != "m" || done.Output != "o" || done.Error != "e" ||
		!done.Ended.Equal(got.Ended) {
		t.Fatalf("finished run: %+v", done)
	}

	if _, err := GetRun(d, 999); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
}

func TestRunListPaging(t *testing.T) {
	d := testDB(t)
	var ids []int64
	for i := range 5 {
		ids = append(ids, addDone(t, d, "a", t0.Add(time.Duration(i)*time.Minute), model.RunOK))
	}
	addDone(t, d, "b", t0, model.RunOK)

	page, err := ListRuns(d, "a", 2, 0)
	must(t, err)
	if len(page) != 2 || page[0].ID != ids[4] || page[1].ID != ids[3] {
		t.Fatalf("first page: %+v", page)
	}
	if page[0].Output != "" {
		t.Fatal("list carries output")
	}

	next, err := ListRuns(d, "a", 10, page[1].ID)
	must(t, err)
	if len(next) != 3 || next[0].ID != ids[2] {
		t.Fatalf("second page: %+v", next)
	}
}

func TestLatestAndLastOK(t *testing.T) {
	d := testDB(t)
	okID := addDone(t, d, "a", t0, model.RunOK)
	failID := addDone(t, d, "a", t0.Add(time.Hour), model.RunFail)
	addDone(t, d, "b", t0, model.RunOK)

	// A dry run never counts as the latest.
	dry := newRun("a", t0.Add(2*time.Hour))
	dry.Dry = true
	_, err := InsertRun(d, dry)
	must(t, err)

	latest, err := LatestRuns(d)
	must(t, err)
	if len(latest) != 2 || latest["a"].ID != failID {
		t.Fatalf("latest: %+v", latest)
	}

	ok, err := LastOKRun(d, "a")
	must(t, err)
	if ok.ID != okID {
		t.Fatalf("last ok: %+v", ok)
	}
	if _, err := LastOKRun(d, "none"); err != ErrNotFound {
		t.Fatalf("no ok run: %v", err)
	}
}

func TestRunningAndAbort(t *testing.T) {
	d := testDB(t)
	addDone(t, d, "a", t0, model.RunOK)
	id, err := InsertRun(d, newRun("a", t0.Add(time.Hour)))
	must(t, err)

	running, err := RunningRuns(d)
	must(t, err)
	if len(running) != 1 || running[0].ID != id {
		t.Fatalf("running: %+v", running)
	}

	n, err := AbortRunning(d, t0.Add(2*time.Hour))
	must(t, err)
	got, _ := GetRun(d, id)
	if n != 1 || got.State != model.RunAborted || !got.Ended.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("aborted %d: %+v", n, got)
	}
	if running, _ = RunningRuns(d); len(running) != 0 {
		t.Fatal("still running")
	}
}

func TestRecentRuns(t *testing.T) {
	d := testDB(t)
	for i := range 4 {
		addDone(t, d, "a", t0.Add(time.Duration(i)*time.Minute), model.RunOK)
	}
	addDone(t, d, "b", t0, model.RunFail)
	addDone(t, d, "c", t0, model.RunOK)

	got, err := RecentRuns(d, []string{"a", "b"}, 3)
	must(t, err)
	if len(got["a"]) != 3 || len(got["b"]) != 1 || len(got["c"]) != 0 {
		t.Fatalf("recent: %+v", got)
	}
	if !got["a"][0].Started.After(got["a"][1].Started) {
		t.Fatal("not newest first")
	}

	if none, err := RecentRuns(d, nil, 3); err != nil || len(none) != 0 {
		t.Fatalf("no jobs: %v %v", none, err)
	}
}

func TestPruneRuns(t *testing.T) {
	d := testDB(t)
	running, err := InsertRun(d, newRun("a", t0.Add(-100*time.Hour)))
	must(t, err)
	for i := range 5 {
		addDone(t, d, "a", t0.Add(time.Duration(i)*time.Hour), model.RunOK)
	}
	addDone(t, d, "b", t0, model.RunOK)

	// Count limit: keep the newest 2 (the old running run is untouched).
	n, err := PruneRuns(d, "a", t0.Add(-1000*time.Hour), 2)
	must(t, err)
	list, _ := ListRuns(d, "a", 100, 0)
	if n != 3 || len(list) != 3 {
		t.Fatalf("by count: deleted %d, left %d", n, len(list))
	}

	// Age limit, no count limit.
	n, err = PruneRuns(d, "a", t0.Add(3*time.Hour+30*time.Minute), 0)
	must(t, err)
	list, _ = ListRuns(d, "a", 100, 0)
	if n != 1 || len(list) != 2 {
		t.Fatalf("by age: deleted %d, left %d", n, len(list))
	}
	if _, err := GetRun(d, running); err != nil {
		t.Fatal("running run pruned")
	}
	if other, _ := ListRuns(d, "b", 10, 0); len(other) != 1 {
		t.Fatal("other job pruned")
	}
}

func TestPruneGone(t *testing.T) {
	d := testDB(t)
	addDone(t, d, "live", t0, model.RunOK)
	addDone(t, d, "gone", t0, model.RunOK)
	addDone(t, d, "gone", t0.Add(48*time.Hour), model.RunOK)

	n, err := PruneGone(d, []string{"live"}, t0.Add(24*time.Hour))
	must(t, err)
	if n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if left, _ := ListRuns(d, "gone", 10, 0); len(left) != 1 {
		t.Fatalf("gone left: %d", len(left))
	}
	if left, _ := ListRuns(d, "live", 10, 0); len(left) != 1 {
		t.Fatal("live job pruned")
	}
}

func TestRunsBetween(t *testing.T) {
	d := testDB(t)
	addDone(t, d, "a", t0.Add(-time.Hour), model.RunOK)
	in := addDone(t, d, "a", t0, model.RunOK)
	addDone(t, d, "b", t0.Add(time.Minute), model.RunFail)
	addDone(t, d, "a", t0.Add(time.Hour), model.RunOK)

	got, err := RunsBetween(d, t0, t0.Add(time.Hour))
	must(t, err)
	if len(got) != 2 || got[0].ID != in || got[1].Job != "b" {
		t.Fatalf("between: %+v", got)
	}
}
