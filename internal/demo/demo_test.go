//go:build !release

package demo

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/cron"
	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
)

const (
	testKey = "demo-test-key"
	// A Wednesday at noon.
	testDay = "2026-11-11T12:00:00Z"
)

type fixture struct {
	dir  string
	now  time.Time
	set  model.Set
	conn *sql.DB
}

// prepare seeds a demo in a temporary folder and opens its database.
func prepare(t *testing.T, lang string) fixture {
	t.Helper()
	t.Setenv(envKey, testKey)

	now, err := time.Parse(time.RFC3339, testDay)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	jobs, err := Prepare(dir, lang, now)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := db.Open(filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	return fixture{dir: dir, now: now, set: conf.Load(jobs, nil), conn: conn}
}

func (f fixture) runs(t *testing.T, id string) []model.Run {
	t.Helper()

	runs, err := repos.ListRuns(f.conn, id, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

func TestRunsForEveryJob(t *testing.T) {
	f := prepare(t, "en")

	if len(f.set.Jobs) < 8 {
		t.Fatalf("only %d jobs loaded", len(f.set.Jobs))
	}

	for id := range f.set.Jobs {
		runs := f.runs(t, id)
		if len(runs) == 0 {
			t.Errorf("job %s has no runs", id)
		}

		for _, r := range runs {
			if r.Started.After(f.now) || r.Ended.Before(r.Started) {
				t.Errorf("job %s run %d: %v .. %v", id, r.ID, r.Started, r.Ended)
			}
		}
	}
}

func TestInvalidJob(t *testing.T) {
	f := prepare(t, "en")

	var found bool
	for _, le := range f.set.Invalid {
		found = found || le.Job == "abrechnung-monat"
	}
	if !found {
		t.Fatalf("no invalid job: %+v", f.set.Invalid)
	}

	a, err := repos.GetAlert(f.conn, alerts.FileKey("abrechnung-monat"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Event != model.EvInvalid || !strings.Contains(a.Detail, "schedule") {
		t.Errorf("alert = %+v", a)
	}
}

func TestMissedWatch(t *testing.T) {
	f := prepare(t, "en")

	job := f.set.Jobs["borg-laptop"]
	if job == nil || job.Kind != model.KindWatch {
		t.Fatalf("borg-laptop = %+v", job)
	}

	ok, err := repos.LastOKRun(f.conn, "borg-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if deadline := ok.Ended.Add(job.Expect); !deadline.Before(f.now) {
		t.Errorf("last ping %v, expect %v: not overdue at %v", ok.Ended, job.Expect, f.now)
	}

	a, err := repos.GetAlert(f.conn, "borg-laptop")
	if err != nil || a.Event != model.EvMissed {
		t.Fatalf("alert = %+v, %v", a, err)
	}

	// The other watch is on time.
	ok, err = repos.LastOKRun(f.conn, "borg-nebelhorn")
	if err != nil {
		t.Fatal(err)
	}
	if f.now.Sub(ok.Ended) > f.set.Jobs["borg-nebelhorn"].Expect {
		t.Errorf("borg-nebelhorn overdue: %v", ok.Ended)
	}
}

func TestFlags(t *testing.T) {
	f := prepare(t, "en")

	flags, err := repos.AllFlags(f.conn)
	if err != nil {
		t.Fatal(err)
	}

	if flags["render-proxies"].PausedAt == nil {
		t.Error("render-proxies is not paused")
	}
	if flags["paperless-eingang"].AckedAt == nil {
		t.Error("paperless-eingang is not acknowledged")
	}
	if m := flags["nebelhorn-snapshots"].MutedUntil; m == nil || !m.After(f.now) {
		t.Error("nebelhorn-snapshots is not muted")
	}
}

func TestFailingJob(t *testing.T) {
	f := prepare(t, "en")

	newest, err := repos.GetRun(f.conn, f.runs(t, "paperless-eingang")[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !newest.State.Broken() || !strings.Contains(newest.Output, "502") {
		t.Errorf("newest run = %+v", newest)
	}

	a, err := repos.GetAlert(f.conn, "paperless-eingang")
	if err != nil {
		t.Fatal(err)
	}
	if a.Streak < 2 || a.RunID == 0 || len(a.Sent) == 0 {
		t.Errorf("alert = %+v", a)
	}
}

func TestRunningJobCatchesUp(t *testing.T) {
	f := prepare(t, "en")

	job := f.set.Jobs["immich-alben"]
	sched, err := cron.Parse(job.Schedule, job.Zone)
	if err != nil {
		t.Fatal(err)
	}

	newest := sched.Last(f.now)
	for _, r := range f.runs(t, "immich-alben") {
		if r.Slot.Equal(newest) {
			t.Fatalf("slot %v already has run %d", newest, r.ID)
		}
	}

	last, ok, err := repos.LastSlot(f.conn, "immich-alben")
	if err != nil || !ok || !last.Before(newest) {
		t.Fatalf("last slot %v (%v), newest %v: no catchup", last, err, newest)
	}
	if !job.Catchup {
		t.Error("catchup is off")
	}

	// A job without catchup has nothing to catch up on.
	last, _, _ = repos.LastSlot(f.conn, "paperless-eingang")
	pj := f.set.Jobs["paperless-eingang"]
	psched, _ := cron.Parse(pj.Schedule, pj.Zone)
	if !last.Equal(psched.Last(f.now)) {
		t.Errorf("last slot of paperless-eingang = %v", last)
	}
}

func TestTimeoutWithRetry(t *testing.T) {
	f := prepare(t, "en")

	var timeouts, retries int
	for _, r := range f.runs(t, "immich-alben") {
		if r.State == model.RunTimeout {
			timeouts++
		}
		if r.Trigger == model.TrigRetry {
			retries++
		}
	}
	if timeouts < 3 || retries < 2 {
		t.Errorf("timeouts %d, retries %d", timeouts, retries)
	}
}

// The night copy takes seven minutes, however much it copies.
func TestNightCopy(t *testing.T) {
	f := prepare(t, "en")

	runs := f.runs(t, "nachtaufnahmen")
	if len(runs) < histDays-2 {
		t.Fatalf("%d runs", len(runs))
	}

	for _, r := range runs {
		if r.Duration(f.now) != 7*time.Minute {
			t.Fatalf("run %d took %v", r.ID, r.Duration(f.now))
		}
	}
}

func TestSecrets(t *testing.T) {
	f := prepare(t, "en")

	list, err := repos.ListSecrets(f.conn)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, s := range list {
		have[s.Name] = true
	}

	used := map[string]bool{}
	for _, j := range f.set.Jobs {
		for _, n := range j.Secrets {
			used[n] = true
			if !have[n] && n != "SMB_PASSWORD_NEBELHORN" {
				t.Errorf("secret %s is used but missing", n)
			}
		}
	}
	if have["SMB_PASSWORD_NEBELHORN"] {
		t.Error("the missing secret exists")
	}
	if !have["OLD_FRITZBOX_PASSWORD"] || used["OLD_FRITZBOX_PASSWORD"] {
		t.Error("no unused secret")
	}

	// The key of the folder opens the values, as jidoka serve derives it.
	salt, err := db.ReadSalt(filepath.Join(f.dir, dbFile))
	if err != nil || salt == nil {
		t.Fatalf("salt: %v", err)
	}
	crypto.Init(crypto.Derive(testKey, salt))

	enc, err := repos.GetSecretEnc(f.conn, "KIMAI_TOKEN")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := crypto.Decrypt(enc, crypto.PurposeSecret); err != nil || v == "" {
		t.Errorf("decrypt: %q, %v", v, err)
	}
}

// Nothing the demo configures can reach a real host.
func TestChannelsCannotSend(t *testing.T) {
	f := prepare(t, "en")

	s := f.set.Settings
	if s.Apprise == nil || s.Mail == nil {
		t.Fatal("channels missing")
	}
	for _, target := range []string{s.Apprise.URL, s.Mail.SMTP} {
		host := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(target, "http://"), "https://"), ":", 2)[0]
		if !strings.HasSuffix(host, ".invalid") {
			t.Errorf("%q can resolve", target)
		}
	}

	if s.Heartbeat != 0 {
		t.Error("heartbeat is on")
	}
}

func TestLanguages(t *testing.T) {
	de := prepare(t, "de").set.Jobs["paperless-eingang"].Name
	en := prepare(t, "en").set.Jobs["paperless-eingang"].Name

	if de == "" || en == "" || de == en {
		t.Errorf("de %q, en %q", de, en)
	}
}

func TestPrepareGuards(t *testing.T) {
	t.Setenv(envKey, testKey)
	now := time.Now()

	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "keep.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(foreign, "en", now); !errors.Is(err, ErrNotDemo) {
		t.Errorf("foreign folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreign, "keep.txt")); err != nil {
		t.Errorf("foreign file touched: %v", err)
	}

	t.Setenv(envKey, "")
	if _, err := Prepare(t.TempDir(), "en", now); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
}

// A second Prepare replaces the first.
func TestPrepareAgain(t *testing.T) {
	f := prepare(t, "en")
	f.conn.Close()

	jobs, err := Prepare(f.dir, "de", f.now)
	if err != nil {
		t.Fatal(err)
	}

	set := conf.Load(jobs, nil)
	if set.Jobs["dyndns"] == nil {
		t.Error("jobs missing after the second run")
	}
}

// A start must not send anything: no reminder is due for an open alert.
func TestNoReminderDue(t *testing.T) {
	f := prepare(t, "en")

	alerts, err := repos.AllAlerts(f.conn)
	if err != nil || len(alerts) < 4 {
		t.Fatalf("alerts %d, %v", len(alerts), err)
	}

	for id, a := range alerts {
		for i, rule := range rulesOf(f.set, f.set.Jobs[id]) {
			sent, ok := a.Sent[i]
			if ok && rule.Remind > 0 && f.now.Sub(sent) >= rule.Remind {
				t.Errorf("%s: reminder %d is due", id, i)
			}
		}
		if len(a.Sent) == 0 {
			t.Errorf("%s: no message marked as sent", id)
		}
	}
}
