// Package plan is the planner: it turns cron slots into runs (J3, J5, J9,
// J10, J13, J14, J15, B4).
//
//	Tick (1/s) ──► due slots per job ──► last_slot stored ──► item
//	                                                            │ jitter, retry gap: ready
//	                                                            ▼
//	                          queue (FIFO by ready) ──► pump ──► Executor (goroutine)
//	                          │ max_parallel, lock                 │
//	                          │ waited longer than the             ▼
//	                          │ next slot is away: skipped      finish ──► retry item
//	                          ▼                                          └► OnFinal
//	                       Run{skipped}
//
// last_slot is written before a slot starts, so a restart never fires a
// slot twice. All state sits behind one mutex; runs execute outside it.
package plan

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"jidoka/internal/cron"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const (
	tickEvery       = time.Second
	defaultParallel = 2

	// abortedLookback is how many runs redoAborted reads to find the last slot's.
	abortedLookback = 20

	// Skip reasons, stored in Run.Error of a skipped run.
	reasonRunning = "skip.running"
	reasonQueue   = "skip.queue"

	// Audit actions (O11).
	actionPause  = "pause"
	actionResume = "resume"
	actionRun    = "run"
	actionDry    = "dry"
)

var (
	// ErrUnknownJob means the id is not a planned job.
	ErrUnknownJob = errors.New("plan: unknown job")
	// ErrBusy means the job is running or waiting to run (J5).
	ErrBusy = errors.New("plan: job is busy")
	// ErrDisabled means enabled = false (J19).
	ErrDisabled = errors.New("plan: job is disabled")
	// ErrStopped means the planner is shutting down.
	ErrStopped = errors.New("plan: stopped")
)

// Executor runs one attempt and returns the finished run. The runs
// service implements it; it stores the run itself.
type Executor interface {
	Execute(ctx context.Context, job *model.Job, trig model.Trigger, slot time.Time, attempt int, commit string) model.Run
}

// Config wires the planner. DB and Exec are required.
type Config struct {
	DB   *sql.DB
	Exec Executor
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Rand returns a number in [0, n); nil means math/rand. Tests pin the jitter.
	Rand func(n int64) int64
	// OnFinal fires once per slot: after a success, or after the last
	// attempt (J13). It also fires for aborted and manual runs, never for
	// dry runs (J24). A retry that never started (paused, removed, queue
	// timeout) reports its last failed attempt. Called without locks.
	OnFinal func(job *model.Job, run model.Run)
}

// entry is what the planner knows of one planned job.
type entry struct {
	job    *model.Job
	sched  *cron.Schedule
	last   time.Time // last slot handled (stored)
	next   time.Time // cron.Next(last), cached
	paused bool
	// idle: the job did not run since it was paused or disabled; when it
	// becomes active again last jumps to now, so no catchup of the pause.
	idle bool
	// boot: first tick after process start; J10 catchup is due.
	boot bool
}

func (e *entry) active() bool { return e.job.Enabled && !e.paused }

// item is one run waiting for its turn.
type item struct {
	seq     int
	job     string
	ref     *model.Job // version at enqueue time, for OnFinal
	trig    model.Trigger
	slot    time.Time
	attempt int
	ready   time.Time
	prev    *model.Run // failed attempt a retry follows
}

// QueueItem is an item as the page shows it.
type QueueItem struct {
	Job     string
	Trigger model.Trigger
	Slot    time.Time
	Attempt int
	Ready   time.Time
	// Waiting: ready, but no free place or lock yet; otherwise jitter or
	// retry gap still runs.
	Waiting bool
}

type final struct {
	job *model.Job
	run model.Run
}

// Planner decides when jobs run.
type Planner struct {
	cfg Config

	mu      sync.Mutex
	set     model.Set
	applied bool
	entries map[string]*entry
	items   []item
	seq     int
	running map[string]bool
	locks   map[string]string // lock name → job holding it
	stopped bool
	finals  []final
	wg      sync.WaitGroup

	lastTick atomic.Int64 // unix nano
	quit     chan struct{}
	quitOnce sync.Once
}

// New returns a planner; call Apply, then Start.
func New(cfg Config) *Planner {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Int63n
	}
	return &Planner{
		cfg:     cfg,
		entries: map[string]*entry{},
		running: map[string]bool{},
		locks:   map[string]string{},
		quit:    make(chan struct{}),
	}
}

// unlock releases the mutex, then reports the finished slots collected
// meanwhile, so OnFinal never runs under the lock.
func (p *Planner) unlock() {
	fin := p.finals
	p.finals = nil
	p.mu.Unlock()

	if p.cfg.OnFinal == nil {
		return
	}
	for _, f := range fin {
		p.cfg.OnFinal(f.job, f.run)
	}
}

// Start ticks once a second until ctx ends or Stop is called. It blocks.
func (p *Planner) Start(ctx context.Context) {
	t := time.NewTicker(tickEvery)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.quit:
			return
		case <-t.C:
			p.Tick(p.cfg.Now())
		}
	}
}

// LastTick is when the planner last ticked (B5, /healthz); zero before the first.
func (p *Planner) LastTick() time.Time {
	n := p.lastTick.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Halt stops ticking and starting runs and drops the queue; running runs
// go on. It returns at once and may be called repeatedly. Dropped slots
// come back after the next start for catchup jobs (J10): a run the stop
// aborts is redone, a queued one is lost without catchup.
func (p *Planner) Halt() {
	p.quitOnce.Do(func() { close(p.quit) })

	p.mu.Lock()
	p.stopped = true
	for _, it := range p.items {
		p.drop(it)
	}
	p.items = nil
	p.unlock()
}

// Stop halts the planner and waits for running runs until ctx ends.
// Cancelling them is the runs service's job.
func (p *Planner) Stop(ctx context.Context) error {
	p.Halt()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Apply installs a freshly loaded set: removed jobs stop being planned
// (running ones finish with their old version), new ones get a schedule row.
func (p *Planner) Apply(set model.Set) {
	p.mu.Lock()
	defer p.unlock()

	now := p.cfg.Now()
	boot := !p.applied
	p.applied, p.set = true, set

	flags, err := repos.AllFlags(p.cfg.DB)
	if err != nil {
		slog.Error("plan: flags", "err", err)
	}
	rows, err := repos.AllSchedules(p.cfg.DB)
	if err != nil {
		slog.Error("plan: schedules", "err", err)
	}

	for id, job := range set.Jobs {
		if err := repos.EnsureSchedule(p.cfg.DB, id, now); err != nil {
			slog.Error("plan: schedule row", "job", id, "err", err)
		}
		p.applyJob(job, flags[id], rows[id], boot, now)
	}

	for id := range p.entries {
		if set.Jobs[id] == nil {
			p.forget(id)
		}
	}
}

// applyJob creates or updates the entry of one job.
func (p *Planner) applyJob(job *model.Job, fl model.Flags, row repos.Schedule, boot bool, now time.Time) {
	if job.Kind != model.KindJob || job.Schedule == "" {
		p.forget(job.ID)
		return
	}

	sched, err := cron.Parse(job.Schedule, job.Zone)
	if err != nil {
		slog.Error("plan: schedule", "job", job.ID, "err", err)
		p.forget(job.ID)
		return
	}

	e := p.entries[job.ID]
	switch {
	case e == nil:
		e = &entry{job: job, sched: sched, paused: fl.PausedAt != nil}
		e.idle = !e.active()
		p.entries[job.ID] = e

		// Only the first load after a start can catch up on missed slots;
		// a job that returns later starts fresh.
		if boot && !row.LastSlot.IsZero() {
			e.last, e.boot = row.LastSlot, true
			break
		}
		p.setLast(e, now)
	case e.job.Schedule != job.Schedule || e.job.Zone.String() != job.Zone.String():
		e.job, e.sched = job, sched
		p.setLast(e, now)
	default:
		e.job = job
	}
	e.next = e.sched.Next(e.last)
}

// forget stops planning id and drops what waits for it.
func (p *Planner) forget(id string) {
	delete(p.entries, id)
	p.dropJob(id)
}

// dropJob removes the waiting items of id.
func (p *Planner) dropJob(id string) {
	kept := p.items[:0]
	for _, it := range p.items {
		if it.job != id {
			kept = append(kept, it)
			continue
		}
		p.drop(it)
	}
	p.items = kept
}

// drop discards an item that never started. A retry reports the failure
// it followed, or the slot would end without a result.
func (p *Planner) drop(it item) {
	if it.prev == nil {
		return
	}
	p.finals = append(p.finals, final{it.ref, *it.prev})
}

// setLast stores the slot just handled and moves next on. It reports
// whether the slot is stored; if not, the caller must not fire it.
func (p *Planner) setLast(e *entry, slot time.Time) bool {
	if err := repos.SetLastSlot(p.cfg.DB, e.job.ID, slot); err != nil {
		slog.Error("plan: last slot", "job", e.job.ID, "err", err)
		return false
	}
	e.last, e.next = slot, e.sched.Next(slot)
	return true
}

// Tick plans everything due at now and starts what may start.
func (p *Planner) Tick(now time.Time) {
	p.lastTick.Store(now.UnixNano())

	p.mu.Lock()
	defer p.unlock()
	if p.stopped {
		return
	}

	for _, id := range p.ids() {
		p.plan(p.entries[id], now)
	}
	p.pump(now)
}

// ids lists the planned jobs in a fixed order.
func (p *Planner) ids() []string {
	ids := make([]string, 0, len(p.entries))
	for id := range p.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// plan fires the slots of one job that are due at now.
func (p *Planner) plan(e *entry, now time.Time) {
	if !e.active() {
		e.idle = true
		return
	}

	if e.idle {
		e.idle, e.boot = false, false
		p.setLast(e, now)
		return
	}

	if e.boot {
		e.boot = false
		p.catchup(e, now)
	}

	for !e.next.IsZero() && !e.next.After(now) {
		slot := e.next
		if !p.setLast(e, slot) {
			return
		}
		p.enqueue(e, slot, model.TrigSchedule, now)
	}
}

// catchup runs the newest slot missed while the process was off, once
// (J10). Without catchup the missed slots are just skipped over.
func (p *Planner) catchup(e *entry, now time.Time) {
	last := e.sched.Last(now)
	if last.IsZero() || !last.After(e.last) {
		p.redoAborted(e, now)
		return
	}
	if !p.setLast(e, last) {
		return
	}
	if !e.job.Catchup {
		return
	}
	p.enqueue(e, last, model.TrigCatchup, now)
}

// redoAborted runs the stored slot again if a container stop cut off its
// last run (J23, J10): last_slot is written before a start, so without
// this the slot would be lost. The slot's newest run must be aborted;
// once any other run followed, nothing repeats.
func (p *Planner) redoAborted(e *entry, now time.Time) {
	if !e.job.Catchup || e.last.IsZero() {
		return
	}

	runs, err := repos.ListRuns(p.cfg.DB, e.job.ID, abortedLookback, 0)
	if err != nil {
		slog.Error("plan: runs", "job", e.job.ID, "err", err)
		return
	}
	for _, r := range runs {
		if !r.Slot.Equal(e.last) {
			continue
		}
		if r.State == model.RunAborted {
			p.enqueue(e, e.last, model.TrigCatchup, now)
		}
		return
	}
}

// enqueue turns a due slot into an item, or records it as skipped when
// the job is still busy (J5).
func (p *Planner) enqueue(e *entry, slot time.Time, trig model.Trigger, now time.Time) {
	if p.busy(e.job.ID) {
		p.skip(e.job, trig, slot, 1, now, reasonRunning)
		return
	}

	// Jitter delays the start only; the slot keeps the scheduled time (J14).
	ready := now
	if e.job.Jitter > 0 {
		ready = now.Add(time.Duration(p.cfg.Rand(int64(e.job.Jitter))))
	}
	p.push(item{job: e.job.ID, ref: e.job, trig: trig, slot: slot, attempt: 1, ready: ready})
}

func (p *Planner) push(it item) {
	p.seq++
	it.seq = p.seq
	p.items = append(p.items, it)
}

// busy: running or waiting, retry gap included.
func (p *Planner) busy(id string) bool {
	if p.running[id] {
		return true
	}
	for _, it := range p.items {
		if it.job == id {
			return true
		}
	}
	return false
}

// skip stores a run that did not happen; the board shows it as "entfallen".
func (p *Planner) skip(job *model.Job, trig model.Trigger, slot time.Time, attempt int, now time.Time, reason string) {
	r := model.Run{Job: job.ID, Trigger: trig, State: model.RunSkipped, Slot: slot,
		Attempt: attempt, Started: now, Commit: p.set.Commit}

	err := db.WithTx(p.cfg.DB, func(tx *sql.Tx) error {
		id, err := repos.InsertRun(tx, r)
		if err != nil {
			return err
		}
		r.ID, r.Ended, r.Error = id, now, reason
		return repos.FinishRun(tx, r)
	})
	if err != nil {
		slog.Error("plan: skipped run", "job", job.ID, "err", err)
	}
}

// pump drops items that waited too long, then starts what fits (B4, J15).
func (p *Planner) pump(now time.Time) {
	if p.stopped {
		return
	}
	p.expire(now)

	for len(p.running) < p.parallel() {
		i := p.pick(now)
		if i < 0 {
			return
		}
		it := p.items[i]
		p.items = append(p.items[:i], p.items[i+1:]...)
		p.launch(it)
	}
}

func (p *Planner) parallel() int {
	if p.set.Settings.MaxParallel > 0 {
		return p.set.Settings.MaxParallel
	}
	return defaultParallel
}

// expire drops items whose job is gone or inactive, and scheduled items
// that waited longer than the distance to the job's next slot: the next
// slot would find the job busy anyway (B4, J5).
func (p *Planner) expire(now time.Time) {
	kept := p.items[:0]
	for _, it := range p.items {
		e := p.entries[it.job]
		switch {
		case e == nil || !p.mayRun(e, it):
			p.drop(it)
		case p.tooLate(e, it, now):
			p.skip(e.job, it.trig, it.slot, it.attempt, now, reasonQueue)
			p.drop(it)
		default:
			kept = append(kept, it)
		}
	}
	p.items = kept
}

// mayRun: scheduled items need an active job; a run by hand only needs an
// enabled one, so a paused job can still be started from the page (J9).
func (p *Planner) mayRun(e *entry, it item) bool {
	if it.trig == model.TrigManual || it.trig == model.TrigDry {
		return e.job.Enabled
	}
	return e.active()
}

// tooLate reports whether it waited longer than its slot is away from the next.
func (p *Planner) tooLate(e *entry, it item, now time.Time) bool {
	if it.trig == model.TrigManual || it.trig == model.TrigDry || it.ready.After(now) {
		return false
	}

	next := e.sched.Next(it.slot)
	if next.IsZero() {
		return false
	}
	return now.Sub(it.ready) > next.Sub(it.slot)
}

// pick returns the index of the next item to start: oldest ready first,
// skipping those whose lock is held; -1 if none.
func (p *Planner) pick(now time.Time) int {
	best := -1
	for i, it := range p.items {
		if it.ready.After(now) || p.locked(it) {
			continue
		}
		if best >= 0 && !before(it, p.items[best]) {
			continue
		}
		best = i
	}
	return best
}

func before(a, b item) bool {
	if a.ready.Equal(b.ready) {
		return a.seq < b.seq
	}
	return a.ready.Before(b.ready)
}

// locked: another running job holds the lock of it (J15).
func (p *Planner) locked(it item) bool {
	e := p.entries[it.job]
	if e == nil || e.job.Lock == "" {
		return false
	}
	_, held := p.locks[e.job.Lock]
	return held
}

// launch starts one run in its own goroutine. The timeout clock starts
// inside Execute, so the wait in the queue does not count.
func (p *Planner) launch(it item) {
	e := p.entries[it.job]
	job, commit := e.job, p.set.Commit

	p.running[it.job] = true
	if job.Lock != "" {
		p.locks[job.Lock] = it.job
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		run := p.cfg.Exec.Execute(context.Background(), job, it.trig, it.slot, it.attempt, commit)
		p.finish(it, job, run)
	}()
}

// finish books a finished run: a retry, or the end of the slot.
func (p *Planner) finish(it item, job *model.Job, run model.Run) {
	p.mu.Lock()
	defer p.unlock()

	delete(p.running, it.job)
	if job.Lock != "" {
		delete(p.locks, job.Lock)
	}

	now := p.cfg.Now()
	p.settle(it, job, run, now)
	p.pump(now)
}

// settle schedules the next attempt, or reports the slot as done (J13).
func (p *Planner) settle(it item, job *model.Job, run model.Run, now time.Time) {
	if it.trig == model.TrigDry {
		return
	}

	e := p.entries[it.job]
	retry := e != nil && e.active() && !p.stopped && run.State.Broken() &&
		it.trig != model.TrigManual && it.attempt <= e.job.Retries
	if !retry {
		p.finals = append(p.finals, final{job, run})
		return
	}

	p.push(item{job: it.job, ref: e.job, trig: model.TrigRetry, slot: it.slot,
		attempt: it.attempt + 1, ready: now.Add(e.job.RetryGap), prev: &run})
}

// Next is the job's next slot; zero when paused, disabled or unknown.
func (p *Planner) Next(id string) time.Time {
	p.mu.Lock()
	defer p.unlock()

	e := p.entries[id]
	if e == nil || !e.active() {
		return time.Time{}
	}
	return e.sched.Next(p.cfg.Now())
}

// Busy reports whether the job is running or waiting.
func (p *Planner) Busy(id string) bool {
	p.mu.Lock()
	defer p.unlock()
	return p.busy(id)
}

// Queue lists the waiting items, next first.
func (p *Planner) Queue() []QueueItem {
	p.mu.Lock()
	defer p.unlock()

	now := p.cfg.Now()
	items := append([]item(nil), p.items...)
	sort.Slice(items, func(i, j int) bool { return before(items[i], items[j]) })

	out := make([]QueueItem, 0, len(items))
	for _, it := range items {
		out = append(out, QueueItem{Job: it.job, Trigger: it.trig, Slot: it.slot,
			Attempt: it.attempt, Ready: it.ready, Waiting: !it.ready.After(now)})
	}
	return out
}

// RunNow starts the job by hand (J9). It refuses a busy job (J5); a full
// parallel limit queues it.
func (p *Planner) RunNow(id, actor string) error {
	return p.manual(id, actor, model.TrigManual, actionRun)
}

// DryRun starts the job with JIDOKA_TRIGGER=dry (J24).
func (p *Planner) DryRun(id, actor string) error {
	return p.manual(id, actor, model.TrigDry, actionDry)
}

func (p *Planner) manual(id, actor string, trig model.Trigger, action string) error {
	p.mu.Lock()
	defer p.unlock()

	e := p.entries[id]
	switch {
	case p.stopped:
		return ErrStopped
	case e == nil:
		return ErrUnknownJob
	case !e.job.Enabled:
		return ErrDisabled
	case p.busy(id):
		return ErrBusy
	}

	now := p.cfg.Now()
	if err := repos.AddAudit(p.cfg.DB, actor, action, id, now); err != nil {
		return err
	}

	p.push(item{job: id, ref: e.job, trig: trig, slot: now, attempt: 1, ready: now})
	p.pump(now)
	return nil
}

// Pause stops the job from firing; the flag survives restarts (J9).
// Waiting items of the job are dropped, a running run finishes.
func (p *Planner) Pause(id, actor string) error {
	p.mu.Lock()
	defer p.unlock()

	e := p.entries[id]
	if e == nil {
		return ErrUnknownJob
	}

	now := p.cfg.Now()
	err := db.WithTx(p.cfg.DB, func(tx *sql.Tx) error {
		if err := repos.SetPaused(tx, id, actor, now); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionPause, id, now)
	})
	if err != nil {
		return err
	}

	e.paused, e.idle = true, true
	p.dropJob(id)
	return nil
}

// Resume lets the job fire again. The paused time is not caught up: last
// slot moves to now.
func (p *Planner) Resume(id, actor string) error {
	p.mu.Lock()
	defer p.unlock()

	e := p.entries[id]
	if e == nil {
		return ErrUnknownJob
	}

	now := p.cfg.Now()
	err := db.WithTx(p.cfg.DB, func(tx *sql.Tx) error {
		if err := repos.ClearPaused(tx, id); err != nil {
			return err
		}
		if err := repos.SetLastSlot(tx, id, now); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionResume, id, now)
	})
	if err != nil {
		return err
	}

	e.paused, e.idle, e.boot = false, false, false
	e.last, e.next = now, e.sched.Next(now)
	return nil
}
