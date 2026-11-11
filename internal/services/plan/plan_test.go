package plan

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type call struct {
	job     string
	trig    model.Trigger
	slot    time.Time
	attempt int
}

// fakeExec records calls; a job with a gate blocks until it is closed.
type fakeExec struct {
	mu    sync.Mutex
	calls []call
	gates map[string]chan struct{}
	state func(c call) model.RunState
}

func (f *fakeExec) Execute(_ context.Context, job *model.Job, trig model.Trigger, slot time.Time, attempt int, _ string) model.Run {
	c := call{job.ID, trig, slot, attempt}

	f.mu.Lock()
	f.calls = append(f.calls, c)
	gate := f.gates[job.ID]
	f.mu.Unlock()

	if gate != nil {
		<-gate
	}
	st := model.RunOK
	if f.state != nil {
		st = f.state(c)
	}
	return model.Run{Job: job.ID, Trigger: trig, State: st, Slot: slot, Attempt: attempt}
}

func (f *fakeExec) gate(job string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gates == nil {
		f.gates = map[string]chan struct{}{}
	}
	f.gates[job] = make(chan struct{})
	return f.gates[job]
}

func (f *fakeExec) list() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

type env struct {
	t    *testing.T
	db   *sql.DB
	clk  *clock
	exec *fakeExec
	p    *Planner
	mu   sync.Mutex
	fin  []model.Run
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newEnv builds a planner on d; a second env on the same d is a restart.
func newEnv(t *testing.T, d *sql.DB, now time.Time) *env {
	t.Helper()
	e := &env{t: t, db: d, clk: &clock{t: now}, exec: &fakeExec{}}
	e.p = New(Config{DB: d, Exec: e.exec, Now: e.clk.Now,
		Rand:    func(n int64) int64 { return 0 },
		OnFinal: func(_ *model.Job, r model.Run) { e.mu.Lock(); e.fin = append(e.fin, r); e.mu.Unlock() },
	})
	return e
}

func (e *env) finals() []model.Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]model.Run(nil), e.fin...)
}

// at sets the clock and ticks.
func (e *env) at(t time.Time) {
	e.clk.Set(t)
	e.p.Tick(t)
}

// settle waits until no run is running.
func (e *env) settle() {
	e.t.Helper()
	waitFor(e.t, func() bool {
		e.p.mu.Lock()
		defer e.p.mu.Unlock()
		return len(e.p.running) == 0
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func jobOf(id, sched string, mod func(*model.Job)) *model.Job {
	j := &model.Job{ID: id, Kind: model.KindJob, Schedule: sched, Zone: time.UTC, Enabled: true}
	if mod != nil {
		mod(j)
	}
	return j
}

func setOf(parallel int, jobs ...*model.Job) model.Set {
	s := model.Set{Settings: model.Settings{MaxParallel: parallel}, Jobs: map[string]*model.Job{}, Commit: "c1"}
	for _, j := range jobs {
		s.Jobs[j.ID] = j
	}
	return s
}

func skipped(t *testing.T, d *sql.DB, job string) []model.Run {
	t.Helper()
	list, err := repos.ListRuns(d, job, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Run
	for _, r := range list {
		if r.State == model.RunSkipped {
			out = append(out, r)
		}
	}
	return out
}

func TestDSTMarchFiresOnceAt0300(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, newDB(t), start)
	e.p.Apply(setOf(2, jobOf("a", "30 2 * * *", func(j *model.Job) { j.Zone = berlin })))

	for at := start; at.Before(start.Add(24 * time.Hour)); at = at.Add(time.Minute) {
		e.at(at)
	}
	e.settle()

	calls := e.exec.list()
	want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC) // 03:00 CEST
	if len(calls) != 1 || !calls[0].slot.Equal(want) {
		t.Fatalf("calls = %+v, want one at %v", calls, want)
	}
}

func TestDSTOctoberFiresOnce(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	start := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	e := newEnv(t, newDB(t), start)
	e.p.Apply(setOf(2, jobOf("a", "30 2 * * *", func(j *model.Job) { j.Zone = berlin })))

	for at := start; at.Before(start.Add(24 * time.Hour)); at = at.Add(time.Minute) {
		e.at(at)
	}
	e.settle()

	calls := e.exec.list()
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC) // first 02:30, CEST
	if len(calls) != 1 || !calls[0].slot.Equal(want) {
		t.Fatalf("calls = %+v, want one at %v", calls, want)
	}
}

func TestNoDoubleFireAfterRestart(t *testing.T) {
	d := newDB(t)
	job := jobOf("a", "0 * * * *", func(j *model.Job) { j.Catchup = true })

	a := newEnv(t, d, t0)
	a.p.Apply(setOf(2, job))
	a.at(t0.Add(time.Hour))
	a.settle()
	if n := len(a.exec.list()); n != 1 {
		t.Fatalf("first process: %d calls", n)
	}

	b := newEnv(t, d, t0.Add(time.Hour+30*time.Second))
	b.p.Apply(setOf(2, job))
	b.at(t0.Add(time.Hour + 30*time.Second))
	b.at(t0.Add(time.Hour + 31*time.Second))
	b.settle()
	if n := len(b.exec.list()); n != 0 {
		t.Fatalf("restarted process fired again: %+v", b.exec.list())
	}
}

func TestCatchupOnce(t *testing.T) {
	d := newDB(t)
	job := jobOf("a", "0 * * * *", func(j *model.Job) { j.Catchup = true })

	a := newEnv(t, d, t0)
	a.p.Apply(setOf(2, job)) // last slot = 10:00, then the container is off

	late := t0.Add(2*time.Hour + 30*time.Minute) // 12:30
	b := newEnv(t, d, late)
	b.p.Apply(setOf(2, job))
	b.at(late)
	b.at(late.Add(time.Second))
	b.at(late.Add(time.Minute))
	b.settle()

	calls := b.exec.list()
	want := t0.Add(2 * time.Hour) // 12:00
	if len(calls) != 1 || calls[0].trig != model.TrigCatchup || !calls[0].slot.Equal(want) {
		t.Fatalf("calls = %+v, want one catchup at %v", calls, want)
	}
}

func TestNoCatchupAdvancesSlot(t *testing.T) {
	d := newDB(t)
	job := jobOf("a", "0 * * * *", nil)

	a := newEnv(t, d, t0)
	a.p.Apply(setOf(2, job))

	late := t0.Add(2*time.Hour + 30*time.Minute)
	b := newEnv(t, d, late)
	b.p.Apply(setOf(2, job))
	b.at(late)
	b.settle()

	if n := len(b.exec.list()); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
	slot, ok, err := repos.LastSlot(d, "a")
	if err != nil || !ok || !slot.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("last slot = %v %v %v", slot, ok, err)
	}
}

func TestNewJobNoCatchup(t *testing.T) {
	e := newEnv(t, newDB(t), t0.Add(30*time.Minute))
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", func(j *model.Job) { j.Catchup = true })))
	e.at(t0.Add(30 * time.Minute))
	e.settle()
	if n := len(e.exec.list()); n != 0 {
		t.Fatalf("new job ran: %d", n)
	}
}

func TestSkippedWhenRunning(t *testing.T) {
	d := newDB(t)
	e := newEnv(t, d, t0)
	gate := e.exec.gate("a")
	e.p.Apply(setOf(2, jobOf("a", "* * * * *", nil)))

	e.at(t0.Add(time.Minute))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })
	e.at(t0.Add(2 * time.Minute))

	sk := skipped(t, d, "a")
	if len(sk) != 1 || sk[0].Trigger != model.TrigSchedule || !sk[0].Slot.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("skipped = %+v", sk)
	}
	if n := len(e.exec.list()); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}

	close(gate)
	e.settle()
}

func TestParallelLimitFIFO(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	ga, gb := e.exec.gate("a"), e.exec.gate("b")
	e.exec.gate("c")
	e.p.Apply(setOf(2, jobOf("a", "* * * * *", nil), jobOf("b", "* * * * *", nil), jobOf("c", "* * * * *", nil)))

	e.at(t0.Add(time.Minute))
	waitFor(t, func() bool { return len(e.exec.list()) == 2 })

	q := e.p.Queue()
	if len(q) != 1 || q[0].Job != "c" || !q[0].Waiting {
		t.Fatalf("queue = %+v", q)
	}

	close(gb) // frees one place: c starts
	waitFor(t, func() bool { return len(e.exec.list()) == 3 })
	if got := e.exec.list()[2].job; got != "c" {
		t.Fatalf("third = %s", got)
	}

	close(ga)
	close(e.exec.gates["c"])
	e.settle()
}

func TestFIFOOrder(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	g := e.exec.gate("blocker")
	e.p.Apply(setOf(1, jobOf("blocker", "0 0 1 1 *", nil), jobOf("x", "* * * * *", nil), jobOf("y", "* * * * *", nil)))

	must(t, e.p.RunNow("blocker", "t"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })

	// x became ready before y, so it starts first.
	e.p.mu.Lock()
	e.p.push(item{job: "y", ref: e.p.entries["y"].job, trig: model.TrigSchedule, slot: t0, attempt: 1, ready: t0})
	e.p.push(item{job: "x", ref: e.p.entries["x"].job, trig: model.TrigSchedule, slot: t0, attempt: 1, ready: t0.Add(time.Second)})
	e.p.mu.Unlock()

	close(g)
	waitFor(t, func() bool { return len(e.exec.list()) == 2 })
	e.clk.Set(t0.Add(2 * time.Second))
	e.settle()
	e.p.Tick(t0.Add(2 * time.Second))
	waitFor(t, func() bool { return len(e.exec.list()) == 3 })

	calls := e.exec.list()
	if calls[1].job != "y" || calls[2].job != "x" {
		t.Fatalf("order = %+v", calls)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestLockSerializes(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	ga := e.exec.gate("a")
	e.exec.gate("b")
	lock := func(j *model.Job) { j.Lock = "davis" }
	e.p.Apply(setOf(5, jobOf("a", "* * * * *", lock), jobOf("b", "* * * * *", lock), jobOf("c", "* * * * *", nil)))

	e.at(t0.Add(time.Minute))
	waitFor(t, func() bool { return len(e.exec.list()) == 2 })
	got := map[string]bool{}
	for _, c := range e.exec.list() {
		got[c.job] = true
	}
	if !got["a"] || !got["c"] || got["b"] {
		t.Fatalf("started = %v, want a and c, not b", got)
	}

	close(ga)
	waitFor(t, func() bool { return len(e.exec.list()) == 3 })
	close(e.exec.gates["b"])
	e.settle()
}

func TestQueueTimeoutSkips(t *testing.T) {
	d := newDB(t)
	e := newEnv(t, d, t0)
	g := e.exec.gate("blocker")
	e.p.Apply(setOf(1, jobOf("blocker", "0 0 1 1 *", nil), jobOf("x", "* * * * *", nil)))

	must(t, e.p.RunNow("blocker", "t"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })

	e.at(t0.Add(time.Minute)) // x queued
	if len(e.p.Queue()) != 1 {
		t.Fatalf("queue = %+v", e.p.Queue())
	}
	e.at(t0.Add(2*time.Minute + time.Second)) // waited over a minute

	var reasons []string
	for _, r := range skipped(t, d, "x") {
		reasons = append(reasons, r.Error)
	}
	if len(e.p.Queue()) != 0 || !contains(reasons, reasonQueue) {
		t.Fatalf("queue = %+v, reasons = %v", e.p.Queue(), reasons)
	}

	close(g)
	e.settle()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestRetriesKeepSlot(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.exec.state = func(call) model.RunState { return model.RunFail }
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", func(j *model.Job) { j.Retries = 2; j.RetryGap = time.Minute })))

	slot := t0.Add(time.Hour)
	e.at(slot)
	e.settle()
	if len(e.finals()) != 0 {
		t.Fatal("final after first attempt")
	}

	e.at(slot.Add(30 * time.Second)) // gap not over
	e.settle()
	if n := len(e.exec.list()); n != 1 {
		t.Fatalf("retry before the gap: %d calls", n)
	}

	e.at(slot.Add(time.Minute))
	e.settle()
	e.at(slot.Add(2 * time.Minute))
	e.settle()
	e.at(slot.Add(10 * time.Minute))
	e.settle()

	calls := e.exec.list()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	for i, c := range calls {
		wantTrig := model.TrigRetry
		if i == 0 {
			wantTrig = model.TrigSchedule
		}
		if c.attempt != i+1 || c.trig != wantTrig || !c.slot.Equal(slot) {
			t.Fatalf("call %d = %+v", i, c)
		}
	}
	fin := e.finals()
	if len(fin) != 1 || fin[0].Attempt != 3 {
		t.Fatalf("finals = %+v", fin)
	}
}

func TestFinalAfterSuccessOnRetry(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.exec.state = func(c call) model.RunState {
		if c.attempt == 1 {
			return model.RunFail
		}
		return model.RunOK
	}
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", func(j *model.Job) { j.Retries = 2; j.RetryGap = time.Minute })))

	slot := t0.Add(time.Hour)
	e.at(slot)
	e.settle()
	e.at(slot.Add(time.Minute))
	e.settle()
	e.at(slot.Add(5 * time.Minute))
	e.settle()

	fin := e.finals()
	if len(fin) != 1 || fin[0].Attempt != 2 || fin[0].State != model.RunOK || len(e.exec.list()) != 2 {
		t.Fatalf("finals = %+v, calls = %+v", fin, e.exec.list())
	}
}

func TestPauseResumeNoCatchup(t *testing.T) {
	d := newDB(t)
	job := jobOf("a", "0 * * * *", func(j *model.Job) { j.Catchup = true })
	e := newEnv(t, d, t0)
	e.p.Apply(setOf(2, job))

	must(t, e.p.Pause("a", "me"))
	if !e.p.Next("a").IsZero() {
		t.Fatal("paused job has a next slot")
	}
	for h := 1; h <= 3; h++ {
		e.at(t0.Add(time.Duration(h) * time.Hour))
	}
	e.settle()
	if n := len(e.exec.list()); n != 0 {
		t.Fatalf("paused job ran %d times", n)
	}

	// The pause survives a restart.
	r := newEnv(t, d, t0.Add(3*time.Hour+10*time.Minute))
	r.p.Apply(setOf(2, job))
	r.at(t0.Add(3*time.Hour + 10*time.Minute))
	r.settle()
	if n := len(r.exec.list()); n != 0 {
		t.Fatalf("restart ran a paused job: %d", n)
	}

	resume := t0.Add(3*time.Hour + 20*time.Minute)
	r.clk.Set(resume)
	must(t, r.p.Resume("a", "me"))
	r.at(resume.Add(time.Minute))
	r.settle()
	if n := len(r.exec.list()); n != 0 {
		t.Fatalf("resume caught up: %d", n)
	}

	r.at(t0.Add(4 * time.Hour))
	r.settle()
	if n := len(r.exec.list()); n != 1 {
		t.Fatalf("after resume %d calls, want 1", n)
	}
}

func TestRemovedJobNotPlanned(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.p.Apply(setOf(2, jobOf("a", "* * * * *", nil)))
	e.p.Apply(setOf(2))

	e.at(t0.Add(5 * time.Minute))
	e.settle()
	if n := len(e.exec.list()); n != 0 || !e.p.Next("a").IsZero() {
		t.Fatalf("removed job planned: %d calls", n)
	}
	if err := e.p.RunNow("a", "t"); err != ErrUnknownJob {
		t.Fatalf("RunNow = %v", err)
	}
}

func TestRunNowAndDry(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	g := e.exec.gate("a")
	e.p.Apply(setOf(2, jobOf("a", "0 0 1 1 *", nil)))

	must(t, e.p.RunNow("a", "me"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })
	if err := e.p.RunNow("a", "me"); err != ErrBusy {
		t.Fatalf("second RunNow = %v", err)
	}
	if err := e.p.DryRun("a", "me"); err != ErrBusy {
		t.Fatalf("DryRun while busy = %v", err)
	}
	close(g)
	e.settle()

	e.exec.gates = nil
	must(t, e.p.DryRun("a", "me"))
	waitFor(t, func() bool { return len(e.exec.list()) == 2 })
	e.settle()

	calls := e.exec.list()
	if calls[0].trig != model.TrigManual || calls[1].trig != model.TrigDry {
		t.Fatalf("calls = %+v", calls)
	}
	if fin := e.finals(); len(fin) != 1 {
		t.Fatalf("finals = %+v, dry run must not report", fin)
	}
}

func TestJitterDelaysStart(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.p.cfg.Rand = func(n int64) int64 { return int64(45 * time.Second) }
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", func(j *model.Job) { j.Jitter = time.Minute })))

	slot := t0.Add(time.Hour)
	e.at(slot)
	e.settle()
	if n := len(e.exec.list()); n != 0 {
		t.Fatalf("started before the jitter: %d", n)
	}
	if q := e.p.Queue(); len(q) != 1 || q[0].Waiting {
		t.Fatalf("queue = %+v", q)
	}

	e.at(slot.Add(45 * time.Second))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })
	if c := e.exec.list()[0]; !c.slot.Equal(slot) {
		t.Fatalf("slot = %v, want %v", c.slot, slot)
	}
	e.settle()
}

func TestStopWaits(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	g := e.exec.gate("a")
	e.p.Apply(setOf(2, jobOf("a", "0 0 1 1 *", nil)))
	must(t, e.p.RunNow("a", "t"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := e.p.Stop(ctx); err != context.DeadlineExceeded {
		t.Fatalf("Stop = %v", err)
	}

	close(g)
	if err := e.p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.p.RunNow("a", "t"); err != ErrStopped {
		t.Fatalf("RunNow after Stop = %v", err)
	}
}

func TestLastTick(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	if !e.p.LastTick().IsZero() {
		t.Fatal("tick before first")
	}
	e.p.Tick(t0)
	if !e.p.LastTick().Equal(t0) {
		t.Fatalf("LastTick = %v", e.p.LastTick())
	}
}

// A run cut off by a container stop is marked aborted by the runs service;
// after the restart J10 must run its slot again.
func TestCatchupAfterAbortedRun(t *testing.T) {
	d := newDB(t)
	job := jobOf("a", "0 * * * *", func(j *model.Job) { j.Catchup = true })

	a := newEnv(t, d, t0)
	g := a.exec.gate("a")
	a.p.Apply(setOf(2, job))
	slot := t0.Add(time.Hour)
	a.at(slot)
	waitFor(t, func() bool { return len(a.exec.list()) == 1 })

	// What runs does on stop: the run ends aborted.
	r := model.Run{Job: "a", Trigger: model.TrigSchedule, State: model.RunRunning, Slot: slot, Attempt: 1, Started: slot}
	id, err := repos.InsertRun(d, r)
	must(t, err)
	r.ID, r.State, r.Ended = id, model.RunAborted, slot.Add(time.Minute)
	must(t, repos.FinishRun(d, r))
	close(g)
	a.settle()

	later := slot.Add(5 * time.Minute)
	b := newEnv(t, d, later)
	b.p.Apply(setOf(2, job))
	b.at(later)
	b.at(later.Add(time.Second))
	b.settle()

	calls := b.exec.list()
	if len(calls) != 1 || calls[0].trig != model.TrigCatchup || !calls[0].slot.Equal(slot) {
		t.Fatalf("calls = %+v, want one catchup at %v", calls, slot)
	}

	// The catchup ended ok: a third start must not repeat it.
	r2 := model.Run{Job: "a", Trigger: model.TrigCatchup, State: model.RunOK, Slot: slot, Attempt: 1, Started: later}
	id, err = repos.InsertRun(d, r2)
	must(t, err)
	r2.ID, r2.Ended = id, later
	must(t, repos.FinishRun(d, r2))

	c := newEnv(t, d, later.Add(time.Minute))
	c.p.Apply(setOf(2, job))
	c.at(later.Add(time.Minute))
	c.settle()
	if n := len(c.exec.list()); n != 0 {
		t.Fatalf("repeated: %d calls", n)
	}
}

func TestManualRunOnPausedJob(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", nil)))
	must(t, e.p.Pause("a", "me"))

	must(t, e.p.RunNow("a", "me"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })
	e.settle()
	must(t, e.p.DryRun("a", "me"))
	waitFor(t, func() bool { return len(e.exec.list()) == 2 })
	e.settle()
}

func TestManualRunOnDisabledJob(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	e.p.Apply(setOf(2, jobOf("a", "0 * * * *", func(j *model.Job) { j.Enabled = false })))
	if err := e.p.RunNow("a", "me"); err != ErrDisabled {
		t.Fatalf("RunNow = %v", err)
	}
}

func TestHaltKeepsQueueDropped(t *testing.T) {
	e := newEnv(t, newDB(t), t0)
	g := e.exec.gate("blocker")
	e.p.Apply(setOf(1, jobOf("blocker", "0 0 1 1 *", nil), jobOf("x", "* * * * *", nil)))
	must(t, e.p.RunNow("blocker", "t"))
	waitFor(t, func() bool { return len(e.exec.list()) == 1 })
	e.at(t0.Add(time.Minute)) // x waits in the queue
	if len(e.p.Queue()) != 1 {
		t.Fatalf("queue = %+v", e.p.Queue())
	}

	e.p.Halt()
	e.p.Halt() // idempotent
	close(g)
	e.settle()

	if n := len(e.exec.list()); n != 1 || len(e.p.Queue()) != 0 {
		t.Fatalf("calls = %d, queue = %+v", n, e.p.Queue())
	}
}
