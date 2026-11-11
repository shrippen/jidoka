package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/beat"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/watch"
)

// Environment of the server (B2). Everything else is in /jobs/jidoka.toml
// or a secret.
const (
	envData    = "DATA_DIR"
	envJobs    = "JOBS_DIR"
	envKey     = "MASTER_KEY"
	envShim    = "JIDOKA_SHIM"
	envJobUID  = "JIDOKA_JOB_UID"
	envListen  = "LISTEN"
	defData    = "/data"
	defJobs    = "/jobs"
	defListen  = ":8080"
	dbFile     = "jidoka.db"
	saltFile   = "jidoka.salt"
	tickEvery  = time.Second
	pruneEvery = time.Hour
	stopWait   = 30 * time.Second
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
	listen  string
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

	uid, err := jobUID()
	if err != nil {
		d.Close()
		return nil, err
	}

	a := &app{db: d, listen: envOr(envListen, defListen)}
	a.secrets = secrets.New(d)
	a.jobs = jobs.New(d, envOr(envJobs, defJobs))
	a.runs = runs.New(d, a.secrets, runs.Config{DataDir: dataDir, Shim: os.Getenv(envShim), UID: uid, GID: uid})
	a.watch = watch.New(watch.Config{DB: d})
	a.alerts = alerts.New(alerts.Config{
		Store:   alerts.NewDBStore(d),
		Set:     a.jobs.Current,
		Secrets: a.secretMap,
	})
	a.plan = plan.New(plan.Config{DB: d, Exec: a.runs, OnFinal: a.final})
	a.beat = beat.New(a.beatURL)
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

	path := filepath.Join(dataDir, saltFile)
	salt, err := db.ReadSalt(path)
	if errors.Is(err, os.ErrNotExist) {
		salt = crypto.NewSalt()
		err = db.WriteSalt(path, salt)
	}
	if err != nil {
		return fmt.Errorf("salt: %w", err)
	}

	crypto.Init(crypto.Derive(secret, salt))
	return nil
}

// jobUID is the user scripts run as (Z3); 0 keeps the server's user
// (development only).
func jobUID() (uint32, error) {
	text := os.Getenv(envJobUID)
	if text == "" {
		return 0, nil
	}

	n, err := strconv.ParseUint(text, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", envJobUID, err)
	}
	return uint32(n), nil
}

// wire connects the services' hooks (see the drawing at app).
func (a *app) wire() {
	a.runs.OnStart(func(job *model.Job, run model.Run) {
		logErr("alert start", a.alerts.Started(job, run))
	})

	a.watch.OnMissed(func(job *model.Job, since time.Time) {
		logErr("alert missed", a.alerts.Missed(job, since))
	})

	// A ping is a run of a watch: start, then ok or fail.
	a.watch.OnPing(func(job *model.Job, run model.Run) {
		if run.State == model.RunRunning {
			logErr("alert start", a.alerts.Started(job, run))
			return
		}
		logErr("alert ping", a.alerts.RunDone(job, run, "", alerts.AttemptFinal))
	})

	a.jobs.OnReload(a.reloaded)
}

// final reports the end of a slot (after the last retry, J13).
func (a *app) final(job *model.Job, run model.Run) {
	logErr("alert run", a.alerts.RunDone(job, run, "", alerts.AttemptFinal))
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

	return nil
}

// reloaded passes a new set on and reports what changed (J20).
func (a *app) reloaded(d jobs.Diff, set model.Set) {
	a.plan.Apply(set)
	a.watch.Apply(set)
	a.reportInvalid(set)

	if d.Empty() {
		return
	}

	r := alerts.Reload{Added: d.Added, Changed: d.Changed, Removed: d.Removed, Fixed: fixed(set)}
	logErr("alert reload", a.alerts.Reloaded(r))
}

// fixed lists every loaded job: their load error, if any was open, is
// gone. The engine only closes "invalid" disturbances that exist.
func fixed(set model.Set) []string {
	ids := make([]string, 0, len(set.Jobs)+1)
	for id := range set.Jobs {
		ids = append(ids, id)
	}
	if set.SettingsErr == nil {
		ids = append(ids, alerts.KeyGlobal)
	}
	return ids
}

// reportInvalid raises "invalid" for every file that did not load; the
// engine's store keeps one disturbance per job, so repeats stay silent.
func (a *app) reportInvalid(set model.Set) {
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

			if now.Sub(pruned) >= pruneEvery {
				pruned = now
				logErr("prune", a.runs.Prune(set, now))
			}
		}
	}
}

// shutdown ends running jobs as aborted and waits for them (J23).
func (a *app) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()

	a.runs.CancelAll()
	logErr("stop planner", a.plan.Stop(ctx))
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
