package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "tok-secret-123"

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// origin makes a repo on main with one job folder and returns its path.
func origin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary")
	}

	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	put(t, dir, "a/job.toml", "x = 1\n", 0o644)
	put(t, dir, "a/run.sh", "#!/bin/sh\n", 0o755)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "one")

	return dir
}

func put(t *testing.T, dir, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestSyncFollowsOrigin(t *testing.T) {
	src := origin(t)
	co := filepath.Join(t.TempDir(), "jobs")
	r := New(Config{URL: "file://" + src, Dir: co, Token: testToken, Protocols: "file"})

	old := syscallUmask(0o077)
	defer syscallUmask(old)

	if err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(co, "a/job.toml")); string(b) != "x = 1\n" {
		t.Fatalf("job.toml: %q", b)
	}

	// Modes for other uids: readable, script executable.
	for p, want := range map[string]os.FileMode{"a": 0o755, "a/job.toml": 0o644, "a/run.sh": 0o755} {
		info, err := os.Stat(filepath.Join(co, p))
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v %v, want %o", p, info, err, want)
		}
	}

	// A new commit arrives, local edits are wiped.
	put(t, src, "a/job.toml", "x = 2\n", 0o644)
	run(t, src, "commit", "-q", "-am", "two")
	put(t, co, "a/job.toml", "local\n", 0o644)
	put(t, co, "stray", "x", 0o644)

	if err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(co, "a/job.toml")); string(b) != "x = 2\n" {
		t.Errorf("job.toml: %q", b)
	}
	if _, err := os.Stat(filepath.Join(co, "stray")); err == nil {
		t.Error("stray file kept")
	}
}

func TestTokenOnlyInEnvironment(t *testing.T) {
	src := origin(t)
	co := filepath.Join(t.TempDir(), "jobs")
	r := New(Config{URL: "file://" + src, Dir: co, Token: testToken, Protocols: "file"})

	if err := r.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.Join(r.env(), "\n"), "GIT_CONFIG_VALUE_0=Authorization: token "+testToken) {
		t.Error("environment lacks the header")
	}
	if !strings.Contains(strings.Join(r.env(), "\n"), "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Error("global config not cut off")
	}

	// Nowhere in the checkout, config included.
	filepath.WalkDir(co, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, _ := os.ReadFile(p); strings.Contains(string(b), testToken) {
			t.Errorf("token in %s", p)
		}
		return nil
	})
}

// The token goes to the git process through its environment, and an
// error never shows it.
func TestFakeGitSeesHeader(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	bin := filepath.Join(dir, "fakegit")
	put(t, dir, "fakegit", "#!/bin/sh\nenv > "+dump+"\necho \"fatal: bad $GIT_CONFIG_VALUE_0\" >&2\nexit 1\n", 0o755)

	r := New(Config{URL: "https://x/y.git", Dir: filepath.Join(dir, "co"), Token: testToken, Bin: bin})
	err := r.Sync(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("token in error: %v", err)
	}

	b, _ := os.ReadFile(dump)
	for _, want := range []string{"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: token " + testToken, "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("env lacks %q:\n%s", want, b)
		}
	}
}

func TestFailureKeepsOldState(t *testing.T) {
	src := origin(t)
	co := filepath.Join(t.TempDir(), "jobs")
	if err := New(Config{URL: "file://" + src, Dir: co, Protocols: "file"}).Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	bad := New(Config{URL: "file://" + src + "-gone", Dir: co, Timeout: 10 * time.Second, Protocols: "file"})
	if err := bad.Sync(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(co, "a/job.toml")); err != nil {
		t.Errorf("old state lost: %v", err)
	}
}

func TestFirstSyncFailsLeavesEmptyDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary")
	}
	co := filepath.Join(t.TempDir(), "jobs")
	r := New(Config{URL: "file://" + co + "-none", Dir: co, Protocols: "file"})
	if err := r.Sync(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.ReadDir(co); err != nil {
		t.Errorf("dir missing: %v", err)
	}
}
