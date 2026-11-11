package runs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/drivers/proc"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/secrets"
)

// The test binary doubles as the shim, like cmd/jidoka does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == proc.ShimArg {
		os.Exit(proc.Shim(os.Args[2:]))
	}
	os.Exit(m.Run())
}

type env struct {
	d    *sql.DB
	sec  *secrets.Service
	svc  *Service
	data string
}

func setup(t *testing.T) env {
	t.Helper()

	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	crypto.Init(crypto.Derive("test-key", []byte("test-salt-0123456789")))
	t.Cleanup(func() { crypto.Init(nil) })

	data := t.TempDir()
	sec := secrets.New(d)

	return env{d: d, sec: sec, data: data, svc: New(d, sec, Config{DataDir: data})}
}

// job writes body as run.sh and returns a job for it.
func job(t *testing.T, body string) *model.Job {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	return &model.Job{
		ID: "demo", Dir: dir, Run: "run.sh", Zone: time.UTC,
		Timeout: 10 * time.Second, Enabled: true,
	}
}

func (e env) exec(j *model.Job, trig model.Trigger) model.Run {
	return e.svc.Execute(context.Background(), j, trig, time.Now(), 1, "abc1234")
}

func TestOKWithSummaryAndNotify(t *testing.T) {
	e := setup(t)
	j := job(t, `echo hello
echo "3 events synced
second line" > "$JIDOKA_SUMMARY"
echo "careful now" > "$JIDOKA_NOTIFY"
`)

	r := e.exec(j, model.TrigManual)
	if r.State != model.RunOK || r.Exit == nil || *r.Exit != 0 {
		t.Fatalf("run: %+v", r)
	}
	if r.Summary != "3 events synced" || strings.TrimSpace(r.Message) != "careful now" {
		t.Errorf("summary %q message %q", r.Summary, r.Message)
	}
	if !strings.Contains(r.Output, "hello") || r.Commit != "abc1234" {
		t.Errorf("run: %+v", r)
	}

	stored, err := repos.GetRun(e.d, r.ID)
	if err != nil || stored.State != model.RunOK || stored.Summary != r.Summary {
		t.Errorf("stored: %+v %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(e.data, "tmp", "run-"+itoa(r.ID))); !os.IsNotExist(err) {
		t.Errorf("TMPDIR not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.data, "state", "demo")); err != nil {
		t.Errorf("state dir: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestFailExit(t *testing.T) {
	e := setup(t)
	r := e.exec(job(t, "echo bad; exit 3\n"), model.TrigSchedule)

	if r.State != model.RunFail || r.Exit == nil || *r.Exit != 3 {
		t.Fatalf("run: %+v", r)
	}
}

func TestTimeout(t *testing.T) {
	e := setup(t)
	j := job(t, "sleep 30\n")
	j.Timeout = 300 * time.Millisecond

	r := e.exec(j, model.TrigSchedule)
	if r.State != model.RunTimeout {
		t.Fatalf("run: %+v", r)
	}
}

func TestMissingSecretDoesNotStart(t *testing.T) {
	e := setup(t)
	marker := filepath.Join(t.TempDir(), "started")
	j := job(t, "touch "+marker+"\n")
	j.Secrets = []string{"NOPE_ONE", "NOPE_TWO"}

	r := e.exec(j, model.TrigSchedule)
	if r.State != model.RunFail || !strings.Contains(r.Error, "NOPE_ONE") || !strings.Contains(r.Error, "NOPE_TWO") {
		t.Fatalf("run: %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("script was started")
	}
}

func TestMissingMasterKey(t *testing.T) {
	e := setup(t)
	marker := filepath.Join(t.TempDir(), "started")
	j := job(t, "touch "+marker+"\n")
	j.Secrets = []string{"ANY"}
	crypto.Init(nil)

	r := e.exec(j, model.TrigSchedule)
	if r.State != model.RunFail || r.Error != "MASTER_KEY fehlt" {
		t.Fatalf("run: %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("script was started")
	}
}

func TestSecretsMasked(t *testing.T) {
	e := setup(t)
	const mine, other = "hunter2-hunter2", "other-secret-value"
	for name, v := range map[string]string{"MINE": mine, "OTHER": other} {
		if err := e.sec.Set(name, v, "test"); err != nil {
			t.Fatal(err)
		}
	}

	b64 := base64.StdEncoding.EncodeToString([]byte(mine))
	j := job(t, "echo \"plain $MINE\"\necho "+b64+"\necho "+other+"\necho \"$MINE\" > \"$JIDOKA_SUMMARY\"\n")
	j.Secrets = []string{"MINE"}

	r := e.exec(j, model.TrigManual)
	if r.State != model.RunOK {
		t.Fatalf("run: %+v", r)
	}

	stored, _ := repos.GetRun(e.d, r.ID)
	for _, text := range []string{r.Output, r.Summary, stored.Output, stored.Summary} {
		for _, leak := range []string{mine, b64, other} {
			if strings.Contains(text, leak) {
				t.Errorf("%q leaks in %q", leak, text)
			}
		}
	}
	if !strings.Contains(r.Output, "plain ***") {
		t.Errorf("output: %q", r.Output)
	}
}

func TestCleanEnv(t *testing.T) {
	e := setup(t)
	t.Setenv("LEAK_ME", "x")
	if err := e.sec.Set("TOKEN", "token-value-1", "test"); err != nil {
		t.Fatal(err)
	}

	j := job(t, "env\n")
	j.Secrets = []string{"TOKEN"}
	j.Env = map[string]string{"MY_URL": "http://x"}

	r := e.exec(j, model.TrigManual)
	got := map[string]bool{}
	for _, line := range strings.Split(r.Output, "\n") {
		k, _, ok := strings.Cut(line, "=")
		if ok {
			got[k] = true
		}
	}

	allowed := []string{
		"PATH", "HOME", "TZ", "LANG", "MY_URL", "TOKEN", "JIDOKA_JOB", "JIDOKA_RUN",
		"JIDOKA_TRIGGER", "JIDOKA_LAST_OK", "JIDOKA_STATE", "JIDOKA_SUMMARY",
		"JIDOKA_NOTIFY", "TMPDIR",
	}
	for _, k := range allowed {
		if !got[k] {
			t.Errorf("%s missing in env", k)
		}
		delete(got, k)
	}
	// The shell itself adds PWD, SHLVL, _ and OLDPWD.
	for _, k := range []string{"PWD", "SHLVL", "_", "OLDPWD"} {
		delete(got, k)
	}
	for k := range got {
		t.Errorf("unexpected env %s", k)
	}
	if !strings.Contains(r.Output, "JIDOKA_TRIGGER=manual") || !strings.Contains(r.Output, "PATH=/usr/local/bin:/usr/bin:/bin") {
		t.Errorf("output: %q", r.Output)
	}
}

func TestLastOK(t *testing.T) {
	e := setup(t)
	j := job(t, "echo \"last=$JIDOKA_LAST_OK\"\n")

	first := e.exec(j, model.TrigManual)
	if !strings.Contains(first.Output, "last=\n") {
		t.Errorf("first: %q", first.Output)
	}

	second := e.exec(j, model.TrigManual)
	if !strings.Contains(second.Output, "last=20") {
		t.Errorf("second: %q", second.Output)
	}
}

func TestDry(t *testing.T) {
	e := setup(t)
	r := e.exec(job(t, "echo $JIDOKA_TRIGGER\n"), model.TrigDry)

	if !r.Dry || !strings.Contains(r.Output, "dry") {
		t.Fatalf("run: %+v", r)
	}
	if stored, _ := repos.GetRun(e.d, r.ID); !stored.Dry {
		t.Error("stored run not dry")
	}
}

func TestCancel(t *testing.T) {
	e := setup(t)
	j := job(t, "sleep 30\n")

	started := make(chan model.Run, 1)
	e.svc.OnStart(func(_ *model.Job, r model.Run) { started <- r })
	done := make(chan model.Run, 1)
	e.svc.OnDone(func(_ *model.Job, r model.Run) { done <- r })

	go e.exec(j, model.TrigManual)

	r := <-started
	if !e.svc.Cancel(r.ID) {
		t.Fatal("Cancel: not running")
	}

	select {
	case got := <-done:
		if got.State != model.RunAborted {
			t.Errorf("state %s", got.State)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no end")
	}
	if e.svc.Cancel(r.ID) {
		t.Error("Cancel after the end")
	}
}

func TestCancelAll(t *testing.T) {
	e := setup(t)
	started := make(chan model.Run, 1)
	e.svc.OnStart(func(_ *model.Job, r model.Run) { started <- r })
	out := make(chan model.Run, 1)

	go func() { out <- e.exec(job(t, "sleep 30\n"), model.TrigManual) }()

	<-started
	e.svc.CancelAll()

	select {
	case got := <-out:
		if got.State != model.RunAborted {
			t.Errorf("state %s", got.State)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no end")
	}
}

func TestRecover(t *testing.T) {
	e := setup(t)
	id, err := repos.InsertRun(e.d, model.Run{
		Job: "demo", Trigger: model.TrigSchedule, State: model.RunRunning,
		Slot: time.Now(), Attempt: 1, Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if r, _ := repos.GetRun(e.d, id); r.State != model.RunAborted {
		t.Errorf("state %s", r.State)
	}
}

// The jobs user must pass through state/ and tmp/ to its own folders,
// also after Recover cleared tmp/ and under the image's umask 077 (Z3).
func TestParentsTraversable(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	e := setup(t)
	if err := e.svc.Recover(); err != nil {
		t.Fatal(err)
	}
	if r := e.exec(job(t, "exit 0\n"), model.TrigManual); r.State != model.RunOK {
		t.Fatalf("run: %s %s", r.State, r.Error)
	}

	for _, d := range []string{"state", "home", "tmp"} {
		fi, err := os.Stat(filepath.Join(e.data, d))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o011 != 0o011 {
			t.Errorf("%s: mode %o, others cannot pass", d, fi.Mode().Perm())
		}
	}
}

func TestHomePerJob(t *testing.T) {
	e := setup(t)
	j := job(t, "echo \"home=$HOME\"\n")

	r := e.exec(j, model.TrigManual)
	want := "home=" + filepath.Join(e.data, "home", "demo")
	if !strings.Contains(r.Output, want) {
		t.Errorf("output %q, want %q", r.Output, want)
	}
	if _, err := os.Stat(filepath.Join(e.data, "home", "demo")); err != nil {
		t.Errorf("home dir: %v", err)
	}
}
