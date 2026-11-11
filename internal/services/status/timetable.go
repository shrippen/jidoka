package status

import (
	"database/sql"
	"time"

	"jidoka/internal/cron"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

// Mark is one run on a lane (O2).
type Mark struct {
	ID      int64
	State   model.RunState
	Trigger model.Trigger
	Start   time.Time
	End     time.Time // zero while running
}

// Overdue is a window in which a watch waited for a ping that did not
// come: from the end of its window up to now (O2).
type Overdue struct {
	Since, Until time.Time
}

// Lane is one job's row on the timetable.
type Lane struct {
	ID    string
	Name  string
	Kind  model.Kind
	State model.JobState
	// Removed: the job is gone but has runs in the window (J21).
	Removed bool

	Runs []Mark
	// Planned are the slots still to come inside the window, oldest first.
	Planned []time.Time
	// Dense: more than timetableCap slots follow; Planned is cut.
	Dense   bool
	Overdue []Overdue
}

// Timetable is yesterday, today and tomorrow (O2).
//
//	From                 Day                  Day+1d               To
//	 │◄──── yesterday ───►│◄───── today ──────►│◄──── tomorrow ────►│
//	                              ▲ Now
type Timetable struct {
	Zone     *time.Location
	From, To time.Time
	// Day is midnight of the chosen day in Zone.
	Day time.Time
	Now time.Time
	// Lanes follow the board's order.
	Lanes []Lane
}

// Timetable returns the window around day, in Jidoka's zone.
func (s *Service) Timetable(day time.Time) (Timetable, error) {
	set := s.d.Jobs.Current()
	now := s.d.Now()

	zone := set.Settings.Zone
	if zone == nil {
		zone = time.UTC
	}
	local := day.In(zone)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, zone)

	tt := Timetable{
		Zone: zone, Day: start, Now: now,
		From: start.AddDate(0, 0, -1), To: start.AddDate(0, 0, 2),
		Lanes: []Lane{},
	}

	tiles, err := s.tiles(set, now, withoutTail)
	if err != nil {
		return tt, err
	}

	var runs []model.Run
	err = db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		runs, err = repos.RunsBetween(tx, tt.From, tt.To)
		return err
	})
	if err != nil {
		return tt, err
	}

	byJob := map[string][]Mark{}
	for _, r := range runs {
		byJob[r.Job] = append(byJob[r.Job], Mark{ID: r.ID, State: r.State, Trigger: r.Trigger, Start: r.Started, End: r.Ended})
	}

	known := map[string]bool{}
	for _, t := range tiles {
		known[t.Job.ID] = true
		tt.Lanes = append(tt.Lanes, tt.lane(t, byJob[t.Job.ID]))
	}

	// Lanes of removed jobs, by id.
	for _, id := range sortedKeys(byJob) {
		if known[id] {
			continue
		}
		tt.Lanes = append(tt.Lanes, Lane{ID: id, Name: id, Kind: model.KindJob, Removed: true, Runs: byJob[id], Planned: []time.Time{}})
	}
	return tt, nil
}

// lane builds the row of one tile.
func (tt Timetable) lane(t Tile, marks []Mark) Lane {
	l := Lane{
		ID: t.Job.ID, Name: t.Job.Name, Kind: t.Job.Kind, State: t.State,
		Runs: marks, Planned: []time.Time{}, Overdue: []Overdue{},
	}
	if l.Runs == nil {
		l.Runs = []Mark{}
	}

	if t.State == model.JobMissed && t.Since.Before(tt.To) {
		l.Overdue = append(l.Overdue, Overdue{Since: laterOf(t.Since, tt.From), Until: tt.Now})
	}

	if !t.Job.Enabled || t.Paused || t.Job.Schedule == "" {
		return l
	}
	l.Planned, l.Dense = tt.slots(t.Job)
	return l
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// slots are the planned slots of job inside the window, from now on, in
// the job's zone: a slot in the missing hour of a DST change runs when the
// hour ends, a slot in the doubled hour runs once (J3).
func (tt Timetable) slots(job *model.Job) ([]time.Time, bool) {
	sched, err := cron.Parse(job.Schedule, job.Zone)
	if err != nil {
		return []time.Time{}, false
	}

	// Next is strictly after its argument: step back so a slot at From counts.
	after := tt.From.Add(-time.Nanosecond)
	if tt.Now.After(tt.From) {
		after = tt.Now
	}

	out := []time.Time{}
	for t := sched.Next(after); !t.IsZero() && t.Before(tt.To); t = sched.Next(t) {
		if len(out) == timetableCap {
			return out, true
		}
		out = append(out, t)
	}
	return out, false
}
