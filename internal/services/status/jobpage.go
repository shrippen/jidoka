package status

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/watch"
)

// RulesFrom tells where the effective notification rules come from (M3, M4).
type RulesFrom string

const (
	RulesJob     RulesFrom = "job"     // [[notify]] in job.toml
	RulesProfile RulesFrom = "profile" // notify_profile, or "standard"
)

const defaultProfile = "standard"

// Rules are the effective notification rules of a job (M11). The index in
// List is the one the test-message action takes.
type Rules struct {
	From RulesFrom
	// File is the job.toml or watch.toml, relative to /jobs; Profile the profile's name.
	File    string
	Profile string
	List    []model.Rule
}

// SecretRef is a secret a job names and whether it is set (J7).
type SecretRef struct {
	Name    string
	Present bool
}

// Duration is one point of the duration chart (O3).
type Duration struct {
	RunID int64
	At    time.Time
	Value time.Duration
	State model.RunState
}

// Ping is what the page of a watch shows to copy (W3).
type Ping struct {
	// NeedsKey: no MASTER_KEY, so the token cannot be shown. Pings with
	// the token still work.
	NeedsKey bool
	Token    string
	// Path is /ping/<token>; URL has the base_url in front when set.
	Path string
	URL  string
}

// JobPage is the page of one job or watch (O3).
type JobPage struct {
	Tile Tile
	// Removed: not loaded any more, only its runs remain (J21). Tile.Job is
	// a stub then.
	Removed bool
	Rules   Rules
	// Runs is one page of runs, newest first, without output.
	Runs []model.Run
	// NextBefore is the Page.Before of the following page; 0 at the end.
	NextBefore int64
	// Durations are the last runs that ended, oldest first.
	Durations []Duration
	// Ping is set for a watch only.
	Ping    *Ping
	Secrets []SecretRef
}

// Job returns the page of job id, or ErrNotFound if neither a job nor
// runs of it exist.
func (s *Service) Job(id string, pg Page) (JobPage, error) {
	set := s.d.Jobs.Current()
	now := s.d.Now()
	invalid := invalidJobs(set)

	d, err := s.read([]string{id}, withTail)
	if err != nil {
		return JobPage{}, err
	}

	p := JobPage{}
	_, bad := invalid[id]
	switch {
	case set.Jobs[id] != nil || bad:
		p.Tile = s.tileOf(set, invalid, id, &d, now)
	case len(d.recent[id]) > 0:
		p.Removed = true
		p.Tile = base(&model.Job{ID: id, Name: id, Kind: model.KindJob}, &d, now)
	default:
		return JobPage{}, ErrNotFound
	}

	p.Runs, p.NextBefore, err = s.runs(id, pg)
	if err != nil {
		return p, err
	}

	p.Durations = durations(p.Tile.Ticks)
	p.Rules = rulesOf(set, p.Tile.Job)
	p.Secrets = s.namedSecrets(p.Tile.Job)

	if p.Tile.Job.Kind == model.KindWatch && !p.Removed {
		p.Ping, err = s.ping(set, id)
	}
	return p, err
}

// Runs is the run list of job id for the API (I1).
func (s *Service) Runs(id string, pg Page) ([]model.Run, int64, error) {
	runs, next, err := s.runs(id, pg)
	if err != nil {
		return nil, 0, err
	}

	if len(runs) == 0 && pg.Before == 0 && s.d.Jobs.Current().Jobs[id] == nil {
		return nil, 0, ErrNotFound
	}
	return runs, next, nil
}

// runs reads one page; it asks for one more to know whether another follows.
func (s *Service) runs(id string, pg Page) ([]model.Run, int64, error) {
	limit := pg.limit()

	var list []model.Run
	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		list, err = repos.ListRuns(tx, id, limit+1, pg.Before)
		return err
	})
	if err != nil {
		return nil, 0, err
	}

	if len(list) <= limit {
		return append([]model.Run{}, list...), 0, nil
	}
	list = list[:limit]
	return list, list[limit-1].ID, nil
}

func durations(ticks []Tick) []Duration {
	out := []Duration{}
	for _, t := range ticks {
		if t.End.IsZero() || t.State == model.RunSkipped {
			continue
		}
		out = append(out, Duration{RunID: t.ID, At: t.Start, Value: t.Length, State: t.State})
	}
	return out
}

// rulesOf: the job's own rules replace the profile, never merge (M4).
func rulesOf(set model.Set, job *model.Job) Rules {
	if job.Rules != nil {
		return Rules{From: RulesJob, File: job.File, List: job.Rules}
	}

	name := job.Profile
	if name == "" {
		name = defaultProfile
	}
	return Rules{From: RulesProfile, File: job.File, Profile: name, List: set.Settings.Profiles[name]}
}

// namedSecrets lists the job's secrets with their presence.
func (s *Service) namedSecrets(job *model.Job) []SecretRef {
	out := []SecretRef{}
	if len(job.Secrets) == 0 || s.d.Secrets == nil {
		for _, n := range job.Secrets {
			out = append(out, SecretRef{Name: n})
		}
		return out
	}

	have := map[string]bool{}
	if list, err := s.d.Secrets.List(); err == nil {
		for _, i := range list {
			have[i.Name] = true
		}
	}
	for _, n := range job.Secrets {
		out = append(out, SecretRef{Name: n, Present: have[n]})
	}
	return out
}

// ping returns the token of a watch; the first sight creates it (W3).
func (s *Service) ping(set model.Set, id string) (*Ping, error) {
	if s.d.Watch == nil {
		return &Ping{NeedsKey: true}, nil
	}

	token, err := s.d.Watch.Token(id)
	if errors.Is(err, crypto.ErrMissingKey) {
		return &Ping{NeedsKey: true}, nil
	}
	if errors.Is(err, watch.ErrUnknown) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	path := "/ping/" + token
	return &Ping{Token: token, Path: path, URL: strings.TrimRight(set.Settings.BaseURL, "/") + path}, nil
}

// RunPage is the page of one run (O4).
type RunPage struct {
	Run model.Run // with output
	// Job is nil when the job is gone (J21); Name falls back to the id.
	Job  *model.Job
	Name string
	// Older and Newer are the neighbours in the same job; 0 = none.
	Older, Newer int64
	// Deliveries are the messages sent for this run, oldest first.
	Deliveries []model.Delivery
}

// Run returns one run with its output, its neighbours and deliveries.
func (s *Service) Run(id int64) (RunPage, error) {
	var p RunPage
	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		if p.Run, err = repos.GetRun(tx, id); err != nil {
			return err
		}
		if p.Older, p.Newer, err = repos.RunNeighbours(tx, p.Run.Job, id); err != nil {
			return err
		}
		p.Deliveries, err = repos.DeliveriesByRun(tx, id)
		return err
	})
	if errors.Is(err, repos.ErrNotFound) {
		return RunPage{}, ErrNotFound
	}
	if err != nil {
		return RunPage{}, err
	}

	p.Name = p.Run.Job
	if job, ok := s.d.Jobs.Job(p.Run.Job); ok {
		p.Job = job
		if job.Name != "" {
			p.Name = job.Name
		}
	}
	if p.Deliveries == nil {
		p.Deliveries = []model.Delivery{}
	}
	return p, nil
}
