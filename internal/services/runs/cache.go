package runs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/drivers/proc"
	"jidoka/internal/repos"
)

const (
	// defaultCacheLimit caps all uv caches together (J11).
	defaultCacheLimit int64 = 2 << 30

	// uvCache is the uv cache below a job's HOME.
	uvCache = ".cache/uv"
)

type cacheOf struct {
	job  string
	uid  uint32
	size int64
	last time.Time // newest run start; zero = never ran
}

// PruneCache keeps the uv caches of all jobs within the limit (J11). While
// the sum is over it, whole caches go, the job that ran longest ago first.
// The cache of a running job stays. A deleted cache is refilled by the
// job's next run.
//
//	home/a/.cache/uv  900 MB  last run 3 days ago   ← deleted first
//	home/b/.cache/uv  800 MB  last run 1 day ago
//	home/c/.cache/uv  700 MB  last run now          ← 2.4 GB > 2 GB: a goes
//
// The caches belong to the job uids (0700); the shim measures and deletes
// them as that uid (proc.DirSize, proc.RemoveDir).
func (s *Service) PruneCache() error {
	limit := s.cfg.CacheLimit
	if limit <= 0 {
		limit = defaultCacheLimit
	}

	caches, total := s.caches()
	if total <= limit {
		return nil
	}

	// Never-run jobs (zero time) sort first, ties by name.
	sort.Slice(caches, func(i, j int) bool {
		if !caches[i].last.Equal(caches[j].last) {
			return caches[i].last.Before(caches[j].last)
		}
		return caches[i].job < caches[j].job
	})

	var errs []error
	for _, c := range caches {
		if total <= limit {
			break
		}
		if !s.clear(c) {
			continue
		}

		err := proc.RemoveDir(s.cfg.Shim, c.uid, s.cacheDir(c.job))
		s.cleared(c.job)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		total -= c.size
		slog.Info("uv cache removed", "job", c.job, "bytes", c.size, "limit", limit)
	}

	return errors.Join(errs...)
}

// caches measures the uv cache of every job with a home folder, removed
// jobs included, and returns them with their sum. What cannot be measured
// is logged and left out.
func (s *Service) caches() ([]cacheOf, int64) {
	entries, err := os.ReadDir(filepath.Join(s.cfg.DataDir, "home"))
	if err != nil {
		return nil, 0
	}

	var uids map[string]uint32
	var lasts map[string]time.Time
	err = db.WithRead(s.db, func(tx *sql.Tx) error {
		var err error
		if uids, err = repos.JobUIDs(tx); err != nil {
			return err
		}
		lasts, err = repos.LastStarts(tx)
		return err
	})
	if err != nil {
		slog.Error("uv cache: database", "err", err)
		return nil, 0
	}

	var out []cacheOf
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		c := cacheOf{job: e.Name(), last: lasts[e.Name()]}
		if s.cfg.UIDBase != 0 {
			// No uid, no run, no cache.
			var ok bool
			if c.uid, ok = uids[c.job]; !ok {
				continue
			}
		}

		c.size, err = proc.DirSize(s.cfg.Shim, c.uid, s.cacheDir(c.job))
		if err != nil {
			slog.Error("uv cache: measure", "job", c.job, "err", err)
			continue
		}
		if c.size == 0 {
			continue
		}

		out = append(out, c)
		total += c.size
	}

	return out, total
}

func (s *Service) cacheDir(job string) string {
	return filepath.Join(s.cfg.DataDir, "home", job, uvCache)
}

// hold marks job as running until the returned func is called. While its
// cache is being deleted, hold waits: a run must not start on a half-empty
// cache.
func (s *Service) hold(ctx context.Context, job string) func() {
	for {
		s.mu.Lock()
		wait, deleting := s.clearing[job]
		if !deleting {
			s.busy[job]++
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			// Aborted anyway; proc.Run sees the cancelled context.
			s.mu.Lock()
			s.busy[job]++
			s.mu.Unlock()

			return func() { s.release(job) }
		}
	}

	return func() { s.release(job) }
}

func (s *Service) release(job string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.busy[job]--; s.busy[job] <= 0 {
		delete(s.busy, job)
	}
}

// clear claims the cache of c for deletion; false while a run holds it.
func (s *Service) clear(c cacheOf) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.busy[c.job] > 0 {
		return false
	}
	s.clearing[c.job] = make(chan struct{})

	return true
}

func (s *Service) cleared(job string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	close(s.clearing[job])
	delete(s.clearing, job)
}
