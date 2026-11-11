// Package jobs holds the current model.Set, loaded from /jobs (J20).
//
//	Reload ──► conf.Load (whole tree, off to the side)
//	              │ commit changed meanwhile? read once more
//	              ▼
//	        swap under lock ──► Diff ──► OnReload hooks
//
// Readers get a complete old or a complete new Set, never a mix.
package jobs

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/db"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const (
	// kvSettings holds the raw bytes of the last valid jidoka.toml (B8).
	kvSettings   = "jobs.settings_valid"
	settingsFile = "jidoka.toml"

	// settingsID names a broken jidoka.toml in Diff.Invalid.
	settingsID = "jidoka.toml"
)

// Diff says what a reload changed, by job id.
type Diff struct {
	Added, Changed, Removed, Invalid []string
	Commit                           string
	At                               time.Time
}

// Empty reports whether nothing changed.
func (d Diff) Empty() bool {
	return len(d.Added)+len(d.Changed)+len(d.Removed) == 0
}

// Fetcher brings /jobs up to date before each load (J1); the git driver.
type Fetcher interface {
	Sync(ctx context.Context) error
}

// Service holds the set.
type Service struct {
	db  *sql.DB
	dir string

	mu    sync.RWMutex
	set   *model.Set
	valid *model.Settings // last valid jidoka.toml
	diff  Diff
	hooks []func(Diff, model.Set)

	// loadMu lets one load at a time run: fetch and read form one step.
	loadMu  sync.Mutex
	fetch   Fetcher
	baseURL string
}

// New returns a Service for the jobs folder dir. Call Load before use.
func New(d *sql.DB, dir string) *Service {
	return &Service{db: d, dir: dir, set: &model.Set{Jobs: map[string]*model.Job{}}}
}

// UseRepo makes every load fetch the jobs first (J1). A failed fetch keeps
// the checkout as it is and shows in Set.RepoErr.
func (s *Service) UseRepo(f Fetcher) { s.fetch = f }

// SetBaseURL overrides settings.base_url (BASE_URL); "" keeps the file's.
func (s *Service) SetBaseURL(u string) { s.baseURL = u }

// OnReload registers fn, called after every Reload with the new set.
func (s *Service) OnReload(fn func(Diff, model.Set)) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// Load reads /jobs at start. The last valid settings come from the
// database, so a broken jidoka.toml does not reset them (B8).
func (s *Service) Load() error {
	raw, ok, err := s.storedSettings()
	if err != nil {
		return err
	}

	if ok {
		st, lerr := conf.ParseSettings([]byte(raw))
		if lerr == nil {
			s.valid = &st
		}
	}

	_, err = s.load()
	return err
}

// storedSettings returns the saved bytes of the last valid jidoka.toml.
func (s *Service) storedSettings() (string, bool, error) {
	var raw string
	var ok bool
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		var err error
		raw, ok, err = repos.GetKV(tx, kvSettings)
		return err
	})

	return raw, ok, err
}

// Reload reads /jobs again and swaps the set in (J20).
func (s *Service) Reload() (Diff, error) {
	diff, err := s.load()
	if err != nil {
		return diff, err
	}

	s.mu.RLock()
	hooks := append([]func(Diff, model.Set){}, s.hooks...)
	set := *s.set
	s.mu.RUnlock()

	for _, fn := range hooks {
		fn(diff, set)
	}

	return diff, nil
}

// load reads the whole tree before it takes the lock. If a git pull moved
// HEAD while reading, the files may mix two commits: read once more.
func (s *Service) load() (Diff, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()

	s.mu.RLock()
	prev := s.valid
	s.mu.RUnlock()

	repoErr := s.fetchRepo()

	set := conf.Load(s.dir, prev)
	if conf.Commit(s.dir) != set.Commit {
		set = conf.Load(s.dir, prev)
	}
	set.RepoErr = repoErr
	if s.baseURL != "" {
		set.Settings.BaseURL = s.baseURL
	}

	valid, err := s.keepSettings(&set)
	if err != nil {
		return Diff{}, err
	}

	if err := s.ensureSeen(set); err != nil {
		return Diff{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if valid != nil {
		s.valid = valid
	}
	s.diff = makeDiff(s.set, &set)
	s.set = &set

	return s.diff, nil
}

// fetchRepo syncs the checkout; nil without a repo or on success.
func (s *Service) fetchRepo() *model.LoadError {
	if s.fetch == nil {
		return nil
	}

	err := s.fetch.Sync(context.Background())
	if err == nil {
		return nil
	}

	// The git driver strips the token from err.
	slog.Warn("jobs repo fetch", "err", err)

	m := i18n.M("conf.repo", "detail", err.Error())
	return &model.LoadError{File: model.RepoFile, Key: m.Key, Params: m.Params, Msg: m.Error()}
}

// keepSettings stores the jidoka.toml bytes when the file is valid, so a
// later broken file falls back to them after a restart (B8). It returns the
// settings to remember, nil when the file is missing or broken.
func (s *Service) keepSettings(set *model.Set) (*model.Settings, error) {
	if set.SettingsErr != nil {
		return nil, nil
	}

	src, err := os.ReadFile(filepath.Join(s.dir, settingsFile))
	if err != nil {
		return nil, nil
	}

	// Parse the bytes stored, not those Load saw: a write in between
	// must not store something that was never validated.
	st, lerr := conf.ParseSettings(src)
	if lerr != nil {
		return nil, nil
	}

	err = db.WithTx(s.db, func(tx *sql.Tx) error {
		return repos.SetKV(tx, kvSettings, string(src))
	})
	if err != nil {
		return nil, err
	}

	return &st, nil
}

// ensureSeen records when each job was first seen (W1 window start).
func (s *Service) ensureSeen(set model.Set) error {
	return db.WithTx(s.db, func(tx *sql.Tx) error {
		for id := range set.Jobs {
			if err := repos.EnsureSchedule(tx, id, set.LoadedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// makeDiff compares two sets by job hash.
func makeDiff(old, cur *model.Set) Diff {
	d := Diff{Commit: cur.Commit, At: cur.LoadedAt}

	for id, j := range cur.Jobs {
		o, ok := old.Jobs[id]
		switch {
		case !ok:
			d.Added = append(d.Added, id)
		case o.Hash != j.Hash:
			d.Changed = append(d.Changed, id)
		}
	}
	for id := range old.Jobs {
		if _, ok := cur.Jobs[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}

	seen := map[string]bool{}
	for _, e := range cur.Invalid {
		id := e.Job
		if id == "" {
			id = e.File
		}
		if !seen[id] {
			seen[id] = true
			d.Invalid = append(d.Invalid, id)
		}
	}
	if cur.SettingsErr != nil {
		d.Invalid = append(d.Invalid, settingsID)
	}
	if cur.RepoErr != nil {
		d.Invalid = append(d.Invalid, model.RepoFile)
	}

	sort.Strings(d.Added)
	sort.Strings(d.Changed)
	sort.Strings(d.Removed)
	sort.Strings(d.Invalid)

	return d
}

// Current returns the loaded set. The set is never modified after the
// swap; treat it as read-only.
func (s *Service) Current() model.Set {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return *s.set
}

// Job returns one job by id.
func (s *Service) Job(id string) (*model.Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	j, ok := s.set.Jobs[id]
	return j, ok
}

// LastDiff returns what the latest load changed, for the page.
func (s *Service) LastDiff() Diff {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.diff
}
