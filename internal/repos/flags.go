package repos

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

const flagCols = "job, paused_at, paused_by, acked_at, muted_until, mute_recover"

func scanFlags(s scanner) (model.Flags, error) {
	var (
		f                    model.Flags
		paused, acked, muted sql.NullString
		recoverFlag          int
	)
	if err := s.Scan(&f.Job, &paused, &f.PausedBy, &acked, &muted, &recoverFlag); err != nil {
		return f, err
	}
	f.MuteRecover = recoverFlag != 0

	var err error
	if f.PausedAt, err = parseNull(paused); err != nil {
		return f, err
	}
	if f.AckedAt, err = parseNull(acked); err != nil {
		return f, err
	}
	f.MutedUntil, err = parseNull(muted)
	return f, err
}

// GetFlags returns the flags of job; a job without a row has none set.
func GetFlags(q db.Queryer, job string) (model.Flags, error) {
	f, err := queryOne(q, scanFlags, "SELECT "+flagCols+" FROM flags WHERE job = ?", job)
	if err == ErrNotFound {
		return model.Flags{Job: job}, nil
	}
	return f, err
}

// AllFlags returns the flags of every job that has a row.
func AllFlags(q db.Queryer) (map[string]model.Flags, error) {
	list, err := queryAll(q, scanFlags, "SELECT "+flagCols+" FROM flags")
	if err != nil {
		return nil, err
	}

	out := make(map[string]model.Flags, len(list))
	for _, f := range list {
		out[f.Job] = f
	}
	return out, nil
}

// SetPaused pauses job.
func SetPaused(q db.Queryer, job, by string, at time.Time) error {
	_, err := q.Exec(`INSERT INTO flags (job, paused_at, paused_by) VALUES (?, ?, ?)
		ON CONFLICT (job) DO UPDATE SET paused_at = excluded.paused_at,
		paused_by = excluded.paused_by`, job, db.TimeStr(at), by)
	return err
}

// ClearPaused resumes job.
func ClearPaused(q db.Queryer, job string) error {
	_, err := q.Exec("UPDATE flags SET paused_at = NULL, paused_by = '' WHERE job = ?", job)
	return err
}

// SetAcked acknowledges the disturbance of job: no reminders (M10).
func SetAcked(q db.Queryer, job string, at time.Time) error {
	_, err := q.Exec(`INSERT INTO flags (job, acked_at) VALUES (?, ?)
		ON CONFLICT (job) DO UPDATE SET acked_at = excluded.acked_at`, job, db.TimeStr(at))
	return err
}

// ClearAcked removes the acknowledgement.
func ClearAcked(q db.Queryer, job string) error {
	_, err := q.Exec("UPDATE flags SET acked_at = NULL WHERE job = ?", job)
	return err
}

// SetMuted mutes job until until; with recover (and until nil) until the
// job recovers.
func SetMuted(q db.Queryer, job string, until *time.Time, recover bool) error {
	_, err := q.Exec(`INSERT INTO flags (job, muted_until, mute_recover) VALUES (?, ?, ?)
		ON CONFLICT (job) DO UPDATE SET muted_until = excluded.muted_until,
		mute_recover = excluded.mute_recover`, job, db.NullTimeStr(until), boolInt(recover))
	return err
}

// ClearMuted unmutes job.
func ClearMuted(q db.Queryer, job string) error {
	_, err := q.Exec("UPDATE flags SET muted_until = NULL, mute_recover = 0 WHERE job = ?", job)
	return err
}

// ResetRecover ends what lasts until recovery: clears the acknowledgement
// and the mute-until-recovery. MutedUntil and the pause stay.
func ResetRecover(q db.Queryer, job string) error {
	_, err := q.Exec("UPDATE flags SET acked_at = NULL, mute_recover = 0 WHERE job = ?", job)
	return err
}
