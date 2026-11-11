// Package watch is the dead-man's switch (W1-W3): it notices a job that
// should have succeeded by now, and takes the pings of external watches.
//
//	Tick ──► per job with expect / watch: deadline = base + window
//	         base = max(last ok run, first seen, last resume)
//	         past the deadline, grace over ──► missed ──► OnMissed (once)
//	         ok run again ──► missed ends (alerts sees the ok run: recover)
//
//	POST /ping/<token>[/start|/fail] ──► Ping ──► rate limit ──► token hash
//	         ──► run{trigger ping}: start inserts running, ok/fail finishes it
//
// The missed set lives in memory. After a restart it is rebuilt by the same
// pass over the database: an overdue job reports "missed" again once the
// startup grace is over. The alerts store holds the open disturbance, so
// the repeat sends no second message.
package watch

import (
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"jidoka/internal/cron"
	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const (
	// graceExtra is added to timeout and jitter after a start, so J10
	// catchup runs before anything is called missed.
	graceExtra = 5 * time.Minute
	// bodyMax caps a ping body, which becomes the run's output (W2).
	bodyMax = 10 * 1024
)

var (
	// ErrUnknown: no such token, or no such watch. The web layer answers 404.
	ErrUnknown = errors.New("watch: unknown")
	// ErrThrottled: too many pings from one address (W3).
	ErrThrottled = errors.New("watch: throttled")
)

// PingKind is the endpoint a ping came in on.
type PingKind string

const (
	PingStart PingKind = "start"
	PingOK    PingKind = "ok"
	PingFail  PingKind = "fail"
)

// Config wires the watch. DB is required.
type Config struct {
	DB *sql.DB
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// state is what the watch remembers of one job between ticks.
type state struct {
	// resumed: when the job last became active after a pause or disable.
	resumed time.Time
	// inactive: paused or disabled at the last tick.
	inactive bool
	missed   time.Time // since; zero = not missed
}

// Watch checks deadlines and receives pings.
type Watch struct {
	cfg     Config
	started time.Time
	limit   *limiter

	mu       sync.Mutex // guards everything below
	set      model.Set
	states   map[string]*state
	onMissed []func(job *model.Job, since time.Time)
	onPing   []func(job *model.Job, run model.Run)
	tokenMu  sync.Mutex // one token created per job
}

// New returns a watch; its start time begins the startup grace.
func New(cfg Config) *Watch {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Watch{
		cfg:     cfg,
		started: cfg.Now(),
		limit:   newLimiter(cfg.Now),
		states:  map[string]*state{},
	}
}

// OnMissed registers fn, called when a job becomes missed: once per
// transition, without locks held.
func (w *Watch) OnMissed(fn func(job *model.Job, since time.Time)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onMissed = append(w.onMissed, fn)
}

// OnPing registers fn, called after a ping stored a run (state running
// for /start, ok or fail otherwise), without locks held. The alerts
// engine takes the result of a watch from here.
func (w *Watch) OnPing(fn func(job *model.Job, run model.Run)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onPing = append(w.onPing, fn)
}

// Apply installs a freshly loaded set. Jobs that left it lose their state.
func (w *Watch) Apply(set model.Set) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.set = set
	for id := range w.states {
		if set.Jobs[id] == nil {
			delete(w.states, id)
		}
	}

	now := w.cfg.Now()
	for id := range set.Jobs {
		if err := repos.EnsureSchedule(w.cfg.DB, id, now); err != nil {
			slog.Error("watch: schedule row", "job", id, "err", err)
		}
	}
}

// State is since when the job counts as missed; zero if it does not.
func (w *Watch) State(id string) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()

	st := w.states[id]
	if st == nil {
		return time.Time{}
	}
	return st.missed
}

// Tick checks every watched job at now.
func (w *Watch) Tick(now time.Time) {
	w.mu.Lock()
	cbs := w.onMissed
	hits := w.check(now)
	w.mu.Unlock()

	for _, h := range hits {
		for _, fn := range cbs {
			fn(h.job, h.since)
		}
	}
}

type hit struct {
	job   *model.Job
	since time.Time
}

// check updates the missed set and returns the new transitions.
func (w *Watch) check(now time.Time) []hit {
	flags, err := repos.AllFlags(w.cfg.DB)
	if err != nil {
		slog.Error("watch: flags", "err", err)
		return nil
	}
	rows, err := repos.AllSchedules(w.cfg.DB)
	if err != nil {
		slog.Error("watch: schedules", "err", err)
		return nil
	}

	var hits []hit
	for _, job := range w.set.Jobs {
		if !watched(job) {
			continue
		}

		st := w.states[job.ID]
		if st == nil {
			st = &state{inactive: !job.Enabled || flags[job.ID].PausedAt != nil}
			w.states[job.ID] = st
		}

		if h := w.checkJob(job, st, flags[job.ID], rows[job.ID], now); h != nil {
			hits = append(hits, *h)
		}
	}
	return hits
}

// watched: the job has a window to keep (W1, W2).
func watched(job *model.Job) bool {
	if job.Expect > 0 {
		return true
	}
	return job.Kind == model.KindWatch && (job.Period > 0 || job.Schedule != "")
}

// checkJob judges one job; it returns a hit when the job just became missed.
func (w *Watch) checkJob(job *model.Job, st *state, fl model.Flags, row repos.Schedule, now time.Time) *hit {
	// The window rests while paused or disabled and starts anew on resume.
	if !job.Enabled || fl.PausedAt != nil {
		st.inactive, st.missed = true, time.Time{}
		return nil
	}
	if st.inactive {
		st.inactive, st.resumed = false, now
	}

	base := latest(row.FirstSeen, st.resumed)
	ok, err := repos.LastOKRun(w.cfg.DB, job.ID)
	if err != nil && !errors.Is(err, repos.ErrNotFound) {
		slog.Error("watch: last ok", "job", job.ID, "err", err)
		return nil
	}
	if err == nil {
		base = latest(base, endOf(ok))
	}

	deadline := w.deadline(job, base, now)
	if deadline.IsZero() || now.Before(deadline) {
		st.missed = time.Time{}
		return nil
	}

	// Within the grace after a start nothing becomes missed; a state kept
	// from before stays until a run ends it.
	if now.Before(w.started.Add(job.Timeout + job.Jitter + graceExtra)) {
		return nil
	}
	if !st.missed.IsZero() {
		return nil
	}

	st.missed = deadline
	return &hit{job, deadline}
}

// deadline is the instant by which the job needed an ok run; zero if none
// is due. base is the newest of last ok, first seen and resume.
func (w *Watch) deadline(job *model.Job, base, now time.Time) time.Time {
	if job.Expect > 0 {
		return base.Add(job.Expect)
	}
	if job.Period > 0 {
		return base.Add(job.Period + job.Grace)
	}

	// A watch with a schedule: the newest slot after base must have been
	// answered by an ok run within the grace.
	sched, err := cron.Parse(job.Schedule, job.Zone)
	if err != nil {
		return time.Time{}
	}
	due := sched.Last(now)
	if due.IsZero() || !due.After(base) {
		return time.Time{}
	}
	return due.Add(job.Grace)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// endOf is when a run ended; the start for a run without end.
func endOf(r model.Run) time.Time {
	if r.Ended.IsZero() {
		return r.Started
	}
	return r.Ended
}

// watchJob returns the watch with that id (nil if none) and the jobs commit.
func (w *Watch) watchJob(id string) (*model.Job, string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	job := w.set.Jobs[id]
	if job == nil || job.Kind != model.KindWatch {
		return nil, ""
	}
	return job, w.set.Commit
}

// Token returns the ping token of a watch, creating it on first use (W3).
// Without MASTER_KEY it returns crypto.ErrMissingKey; pings with an
// existing token still work, they only need its hash.
func (w *Watch) Token(id string) (string, error) {
	if job, _ := w.watchJob(id); job == nil {
		return "", ErrUnknown
	}

	w.tokenMu.Lock()
	defer w.tokenMu.Unlock()

	p, err := repos.GetPing(w.cfg.DB, id)
	if err == nil {
		return crypto.Decrypt(p.Enc, crypto.PurposePing)
	}
	if !errors.Is(err, repos.ErrNotFound) {
		return "", err
	}
	return w.newToken(id)
}

// Renew replaces the token; the old one stops working at once.
func (w *Watch) Renew(id string) (string, error) {
	if job, _ := w.watchJob(id); job == nil {
		return "", ErrUnknown
	}

	w.tokenMu.Lock()
	defer w.tokenMu.Unlock()
	return w.newToken(id)
}

// newToken stores a new token: hashed for lookup, sealed for display.
func (w *Watch) newToken(id string) (string, error) {
	token := crypto.NewToken()
	enc, err := crypto.Encrypt(token, crypto.PurposePing, nil)
	if err != nil {
		return "", err
	}

	err = repos.PutPing(w.cfg.DB, id, crypto.TokenHash(token), enc, w.cfg.Now())
	if err != nil {
		return "", err
	}
	return token, nil
}

// Allow counts one ping of addr against its rate limit (W3).
func (w *Watch) Allow(addr string) bool { return w.limit.allow(addr) }

// Ping records a ping of the watch the token belongs to (W2). from is the
// remote address without port, used for the rate limit.
func (w *Watch) Ping(token string, kind PingKind, body, from string) error {
	if !w.Allow(from) {
		return ErrThrottled
	}

	id, err := repos.JobByPing(w.cfg.DB, crypto.TokenHash(token))
	if errors.Is(err, repos.ErrNotFound) {
		return ErrUnknown
	}
	if err != nil {
		return err
	}

	job, commit := w.watchJob(id)
	if job == nil {
		return ErrUnknown
	}

	run, err := w.store(job, commit, kind, clip(body, bodyMax))
	if err != nil {
		return err
	}

	w.mu.Lock()
	cbs := w.onPing
	w.mu.Unlock()
	for _, fn := range cbs {
		fn(job, run)
	}
	return nil
}

// store writes the run of one ping: /start opens a running run, ok and
// fail close the open one, or insert a finished run if none is open.
func (w *Watch) store(job *model.Job, commit string, kind PingKind, body string) (model.Run, error) {
	now := w.cfg.Now()
	var out model.Run

	err := db.WithTx(w.cfg.DB, func(tx *sql.Tx) error {
		open, err := openRun(tx, job.ID)
		if err != nil {
			return err
		}

		// A second /start means the first run never ended.
		if kind == PingStart {
			if open != nil {
				open.State, open.Ended = model.RunAborted, now
				if err := repos.FinishRun(tx, *open); err != nil {
					return err
				}
			}
			out = newRun(job, commit, now, model.RunRunning, body)
			out.ID, err = repos.InsertRun(tx, out)
			return err
		}

		state := model.RunOK
		if kind == PingFail {
			state = model.RunFail
		}
		if open == nil {
			r := newRun(job, commit, now, state, body)
			if r.ID, err = repos.InsertRun(tx, r); err != nil {
				return err
			}
			open = &r
		}

		open.State, open.Ended, open.Output = state, now, body
		out = *open
		return repos.FinishRun(tx, out)
	})
	return out, err
}

func newRun(job *model.Job, commit string, now time.Time, state model.RunState, body string) model.Run {
	return model.Run{Job: job.ID, Trigger: model.TrigPing, State: state, Slot: now,
		Attempt: 1, Started: now, Output: body, Commit: commit}
}

// openRun returns the running ping run of job, or nil.
func openRun(q db.Queryer, id string) (*model.Run, error) {
	list, err := repos.RunningRuns(q)
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.Job == id && r.Trigger == model.TrigPing {
			return &r, nil
		}
	}
	return nil, nil
}

// clip cuts s to n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
