package watch

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

var t0 = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

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

type missedEv struct {
	job   string
	since time.Time
}

type env struct {
	w   *Watch
	d   *sql.DB
	clk *clock
	mu  sync.Mutex
	got []missedEv
}

func newEnv(t *testing.T, jobs ...*model.Job) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	e := &env{d: d, clk: &clock{t: t0}}
	e.w = New(Config{DB: d, Now: e.clk.Now})
	e.w.OnMissed(func(j *model.Job, since time.Time) {
		e.mu.Lock()
		e.got = append(e.got, missedEv{j.ID, since})
		e.mu.Unlock()
	})

	set := model.Set{Jobs: map[string]*model.Job{}, Commit: "c1"}
	for _, j := range jobs {
		set.Jobs[j.ID] = j
	}
	e.w.Apply(set)
	return e
}

func (e *env) events() []missedEv {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]missedEv(nil), e.got...)
}

func (e *env) tick(at time.Time) {
	e.clk.Set(at)
	e.w.Tick(at)
}

func expectJob(id string, mod func(*model.Job)) *model.Job {
	j := &model.Job{ID: id, Kind: model.KindJob, Enabled: true, Zone: time.UTC, Expect: time.Hour}
	if mod != nil {
		mod(j)
	}
	return j
}

func watchJob(id string) *model.Job {
	return &model.Job{ID: id, Kind: model.KindWatch, Enabled: true, Zone: time.UTC}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func addOK(t *testing.T, d *sql.DB, job string, at time.Time) {
	t.Helper()
	r := model.Run{Job: job, Trigger: model.TrigSchedule, State: model.RunRunning, Slot: at, Attempt: 1, Started: at}
	id, err := repos.InsertRun(d, r)
	must(t, err)
	r.ID, r.State, r.Ended = id, model.RunOK, at.Add(time.Second)
	must(t, repos.FinishRun(d, r))
}

func TestMissedAfterWindow(t *testing.T) {
	e := newEnv(t, expectJob("a", nil))

	e.tick(t0.Add(30 * time.Minute))
	if len(e.events()) != 0 {
		t.Fatal("missed inside the window")
	}

	// Window runs from first seen (t0); grace (5 min) is long over.
	e.tick(t0.Add(time.Hour))
	e.tick(t0.Add(2 * time.Hour))
	ev := e.events()
	if len(ev) != 1 || ev[0].job != "a" || !ev[0].since.Equal(t0.Add(time.Hour)) {
		t.Fatalf("events = %+v", ev)
	}
	if !e.w.State("a").Equal(t0.Add(time.Hour)) {
		t.Fatalf("State = %v", e.w.State("a"))
	}

	// An ok run ends it; the next overdue window reports again.
	addOK(t, e.d, "a", t0.Add(2*time.Hour+time.Minute))
	e.tick(t0.Add(2*time.Hour + 2*time.Minute))
	if !e.w.State("a").IsZero() {
		t.Fatal("still missed after ok")
	}
	e.tick(t0.Add(4 * time.Hour))
	if len(e.events()) != 2 {
		t.Fatalf("events = %+v", e.events())
	}
}

func TestStartupGrace(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	must(t, err)
	t.Cleanup(func() { d.Close() })

	// Seen a day ago, long overdue, process just started.
	must(t, repos.EnsureSchedule(d, "a", t0.Add(-24*time.Hour)))
	job := expectJob("a", func(j *model.Job) { j.Timeout = 10 * time.Minute; j.Jitter = 5 * time.Minute })
	clk := &clock{t: t0}
	w := New(Config{DB: d, Now: clk.Now})
	var n int
	w.OnMissed(func(*model.Job, time.Time) { n++ })
	w.Apply(model.Set{Jobs: map[string]*model.Job{"a": job}})

	w.Tick(t0.Add(19 * time.Minute)) // grace is 10 + 5 + 5 = 20 min
	if n != 0 {
		t.Fatal("missed during the grace")
	}
	w.Tick(t0.Add(20 * time.Minute))
	w.Tick(t0.Add(21 * time.Minute))
	if n != 1 {
		t.Fatalf("emitted %d times", n)
	}
}

func TestRestsWhilePaused(t *testing.T) {
	e := newEnv(t, expectJob("a", nil))
	must(t, repos.SetPaused(e.d, "a", "me", t0))

	e.tick(t0.Add(3 * time.Hour))
	if len(e.events()) != 0 || !e.w.State("a").IsZero() {
		t.Fatalf("paused job missed: %+v", e.events())
	}

	resume := t0.Add(3 * time.Hour)
	must(t, repos.ClearPaused(e.d, "a"))
	e.tick(resume)
	e.tick(resume.Add(59 * time.Minute))
	if len(e.events()) != 0 {
		t.Fatal("window did not restart on resume")
	}
	e.tick(resume.Add(time.Hour))
	if len(e.events()) != 1 {
		t.Fatalf("events = %+v", e.events())
	}
}

func TestRestsWhileDisabled(t *testing.T) {
	e := newEnv(t, expectJob("a", func(j *model.Job) { j.Enabled = false }))
	e.tick(t0.Add(5 * time.Hour))
	if len(e.events()) != 0 {
		t.Fatalf("disabled job missed: %+v", e.events())
	}
}

func TestWatchPeriod(t *testing.T) {
	e := newEnv(t, &model.Job{ID: "w", Kind: model.KindWatch, Enabled: true, Zone: time.UTC,
		Period: time.Hour, Grace: 10 * time.Minute})

	e.tick(t0.Add(time.Hour + 5*time.Minute))
	if len(e.events()) != 0 {
		t.Fatal("missed inside the grace")
	}
	e.tick(t0.Add(time.Hour + 10*time.Minute))
	if len(e.events()) != 1 {
		t.Fatalf("events = %+v", e.events())
	}
}

func TestWatchSchedule(t *testing.T) {
	e := newEnv(t, &model.Job{ID: "w", Kind: model.KindWatch, Enabled: true, Zone: time.UTC,
		Schedule: "0 * * * *", Grace: 10 * time.Minute})

	e.tick(t0.Add(time.Hour + 5*time.Minute)) // slot 11:00, answered until 11:10
	if len(e.events()) != 0 {
		t.Fatal("missed inside the grace")
	}
	e.tick(t0.Add(time.Hour + 10*time.Minute))
	if len(e.events()) != 1 {
		t.Fatalf("events = %+v", e.events())
	}
}

// weeklyWatch has an uneven schedule: Expect (as conf sets it) is the longest
// gap plus grace, yet a single missed slot must be reported at its own grace.
func weeklyWatch() *model.Job {
	return &model.Job{ID: "w", Kind: model.KindWatch, Enabled: true, Zone: time.UTC,
		Schedule: "0 3 * * 4,5", Grace: 2 * time.Hour, Expect: 6*24*time.Hour + 2*time.Hour}
}

func TestWatchScheduleMissedSlot(t *testing.T) {
	e := newEnv(t, weeklyWatch())

	thu := time.Date(2026, 11, 12, 3, 10, 0, 0, time.UTC)
	fri := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	addOK(t, e.d, "w", thu)

	e.tick(fri.Add(4*time.Hour + 30*time.Minute)) // slot 03:00, grace until 05:00
	if len(e.events()) != 0 {
		t.Fatal("missed inside the grace")
	}
	e.tick(fri.Add(5*time.Hour + 30*time.Minute))
	ev := e.events()
	if len(ev) != 1 || !ev[0].since.Equal(fri.Add(5*time.Hour)) {
		t.Fatalf("events = %+v", ev)
	}
}

func TestWatchScheduleNewWatch(t *testing.T) {
	e := newEnv(t, weeklyWatch())

	// Loaded Wed 10:00, no ping yet: the first slot is Thu 03:00.
	e.tick(time.Date(2026, 11, 11, 20, 0, 0, 0, time.UTC))
	e.tick(time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC))
	if len(e.events()) != 0 {
		t.Fatalf("missed before the first slot ran out: %+v", e.events())
	}
	e.tick(time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC))
	if len(e.events()) != 1 {
		t.Fatalf("events = %+v", e.events())
	}
}

func TestDeadlineEarlyPing(t *testing.T) {
	job := &model.Job{Kind: model.KindWatch, Zone: time.UTC, Schedule: "0 3 * * *",
		Grace: 2 * time.Hour, Expect: 26 * time.Hour}
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, time.UTC) }

	for _, c := range []struct {
		name       string
		ping, want time.Time
	}{
		{"after the slot", day(12, 3, 10), day(13, 5, 0)},
		{"just before the slot", day(12, 2, 59), day(13, 5, 0)},
		{"within grace before", day(12, 1, 30), day(13, 5, 0)},
		{"long before", day(12, 0, 30), day(12, 5, 0)},
	} {
		if got := Deadline(job, c.ping, BaseOK); !got.Equal(c.want) {
			t.Errorf("%s: Deadline = %v, want %v", c.name, got, c.want)
		}
	}
}

// A grace longer than the gap between slots must not delay the alarm: a
// ping answers its own slot, not every slot within the grace after it.
func TestDeadlineGraceOverGap(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 12, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		name, sched string
		grace       time.Duration
		ping, want  time.Time
	}{
		{"hourly, ping at the slot", "0 * * * *", 2 * time.Hour, at(10, 0), at(13, 0)},
		{"every 5 min, ping at the slot", "*/5 * * * *", time.Hour, at(10, 0), at(11, 5)},
		{"hourly, ping just before", "0 * * * *", 2 * time.Hour, at(9, 58), at(13, 0)},
	} {
		job := &model.Job{Kind: model.KindWatch, Zone: time.UTC, Schedule: c.sched, Grace: c.grace, Expect: time.Hour}
		if got := Deadline(job, c.ping, BaseOK); !got.Equal(c.want) {
			t.Errorf("%s: Deadline = %v, want %v", c.name, got, c.want)
		}
	}
}

// First sight or a resume is no ping: it answers no slot, so the next
// slot is due even if it comes soon.
func TestDueAfterFirstSight(t *testing.T) {
	job := &model.Job{ID: "w", Kind: model.KindWatch, Zone: time.UTC, Schedule: "0 3 * * *",
		Grace: 2 * time.Hour, Expect: 26 * time.Hour}
	w := New(Config{})

	seen := time.Date(2026, 11, 12, 2, 59, 0, 0, time.UTC)
	want := time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC)
	if got := w.Due(job, seen, time.Time{}); !got.Equal(want) {
		t.Errorf("first seen 02:59: Due = %v, want %v", got, want)
	}
}

// A ping during a pause, just before a slot, still answers that slot when
// the watch is resumed right after it: no false missed.
func TestDueEarlyPingBeforeResume(t *testing.T) {
	job := &model.Job{ID: "w", Kind: model.KindWatch, Zone: time.UTC, Schedule: "0 3 * * *",
		Grace: 2 * time.Hour, Expect: 26 * time.Hour}
	w := New(Config{})
	w.states["w"] = &state{resumed: time.Date(2026, 11, 12, 2, 59, 0, 0, time.UTC)}

	ping := time.Date(2026, 11, 12, 2, 58, 0, 0, time.UTC)
	want := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)
	if got := w.Due(job, time.Time{}, ping); !got.Equal(want) {
		t.Errorf("ping 02:58, resumed 02:59: Due = %v, want %v", got, want)
	}
}

func TestWatchScheduleEarlyPing(t *testing.T) {
	e := newEnv(t, &model.Job{ID: "w", Kind: model.KindWatch, Enabled: true, Zone: time.UTC,
		Schedule: "0 3 * * *", Grace: 2 * time.Hour, Expect: 26 * time.Hour})

	addOK(t, e.d, "w", time.Date(2026, 11, 12, 2, 59, 0, 0, time.UTC))
	e.tick(time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC))
	if len(e.events()) != 0 {
		t.Fatalf("false alarm after an early ping: %+v", e.events())
	}
}

func useKey(t *testing.T) {
	t.Helper()
	crypto.Init([]byte("0123456789abcdef0123456789abcdef"))
	t.Cleanup(func() { crypto.Init(nil) })
}

func TestPingLifecycle(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	token, err := e.w.Token("w")
	must(t, err)

	again, err := e.w.Token("w")
	if err != nil || again != token {
		t.Fatalf("token not stable: %q %q %v", token, again, err)
	}

	var seen []model.RunState
	e.w.OnPing(func(_ *model.Job, r model.Run) { seen = append(seen, r.State) })

	must(t, e.w.Ping(token, PingStart, "", "1.1.1.1"))
	e.clk.Set(t0.Add(time.Minute))
	must(t, e.w.Ping(token, PingOK, "done", "1.1.1.1"))

	runs, err := repos.ListRuns(e.d, "w", 10, 0)
	must(t, err)
	if len(runs) != 1 || runs[0].State != model.RunOK || runs[0].Trigger != model.TrigPing ||
		!runs[0].Started.Equal(t0) || !runs[0].Ended.Equal(t0.Add(time.Minute)) {
		t.Fatalf("runs = %+v", runs)
	}
	full, err := repos.GetRun(e.d, runs[0].ID)
	must(t, err)
	if full.Output != "done" {
		t.Fatalf("output = %q", full.Output)
	}

	// Fail without start inserts a finished run.
	must(t, e.w.Ping(token, PingFail, "disk full", "1.1.1.1"))
	runs, _ = repos.ListRuns(e.d, "w", 10, 0)
	if len(runs) != 2 || runs[0].State != model.RunFail || runs[0].Ended.IsZero() {
		t.Fatalf("runs = %+v", runs)
	}

	// A second start aborts the open one.
	must(t, e.w.Ping(token, PingStart, "", "1.1.1.1"))
	must(t, e.w.Ping(token, PingStart, "", "1.1.1.1"))
	runs, _ = repos.ListRuns(e.d, "w", 10, 0)
	if len(runs) != 4 || runs[0].State != model.RunRunning || runs[1].State != model.RunAborted {
		t.Fatalf("runs = %+v", runs)
	}

	want := []model.RunState{model.RunRunning, model.RunOK, model.RunFail, model.RunRunning, model.RunRunning}
	if len(seen) != len(want) {
		t.Fatalf("OnPing = %v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("OnPing = %v, want %v", seen, want)
		}
	}
}

func TestPingOKEndsMissed(t *testing.T) {
	useKey(t)
	e := newEnv(t, &model.Job{ID: "w", Kind: model.KindWatch, Enabled: true, Zone: time.UTC, Period: time.Hour})
	token, err := e.w.Token("w")
	must(t, err)

	e.tick(t0.Add(2 * time.Hour))
	if e.w.State("w").IsZero() {
		t.Fatal("not missed")
	}
	must(t, e.w.Ping(token, PingOK, "", "x"))
	e.tick(t0.Add(2*time.Hour + time.Second))
	if !e.w.State("w").IsZero() {
		t.Fatal("still missed after ping")
	}
}

func TestPingBodyCapped(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	token, _ := e.w.Token("w")

	must(t, e.w.Ping(token, PingOK, strings.Repeat("ä", bodyMax), "x"))
	runs, _ := repos.ListRuns(e.d, "w", 1, 0)
	full, err := repos.GetRun(e.d, runs[0].ID)
	must(t, err)
	if len(full.Output) > bodyMax || len(full.Output) < bodyMax-1 {
		t.Fatalf("output = %d bytes", len(full.Output))
	}
}

func TestUnknownToken(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"), expectJob("j", nil))

	if err := e.w.Ping("nope", PingOK, "", "x"); err != ErrUnknown {
		t.Fatalf("Ping = %v", err)
	}
	if _, err := e.w.Token("j"); err != ErrUnknown {
		t.Fatalf("Token of a script job = %v", err)
	}
	if _, err := e.w.Token("missing"); err != ErrUnknown {
		t.Fatalf("Token of nothing = %v", err)
	}
}

func TestRenewRevokes(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	old, _ := e.w.Token("w")
	fresh, err := e.w.Renew("w")
	must(t, err)

	if fresh == old {
		t.Fatal("token not renewed")
	}
	if err := e.w.Ping(old, PingOK, "", "x"); err != ErrUnknown {
		t.Fatalf("old token = %v", err)
	}
	must(t, e.w.Ping(fresh, PingOK, "", "x"))
}

func TestNoKey(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	token, _ := e.w.Token("w")

	crypto.Init(nil)
	if _, err := e.w.Token("w"); err != crypto.ErrMissingKey {
		t.Fatalf("Token without key = %v", err)
	}
	must(t, e.w.Ping(token, PingOK, "", "x")) // the hash is enough
}

func TestRateLimit(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	token, _ := e.w.Token("w")

	var ok, throttled int
	for i := 0; i < 100; i++ {
		switch err := e.w.Ping(token, PingOK, "", "9.9.9.9"); err {
		case nil:
			ok++
		case ErrThrottled:
			throttled++
		default:
			t.Fatal(err)
		}
	}
	if ok != int(pingBurst) || throttled != 100-int(pingBurst) {
		t.Fatalf("ok = %d, throttled = %d", ok, throttled)
	}

	if !e.w.Allow("8.8.8.8") {
		t.Fatal("other address limited")
	}

	e.clk.Set(t0.Add(time.Second)) // refills 10
	if !e.w.Allow("9.9.9.9") {
		t.Fatal("no refill")
	}
}

func TestPingBodyMasked(t *testing.T) {
	useKey(t)
	e := newEnv(t, watchJob("w"))
	e.w.cfg.Mask = func(s string) string { return strings.ReplaceAll(s, "hunter22", "***") }
	token, _ := e.w.Token("w")

	must(t, e.w.Ping(token, PingFail, "login hunter22 failed", "x"))
	runs, _ := repos.ListRuns(e.d, "w", 1, 0)
	full, err := repos.GetRun(e.d, runs[0].ID)
	must(t, err)
	if full.Output != "login *** failed" {
		t.Fatalf("output = %q", full.Output)
	}
}
