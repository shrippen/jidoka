//go:build !release

// Package demo seeds a jobs folder and a database from the shared demo
// world, for screenshots and QA (O10). It exists only in builds without
// the release tag; scripts/release-check.sh makes sure it stays out.
//
//	world.json ──► jobs folder (jidoka.toml, <id>/job.toml, run.sh)
//	           └─► jidoka.db: 30 days of runs, deliveries, secrets,
//	                          audit, flags, open alerts
//
// Nothing here sends anything: the channels in jidoka.toml point to
// hosts under .invalid, which never resolve.
package demo

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const (
	dbFile          = "jidoka.db"
	jobsDir         = "jobs"
	markFile        = ".jidoka-demo"
	envKey          = "MASTER_KEY"
	dataMode        = 0o700
	defaultKeepRuns = 500
)

// ErrNotDemo means the data folder holds something that is not a demo.
var ErrNotDemo = errors.New("demo: data folder is not empty and not a demo folder")

// ErrNoKey means MASTER_KEY is not set; the secrets cannot be sealed.
var ErrNoKey = errors.New("demo: MASTER_KEY not set")

// Prepare writes the jobs folder and the database below dataDir, in lang
// (de or en), with history up to now. An older demo in dataDir is
// replaced; a folder that is not a demo is refused. MASTER_KEY must be
// set; the same key opens the database later.
func Prepare(dataDir, lang string, now time.Time) (string, error) {
	d, err := loadData(lang)
	if err != nil {
		return "", err
	}

	key := os.Getenv(envKey)
	if key == "" {
		return "", ErrNoKey
	}

	if err := reset(dataDir); err != nil {
		return "", err
	}

	dir := filepath.Join(dataDir, jobsDir)
	if err := writeJobs(dir, d); err != nil {
		return "", err
	}

	set := conf.Load(dir, nil)
	if set.SettingsErr != nil {
		return "", fmt.Errorf("demo: jidoka.toml: %s", set.SettingsErr.Msg)
	}

	if err := initKey(dataDir, key); err != nil {
		return "", err
	}

	conn, err := db.Open(filepath.Join(dataDir, dbFile))
	if err != nil {
		return "", err
	}
	defer conn.Close()

	err = db.WithTx(conn, func(tx *sql.Tx) error {
		return seed(tx, d, set, now.UTC())
	})
	if err != nil {
		return "", err
	}

	return dir, nil
}

// reset empties an older demo folder; any other non-empty one is refused.
func reset(dataDir string) error {
	if err := os.MkdirAll(dataDir, dataMode); err != nil {
		return err
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}

	_, markErr := os.Stat(filepath.Join(dataDir, markFile))
	if len(entries) > 0 && markErr != nil {
		return ErrNotDemo
	}

	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dataDir, e.Name())); err != nil {
			return err
		}
	}

	return os.WriteFile(filepath.Join(dataDir, markFile), nil, fileMode)
}

// initKey writes the salt and derives the master key like jidoka serve.
func initKey(dataDir, key string) error {
	salt := crypto.NewSalt()
	if err := db.WriteSalt(filepath.Join(dataDir, dbFile), salt); err != nil {
		return err
	}

	crypto.Init(crypto.Derive(key, salt))
	return nil
}

// seed fills the database from the world; q is one transaction.
func seed(q db.Queryer, d data, set model.Set, now time.Time) error {
	b := builder{now: now, keepRuns: set.Settings.KeepRuns}
	if b.keepRuns <= 0 {
		b.keepRuns = defaultKeepRuns
	}

	hist := map[string]history{}
	var all []model.Run

	for _, def := range d.Jobs {
		job := set.Jobs[def.ID]
		if job == nil {
			continue
		}

		h, err := b.build(def, job)
		if err != nil {
			return fmt.Errorf("history %s: %w", def.ID, err)
		}
		hist[def.ID] = h
		all = append(all, h.runs...)
	}

	runs, err := storeRuns(q, all)
	if err != nil {
		return err
	}

	if err := seedSchedules(q, set, hist, now); err != nil {
		return err
	}
	if err := seedFlags(q, d, now); err != nil {
		return err
	}
	if err := seedAlerts(q, d, set, runs, now); err != nil {
		return err
	}
	if err := seedDeliveries(q, d, runs, now); err != nil {
		return err
	}

	return seedSecrets(q, d, now)
}

// storeRuns inserts the runs oldest first, so ids follow time; the
// result has the ids, per job in the same order.
func storeRuns(q db.Queryer, all []model.Run) (map[string][]model.Run, error) {
	sort.SliceStable(all, func(i, j int) bool { return all[i].Started.Before(all[j].Started) })

	out := map[string][]model.Run{}
	for _, r := range all {
		id, err := repos.InsertRun(q, r)
		if err != nil {
			return nil, err
		}

		r.ID = id
		if err := repos.FinishRun(q, r); err != nil {
			return nil, err
		}
		out[r.Job] = append(out[r.Job], r)
	}

	return out, nil
}

// seedSchedules marks every slot up to the newest as handled, so the
// planner starts no catchup, except for the running job.
func seedSchedules(q db.Queryer, set model.Set, hist map[string]history, now time.Time) error {
	first := now.AddDate(0, 0, -histDays)

	for id := range set.Jobs {
		if err := repos.EnsureSchedule(q, id, first); err != nil {
			return err
		}
		if hist[id].last.IsZero() {
			continue
		}
		if err := repos.SetLastSlot(q, id, hist[id].last); err != nil {
			return err
		}
	}

	return nil
}

func seedFlags(q db.Queryer, d data, now time.Time) error {
	for _, f := range d.Flags {
		if f.Paused != "" {
			at, err := offset(f.Paused, now)
			if err != nil {
				return err
			}
			if err := repos.SetPaused(q, f.Job, f.By, at); err != nil {
				return err
			}
		}

		if f.Acked != "" {
			at, err := offset(f.Acked, now)
			if err != nil {
				return err
			}
			if err := repos.SetAcked(q, f.Job, at); err != nil {
				return err
			}
		}

		if f.MutedUntil != "" {
			until, err := offset(f.MutedUntil, now)
			if err != nil {
				return err
			}
			if err := repos.SetMuted(q, f.Job, &until, false); err != nil {
				return err
			}
		}
	}

	return nil
}

func seedDeliveries(q db.Queryer, d data, runs map[string][]model.Run, now time.Time) error {
	for _, def := range d.Deliveries {
		at, err := offset(def.At, now)
		if err != nil {
			return err
		}

		del := model.Delivery{
			Job: def.Job, Event: model.Event(def.Event), Channel: def.Channel,
			At: at, OK: def.OK, Error: def.Error,
		}
		if r, ok := lastBefore(runs[def.Job], at); ok {
			del.RunID = &r.ID
		}

		if _, err := repos.AddDelivery(q, del); err != nil {
			return err
		}
	}

	return nil
}

// lastBefore is the newest run that started at or before t.
func lastBefore(runs []model.Run, t time.Time) (model.Run, bool) {
	for i := len(runs) - 1; i >= 0; i-- {
		if !runs[i].Started.After(t) {
			return runs[i], true
		}
	}

	return model.Run{}, false
}

// seedSecrets seals the dummy values and writes the audit trail, the
// world's entries and one for every secret, oldest first.
func seedSecrets(q db.Queryer, d data, now time.Time) error {
	type entry struct {
		at                  time.Time
		actor, action, what string
	}

	var trail []entry
	for _, s := range d.Secrets {
		at, err := offset(s.Changed, now)
		if err != nil {
			return err
		}

		enc, err := crypto.Encrypt(d.DummySecret, crypto.PurposeSecret, nil)
		if err != nil {
			return err
		}
		if err := repos.PutSecret(q, s.Name, enc, s.By, at); err != nil {
			return err
		}
		trail = append(trail, entry{at, s.By, "set", s.Name})
	}

	for _, a := range d.Audit {
		at, err := offset(a.At, now)
		if err != nil {
			return err
		}
		trail = append(trail, entry{at, a.Actor, a.Action, a.Target})
	}

	sort.SliceStable(trail, func(i, j int) bool { return trail[i].at.Before(trail[j].at) })
	for _, e := range trail {
		if err := repos.AddAudit(q, e.actor, e.action, e.what, e.at); err != nil {
			return err
		}
	}

	return nil
}
