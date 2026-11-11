package repos

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
)

// Schedule is what the planner remembers of one job.
type Schedule struct {
	Job       string
	FirstSeen time.Time
	LastSlot  time.Time // zero: no slot handled yet
}

func scanSchedule(s scanner) (Schedule, error) {
	var (
		sc    Schedule
		first string
		last  sql.NullString
	)
	if err := s.Scan(&sc.Job, &first, &last); err != nil {
		return sc, err
	}

	var err error
	if sc.FirstSeen, err = parseTime(first); err != nil {
		return sc, err
	}
	slot, err := parseNull(last)
	if err != nil {
		return sc, err
	}
	if slot != nil {
		sc.LastSlot = *slot
	}
	return sc, nil
}

// EnsureSchedule records when job was first seen; a known job keeps its date.
func EnsureSchedule(q db.Queryer, job string, firstSeen time.Time) error {
	_, err := q.Exec("INSERT OR IGNORE INTO schedule (job, first_seen) VALUES (?, ?)",
		job, db.TimeStr(firstSeen))
	return err
}

// LastSlot returns the last slot handled for job; false when none.
func LastSlot(q db.Queryer, job string) (time.Time, bool, error) {
	sc, err := queryOne(q, scanSchedule, "SELECT job, first_seen, last_slot FROM schedule WHERE job = ?", job)
	if err == ErrNotFound {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return sc.LastSlot, !sc.LastSlot.IsZero(), nil
}

// SetLastSlot records the slot just handled. A job without a row gets one,
// first seen at the slot.
func SetLastSlot(q db.Queryer, job string, slot time.Time) error {
	_, err := q.Exec(`INSERT INTO schedule (job, first_seen, last_slot) VALUES (?, ?, ?)
		ON CONFLICT (job) DO UPDATE SET last_slot = excluded.last_slot`,
		job, db.TimeStr(slot), db.TimeStr(slot))
	return err
}

// AllSchedules returns the schedule rows by job.
func AllSchedules(q db.Queryer) (map[string]Schedule, error) {
	list, err := queryAll(q, scanSchedule, "SELECT job, first_seen, last_slot FROM schedule")
	if err != nil {
		return nil, err
	}

	out := make(map[string]Schedule, len(list))
	for _, sc := range list {
		out[sc.Job] = sc
	}
	return out, nil
}
