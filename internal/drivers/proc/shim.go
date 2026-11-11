package proc

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ShimArg is the first argument that makes the Jidoka binary act as the
// shim instead of the server. cmd/jidoka calls Shim first thing in main:
//
//	if len(os.Args) > 1 && os.Args[1] == proc.ShimArg {
//		os.Exit(proc.Shim(os.Args[2:]))
//	}
const ShimArg = "__jidoka_shim"

const (
	// fd 3 of the shim: carries the error if the script cannot start.
	startErrFD = 3

	killSweepPause = 5 * time.Millisecond
	killSweepMax   = 1000 // sweeps of killSweepPause
)

// The shim sits between Run and the script:
//
//	Run ── shim (subreaper, job UID) ── script ── children …
//
// Orphans of the script (a child that detached with setsid, a daemon that
// forked) are re-parented to the shim, not to init. So the shim can find
// and end every process of the job, which a process group cannot do for
// processes that left the group.
//
// Args: --grace <duration> -- <script> [args…]. The script inherits
// environment, working directory and stdout/stderr (the output pipe).
//
// Exit: the script's exit code. If a signal ended the script, the shim
// ends itself by the same signal, so Run sees "killed by signal"
// (ExitCode -1) and an exit code 130 stays a real exit code 130.
func Shim(args []string) int {
	if len(args) == 2 && (args[0] == modeSize || args[0] == modeRemove) {
		return dirMain(args[0], args[1])
	}
	if len(args) < 4 || args[0] != "--grace" || args[2] != "--" {
		fmt.Fprintln(os.Stderr, "jidoka-shim: bad arguments")
		return 2
	}
	grace, err := time.ParseDuration(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jidoka-shim:", err)
		return 2
	}

	errFile := os.NewFile(startErrFD, "starterr")
	unix.CloseOnExec(startErrFD) // the script must not inherit it

	// Orphans come to us from now on.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		reportStart(errFile, err)
		return 2
	}

	// Installed before the start, so an early SIGTERM is not lost.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	cmd := exec.Command(args[3], args[4:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		reportStart(errFile, err)
		return 2
	}
	script := cmd.Process.Pid

	go func() {
		<-sigs
		endAll(grace)
	}()

	ws := waitScript(script)
	sweep()

	if ws.Signaled() {
		signal.Reset()
		_ = unix.Kill(os.Getpid(), syscall.Signal(ws.Signal()))
		time.Sleep(time.Second) // the signal ends us; only reached if ignored
		return 128 + int(ws.Signal())
	}

	return ws.ExitStatus()
}

func reportStart(f *os.File, err error) {
	if f != nil {
		fmt.Fprint(f, err.Error())
	}
}

// waitScript reaps children until the script itself is gone. Orphans that
// end meanwhile are reaped on the way, so they do not pile up as zombies.
func waitScript(script int) syscall.WaitStatus {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return ws // ECHILD: nothing left to wait for
		}
		if pid == script {
			return ws
		}
	}
}

// endAll is the shim's reaction to SIGTERM: SIGTERM to every descendant,
// SIGKILL after grace. The main loop sees the script end and finishes.
func endAll(grace time.Duration) {
	signalAll(syscall.SIGTERM)
	time.Sleep(grace)

	for {
		signalAll(syscall.SIGKILL)
		time.Sleep(killSweepPause)
	}
}

// sweep ends what the script left behind (the job is over, so all of its
// processes end) and reaps every one of them. It repeats because a dying
// process may have forked once more.
func sweep() {
	for i := 0; i < killSweepMax; i++ {
		reapNow()
		if len(descendants()) == 0 {
			return
		}
		signalAll(syscall.SIGKILL)
		time.Sleep(killSweepPause)
	}
}

func reapNow() {
	for {
		pid, err := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func signalAll(sig syscall.Signal) {
	for _, pid := range descendants() {
		_ = syscall.Kill(pid, sig)
	}
}

// descendants lists all processes below this one, by walking the
// parent links in /proc.
func descendants() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	children := map[int][]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := parentOf(pid)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}

	var out []int
	queue := []int{os.Getpid()}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}

	return out
}

// parentOf reads the parent PID from /proc/<pid>/stat:
// "pid (comm) S ppid …"; comm may hold spaces and parentheses, so cut
// after the last ")".
func parentOf(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])

	return ppid, err == nil
}
