package ops

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/watch"
)

var now = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)

type env struct {
	d    *sql.DB
	dir  string
	jobs *jobs.Service
	svc  *Service
}

func setup(t *testing.T) env {
	t.Helper()

	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	crypto.Init(crypto.Derive("k", []byte("salt-salt-salt-salt")))
	t.Cleanup(func() { crypto.Init(nil) })

	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("job/job.toml", "run = \"run.sh\"\nschedule = \"0 6 * * *\"\n")
	write("job/run.sh", "#!/bin/sh\n")
	write("beat/watch.toml", "period = \"1h\"\ngrace = \"5m\"\n")
	write("bad/job.toml", "run = \n")

	js := jobs.New(d, dir)
	if err := js.Load(); err != nil {
		t.Fatal(err)
	}
	w := watch.New(watch.Config{DB: d})
	w.Apply(js.Current())

	svc := New(Deps{DB: d, Jobs: js, Runs: runs.New(d, secrets.New(d), runs.Config{DataDir: t.TempDir()}),
		Watch: w, Now: func() time.Time { return now }})
	return env{d: d, dir: dir, jobs: js, svc: svc}
}

func audit(t *testing.T, d *sql.DB) []repos.AuditEntry {
	t.Helper()
	list, err := repos.RecentAudit(d, 10)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestAckAndMute(t *testing.T) {
	e := setup(t)

	if err := e.svc.Ack("job", "arian"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Mute("job", MuteHour, "arian"); err != nil {
		t.Fatal(err)
	}

	f, _ := repos.GetFlags(e.d, "job")
	if f.AckedAt == nil || f.MutedUntil == nil || !f.MutedUntil.Equal(now.Add(time.Hour)) || f.MuteRecover {
		t.Fatalf("flags: %+v", f)
	}

	if err := e.svc.Mute("job", MuteRecover, "arian"); err != nil {
		t.Fatal(err)
	}
	if f, _ = repos.GetFlags(e.d, "job"); f.MutedUntil != nil || !f.MuteRecover {
		t.Errorf("mute until recovery: %+v", f)
	}

	if err := e.svc.Mute("job", MuteDay, "arian"); err != nil {
		t.Fatal(err)
	}
	if f, _ = repos.GetFlags(e.d, "job"); f.MutedUntil == nil || !f.MutedUntil.Equal(now.Add(24*time.Hour)) || f.MuteRecover {
		t.Errorf("mute a day: %+v", f)
	}

	if err := e.svc.Unmute("job", "arian"); err != nil {
		t.Fatal(err)
	}
	if f, _ = repos.GetFlags(e.d, "job"); f.MutedUntil != nil || f.MuteRecover || f.AckedAt == nil {
		t.Errorf("unmute: %+v", f)
	}

	want := []string{"unmute", "mute-1d", "mute-recover", "mute-1h", "ack"}
	got := audit(t, e.d)
	if len(got) != len(want) {
		t.Fatalf("audit: %+v", got)
	}
	for i, a := range got {
		if a.Action != want[i] || a.Actor != "arian" || a.Target != "job" {
			t.Errorf("audit %d: %+v", i, a)
		}
	}
}

func TestUnknownJobAndSpan(t *testing.T) {
	e := setup(t)

	if err := e.svc.Ack("nope", "a"); err != ErrUnknownJob {
		t.Errorf("ack unknown: %v", err)
	}
	if err := e.svc.Mute("job", "2h", "a"); err != ErrBadSpan {
		t.Errorf("bad span: %v", err)
	}

	// A job that failed to load can still be acknowledged.
	if err := e.svc.Ack("bad", "a"); err != nil {
		t.Errorf("ack invalid job: %v", err)
	}
	if _, ok := ParseSpan("1d"); !ok {
		t.Error("1d not parsed")
	}
	if _, ok := ParseSpan("x"); ok {
		t.Error("x parsed")
	}
	if len(audit(t, e.d)) != 1 {
		t.Error("a refused action was audited")
	}
}

func TestReloadAudited(t *testing.T) {
	e := setup(t)

	diff, err := e.svc.Reload("api")
	if err != nil || !diff.Empty() {
		t.Fatalf("reload: %+v %v", diff, err)
	}
	if got := audit(t, e.d); len(got) != 1 || got[0].Action != "reload" || got[0].Actor != "api" {
		t.Errorf("audit: %+v", got)
	}
}

func TestCancelNotRunning(t *testing.T) {
	e := setup(t)

	if err := e.svc.Cancel(42, "a"); err != ErrNotRunning {
		t.Errorf("unknown run: %v", err)
	}

	id, err := repos.InsertRun(e.d, newRun())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(id, "a"); err != ErrNotRunning {
		t.Errorf("run of another process: %v", err)
	}
	if len(audit(t, e.d)) != 0 {
		t.Error("a refused cancel was audited")
	}
}

func TestRenewToken(t *testing.T) {
	e := setup(t)

	old, err := e.svc.d.Watch.Token("beat")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Renew("beat", "arian"); err != nil {
		t.Fatal(err)
	}

	fresh, _ := e.svc.d.Watch.Token("beat")
	if fresh == old || fresh == "" {
		t.Error("token not replaced")
	}
	if got := audit(t, e.d); len(got) != 1 || got[0].Action != "renew-token" || got[0].Target != "beat" {
		t.Errorf("audit: %+v", got)
	}
	if err := e.svc.Renew("job", "arian"); err == nil {
		t.Error("renewed the token of a job that is no watch")
	}
}

func newRun() model.Run {
	return model.Run{Job: "job", Trigger: model.TrigManual, State: model.RunRunning, Slot: now, Attempt: 1, Started: now}
}
