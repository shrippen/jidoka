package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/drivers/git"
	"jidoka/internal/model"
	"jidoka/internal/redact"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/andon"
	"jidoka/internal/services/beat"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/ops"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/status"
	"jidoka/internal/services/watch"
)

// Environment of the server (B2). Everything else is in /jobs/jidoka.toml
// or a secret.
const (
	envData    = "DATA_DIR"
	envJobs    = "JOBS_DIR"
	envRepo    = "JOBS_REPO"
	envBranch  = "JOBS_BRANCH"
	envRepoTok = "JOBS_REPO_TOKEN"
	envBaseURL = "BASE_URL"
	envKey     = "MASTER_KEY"
	envShim    = "JIDOKA_SHIM"
	envUIDBase = "JIDOKA_UID_BASE"
	envListen  = "LISTEN"
	defData    = "/data"
	defJobs    = "/jobs"
	defListen  = ":8080"
	dbFile     = "jidoka.db"
	// checkoutDir is the jobs checkout inside DATA_DIR (J1).
	checkoutDir = "jobs"
	tickEvery   = time.Second
	pruneEvery  = time.Hour
	stopWait    = 30 * time.Second
	// repoRetry is how often a failed fetch of the jobs repo is tried again (J1).
	repoRetry = 5 * time.Minute
)

// app holds the wired services of one server process.
//
//	jobs ──Apply──► plan ──Execute──► runs ──OnStart──► alerts.Started
//	  │               └──OnFinal──────────────────────► alerts.RunDone
//	  └──Apply──► watch ──OnMissed / OnPing──────────► alerts
type app struct {
	db      *sql.DB
	secrets *secrets.Service
	jobs    *jobs.Service
	runs    *runs.Service
	plan    *plan.Planner
	watch   *watch.Watch
	alerts  *alerts.Engine
	beat    *beat.Sender
	andon   *andon.Notifier

	// states: last job state sent to Andon (I6).
	stateMu sync.Mutex
	states  map[string]model.JobState
	ops     *ops.Service
	status  *status.Service
	listen  string

	// Retry of a failed repo fetch: when it was last tried; one at a time.
	repoTry  time.Time
	retrying atomic.Bool
}

// serve runs Jidoka until SIGTERM or SIGINT (J23).
func serve() int {
	a, err := build()
	if err != nil {
		slog.Error("start failed", "err", err)
		return exitInvalid
	}
	defer a.db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := a.start(); err != nil {
		slog.Error("start failed", "err", err)
		return exitInvalid
	}

	go a.plan.Start(ctx)
	go a.loop(ctx)
	webErr := a.startWeb(ctx)

	select {
	case <-ctx.Done():
	case err := <-webErr:
		slog.Error("web server ended", "err", err)
	}

	a.shutdown()
	return exitOK
}

// build opens the database and creates the services. Without MASTER_KEY
// Jidoka still starts; jobs with secrets fail with a clear message (Z7).
func build() (*app, error) {
	dataDir := envOr(envData, defData)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := initKey(dataDir); err != nil {
		return nil, err
	}

	d, err := db.Open(filepath.Join(dataDir, dbFile))
	if err != nil {
		return nil, err
	}

	uidBase, err := jobUIDBase()
	if err != nil {
		d.Close()
		return nil, err
	}

	a := &app{db: d, listen: envOr(envListen, defListen), states: map[string]model.JobState{}}
	a.secrets = secrets.New(d)
	a.jobs = newJobs(d, dataDir)
	a.runs = runs.New(d, a.secrets, runs.Config{DataDir: dataDir, Shim: os.Getenv(envShim), UIDBase: uidBase})
	a.watch = watch.New(watch.Config{DB: d, Mask: a.mask})
	a.alerts = alerts.New(alerts.Config{
		Store:   alerts.NewDBStore(d),
		Set:     a.jobs.Current,
		Secrets: a.secretMap,
	})
	a.plan = plan.New(plan.Config{DB: d, Exec: a.runs, OnFinal: a.final})
	a.beat = beat.New(a.beatURL)
	a.andon = andon.New(a.andonURL)
	a.ops = ops.New(ops.Deps{DB: d, Jobs: a.jobs, Runs: a.runs, Watch: a.watch})
	a.status = status.New(status.Deps{
		DB: d, Jobs: a.jobs, Plan: a.plan, Watch: a.watch, Alerts: a.alerts,
		Secrets: a.secrets, Version: version,
	})
	a.wire()

	return a, nil
}

// initKey derives the key from MASTER_KEY and the salt next to the
// database; the first start writes the salt (Z7).
func initKey(dataDir string) error {
	secret := os.Getenv(envKey)
	if secret == "" {
		slog.Warn("MASTER_KEY not set: jobs with secrets will fail")
		return nil
	}

	// The salt lives next to the database (db.ReadSalt adds ".salt");
	// none yet means first start.
	path := filepath.Join(dataDir, dbFile)
	salt, err := db.ReadSalt(path)
	if err != nil {
		return fmt.Errorf("salt: %w", err)
	}
	if salt == nil {
		salt = crypto.NewSalt()
		if err := db.WriteSalt(path, salt); err != nil {
			return fmt.Errorf("salt: %w", err)
		}
	}

	crypto.Init(crypto.Derive(secret, salt))
	return nil
}

// jobUIDBase is where the jobs' own uids start (Z3); 0 keeps the server's
// user (development only).
func jobUIDBase() (uint32, error) {
	text := os.Getenv(envUIDBase)
	if text == "" {
		return 0, nil
	}

	n, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", envUIDBase, err)
	}
	return uint32(n), nil
}

// newJobs is the jobs service: with JOBS_REPO it fetches the private repo
// into DATA_DIR/jobs (J1), else it reads JOBS_DIR (development). BASE_URL
// overrides settings.base_url.
func newJobs(d *sql.DB, dataDir string) *jobs.Service {
	repo := os.Getenv(envRepo)
	if repo == "" {
		j := jobs.New(d, envOr(envJobs, defJobs))
		j.SetBaseURL(os.Getenv(envBaseURL))
		return j
	}

	dir := filepath.Join(dataDir, checkoutDir)
	j := jobs.New(d, dir)
	j.UseRepo(git.New(git.Config{
		URL: repo, Branch: os.Getenv(envBranch), Token: os.Getenv(envRepoTok), Dir: dir,
	}))
	j.SetBaseURL(os.Getenv(envBaseURL))

	return j
}

// wire connects the services' hooks (see the drawing at app).
func (a *app) wire() {
	a.runs.OnStart(func(job *model.Job, run model.Run) {
		logErr("alert start", a.alerts.Started(job, run))
	})

	a.watch.OnMissed(func(job *model.Job, since time.Time) {
		logErr("alert missed", a.alerts.Missed(job, since))
		a.changed(job.ID, model.JobMissed)
	})

	// A ping is a run of a watch: start, then ok or fail.
	a.watch.OnPing(func(job *model.Job, run model.Run) {
		if run.State == model.RunRunning {
			logErr("alert start", a.alerts.Started(job, run))
			return
		}
		logErr("alert ping", a.alerts.RunDone(job, run, "", alerts.AttemptFinal))
		a.changed(job.ID, jobState(run.State))
	})

	a.jobs.OnReload(a.reloaded)
}

// final reports the end of a slot (after the last retry, J13).
func (a *app) final(job *model.Job, run model.Run) {
	logErr("alert run", a.alerts.RunDone(job, run, "", alerts.AttemptFinal))
	a.changed(job.ID, jobState(run.State))
}

// changed tells Andon when a job's state differs from the last one seen
// in this process (I6), so its tile and hints follow at once. After a
// restart the first result of each job is sent once; Andon only drops its
// cache, so that is harmless.
func (a *app) changed(id string, state model.JobState) {
	if state == "" {
		return
	}

	a.stateMu.Lock()
	same := a.states[id] == state
	a.states[id] = state
	a.stateMu.Unlock()

	if !same {
		a.andon.Notify(id, state)
	}
}

// jobState maps a run's end to the board state; aborted and skipped
// runs change nothing (J23, J5).
func jobState(s model.RunState) model.JobState {
	switch s {
	case model.RunOK:
		return model.JobOK
	case model.RunFail:
		return model.JobFail
	case model.RunTimeout:
		return model.JobTimeout
	}
	return ""
}

// start loads the jobs and hands them to planner and watch. Runs that
// were running at the last stop end as aborted (J23).
func (a *app) start() error {
	if err := a.runs.Recover(); err != nil {
		return err
	}
	if err := a.jobs.Load(); err != nil {
		return err
	}

	set := a.jobs.Current()
	a.plan.Apply(set)
	a.watch.Apply(set)
	a.reportInvalid(set)

	// What broke before the stop and works now: close it, as a reload does.
	logErr("alert reload", a.alerts.Reloaded(alerts.Reload{Fixed: fixed(set), Known: known(set)}))

	return nil
}

// reloaded passes a new set on and reports what changed (J20).
func (a *app) reloaded(d jobs.Diff, set model.Set) {
	a.plan.Apply(set)
	a.watch.Apply(set)
	a.reportInvalid(set)

	// Also with an empty diff: a repaired jidoka.toml changes no job but
	// ends its disturbance. The engine sends "jobs changed" only when
	// jobs changed.
	r := alerts.Reload{Added: d.Added, Changed: d.Changed, Removed: d.Removed, Fixed: fixed(set), Known: known(set)}
	logErr("alert reload", a.alerts.Reloaded(r))
}

// fixed lists every loaded job: their load error, if any was open, is
// gone. The engine only closes "invalid" disturbances that exist.
func fixed(set model.Set) []string {
	ids := make([]string, 0, len(set.Jobs)+1)
	for id := range set.Jobs {
		ids = append(ids, id)
	}
	if set.SettingsErr == nil && !folderUnreadable(set) {
		ids = append(ids, alerts.KeyGlobal)
	}
	if set.RepoErr == nil {
		ids = append(ids, alerts.KeyRepo)
	}
	return ids
}

// known lists the job ids the engine may keep trouble for: loaded jobs and
// jobs with a load error. Trouble of any other id belongs to a removed job;
// nil when the folder could not be read.
func known(set model.Set) []string {
	// An unreadable folder tells nothing about which jobs exist.
	if folderUnreadable(set) {
		return nil
	}

	ids := make([]string, 0, len(set.Jobs))
	for id := range set.Jobs {
		ids = append(ids, id)
	}
	for _, le := range set.Invalid {
		if le.Job != "" {
			ids = append(ids, le.Job)
		}
	}
	return ids
}

// folderUnreadable: the jobs folder itself failed (an error without a job),
// which the engine files under KeyGlobal too.
func folderUnreadable(set model.Set) bool {
	for _, le := range set.Invalid {
		if le.Job == "" {
			return true
		}
	}
	return false
}

// reportInvalid raises "invalid" for every file that did not load; the
// engine's store keeps one disturbance per job, so repeats stay silent.
func (a *app) reportInvalid(set model.Set) {
	if set.RepoErr != nil {
		logErr("alert repo", a.alerts.Invalid(*set.RepoErr))
	}
	for _, le := range set.Invalid {
		logErr("alert invalid", a.alerts.Invalid(le))
	}
	if set.SettingsErr != nil {
		logErr("alert invalid", a.alerts.Invalid(*set.SettingsErr))
	}
}

// loop drives watch, messages, heartbeat and retention once a second.
func (a *app) loop(ctx context.Context) {
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()

	var pruned time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			a.watch.Tick(now)
			logErr("alerts tick", a.alerts.Tick(ctx, now))

			set := a.jobs.Current()
			a.beat.Tick(ctx, now, set.Settings.Heartbeat)
			go a.retryRepo(now)

			if now.Sub(pruned) >= pruneEvery {
				pruned = now
				logErr("prune", a.runs.Prune(set, now))
			}
		}
	}
}

// retryRepo reloads every repoRetry while the last fetch of the jobs repo
// failed (J1). It goes the way of the button: Reload, then reloaded runs
// the planner, the report and the closing of the disturbance. A fetch can
// take long, so the loop calls it in its own goroutine; one at a time.
func (a *app) retryRepo(now time.Time) {
	if !a.retrying.CompareAndSwap(false, true) {
		return
	}
	defer a.retrying.Store(false)

	if a.jobs.Current().RepoErr == nil {
		a.repoTry = time.Time{}
		return
	}

	// The first failure starts the clock; it was tried just before.
	if a.repoTry.IsZero() {
		a.repoTry = now
		return
	}
	if now.Sub(a.repoTry) < repoRetry {
		return
	}

	a.repoTry = now
	_, err := a.jobs.Reload()
	logErr("repo retry", err)
}

// shutdown ends running jobs as aborted and waits for them (J23).
func (a *app) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()

	// Halt the planner first, so no queued run starts after the cancel
	// and misses its SIGTERM.
	a.plan.Halt()
	a.runs.CancelAll()
	logErr("stop planner", a.plan.Stop(ctx))
}

// mask hides every secret value in a ping body (J7); without the key
// nothing can be read, so nothing is masked.
func (a *app) mask(text string) string {
	values, err := a.secrets.Values()
	if err != nil {
		return text
	}
	return redact.New(values).String(text)
}

// secretMap gives the alerts engine Jidoka's own secrets (Z5).
func (a *app) secretMap(names ...string) (map[string]string, error) {
	values, _, err := a.secrets.Resolve(names)
	return values, err
}

// beatURL is the push URL, "" when the secret is missing.
func (a *app) beatURL() string {
	values, _, err := a.secrets.Resolve([]string{beat.SecretURL})
	if err != nil {
		return ""
	}
	return values[beat.SecretURL]
}

// andonURL is Andon's webhook URL, "" when the secret is missing (I6).
func (a *app) andonURL() string {
	values, _, err := a.secrets.Resolve([]string{andon.SecretHookURL})
	if err != nil {
		return ""
	}
	return values[andon.SecretHookURL]
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func logErr(what string, err error) {
	if err != nil {
		slog.Error(what, "err", err)
	}
}
