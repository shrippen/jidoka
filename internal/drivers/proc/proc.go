// Package proc runs one script as a process and returns its result.
//
// Linux only. The script runs under a shim (see Shim): Spec.Shim, by
// default the calling binary itself, started with ShimArg. In the image
// it is jidoka-shim, a binary without the server's file capabilities
// (a job that ran the server binary would gain CAP_SETUID). The shim becomes the subreaper of the job,
// so a timeout or abort ends the script and every process it started,
// also one that detached with setsid. Shim and script share one process
// group, which gets the first SIGTERM.
//
// The binary that calls Run must call Shim at the start of main (and
// tests in TestMain) when os.Args[1] is ShimArg.
package proc

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	defaultGrace     = 10 * time.Second
	defaultMaxOutput = 256 << 10
	readChunk        = 32 << 10

	// How long to wait for the output pipe after the process is gone.
	// A detached grandchild may hold it open forever.
	pipeDrainWait = 500 * time.Millisecond

	// Extra time for the shim's own SIGKILL round before Run kills the group.
	shimReserve = 500 * time.Millisecond
)

// Status tells why a run ended.
type Status int

const (
	Exited Status = iota
	TimedOut
	Cancelled
	StartFailed
)

func (s Status) String() string {
	switch s {
	case Exited:
		return "exited"
	case TimedOut:
		return "timed_out"
	case Cancelled:
		return "cancelled"
	case StartFailed:
		return "start_failed"
	}
	return "unknown"
}

// Spec describes one run.
type Spec struct {
	Path string   // absolute path of the script
	Args []string // arguments after the script name
	Dir  string   // working directory (job folder)

	// Env is the complete environment. os.Environ() is never inherited;
	// nil means empty.
	Env []string

	// UID and GID switch user when either is non-zero. Set both: a zero
	// GID with a non-zero UID runs as group root.
	UID, GID uint32

	Timeout   time.Duration // 0 = none
	Grace     time.Duration // SIGTERM to SIGKILL, default 10 s
	MaxOutput int           // bytes kept (head + tail), default 256 KiB

	// Live, if set, gets the raw output chunks as they arrive (unmasked).
	// It is called from one goroutine; a slow writer slows the reader only.
	Live io.Writer

	// Shim is the binary that wraps the script; "" = this executable.
	Shim string
}

// Result is what a run left behind.
type Result struct {
	Status   Status
	ExitCode int   // -1 if ended by a signal; valid unless StartFailed
	Err      error // set for StartFailed
	Output   string
	Started  time.Time
	Ended    time.Time
}

// Run starts the script and waits for its end. Cancelling ctx aborts it
// like a timeout does: SIGTERM to the group, SIGKILL after Grace.
func Run(ctx context.Context, s Spec) Result {
	res := Result{Started: time.Now()}

	fail := func(st Status, err error) Result {
		res.Status = st
		res.Err = err
		res.Ended = time.Now()
		return res
	}

	if err := ctx.Err(); err != nil {
		return fail(Cancelled, err)
	}

	grace := s.Grace
	if grace <= 0 {
		grace = defaultGrace
	}
	maxOut := s.MaxOutput
	if maxOut <= 0 {
		maxOut = defaultMaxOutput
	}

	// One pipe for both streams keeps the lines in time order (J6).
	rd, wr, err := os.Pipe()
	if err != nil {
		return fail(StartFailed, err)
	}

	exe, err := shimPath(s.Shim)
	if err != nil {
		rd.Close()
		wr.Close()
		return fail(StartFailed, err)
	}

	// fd 3 of the shim reports a script that cannot start.
	errRd, errWr, err := os.Pipe()
	if err != nil {
		rd.Close()
		wr.Close()
		return fail(StartFailed, err)
	}

	shimArgs := append([]string{ShimArg, "--grace", grace.String(), "--", s.Path}, s.Args...)
	cmd := exec.Command(exe, shimArgs...)
	cmd.ExtraFiles = []*os.File{errWr}
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	cmd.Stdout = wr
	cmd.Stderr = wr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if s.UID != 0 || s.GID != 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: s.UID, Gid: s.GID}
	}

	res.Started = time.Now()
	err = cmd.Start()
	wr.Close()
	errWr.Close()
	if err != nil {
		rd.Close()
		errRd.Close()
		return fail(StartFailed, err)
	}
	pgid := cmd.Process.Pid

	buf := newHeadTail(maxOut)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		copyOut(rd, buf, s.Live)
	}()

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	var timer <-chan time.Time
	if s.Timeout > 0 {
		t := time.NewTimer(s.Timeout)
		defer t.Stop()
		timer = t.C
	}

	var waitErr error
	res.Status = Exited
	select {
	case waitErr = <-waited:
	case <-timer:
		res.Status = TimedOut
		waitErr = stop(pgid, grace, waited)
	case <-ctx.Done():
		res.Status = Cancelled
		waitErr = stop(pgid, grace, waited)
	}

	// Leftovers in the group would keep the pipe open and outlive the run.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	select {
	case <-drained:
	case <-time.After(pipeDrainWait):
		rd.Close() // unblocks the reader
		<-drained
	}
	rd.Close()

	if msg := readStartErr(errRd); msg != "" {
		res.Status = StartFailed
		res.Err = errors.New(msg)
		res.Output = buf.String()
		res.Ended = time.Now()
		return res
	}

	res.ExitCode = -1
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		res.Err = waitErr
	}
	res.Output = buf.String()
	res.Ended = time.Now()

	return res
}

// readStartErr returns what the shim wrote to fd 3, if anything. The shim
// has ended every process of the job, so EOF is near; the deadline only
// guards against a holder we do not know.
func readStartErr(r *os.File) string {
	defer r.Close()

	_ = r.SetReadDeadline(time.Now().Add(pipeDrainWait))
	b, _ := io.ReadAll(r)

	return string(b)
}

// stop ends the group: SIGTERM, then SIGKILL after grace. The shim gets
// the same grace to end the descendants itself, so the outer SIGKILL
// waits a little longer (shimReserve).
func stop(pgid int, grace time.Duration, waited <-chan error) error {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case err := <-waited:
		return err
	case <-time.After(grace + shimReserve):
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	return <-waited
}

// copyOut moves pipe data into buf (and live) until EOF or close.
func copyOut(rd io.Reader, buf io.Writer, live io.Writer) {
	chunk := make([]byte, readChunk)

	for {
		n, err := rd.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if live != nil {
				live.Write(chunk[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// shimPath is the configured shim, or this executable.
func shimPath(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}

	return os.Executable()
}
