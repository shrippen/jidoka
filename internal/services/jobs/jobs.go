// Package jobs holds the current model.Set: the stored settings and the
// jobs loaded from /jobs (J20).
//
//	Reload ──► fetch ──► settings from the database ──► conf.Load (whole tree, off to the side)
//	Revalidate ───────────────────┘                        │ commit changed meanwhile? read once more
//	                                                       ▼
//	                                      swap under lock ──► Diff ──► OnReload hooks
//
// Readers get a complete old or a complete new Set, never a mix.
package jobs

import (
	"context"
	"database/sql"
	"log/slog"
	"sort"
	"sync"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/db"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/repos"
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

// SetBaseURL overrides settings.base_url (BASE_URL); "" keeps the stored one.
func (s *Service) SetBaseURL(u string) { s.baseURL = u }

// OnReload registers fn, called after every Reload with the new set.
func (s *Service) OnReload(fn func(Diff, model.Set)) {
	s.mu.Lock()
	s.hooks = append(s.hooks, fn)
	s.mu.Unlock()
}

// Load reads the settings and /jobs at start.
func (s *Service) Load() error {
	_, err := s.load(withFetch)
	return err
}

// storedSettings returns the stored settings; the defaults of a fresh
// install when none are stored. A stored document that does not pass the
// check (it did when saved) is logged and replaced by the defaults, so
// the page can still open and fix it.
func (s *Service) storedSettings() (model.Settings, error) {
	var doc string
	var ok bool
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		var err error
		doc, ok, err = repos.GetSettings(tx)
		return err
	})
	if err != nil || !ok {
		return conf.DefaultSettings(), err
	}

	st, lerr := conf.ParseSettings([]byte(doc))
	if lerr != nil {
		slog.Error("stored settings invalid", "err", lerr.Msg)
		return conf.DefaultSettings(), nil
	}

	return st, nil
}

// Reload fetches the jobs repo, reads /jobs again and swaps the set in (J20).
func (s *Service) Reload() (Diff, error) {
	return s.reload(withFetch)
}

// Revalidate reads the settings and /jobs again without a fetch: after
// the settings changed, every job is checked against them anew.
func (s *Service) Revalidate() (Diff, error) {
	return s.reload(noFetch)
}

func (s *Service) reload(f fetchMode) (Diff, error) {
	diff, err := s.load(f)
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

// fetchMode says whether a load first brings the checkout up to date.
type fetchMode int

const (
	withFetch fetchMode = iota
	noFetch
)

// load reads the whole tree before it takes the lock. If a git pull moved
// HEAD while reading, the files may mix two commits: read once more.
func (s *Service) load(f fetchMode) (Diff, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()

	// A revalidation keeps the error of the last fetch; the checkout did
	// not change.
	var repoErr *model.LoadError
	if f == withFetch {
		repoErr = s.fetchRepo()
	} else {
		s.mu.RLock()
		repoErr = s.set.RepoErr
		s.mu.RUnlock()
	}

	st, err := s.storedSettings()
	if err != nil {
		return Diff{}, err
	}
	if s.baseURL != "" {
		st.BaseURL = s.baseURL
	}

	set := conf.Load(s.dir, st, conf.CheckFull)
	if conf.Commit(s.dir) != set.Commit {
		set = conf.Load(s.dir, st, conf.CheckFull)
	}
	set.RepoErr = repoErr

	if err := s.ensureSeen(set); err != nil {
		return Diff{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
