package status

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/watch"
)

// Reason says why a job is disturbed, for the API (I1, I2).
type Reason string

const (
	ReasonNone    Reason = ""
	ReasonFailed  Reason = "failed" // last run failed or timed out
	ReasonMissed  Reason = "missed" // the watch window passed without an ok run
	ReasonInvalid Reason = "invalid"
)

// Tick is one run on a tile's history strip (O1).
type Tick struct {
	ID      int64
	State   model.RunState
	Trigger model.Trigger
	Attempt int
	Slot    time.Time
	Start   time.Time
	End     time.Time     // zero while running
	Length  time.Duration // up to now while running
	Summary string
	Error   string // Jidoka's own reason, e.g. a missing secret
	Exit    *int
}

// Tile is one job or watch on the board (O1).
//
// State is derived, in this order:
//
//	disabled       enabled = false
//	paused         paused in the UI
//	running        the newest run still runs
//	fail, timeout  the newest run that counts failed (aborted and skipped runs
//	               are looked through: they are no result)
//	missed         the watch window passed without an ok run
//	never          no run counts yet
//	ok
//
// An invalid job has a stub Job (Id, Name, File) and state invalid.
type Tile struct {
	// Job is read-only: it is shared with the loaded set.
	Job    *model.Job
	State  model.JobState
	Reason Reason
	// Load is the first error of an invalid job.
	Load *model.LoadError

	// Summary is the one-liner of the newest finished run that counts.
	Summary string
	// Since is when the disturbance began and Streak how many slots in a
	// row were disturbed (retries of one slot count once); zero unless the
	// state is a disturbance.
	Since  time.Time
	Streak int

	// Last is the newest run of any state; LastOK the newest success.
	Last   *Tick
	LastOK *Tick
	// Next is the next planned slot; for a watch the time its window ends.
	Next time.Time
	// Due is when the watch window ends (W1, W2), as the alarm reckons it
	// (watch.Due): from the last success, first sight or resume, plus
	// expect or period, or the slot left open plus grace for a schedule.
	// Past Due without an ok run, the job is missed.
	Due time.Time
	// Ticks are the last runs, oldest first: the strip reads left to right.
	Ticks []Tick
	// Tail holds the last lines of the newest run's output; only filled by
	// Overview and Job.
	Tail string

	Flags model.Flags
	// Paused, Acked and Muted are what the tile shows (M10).
	Paused, Acked, Muted bool
	// Busy: running or waiting in the queue.
	Busy bool
	// NeedsYou: a disturbance nobody acknowledged ("Braucht dich").
	NeedsYou bool
}

// Counts sums the board by state.
type Counts struct {
	Total, Jobs, Watches int
	// Attention counts the tiles with NeedsYou.
	Attention int
	ByState   map[model.JobState]int
}

// Reload is what the last load did (J20).
type Reload struct {
	Diff   jobs.Diff
	Commit string
	At     time.Time
}

// Board is the start page.
type Board struct {
	At time.Time
	// Tiles are sorted: fail, timeout and invalid first, then missed,
	// running, ok, never, and paused and disabled last; by name inside.
	Tiles  []Tile
	Counts Counts
	// Invalid lists every file that did not load, jidoka.toml first (O9).
	Invalid []model.LoadError
	// SettingsText is the catalog key of the jidoka.toml banner; "" when
	// the file loaded.
	SettingsText string
	Reload       Reload
	// Channels holds the channels that are down (M9).
	Channels []alerts.ChannelHealth
	// Tags are all tags of all jobs, sorted, for the filter (J17).
	Tags []string
}

// Board returns the board.
func (s *Service) Board() (Board, error) {
	return s.board(withoutTail)
}

// Overview is the board with output tails: the API list (I1).
func (s *Service) Overview() (Board, error) {
	return s.board(withTail)
}

type detail int

const (
	withoutTail detail = iota
	withTail
)

func (s *Service) board(dt detail) (Board, error) {
	set := s.d.Jobs.Current()
	now := s.d.Now()

	tiles, err := s.tiles(set, now, dt)
	if err != nil {
		return Board{}, err
	}

	b := Board{
		At: now, Tiles: tiles, Invalid: loadErrors(set), SettingsText: settingsText(set), Tags: tagsOf(tiles),
		Reload:   Reload{Diff: s.d.Jobs.LastDiff(), Commit: set.Commit, At: set.LoadedAt},
		Channels: []alerts.ChannelHealth{},
		Counts:   count(tiles),
	}

	if s.d.Alerts == nil {
		return b, nil
	}
	for _, h := range s.d.Alerts.Health() {
		if h.Down {
			b.Channels = append(b.Channels, h)
		}
	}
	return b, nil
}

func count(tiles []Tile) Counts {
	c := Counts{Total: len(tiles), ByState: map[model.JobState]int{}}
	for _, t := range tiles {
		c.ByState[t.State]++
		if t.Job.Kind == model.KindWatch {
			c.Watches++
		} else {
			c.Jobs++
		}
		if t.NeedsYou {
			c.Attention++
		}
	}
	return c
}

func tagsOf(tiles []Tile) []string {
	seen := map[string]bool{}
	for _, t := range tiles {
		for _, tag := range t.Job.Tags {
			seen[tag] = true
		}
	}
	return sortedKeys(seen)
}

// data is everything the tiles need, read in one snapshot.
type data struct {
	latest map[string]model.Run
	lastOK map[string]model.Run
	recent map[string][]model.Run // newest first
	flags  map[string]model.Flags
	sched  map[string]repos.Schedule
	alerts map[string]repos.Alert
	tails  map[string]string
}

// read loads the data of ids: a fixed number of queries, however many jobs.
func (s *Service) read(ids []string, dt detail) (data, error) {
	var d data
	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		if d.latest, err = repos.LatestRuns(tx); err != nil {
			return err
		}
		if d.lastOK, err = repos.LastOKRuns(tx); err != nil {
			return err
		}
		if d.recent, err = repos.RecentRuns(tx, ids, tickCount); err != nil {
			return err
		}
		if d.flags, err = repos.AllFlags(tx); err != nil {
			return err
		}
		if d.sched, err = repos.AllSchedules(tx); err != nil {
			return err
		}
		if d.alerts, err = repos.AllAlerts(tx); err != nil {
			return err
		}
		if dt == withTail {
			d.tails, err = repos.LatestTails(tx, tailBytes)
		}
		return err
	})
	return d, err
}

// invalidJobs maps the ids of jobs that did not load to their first error.
func invalidJobs(set model.Set) map[string]model.LoadError {
	out := map[string]model.LoadError{}
	for _, e := range set.Invalid {
		if _, seen := out[e.Job]; e.Job == "" || seen {
			continue
		}
		out[e.Job] = e
	}
	return out
}

// tiles builds every tile, sorted.
func (s *Service) tiles(set model.Set, now time.Time, dt detail) ([]Tile, error) {
	invalid := invalidJobs(set)
	ids := make([]string, 0, len(set.Jobs)+len(invalid))
	for id := range set.Jobs {
		ids = append(ids, id)
	}
	for id := range invalid {
		if set.Jobs[id] == nil {
			ids = append(ids, id)
		}
	}

	d, err := s.read(ids, dt)
	if err != nil {
		return nil, err
	}

	out := make([]Tile, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.tileOf(set, invalid, id, &d, now))
	}
	sortTiles(out)
	return out, nil
}

// tileOf builds the tile of a loaded job, or of one that did not load.
func (s *Service) tileOf(set model.Set, invalid map[string]model.LoadError, id string, d *data, now time.Time) Tile {
	if j := set.Jobs[id]; j != nil {
		return s.tile(j, d, now)
	}

	le := invalid[id]
	stub := &model.Job{ID: id, Name: id, Kind: model.KindJob, File: le.File, Enabled: true}
	return s.invalidTile(stub, &le, d, now)
}

// counts reports whether a run's state is a result: aborted (container stop,
// J23) and skipped (J5) runs say nothing about the job.
func counts(st model.RunState) bool {
	return st == model.RunOK || st == model.RunFail || st == model.RunTimeout || st == model.RunRunning
}

// meaningful returns the newest run that is a result, nil if none.
func meaningful(recent []model.Run) *model.Run {
	for i := range recent {
		if counts(recent[i].State) {
			return &recent[i]
		}
	}
	return nil
}

// derive is the state of a loaded job; see Tile.
func derive(job *model.Job, fl model.Flags, recent []model.Run, missed time.Time) (model.JobState, Reason) {
	switch {
	case !job.Enabled:
		return model.JobDisabled, ReasonNone
	case fl.PausedAt != nil:
		return model.JobPaused, ReasonNone
	}

	m := meaningful(recent)
	switch {
	case m != nil && m.State == model.RunRunning:
		return model.JobRunning, ReasonNone
	case m != nil && m.State == model.RunFail:
		return model.JobFail, ReasonFailed
	case m != nil && m.State == model.RunTimeout:
		return model.JobTimeout, ReasonFailed
	case !missed.IsZero():
		return model.JobMissed, ReasonMissed
	case m == nil:
		return model.JobNever, ReasonNone
	}
	return model.JobOK, ReasonNone
}

// streak walks the broken runs at the head of recent: when the first one
// began, and how many slots they cover (a retry shares its slot, J13).
func streak(recent []model.Run) (time.Time, int) {
	var since time.Time
	slots := map[time.Time]bool{}

	for _, r := range recent {
		if !counts(r.State) || r.State == model.RunRunning {
			continue
		}
		if !r.State.Broken() {
			break
		}
		since = r.Started
		slots[r.Slot] = true
	}
	return since, len(slots)
}

func tickOf(r model.Run, now time.Time) Tick {
	return Tick{
		ID: r.ID, State: r.State, Trigger: r.Trigger, Attempt: r.Attempt,
		Slot: r.Slot, Start: r.Started, End: r.Ended, Length: r.Duration(now),
		Summary: r.Summary, Error: r.Error, Exit: r.Exit,
	}
}

// base fills what every tile has, loaded or not.
func base(job *model.Job, d *data, now time.Time) Tile {
	id := job.ID
	fl := d.flags[id]
	recent := d.recent[id]

	t := Tile{Job: job, Flags: fl, Paused: fl.PausedAt != nil, Acked: fl.AckedAt != nil, Ticks: []Tick{}}
	t.Muted = (fl.MutedUntil != nil && now.Before(*fl.MutedUntil)) || fl.MuteRecover

	// Oldest first; recent is newest first.
	for i := len(recent) - 1; i >= 0; i-- {
		t.Ticks = append(t.Ticks, tickOf(recent[i], now))
	}
	if r, ok := d.latest[id]; ok {
		last := tickOf(r, now)
		t.Last = &last
	}
	if r, ok := d.lastOK[id]; ok {
		okTick := tickOf(r, now)
		t.LastOK = &okTick
	}
	for _, r := range recent {
		if r.State == model.RunOK || r.State.Broken() {
			t.Summary = r.Summary
			break
		}
	}
	t.Tail = tail(d.tails[id], tailLines)
	return t
}

// invalidTile is the tile of a job that did not load.
func (s *Service) invalidTile(job *model.Job, le *model.LoadError, d *data, now time.Time) Tile {
	t := base(job, d, now)
	t.State, t.Reason, t.Load = model.JobInvalid, ReasonInvalid, le
	t.NeedsYou = !t.Acked

	if a, ok := d.alerts[alerts.FileKey(job.ID)]; ok {
		t.Since, t.Streak = a.Since, a.Streak
	}
	return t
}

// tile derives the tile of a loaded job.
func (s *Service) tile(job *model.Job, d *data, now time.Time) Tile {
	t := base(job, d, now)

	var missed time.Time
	if s.d.Watch != nil {
		missed = s.d.Watch.State(job.ID)
	}
	recent := d.recent[job.ID]
	t.State, t.Reason = derive(job, t.Flags, recent, missed)
	t.fillWhen(s, d, recent, missed)

	if s.d.Plan != nil {
		t.Busy = s.d.Plan.Busy(job.ID)
	}
	t.NeedsYou = t.State.Broken() && !t.Acked
	return t
}

// fillWhen sets Since, Streak, Next and Due.
func (t *Tile) fillWhen(s *Service, d *data, recent []model.Run, missed time.Time) {
	job := t.Job

	if t.State.Broken() {
		t.Since, t.Streak = streak(recent)
		if t.State == model.JobMissed {
			t.Since, t.Streak = missed, 1
		}
		// The alert row counts slots with the retries rule of J13: it wins.
		if a, ok := d.alerts[job.ID]; ok {
			t.Since, t.Streak = a.Since, a.Streak
		}
	}

	// A paused or disabled job has no next slot and no window.
	if t.Paused || !job.Enabled {
		return
	}

	if job.Expect > 0 {
		var lastOK time.Time
		if t.LastOK != nil {
			lastOK = t.LastOK.End
		}
		t.Due = s.due(job, d.sched[job.ID].FirstSeen, lastOK)
	}

	if s.d.Plan != nil && job.Kind == model.KindJob {
		t.Next = s.d.Plan.Next(job.ID)
	}
	if job.Kind == model.KindWatch {
		t.Next = t.Due
	}
}

// rank orders the board: lower comes first.
func rank(st model.JobState) int {
	switch st {
	case model.JobFail, model.JobTimeout, model.JobInvalid:
		return 0
	case model.JobMissed:
		return 1
	case model.JobRunning:
		return 2
	case model.JobOK:
		return 3
	case model.JobNever:
		return 4
	}
	return 5 // paused, disabled
}

func sortTiles(tiles []Tile) {
	sort.SliceStable(tiles, func(i, j int) bool {
		a, b := tiles[i], tiles[j]
		if ra, rb := rank(a.State), rank(b.State); ra != rb {
			return ra < rb
		}
		if na, nb := strings.ToLower(a.Job.Name), strings.ToLower(b.Job.Name); na != nb {
			return na < nb
		}
		return a.Job.ID < b.Job.ID
	})
}

// due asks the watch, so board and alarm share one base (resume included).
func (s *Service) due(job *model.Job, firstSeen, lastOK time.Time) time.Time {
	if s.d.Watch == nil {
		return watch.Deadline(job, latest(firstSeen, lastOK))
	}
	return s.d.Watch.Due(job, firstSeen, lastOK)
}

func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
