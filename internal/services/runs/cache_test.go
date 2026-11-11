package runs

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const fillBytes = 64 << 10

// fill makes home/<job>/.cache/uv with one file; ran is the job's last run.
func (e env) fill(t *testing.T, job string, ran time.Time) {
	t.Helper()

	dir := filepath.Join(e.data, "home", job, uvCache)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg"), make([]byte, fillBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if ran.IsZero() {
		return
	}

	err := db.WithTx(e.d, func(tx *sql.Tx) error {
		_, err := repos.InsertRun(tx, model.Run{
			Job: job, Trigger: model.TrigManual, State: model.RunOK,
			Slot: ran, Attempt: 1, Started: ran,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e env) has(job string) bool {
	_, err := os.Stat(filepath.Join(e.data, "home", job, uvCache))

	return err == nil
}

// limited returns a service whose limit holds n caches of one fill.
func (e env) limited(n int) {
	one, _ := e.svc.caches()
	e.svc.cfg.CacheLimit = one[0].size*int64(n) + 1
}

func TestCacheUnderLimitKept(t *testing.T) {
	e := setup(t)
	now := time.Now()
	e.fill(t, "a", now)
	e.fill(t, "b", now)

	if err := e.svc.PruneCache(); err != nil { // default 2 GiB
		t.Fatal(err)
	}
	if !e.has("a") || !e.has("b") {
		t.Error("cache deleted under the limit")
	}
}

func TestCacheOldestRunFirst(t *testing.T) {
	e := setup(t)
	now := time.Now()
	e.fill(t, "recent", now)
	e.fill(t, "old", now.Add(-48*time.Hour))
	e.fill(t, "never", time.Time{})
	e.fill(t, "mid", now.Add(-time.Hour))
	e.limited(2)

	if err := e.svc.PruneCache(); err != nil {
		t.Fatal(err)
	}

	// Never-run and oldest go; two caches fit.
	if e.has("never") || e.has("old") {
		t.Error("oldest caches kept")
	}
	if !e.has("mid") || !e.has("recent") {
		t.Error("newer caches deleted")
	}
	if _, err := os.Stat(filepath.Join(e.data, "home", "old")); err != nil {
		t.Errorf("home deleted, only the cache may go: %v", err)
	}
}

func TestCacheRunningJobStays(t *testing.T) {
	e := setup(t)
	now := time.Now()
	e.fill(t, "busy", now.Add(-48*time.Hour))
	e.fill(t, "other", now.Add(-time.Hour))
	e.fill(t, "new", now)
	e.limited(2)

	release := e.svc.hold(context.Background(), "busy")
	if err := e.svc.PruneCache(); err != nil {
		t.Fatal(err)
	}
	release()

	// busy is oldest but runs; the next oldest goes instead.
	if !e.has("busy") || e.has("other") || !e.has("new") {
		t.Errorf("busy %v other %v new %v", e.has("busy"), e.has("other"), e.has("new"))
	}
}

func TestRunWaitsForDeletion(t *testing.T) {
	e := setup(t)
	e.fill(t, "a", time.Now())

	if !e.svc.clear(cacheOf{job: "a"}) {
		t.Fatal("clear refused")
	}

	started := make(chan struct{})
	go func() {
		e.svc.hold(context.Background(), "a")()
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("run started during deletion")
	case <-time.After(50 * time.Millisecond):
	}

	e.svc.cleared("a")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("run still waits after deletion")
	}
}
