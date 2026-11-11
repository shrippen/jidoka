// Package status is the read model behind the pages and the JSON API. It
// only reads: every method returns plain structs, nothing here changes a
// job, a flag or a message (a watch's ping token is created on first sight,
// W3).
//
//	repos (one query per kind, for all jobs) ─┐
//	jobs.Current  plan.Next/Busy  watch.State ├─► tile(job) ─► Board, Timetable, Job, Run
//	alerts.Health  secrets.List               ┘
//
// Zero times mean "none": check IsZero in a template. Slices are never nil
// where a template ranges over them.
package status

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/watch"
)

const (
	// tickCount is the history on a tile: the last runs (O1).
	tickCount = 30
	// tailBytes is fetched per job for the output tail; tailLines are kept.
	tailBytes = 16 << 10
	tailLines = 20

	// PageDefault and PageMax bound a list of runs (I1).
	PageDefault = 50
	PageMax     = 500

	recentDeliveries = 50
	recentAudit      = 50
	countWindowDays  = 30
	timetableCap     = 300
)

// ErrNotFound means no such job or run.
var ErrNotFound = errors.New("status: not found")

// Deps wires the service. DB and Jobs are required; the others may be nil
// (the matching fields stay empty).
type Deps struct {
	DB      *sql.DB
	Jobs    *jobs.Service
	Plan    *plan.Planner
	Watch   *watch.Watch
	Alerts  *alerts.Engine
	Secrets *secrets.Service
	// Now defaults to time.Now.
	Now func() time.Time
	// Version is the build version, for Status.
	Version string
}

// Service is the read model.
type Service struct {
	d Deps
}

// New returns the read model.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// Page selects a slice of a run list: Limit (0 = PageDefault, at most
// PageMax) runs, newest first, with ids below Before (0 = from the newest).
type Page struct {
	Limit  int
	Before int64
}

func (p Page) limit() int {
	switch {
	case p.Limit <= 0:
		return PageDefault
	case p.Limit > PageMax:
		return PageMax
	}
	return p.Limit
}

// Status is GET /api/v1/status (I1).
type Status struct {
	Version string
	// Commit is the jobs commit of the last load.
	Commit     string
	ReloadedAt time.Time
	// Invalid counts the files that did not load, jidoka.toml included.
	Invalid  int
	Channels []alerts.ChannelHealth
	// LastTick is when the planner last ticked; zero before the first.
	LastTick time.Time
}

// Status returns the state of Jidoka itself.
func (s *Service) Status() Status {
	set := s.d.Jobs.Current()
	st := Status{
		Version: s.d.Version, Commit: set.Commit, ReloadedAt: set.LoadedAt,
		Invalid: len(loadErrors(set)),
	}

	if s.d.Alerts != nil {
		st.Channels = s.d.Alerts.Health()
	}
	if s.d.Plan != nil {
		st.LastTick = s.d.Plan.LastTick().UTC()
	}
	return st
}

// settingsText is the catalog key of the banner for the settings in force:
// none at all (no channels, also when only the jobs folder failed), the
// saved copy of a jidoka.toml that did not load, or "" for the file.
func settingsText(set model.Set) string {
	switch {
	case set.SettingsFrom == model.SettingsDefaults:
		return "board.invalid_settings_none"
	case set.SettingsErr == nil:
		return ""
	}
	return "board.invalid_settings"
}

// loadErrors lists every file that did not load: the repo fetch, then jidoka.toml (O9).
func loadErrors(set model.Set) []model.LoadError {
	out := []model.LoadError{}
	if set.RepoErr != nil {
		out = append(out, *set.RepoErr)
	}
	if set.SettingsErr != nil {
		out = append(out, *set.SettingsErr)
	}
	return append(out, set.Invalid...)
}

// Removed is a job id that has runs but is not loaded (J21).
type Removed struct {
	ID   string
	Runs int
	Last time.Time // start of the newest run
}

// Removed lists the job ids with stored runs that are not in the current
// set, by id. The page calls them "Entfernt".
func (s *Service) Removed() ([]Removed, error) {
	var all []repos.JobRuns
	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		all, err = repos.RunJobs(tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	set := s.d.Jobs.Current()
	invalid := map[string]bool{}
	for _, e := range set.Invalid {
		invalid[e.Job] = true
	}

	out := []Removed{}
	for _, j := range all {
		if set.Jobs[j.Job] != nil || invalid[j.Job] {
			continue
		}
		out = append(out, Removed{ID: j.Job, Runs: j.Count, Last: j.Last})
	}
	return out, nil
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
