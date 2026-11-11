package jobs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const jobTOML = `name = "J"
run = "run.sh"
schedule = "0 6 * * *"
`

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

func putJob(t *testing.T, dir, id, script string) {
	t.Helper()
	put(t, dir, id+"/job.toml", jobTOML)
	put(t, dir, id+"/run.sh", "#!/bin/sh\n"+script)
}

func open(t *testing.T) *sql.DB {
	t.Helper()

	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	return d
}

func TestReloadDiff(t *testing.T) {
	dir := t.TempDir()
	putJob(t, dir, "keep", "echo 1\n")
	putJob(t, dir, "edit", "echo 1\n")
	putJob(t, dir, "gone", "echo 1\n")

	s := New(open(t), dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if first := s.LastDiff(); !slices.Equal(first.Added, []string{"edit", "gone", "keep"}) {
		t.Errorf("first diff: %+v", first)
	}

	if err := os.RemoveAll(filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	putJob(t, dir, "edit", "echo 2\n")
	putJob(t, dir, "new", "echo 1\n")
	put(t, dir, "bad/job.toml", "run = \n")

	var hooked Diff
	s.OnReload(func(d Diff, _ model.Set) { hooked = d })

	d, err := s.Reload()
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(d.Added, []string{"new"}) || !slices.Equal(d.Changed, []string{"edit"}) ||
		!slices.Equal(d.Removed, []string{"gone"}) || !slices.Equal(d.Invalid, []string{"bad"}) {
		t.Errorf("diff: %+v", d)
	}
	if !slices.Equal(hooked.Added, d.Added) || !slices.Equal(s.LastDiff().Changed, d.Changed) {
		t.Errorf("hook %+v, last %+v", hooked, s.LastDiff())
	}

	if _, ok := s.Job("gone"); ok {
		t.Error("removed job still there")
	}
	if _, ok := s.Job("new"); !ok {
		t.Error("new job missing")
	}
	if len(s.Current().Jobs) != 3 {
		t.Errorf("jobs: %d", len(s.Current().Jobs))
	}

	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if again := s.LastDiff(); !again.Empty() {
		t.Errorf("second reload: %+v", again)
	}
}

func TestEnsureSeen(t *testing.T) {
	dir := t.TempDir()
	putJob(t, dir, "a", "echo\n")
	d := open(t)

	s := New(d, dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}

	all, err := repos.AllSchedules(d)
	if err != nil || len(all) != 1 || all["a"].FirstSeen.IsZero() {
		t.Errorf("schedules: %+v %v", all, err)
	}
}

func TestBrokenSettingsKeepOldAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	d := open(t)
	putJob(t, dir, "a", "echo\n")
	put(t, dir, "jidoka.toml", "max_parallel = 7\n")

	s := New(d, dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().Settings.MaxParallel; got != 7 {
		t.Fatalf("MaxParallel %d", got)
	}

	put(t, dir, "jidoka.toml", "max_parallel = [\n")

	// Same process: Reload keeps the old values.
	diff, err := s.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cur := s.Current(); cur.Settings.MaxParallel != 7 || cur.SettingsErr == nil {
		t.Errorf("reload: %+v", cur.Settings)
	}
	if !slices.Contains(diff.Invalid, "jidoka.toml") {
		t.Errorf("diff: %+v", diff)
	}

	// New process on the same database.
	s2 := New(d, dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if cur := s2.Current(); cur.Settings.MaxParallel != 7 || cur.SettingsErr == nil {
		t.Errorf("restart: %+v err %v", cur.Settings, cur.SettingsErr)
	}
}

// fakeRepo stands in for the git driver; fail makes the next Sync fail.
type fakeRepo struct {
	calls int
	fail  error
	apply func()
}

func (f *fakeRepo) Sync(context.Context) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	if f.apply != nil {
		f.apply()
	}
	return nil
}

func TestRepoFailureKeepsOldStateAndRecovers(t *testing.T) {
	dir := t.TempDir()
	putJob(t, dir, "a", "echo 1\n")
	repo := &fakeRepo{}

	s := New(open(t), dir)
	s.UseRepo(repo)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if s.Current().RepoErr != nil {
		t.Fatalf("RepoErr after a good fetch: %+v", s.Current().RepoErr)
	}

	repo.fail = errors.New("gitea down")
	d, err := s.Reload()
	if err != nil {
		t.Fatal(err)
	}
	cur := s.Current()
	if cur.RepoErr == nil || cur.RepoErr.File != model.RepoFile || !strings.Contains(cur.RepoErr.Msg, "gitea down") {
		t.Fatalf("RepoErr: %+v", cur.RepoErr)
	}
	if _, ok := s.Job("a"); !ok {
		t.Error("last state lost")
	}
	if !slices.Contains(d.Invalid, model.RepoFile) || len(d.Removed) != 0 {
		t.Errorf("diff: %+v", d)
	}

	// The fetch works again and brings a job: the error is gone.
	repo.fail = nil
	repo.apply = func() { putJob(t, dir, "b", "echo 1\n") }
	d, err = s.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if s.Current().RepoErr != nil || !slices.Equal(d.Added, []string{"b"}) {
		t.Errorf("recovery: %+v %+v", s.Current().RepoErr, d)
	}
}

// First start without a checkout: no jobs, and the error shows.
func TestRepoFailureOnFirstStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jobs")
	s := New(open(t), dir)
	s.UseRepo(&fakeRepo{fail: errors.New("no token")})

	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	cur := s.Current()
	if cur.RepoErr == nil || len(cur.Jobs) != 0 {
		t.Errorf("set: %+v", cur)
	}
}

func TestBaseURLOverride(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "jidoka.toml", "base_url = \"https://file.example\"\n")

	s := New(open(t), dir)
	s.SetBaseURL("https://env.example")
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().Settings.BaseURL; got != "https://env.example" {
		t.Errorf("BaseURL %q", got)
	}
}
