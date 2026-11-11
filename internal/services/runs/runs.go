// Package runs executes one run end to end and stores it (J4-J9, J16, J22).
//
//	Execute ─► insert "running" ─► secrets? ──missing──► fail, no start (J7)
//	                                  │
//	                  state dir + TMPDIR + clean env (J16, J22)
//	                                  │
//	                     proc.Run (timeout, cancel)
//	                                  │
//	      read summary/notify ─► mask ALL secrets ─► FinishRun ─► OnDone
package runs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/drivers/proc"
	"jidoka/internal/model"
	"jidoka/internal/redact"
	"jidoka/internal/repos"
	"jidoka/internal/services/secrets"
)

const (
	// Fixed PATH of every job (J16).
	jobPath = "/usr/local/bin:/usr/bin:/bin"
	jobLang = "C.UTF-8"

	// uvDownloads keeps uv to the image's Python; a job loads none (J11).
	uvDownloads = "never"

	summaryMax = 200
	notifyMax  = 4 << 10

	summaryName = "summary"
	notifyName  = "notify"

	dirMode  = 0o755
	privMode = 0o700
	// passMode: others may enter, not list (state/, home/, tmp/).
	passMode = 0o711

	msgNoMasterKey = "MASTER_KEY fehlt"
)

// Config is what the runs need from the process settings.
type Config struct {
	DataDir string
	Shim    string // "" = this executable
	// UIDBase: job n gets its own uid and gid from here up (Z3); 0 = no
	// user switch (development, tests).
	UIDBase uint32
	BaseURL string
	// CacheLimit caps all uv caches together (J11); 0 = 2 GiB.
	CacheLimit int64
}

// Service runs jobs.
type Service struct {
	db  *sql.DB
	sec *secrets.Service
	cfg Config

	mu       sync.Mutex
	active   map[int64]context.CancelFunc
	busy     map[string]int           // job -> runs in progress
	clearing map[string]chan struct{} // job -> cache deletion in progress
	onStart  []func(*model.Job, model.Run)
	onDone   []func(*model.Job, model.Run)
}

// New returns a Service.
func New(d *sql.DB, sec *secrets.Service, cfg Config) *Service {
	return &Service{db: d, sec: sec, cfg: cfg, active: map[int64]context.CancelFunc{},
		busy: map[string]int{}, clearing: map[string]chan struct{}{}}
}

// OnStart registers fn, called when a run has been stored as running.
func (s *Service) OnStart(fn func(*model.Job, model.Run)) {
	s.mu.Lock()
	s.onStart = append(s.onStart, fn)
	s.mu.Unlock()
}

// OnDone registers fn, called after a run ended and was stored.
func (s *Service) OnDone(fn func(*model.Job, model.Run)) {
	s.mu.Lock()
	s.onDone = append(s.onDone, fn)
	s.mu.Unlock()
}

// Recover ends runs a crashed process left running as aborted, and removes
// its leftover TMPDIRs (J23). Call once at start.
func (s *Service) Recover() error {
	err := db.WithTx(s.db, func(tx *sql.Tx) error {
		_, err := repos.AbortRunning(tx, time.Now().UTC())
		return err
	})
	if err != nil {
		return err
	}

	return s.clearTmp()
}

// clearTmp removes the TMPDIRs a crashed process left. They belong to the
// job uids (0700): each goes as its owner (proc.RemoveOwned).
func (s *Service) clearTmp() error {
	dir := filepath.Join(s.cfg.DataDir, "tmp")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	var errs []error
	for _, e := range entries {
		errs = append(errs, proc.RemoveOwned(s.cfg.Shim, filepath.Join(dir, e.Name())))
	}

	return errors.Join(errs...)
}

// Cancel aborts a running run (J9); false if it is not running here.
func (s *Service) Cancel(runID int64) bool {
	s.mu.Lock()
	cancel, ok := s.active[runID]
	s.mu.Unlock()

	if ok {
		cancel()
	}

	return ok
}

// CancelAll aborts every running run, on container stop (J23).
func (s *Service) CancelAll() {
	s.mu.Lock()
	all := make([]context.CancelFunc, 0, len(s.active))
	for _, c := range s.active {
		all = append(all, c)
	}
	s.mu.Unlock()

	for _, c := range all {
		c()
	}
}

// Execute runs job once and returns the stored run.
func (s *Service) Execute(ctx context.Context, job *model.Job, trig model.Trigger,
	slot time.Time, attempt int, commit string) model.Run {
	run := model.Run{
		Job: job.ID, Trigger: trig, State: model.RunRunning, Slot: slot,
		Attempt: attempt, Started: time.Now().UTC(), Commit: commit,
		Dry: trig == model.TrigDry,
	}

	err := db.WithTx(s.db, func(tx *sql.Tx) error {
		id, err := repos.InsertRun(tx, run)
		run.ID = id
		return err
	})
	if err != nil {
		// Nothing stored: report it to the caller, not to the database.
		slog.Error("run not stored", "job", job.ID, "err", err)
		run.State, run.Error, run.Ended = model.RunFail, err.Error(), time.Now().UTC()
		return run
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.mu.Lock()
	s.active[run.ID] = cancel
	starts := append([]func(*model.Job, model.Run){}, s.onStart...)
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.active, run.ID)
		s.mu.Unlock()
	}()

	for _, fn := range starts {
		fn(job, run)
	}

	s.execute(ctx, job, &run)

	if err := db.WithTx(s.db, func(tx *sql.Tx) error { return repos.FinishRun(tx, run) }); err != nil {
		slog.Error("run not finished", "job", job.ID, "run", run.ID, "err", err)
	}

	s.mu.Lock()
	dones := append([]func(*model.Job, model.Run){}, s.onDone...)
	s.mu.Unlock()
	for _, fn := range dones {
		fn(job, run)
	}

	return run
}

// execute fills run with the outcome. Whatever happens, the texts are
// masked on the way out.
func (s *Service) execute(ctx context.Context, job *model.Job, run *model.Run) {
	mask := s.masker()
	defer func() {
		run.Output = mask.String(run.Output)
		run.Summary = mask.String(run.Summary)
		run.Message = mask.String(run.Message)
		run.Error = mask.String(run.Error)
		run.Ended = time.Now().UTC()
	}()

	sec, errMsg := s.resolve(job)
	if errMsg != "" {
		run.State, run.Error = model.RunFail, errMsg
		return
	}

	uid, err := s.uidFor(job)
	if err != nil {
		run.State, run.Error = model.RunFail, err.Error()
		return
	}

	defer s.hold(ctx, job.ID)()

	tmp, err := s.prepare(job, run, uid)
	if err != nil {
		run.State, run.Error = model.RunFail, err.Error()
		return
	}
	defer s.dropTmp(job.ID, uid, tmp)

	summaryPath := filepath.Join(tmp, summaryName)
	notifyPath := filepath.Join(tmp, notifyName)

	res := proc.Run(ctx, proc.Spec{
		Path:    scriptPath(job),
		Args:    job.Args,
		Dir:     job.Dir,
		Env:     s.env(job, run, sec, tmp, summaryPath, notifyPath),
		UID:     uid,
		GID:     uid,
		Timeout: job.Timeout,
		Shim:    s.cfg.Shim,
	})

	run.Output = res.Output
	run.Summary = firstLine(readCapped(summaryPath, summaryMax*utf8.UTFMax*2), summaryMax)
	run.Message = capText(readCapped(notifyPath, notifyMax), notifyMax)

	switch res.Status {
	case proc.Exited:
		code := res.ExitCode
		run.Exit = &code
		run.State = model.RunFail
		if code == 0 {
			run.State = model.RunOK
		}
	case proc.TimedOut:
		run.State = model.RunTimeout
	case proc.Cancelled:
		run.State = model.RunAborted
	default:
		run.State = model.RunFail
		run.Error = fmt.Sprint(res.Err)
	}
}

// masker masks every secret, not only the job's (J7). Without a master key
// nothing is known to mask.
func (s *Service) masker() *redact.Masker {
	vals, err := s.sec.Values()
	if err != nil && !errors.Is(err, crypto.ErrMissingKey) {
		slog.Error("secrets for masking", "err", err)
	}

	return redact.New(vals)
}

// resolve returns the job's secrets, or the reason the run cannot start.
func (s *Service) resolve(job *model.Job) (map[string]string, string) {
	vals, missing, err := s.sec.Resolve(job.Secrets)
	if errors.Is(err, crypto.ErrMissingKey) {
		return nil, msgNoMasterKey
	}
	if err != nil {
		return nil, err.Error()
	}
	if len(missing) > 0 {
		return nil, "Geheimnis fehlt: " + strings.Join(missing, ", ")
	}

	return vals, ""
}

// uidFor returns the job's own uid; 0 without UIDBase (no switch).
func (s *Service) uidFor(job *model.Job) (uint32, error) {
	if s.cfg.UIDBase == 0 {
		return 0, nil
	}

	var uid uint32
	err := db.WithTx(s.db, func(tx *sql.Tx) error {
		var err error
		uid, err = repos.JobUID(tx, job.ID, s.cfg.UIDBase)
		return err
	})

	return uid, err
}

// prepare creates the job's private folders (J22, Z3). Each belongs to
// the job's uid, mode 0700, so no other job can enter it:
//
//	<data>/state/<id>        JIDOKA_STATE, survives runs
//	<data>/home/<id>         HOME, holds .cache/uv of this job only
//	<data>/tmp/run-<id>      TMPDIR, removed after the run
//
// The uv cache is per job: a cache several uids may write lets one job
// poison the packages of another.
func (s *Service) prepare(job *model.Job, run *model.Run, uid uint32) (string, error) {
	data := s.cfg.DataDir
	tmp := filepath.Join(data, "tmp", "run-"+strconv.FormatInt(run.ID, 10))
	home := s.homeDir(job)

	// Jobs pass through state/, home/ and tmp/ to their own folder but
	// list nothing; set the mode, the umask (077 in the image) cuts it.
	for _, d := range []string{filepath.Join(data, "state"), filepath.Join(data, "home"), filepath.Join(data, "tmp")} {
		if err := os.MkdirAll(d, passMode); err != nil {
			return "", err
		}
		if err := os.Chmod(d, passMode); err != nil {
			return "", err
		}
	}

	for _, d := range []string{s.stateDir(job), home, tmp} {
		if err := os.MkdirAll(d, dirMode); err != nil {
			return "", err
		}
		if uid == 0 {
			continue
		}
		if err := handOver(d, uid); err != nil {
			return "", err
		}
	}

	return tmp, nil
}

// dropTmp removes a run's TMPDIR as the job's uid: the server may not
// enter it (0700, no DAC_OVERRIDE).
func (s *Service) dropTmp(job string, uid uint32, tmp string) {
	if err := proc.RemoveDir(s.cfg.Shim, uid, tmp); err != nil {
		slog.Warn("remove TMPDIR", "job", job, "err", err)
	}
}

// handOver gives a new dir to uid, mode 0700. Once it is the job's, the
// server may no longer chmod or enter it (it has no CAP_FOWNER or
// CAP_DAC_OVERRIDE), so a dir already owned by uid is left alone.
func handOver(dir string, uid uint32) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == uid {
		return nil
	}

	if err := os.Chmod(dir, privMode); err != nil {
		return err
	}

	return os.Lchown(dir, int(uid), int(uid))
}

func (s *Service) stateDir(job *model.Job) string {
	return filepath.Join(s.cfg.DataDir, "state", job.ID)
}

func (s *Service) homeDir(job *model.Job) string {
	return filepath.Join(s.cfg.DataDir, "home", job.ID)
}

// env builds the whole environment of the script (J16). Later entries win:
// base < job.Env < secrets < JIDOKA_*. Nothing is inherited.
func (s *Service) env(job *model.Job, run *model.Run, sec map[string]string,
	tmp, summary, notify string) []string {
	tz := "UTC"
	if job.Zone != nil {
		tz = job.Zone.String()
	}

	m := map[string]string{
		"PATH": jobPath,
		"HOME": s.homeDir(job),
		"TZ":   tz,
		"LANG": jobLang,

		"UV_PYTHON_DOWNLOADS": uvDownloads,
	}
	for k, v := range job.Env {
		m[k] = v
	}
	for k, v := range sec {
		m[k] = v
	}

	m["JIDOKA_JOB"] = job.ID
	m["JIDOKA_RUN"] = strconv.FormatInt(run.ID, 10)
	m["JIDOKA_TRIGGER"] = string(run.Trigger)
	m["JIDOKA_LAST_OK"] = s.lastOK(job)
	m["JIDOKA_STATE"] = s.stateDir(job)
	m["JIDOKA_SUMMARY"] = summary
	m["JIDOKA_NOTIFY"] = notify
	m["TMPDIR"] = tmp

	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)

	return out
}

// lastOK is the end of the last real successful run, RFC3339, or "".
func (s *Service) lastOK(job *model.Job) string {
	var r model.Run
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		var err error
		r, err = repos.LastOKRun(tx, job.ID)
		return err
	})
	if err != nil || r.Ended.IsZero() {
		return ""
	}

	return r.Ended.UTC().Format(time.RFC3339)
}

// scriptPath makes the job's run entry absolute.
func scriptPath(job *model.Job) string {
	if filepath.IsAbs(job.Run) {
		return job.Run
	}

	return filepath.Join(job.Dir, job.Run)
}

// readCapped reads at most n bytes of path; "" if it cannot be read.
func readCapped(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	b, _ := io.ReadAll(io.LimitReader(f, int64(n)))

	return string(b)
}

// firstLine returns the first line trimmed, at most n characters.
func firstLine(text string, n int) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(strings.ToValidUTF8(line, ""))

	r := []rune(line)
	if len(r) > n {
		r = r[:n]
	}

	return string(r)
}

// capText cuts text to n bytes without splitting a character.
func capText(text string, n int) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= n {
		return text
	}

	text = text[:n]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}

	return text
}
