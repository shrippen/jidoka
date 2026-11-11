package proc

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as the shim, like cmd/jidoka does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ShimArg {
		os.Exit(Shim(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// script writes body to an executable file and returns a Spec for it.
func script(t *testing.T, body string) Spec {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	return Spec{Path: p, Dir: dir, Env: []string{"PATH=/usr/bin:/bin"}, Grace: time.Second}
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(path)
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pid file")

	return 0
}

func gone(pid int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}

	return false
}

func TestExitZero(t *testing.T) {
	r := Run(context.Background(), script(t, "echo hi\n"))
	if r.Status != Exited || r.ExitCode != 0 || r.Output != "hi\n" || r.Err != nil {
		t.Fatalf("%+v", r)
	}
	if r.Ended.Before(r.Started) || r.Started.IsZero() {
		t.Fatalf("times %v %v", r.Started, r.Ended)
	}
}

func TestExitThree(t *testing.T) {
	r := Run(context.Background(), script(t, "echo bad >&2\nexit 3\n"))
	if r.Status != Exited || r.ExitCode != 3 || r.Output != "bad\n" || r.Err != nil {
		t.Fatalf("%+v", r)
	}
}

func TestStartFailed(t *testing.T) {
	r := Run(context.Background(), Spec{Path: "/nonexistent/x"})
	if r.Status != StartFailed || r.Err == nil {
		t.Fatalf("%+v", r)
	}
}

func TestArgsAndDir(t *testing.T) {
	s := script(t, "echo \"$1 $2\"; pwd\n")
	s.Args = []string{"a", "b"}
	r := Run(context.Background(), s)
	want := "a b\n" + s.Dir + "\n"
	if r.Output != want {
		t.Fatalf("got %q want %q", r.Output, want)
	}
}

// envOf runs env and returns the variables, minus what /bin/sh adds
// itself (PWD, SHLVL, _, OLDPWD).
func envOf(t *testing.T, env []string) []string {
	t.Helper()
	s := script(t, "exec /usr/bin/env\n")
	s.Env = env
	r := Run(context.Background(), s)
	if r.Status != Exited {
		t.Fatalf("%+v", r)
	}

	var out []string
	for _, l := range strings.Split(strings.TrimSpace(r.Output), "\n") {
		k, _, _ := strings.Cut(l, "=")
		if l == "" || k == "PWD" || k == "SHLVL" || k == "_" || k == "OLDPWD" {
			continue
		}
		out = append(out, l)
	}

	return out
}

func TestEmptyEnv(t *testing.T) {
	t.Setenv("JIDOKA_LEAK", "1")
	if got := envOf(t, nil); len(got) != 0 {
		t.Fatalf("env not empty: %q", got)
	}
	if got := envOf(t, []string{"A=1"}); len(got) != 1 || got[0] != "A=1" {
		t.Fatalf("got %q", got)
	}
}

func TestInterleave(t *testing.T) {
	s := script(t, "echo 1; echo 2 >&2; echo 3; echo 4 >&2; echo 5\n")
	r := Run(context.Background(), s)
	if r.Output != "1\n2\n3\n4\n5\n" {
		t.Fatalf("got %q", r.Output)
	}
}

func TestLive(t *testing.T) {
	var b bytes.Buffer
	s := script(t, "echo live\n")
	s.Live = &b
	Run(context.Background(), s)
	if b.String() != "live\n" {
		t.Fatalf("got %q", b.String())
	}
}

func TestHeadTail(t *testing.T) {
	s := script(t, "i=1; while [ $i -le 2000 ]; do echo line$i; i=$((i+1)); done\n")
	s.MaxOutput = 1000
	r := Run(context.Background(), s)

	if !strings.HasPrefix(r.Output, "line1\nline2\n") {
		t.Fatalf("head lost: %.40q", r.Output)
	}
	if !strings.HasSuffix(r.Output, "line2000\n") {
		t.Fatalf("tail lost: %q", r.Output[len(r.Output)-40:])
	}
	if !strings.Contains(r.Output, " bytes cut …\n") || !strings.Contains(r.Output, "\n… ") {
		t.Fatalf("no marker: %q", r.Output)
	}
	if len(r.Output) > 1100 {
		t.Fatalf("too long: %d", len(r.Output))
	}

	// Cut at line boundaries: every non-marker line is a whole "lineN".
	for _, l := range strings.Split(strings.TrimSuffix(r.Output, "\n"), "\n") {
		if !strings.HasPrefix(l, "line") && !strings.HasPrefix(l, "…") {
			t.Fatalf("broken line %q", l)
		}
	}
}

func TestHeadTailCount(t *testing.T) {
	b := newHeadTail(10)
	b.Write([]byte("0123456789ABCDEFGHIJ")) // no newlines
	got := b.String()
	if got != "01234\n… 10 bytes cut …\nFGHIJ" {
		t.Fatalf("got %q", got)
	}
}

func TestTimeoutKillsGroup(t *testing.T) {
	s := script(t, "sleep 100 &\necho $! > child.pid\nsleep 100\n")
	s.Timeout = 300 * time.Millisecond
	start := time.Now()
	r := Run(context.Background(), s)

	if r.Status != TimedOut {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	if pid := readPID(t, filepath.Join(s.Dir, "child.pid")); !gone(pid) {
		t.Fatalf("child %d still alive", pid)
	}
}

func TestTimeoutIgnoresTerm(t *testing.T) {
	s := script(t, "trap '' TERM\nwhile :; do sleep 1; done\n")
	s.Timeout = 200 * time.Millisecond
	s.Grace = 300 * time.Millisecond
	start := time.Now()
	r := Run(context.Background(), s)

	if r.Status != TimedOut || r.ExitCode != -1 {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d < 450*time.Millisecond || d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A grandchild in its own session leaves the process group; the shim
// must still end it (acceptance: detached, hanging child; no zombie).
func TestDetachedChildKilled(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	s := script(t, "setsid sh -c 'echo $$ > child.pid; exec sleep 100' &\nsleep 100\n")
	s.Timeout = 500 * time.Millisecond
	start := time.Now()
	r := Run(context.Background(), s)

	if r.Status != TimedOut {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}

	pid := readPID(t, filepath.Join(s.Dir, "child.pid"))
	if !gone(pid) {
		syscall.Kill(pid, syscall.SIGKILL) // test cleanup
		t.Fatalf("detached child %d still alive", pid)
	}
}

// Same, but the script ends by itself and leaves the detached child.
func TestDetachedChildAfterExit(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	s := script(t, "setsid sh -c 'echo $$ > child.pid; exec sleep 100' &\nsleep 0.3\nexit 4\n")
	r := Run(context.Background(), s)

	if r.Status != Exited || r.ExitCode != 4 {
		t.Fatalf("%+v", r)
	}
	pid := readPID(t, filepath.Join(s.Dir, "child.pid"))
	if !gone(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("detached child %d still alive", pid)
	}
}

func TestSignalExitIsMinusOne(t *testing.T) {
	r := Run(context.Background(), script(t, "kill -KILL $$\n"))
	if r.Status != Exited || r.ExitCode != -1 {
		t.Fatalf("%+v", r)
	}
	r = Run(context.Background(), script(t, "exit 130\n"))
	if r.ExitCode != 130 {
		t.Fatalf("%+v", r)
	}
}

// Script exits but leaves a same-group child with the pipe: cleaned up.
func TestLeftoverChildKilled(t *testing.T) {
	s := script(t, "sleep 100 &\necho $! > child.pid\necho done\n")
	start := time.Now()
	r := Run(context.Background(), s)

	if r.Status != Exited || r.Output != "done\n" {
		t.Fatalf("%+v", r)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	if pid := readPID(t, filepath.Join(s.Dir, "child.pid")); !gone(pid) {
		t.Fatalf("child %d still alive", pid)
	}
}

func TestCancel(t *testing.T) {
	s := script(t, "echo start\nsleep 100\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	r := Run(ctx, s)

	if r.Status != Cancelled {
		t.Fatalf("%+v", r)
	}
	if r.Output != "start\n" {
		t.Fatalf("output lost: %q", r.Output)
	}
}

func TestCancelBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Run(ctx, script(t, "echo x\n"))
	if r.Status != Cancelled || r.Output != "" {
		t.Fatalf("%+v", r)
	}
}

func TestUID(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root")
	}
	s := script(t, "id -u\n")
	s.UID, s.GID = 65534, 65534
	if err := os.Chmod(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), s)
	if strings.TrimSpace(r.Output) != "65534" {
		t.Fatalf("%+v", r)
	}
}
