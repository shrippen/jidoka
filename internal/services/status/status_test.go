package status

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/watch"
)

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

type noExec struct{}

func (noExec) Execute(context.Context, *model.Job, model.Trigger, time.Time, int, string) model.Run {
	return model.Run{}
}

type env struct {
	t    *testing.T
	d    *sql.DB
	dir  string
	clk  *clock
	jobs *jobs.Service
	plan *plan.Planner
	w    *watch.Watch
	sec  *secrets.Service
	svc  *Service
}

func put(t *testing.T, dir, rel, content string) {
	t.Helper()

	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// job writes a job folder with extra lines in job.toml.
func (e *env) job(id, extra string) {
	e.t.Helper()
	toml := "name = \"" + id + "\"\nrun = \"run.sh\"\n"
	if !strings.Contains(extra, "schedule =") {
		toml += "schedule = \"0 6 * * *\"\n"
	}
	put(e.t, e.dir, id+"/job.toml", toml+extra)
	put(e.t, e.dir, id+"/run.sh", "#!/bin/sh\n")
}

func (e *env) watch(id, extra string) {
	e.t.Helper()
	put(e.t, e.dir, id+"/watch.toml", "name = \""+id+"\"\nperiod = \"1h\"\ngrace = \"10m\"\n"+extra)
}

// start loads /jobs and hands the set to planner and watch.
func (e *env) start() {
	e.t.Helper()
	if err := e.jobs.Load(); err != nil {
		e.t.Fatal(err)
	}

	set := e.jobs.Current()
	e.plan.Apply(set)
	e.w.Apply(set)
}

func newEnv(t *testing.T) *env {
	t.Helper()

	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	e := &env{t: t, d: d, dir: t.TempDir(), clk: &clock{t: time.Now().UTC().Truncate(time.Second)}}
	e.jobs = jobs.New(d, e.dir)
	put(t, e.dir, "jidoka.toml", "[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	e.plan = plan.New(plan.Config{DB: d, Exec: noExec{}, Now: e.clk.Now})
	e.w = watch.New(watch.Config{DB: d, Now: e.clk.Now})
	e.sec = secrets.New(d)
	e.svc = New(Deps{DB: d, Jobs: e.jobs, Plan: e.plan, Watch: e.w, Secrets: e.sec, Now: e.clk.Now, Version: "test"})
	return e
}

// run stores a finished run of job, started ago before the clock.
func (e *env) run(job string, state model.RunState, ago time.Duration, slot time.Time, summary string) int64 {
	e.t.Helper()

	started := e.clk.Now().Add(-ago)
	r := model.Run{Job: job, Trigger: model.TrigSchedule, State: state, Slot: slot, Attempt: 1, Started: started}

	id, err := repos.InsertRun(e.d, r)
	if err != nil {
		e.t.Fatal(err)
	}
	if state == model.RunRunning {
		return id
	}

	r.ID, r.Ended, r.Summary, r.Output = id, started.Add(5*time.Second), summary, "line 1\nline 2\nout of "+job
	if err := repos.FinishRun(e.d, r); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) tile(id string) Tile {
	e.t.Helper()
	b, err := e.svc.Board()
	if err != nil {
		e.t.Fatal(err)
	}

	for _, t := range b.Tiles {
		if t.Job.ID == id {
			return t
		}
	}
	e.t.Fatalf("no tile %s", id)
	return Tile{}
}

func TestStatesAndOrder(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"a-ok", "b-fail", "c-run", "d-never", "e-paused", "f-timeout", "g-aborted-only", "h-skip-over-fail"} {
		e.job(id, "")
	}
	e.job("i-disabled", "enabled = false\n")
	e.watch("j-watch", "")
	e.job("k-expect", "expect = \"1h\"\n")
	put(t, e.dir, "z-broken/job.toml", "run = \n")
	e.start()

	slot := e.clk.Now().Add(-time.Hour)
	e.run("a-ok", model.RunOK, time.Hour, slot, "11 events")
	e.run("b-fail", model.RunOK, 3*time.Hour, slot.Add(-time.Hour), "")
	e.run("b-fail", model.RunFail, time.Hour, slot, "")
	e.run("c-run", model.RunRunning, time.Minute, slot, "")
	e.run("f-timeout", model.RunTimeout, time.Hour, slot, "")
	e.run("g-aborted-only", model.RunAborted, time.Hour, slot, "")
	e.run("h-skip-over-fail", model.RunFail, 2*time.Hour, slot.Add(-time.Hour), "")
	e.run("h-skip-over-fail", model.RunSkipped, time.Hour, slot, "")
	if err := e.plan.Pause("e-paused", "me"); err != nil {
		t.Fatal(err)
	}

	// The watch window of j-watch and k-expect passes with no ping and no run.
	e.clk.Set(e.clk.Now().Add(10 * time.Hour))
	e.w.Tick(e.clk.Now())

	want := map[string]model.JobState{
		"a-ok": model.JobOK, "b-fail": model.JobFail, "c-run": model.JobRunning, "d-never": model.JobNever,
		"e-paused": model.JobPaused, "f-timeout": model.JobTimeout, "g-aborted-only": model.JobNever,
		"h-skip-over-fail": model.JobFail, "i-disabled": model.JobDisabled, "j-watch": model.JobMissed,
		"k-expect": model.JobMissed, "z-broken": model.JobInvalid,
	}

	b, err := e.svc.Board()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, tl := range b.Tiles {
		order = append(order, tl.Job.ID)
		if tl.State != want[tl.Job.ID] {
			t.Errorf("%s: state %q, want %q", tl.Job.ID, tl.State, want[tl.Job.ID])
		}
	}
	if len(b.Tiles) != len(want) {
		t.Fatalf("tiles: %v", order)
	}

	// fail, timeout, invalid; missed; running; ok; never; paused, disabled.
	wantOrder := []string{"b-fail", "f-timeout", "h-skip-over-fail", "z-broken", "j-watch", "k-expect",
		"c-run", "a-ok", "d-never", "g-aborted-only", "e-paused", "i-disabled"}
	if !slices.Equal(order, wantOrder) {
		t.Errorf("order:\n got %v\nwant %v", order, wantOrder)
	}

	if tl := e.tile("b-fail"); tl.Reason != ReasonFailed || !tl.NeedsYou || tl.Streak != 1 || tl.Last.State != model.RunFail {
		t.Errorf("b-fail: %+v", tl)
	}
	if tl := e.tile("j-watch"); tl.Reason != ReasonMissed || tl.Job.Kind != model.KindWatch || tl.Next.IsZero() {
		t.Errorf("j-watch: %+v", tl)
	}
	if tl := e.tile("z-broken"); tl.Reason != ReasonInvalid || tl.Load == nil || tl.Load.Msg == "" {
		t.Errorf("z-broken: %+v", tl)
	}
	if tl := e.tile("a-ok"); tl.Summary != "11 events" || tl.Next.IsZero() || len(tl.Ticks) != 1 || tl.LastOK == nil {
		t.Errorf("a-ok: %+v", tl)
	}
	if tl := e.tile("e-paused"); !tl.Paused || !tl.Next.IsZero() {
		t.Errorf("e-paused: %+v", tl)
	}

	if b.Counts.Total != len(want) || b.Counts.Attention != 6 || b.Counts.ByState[model.JobFail] != 2 || b.Counts.Watches != 1 {
		t.Errorf("counts: %+v", b.Counts)
	}
}

func TestStreakCountsSlotsNotRetries(t *testing.T) {
	e := newEnv(t)
	e.job("flaky", "")
	e.start()

	day := 24 * time.Hour
	slot1 := e.clk.Now().Add(-2 * day)
	slot2 := e.clk.Now().Add(-day)

	e.run("flaky", model.RunOK, 3*day, slot1.Add(-day), "")
	e.run("flaky", model.RunFail, 2*day, slot1, "")
	e.run("flaky", model.RunFail, 2*day-10*time.Minute, slot1, "")
	e.run("flaky", model.RunFail, day, slot2, "")
	e.run("flaky", model.RunAborted, time.Hour, slot2, "")

	tl := e.tile("flaky")
	if tl.State != model.JobFail || tl.Streak != 2 {
		t.Fatalf("state %s streak %d", tl.State, tl.Streak)
	}
	if want := e.clk.Now().Add(-2 * day); !tl.Since.Equal(want) {
		t.Errorf("since %v, want %v", tl.Since, want)
	}

	// The alert row, which knows J13, wins when it exists.
	err := repos.OpenAlert(e.d, repos.Alert{Job: "flaky", Event: model.EvFail, Since: tl.Since.Add(time.Minute), Streak: 7})
	if err != nil {
		t.Fatal(err)
	}
	if tl := e.tile("flaky"); tl.Streak != 7 {
		t.Errorf("alert row ignored: %d", tl.Streak)
	}
}

func TestTicksOldestFirstAndCapped(t *testing.T) {
	e := newEnv(t)
	e.job("busy", "")
	e.start()

	slot := e.clk.Now()
	for i := 0; i < tickCount+5; i++ {
		e.run("busy", model.RunOK, time.Duration(tickCount+5-i)*time.Minute, slot, "")
	}

	tl := e.tile("busy")
	if len(tl.Ticks) != tickCount {
		t.Fatalf("ticks %d", len(tl.Ticks))
	}
	if !tl.Ticks[0].Start.Before(tl.Ticks[tickCount-1].Start) || tl.Last.ID != tl.Ticks[tickCount-1].ID {
		t.Error("ticks not oldest first")
	}
}

func TestOverviewHasTailsBoardNot(t *testing.T) {
	e := newEnv(t)
	e.job("talk", "")
	e.start()

	var out strings.Builder
	for i := 0; i < 30; i++ {
		out.WriteString("line\n")
	}
	id := e.run("talk", model.RunOK, time.Minute, e.clk.Now(), "")
	r, _ := repos.GetRun(e.d, id)
	r.Output = out.String() + "last"
	if err := repos.FinishRun(e.d, r); err != nil {
		t.Fatal(err)
	}

	b, _ := e.svc.Board()
	if b.Tiles[0].Tail != "" {
		t.Error("board carries a tail")
	}

	o, err := e.svc.Overview()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(o.Tiles[0].Tail, "\n")
	if len(lines) != tailLines || lines[len(lines)-1] != "last" {
		t.Errorf("tail has %d lines, ends %q", len(lines), lines[len(lines)-1])
	}
}

func TestRemovedJobs(t *testing.T) {
	e := newEnv(t)
	e.job("alive", "")
	e.start()

	e.run("alive", model.RunOK, time.Hour, e.clk.Now(), "")
	e.run("gone", model.RunOK, 2*time.Hour, e.clk.Now(), "")
	e.run("gone", model.RunFail, time.Hour, e.clk.Now(), "")

	got, err := e.svc.Removed()
	if err != nil || len(got) != 1 || got[0].ID != "gone" || got[0].Runs != 2 {
		t.Fatalf("removed: %+v %v", got, err)
	}

	p, err := e.svc.Job("gone", Page{})
	if err != nil || !p.Removed || len(p.Runs) != 2 {
		t.Errorf("removed page: %+v %v", p, err)
	}
	if _, err := e.svc.Job("never-existed", Page{}); err != ErrNotFound {
		t.Errorf("unknown job: %v", err)
	}
}

func TestTimetableAcrossDST(t *testing.T) {
	e := newEnv(t)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	put(t, e.dir, "jidoka.toml", "zone = \"Europe/Berlin\"\n[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	e.job("nightly", "schedule = \"30 2 * * *\"\nzone = \"Europe/Berlin\"\n")
	e.start()

	// 2027-03-28: 02:00 jumps to 03:00. Today is that Sunday.
	e.clk.Set(time.Date(2027, 3, 26, 12, 0, 0, 0, time.UTC))
	day := time.Date(2027, 3, 28, 15, 0, 0, 0, berlin)

	tt, err := e.svc.Timetable(day)
	if err != nil {
		t.Fatal(err)
	}
	if !tt.Day.Equal(time.Date(2027, 3, 28, 0, 0, 0, 0, berlin)) ||
		!tt.From.Equal(time.Date(2027, 3, 27, 0, 0, 0, 0, berlin)) ||
		!tt.To.Equal(time.Date(2027, 3, 30, 0, 0, 0, 0, berlin)) {
		t.Fatalf("window: %v .. %v (day %v)", tt.From, tt.To, tt.Day)
	}

	var lane Lane
	for _, l := range tt.Lanes {
		if l.ID == "nightly" {
			lane = l
		}
	}
	var got []string
	for _, p := range lane.Planned {
		got = append(got, p.In(berlin).Format("2006-01-02 15:04"))
	}
	want := []string{"2027-03-27 02:30", "2027-03-28 03:00", "2027-03-29 02:30"}
	if !slices.Equal(got, want) || lane.Dense {
		t.Errorf("planned %v, want %v", got, want)
	}
}

func TestTimetableRunsFromNowAndDense(t *testing.T) {
	e := newEnv(t)
	e.job("tick", "schedule = \"* * * * *\"\n")
	e.job("quiet", "")
	e.start()

	now := e.clk.Now()
	e.run("tick", model.RunOK, time.Hour, now, "")
	e.run("quiet", model.RunOK, 5*time.Hour, now, "")

	tt, err := e.svc.Timetable(now)
	if err != nil {
		t.Fatal(err)
	}

	lanes := map[string]Lane{}
	for _, l := range tt.Lanes {
		lanes[l.ID] = l
	}
	if l := lanes["tick"]; !l.Dense || len(l.Planned) != timetableCap || len(l.Runs) != 1 {
		t.Errorf("tick lane: dense %v planned %d runs %d", l.Dense, len(l.Planned), len(l.Runs))
	}
	if l := lanes["tick"]; l.Planned[0].Before(now) {
		t.Error("planned slot in the past")
	}
	if l := lanes["quiet"]; l.Dense || len(l.Runs) != 1 {
		t.Errorf("quiet lane: %+v", l)
	}
}

func TestJobPage(t *testing.T) {
	crypto.Init(crypto.Derive("k", []byte("salt-salt-salt-salt")))
	t.Cleanup(func() { crypto.Init(nil) })

	e := newEnv(t)
	put(t, e.dir, "jidoka.toml", "[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	e.job("own", "secrets = [\"HAVE\", \"LACK\"]\n[[notify]]\non = [\"fail\", \"recover\"]\nchannels = [\"mail\"]\n")
	e.job("prof", "notify_profile = \"standard\"\n")
	e.watch("beat", "")
	e.start()

	if err := e.sec.Set("HAVE", "value-of-have", "me"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		e.run("own", model.RunOK, time.Duration(10-i)*time.Minute, e.clk.Now(), "")
	}

	p, err := e.svc.Job("own", Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if p.Rules.From != RulesJob || p.Rules.File != "own/job.toml" || len(p.Rules.List) != 1 || len(p.Rules.List[0].On) != 2 {
		t.Errorf("rules: %+v", p.Rules)
	}
	if len(p.Runs) != 2 || p.NextBefore != p.Runs[1].ID || p.Runs[0].Output != "" {
		t.Errorf("runs: %d next %d", len(p.Runs), p.NextBefore)
	}
	if !slices.Equal(p.Secrets, []SecretRef{{"HAVE", true}, {"LACK", false}}) {
		t.Errorf("secrets: %+v", p.Secrets)
	}
	if len(p.Durations) != 5 || p.Ping != nil {
		t.Errorf("durations %d, ping %v", len(p.Durations), p.Ping)
	}

	// The next page ends the list.
	next, err := e.svc.Job("own", Page{Limit: 2, Before: p.NextBefore})
	if err != nil || len(next.Runs) != 2 || next.NextBefore == 0 {
		t.Errorf("page 2: %d %d %v", len(next.Runs), next.NextBefore, err)
	}
	last, _ := e.svc.Job("own", Page{Limit: 2, Before: next.NextBefore})
	if len(last.Runs) != 1 || last.NextBefore != 0 {
		t.Errorf("page 3: %d %d", len(last.Runs), last.NextBefore)
	}

	prof, _ := e.svc.Job("prof", Page{})
	if prof.Rules.From != RulesProfile || prof.Rules.Profile != "standard" || len(prof.Rules.List) != 1 {
		t.Errorf("profile rules: %+v", prof.Rules)
	}

	w, err := e.svc.Job("beat", Page{})
	if err != nil || w.Ping == nil || w.Ping.NeedsKey || !strings.HasPrefix(w.Ping.Path, "/ping/") {
		t.Fatalf("watch page: %+v %v", w.Ping, err)
	}

	crypto.Init(nil)
	w, _ = e.svc.Job("beat", Page{})
	if w.Ping == nil || !w.Ping.NeedsKey || w.Ping.Token != "" {
		t.Errorf("without key: %+v", w.Ping)
	}
}

func TestRunPage(t *testing.T) {
	e := newEnv(t)
	e.job("alpha", "")
	e.start()

	a := e.run("alpha", model.RunOK, 3*time.Minute, e.clk.Now(), "")
	b := e.run("alpha", model.RunFail, 2*time.Minute, e.clk.Now(), "")
	c := e.run("alpha", model.RunOK, time.Minute, e.clk.Now(), "")
	if _, err := repos.AddDelivery(e.d, model.Delivery{Job: "alpha", RunID: &b, Event: model.EvFail, Channel: "mail", At: e.clk.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}

	p, err := e.svc.Run(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Older != a || p.Newer != c || len(p.Deliveries) != 1 || p.Job == nil || p.Name != "alpha" ||
		!strings.Contains(p.Run.Output, "out of alpha") {
		t.Errorf("run page: %+v", p)
	}
	if _, err := e.svc.Run(9999); err != ErrNotFound {
		t.Errorf("unknown run: %v", err)
	}
}

func TestSecretsPage(t *testing.T) {
	crypto.Init(crypto.Derive("k", []byte("salt-salt-salt-salt")))
	t.Cleanup(func() { crypto.Init(nil) })

	e := newEnv(t)
	put(t, e.dir, "jidoka.toml", "heartbeat = \"5m\"\n[channels.apprise]\nurl = \"http://x/notify\"\n[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	e.job("one", "secrets = [\"SHARED\", \"MISSING_ONE\"]\n")
	e.job("two", "secrets = [\"SHARED\"]\n")
	e.start()

	for _, n := range []string{"SHARED", "LEFTOVER", "ANDON_HOOK_URL"} {
		if err := e.sec.Set(n, "value-"+n, "me"); err != nil {
			t.Fatal(err)
		}
	}

	p, err := e.svc.Secrets()
	if err != nil {
		t.Fatal(err)
	}

	rows := map[string]SecretRow{}
	for _, r := range p.Rows {
		rows[r.Name] = r
	}
	if r := rows["SHARED"]; !r.Set || !slices.Equal(r.UsedBy, []string{"one", "two"}) || r.Missing || r.Unused {
		t.Errorf("SHARED: %+v", r)
	}
	if r := rows["MISSING_ONE"]; r.Set || !r.Missing {
		t.Errorf("MISSING_ONE: %+v", r)
	}
	if r := rows["LEFTOVER"]; !r.Unused {
		t.Errorf("LEFTOVER: %+v", r)
	}
	if r := rows["ANDON_HOOK_URL"]; r.Unused || r.Own != OwnAndon {
		t.Errorf("ANDON_HOOK_URL: %+v", r)
	}

	// Heartbeat and Apprise are configured, so their secrets are needed.
	want := []string{"APPRISE_KEY", "HEARTBEAT_URL", "MISSING_ONE"}
	if !slices.Equal(p.Missing, want) || !slices.Equal(p.Unused, []string{"LEFTOVER"}) {
		t.Errorf("missing %v unused %v", p.Missing, p.Unused)
	}
	if !p.KeyReady || len(p.Audit) != 3 || p.Audit[0].Actor != "me" {
		t.Errorf("audit %d ready %v", len(p.Audit), p.KeyReady)
	}
}

func TestChannelsPage(t *testing.T) {
	e := newEnv(t)
	put(t, e.dir, "jidoka.toml", `
quiet = "22:00-07:00"
[channels.apprise]
url  = "http://x/notify"
tags = { handy = "handy" }
[[profiles.standard]]
on = ["fail"]
channels = ["apprise"]
`)
	e.job("a", "")
	e.start()

	at := e.clk.Now()
	for _, d := range []model.Delivery{
		{Job: "a", Event: model.EvFail, Channel: "apprise:handy", At: at.Add(-time.Hour), OK: true},
		{Job: "a", Event: model.EvFail, Channel: "apprise", At: at, OK: false, Error: "boom"},
	} {
		if _, err := repos.AddDelivery(e.d, d); err != nil {
			t.Fatal(err)
		}
	}
	_, err := repos.AddOutbox(e.d, repos.OutboxItem{Created: at, Due: at, Job: "a", Event: model.EvFail,
		Channel: "mail", Payload: "TEXT OF THE MESSAGE", LastError: "later"})
	if err != nil {
		t.Fatal(err)
	}

	p, err := e.svc.Channels()
	if err != nil {
		t.Fatal(err)
	}

	ap := p.Rows[0]
	if ap.Kind != "apprise" || !ap.Configured || !slices.Equal(ap.Subs, []string{"handy"}) ||
		ap.OK30 != 1 || ap.Failed30 != 1 || ap.Last == nil || ap.Last.Error != "boom" {
		t.Errorf("apprise row: %+v", ap)
	}
	if mail := p.Rows[1]; mail.Configured || mail.Last != nil {
		t.Errorf("mail row: %+v", mail)
	}
	if len(p.Profiles) != 1 || p.Quiet == nil || len(p.Recent) != 2 || len(p.Outbox) != 1 || p.Outbox[0].LastError != "later" {
		t.Errorf("page: %+v", p)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	e.job("a", "")
	put(t, e.dir, "bad/job.toml", "run = \n")
	e.start()

	e.plan.Tick(e.clk.Now())
	st := e.svc.Status()
	if st.Version != "test" || st.Invalid != 1 || st.LastTick.IsZero() || st.ReloadedAt.IsZero() {
		t.Errorf("status: %+v", st)
	}
}
