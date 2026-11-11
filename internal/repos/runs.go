package repos

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

// runCols is every column of runs, in scanRun's order.
const runCols = `id, job, trigger, state, slot, attempt, started_at, ended_at,
	exit_code, summary, message, output, error, jobs_commit, dry`

// runColsSlim leaves out the bulky texts: lists, ticks and the timetable
// need state and times, not the output.
const runColsSlim = `id, job, trigger, state, slot, attempt, started_at, ended_at,
	exit_code, summary, '' AS message, '' AS output, error, jobs_commit, dry`

func scanRun(s scanner) (model.Run, error) {
	var (
		r                     model.Run
		trig, state           string
		slot, started         string
		ended                 sql.NullString
		exit                  sql.NullInt64
		dry                   int
		summary, msg, out, er string
	)
	err := s.Scan(&r.ID, &r.Job, &trig, &state, &slot, &r.Attempt, &started, &ended,
		&exit, &summary, &msg, &out, &er, &r.Commit, &dry)
	if err != nil {
		return r, err
	}

	r.Trigger, r.State = model.Trigger(trig), model.RunState(state)
	r.Summary, r.Message, r.Output, r.Error = summary, msg, out, er
	r.Dry = dry != 0
	if r.Slot, err = parseTime(slot); err != nil {
		return r, err
	}
	if r.Started, err = parseTime(started); err != nil {
		return r, err
	}

	end, err := parseNull(ended)
	if err != nil {
		return r, err
	}
	if end != nil {
		r.Ended = *end
	}
	if exit.Valid {
		code := int(exit.Int64)
		r.Exit = &code
	}
	return r, nil
}

// InsertRun stores a new run (usually state running) and returns its id.
func InsertRun(q db.Queryer, r model.Run) (int64, error) {
	res, err := q.Exec(`INSERT INTO runs
		(job, trigger, state, slot, attempt, started_at, jobs_commit, dry)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Job, string(r.Trigger), string(r.State), db.TimeStr(r.Slot), r.Attempt,
		db.TimeStr(r.Started), r.Commit, boolInt(r.Dry))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRun writes the outcome of run r.ID: state, end, exit code, texts.
func FinishRun(q db.Queryer, r model.Run) error {
	var exit any
	if r.Exit != nil {
		exit = *r.Exit
	}
	_, err := q.Exec(`UPDATE runs SET state = ?, ended_at = ?, exit_code = ?,
		summary = ?, message = ?, output = ?, error = ? WHERE id = ?`,
		string(r.State), db.TimeStr(r.Ended), exit,
		r.Summary, r.Message, r.Output, r.Error, r.ID)
	return err
}

// GetRun returns one run with its output.
func GetRun(q db.Queryer, id int64) (model.Run, error) {
	return queryOne(q, scanRun, "SELECT "+runCols+" FROM runs WHERE id = ?", id)
}

// ListRuns returns up to limit runs of job, newest first, with ids below
// beforeID (0 = from the newest): paging by id stays stable while runs arrive.
func ListRuns(q db.Queryer, job string, limit int, beforeID int64) ([]model.Run, error) {
	if beforeID <= 0 {
		beforeID = 1<<62 - 1
	}
	return queryAll(q, scanRun, "SELECT "+runColsSlim+` FROM runs
		WHERE job = ? AND id < ? ORDER BY id DESC LIMIT ?`, job, beforeID, limit)
}

// LatestRuns returns the newest real (not dry) run of every job, in one query.
func LatestRuns(q db.Queryer) (map[string]model.Run, error) {
	list, err := queryAll(q, scanRun, "SELECT "+runColsSlim+` FROM runs WHERE id IN
		(SELECT MAX(id) FROM runs WHERE dry = 0 GROUP BY job)`)
	if err != nil {
		return nil, err
	}

	out := make(map[string]model.Run, len(list))
	for _, r := range list {
		out[r.Job] = r
	}
	return out, nil
}

// LastOKRun returns the newest successful real run of job.
func LastOKRun(q db.Queryer, job string) (model.Run, error) {
	return queryOne(q, scanRun, "SELECT "+runColsSlim+` FROM runs
		WHERE job = ? AND state = ? AND dry = 0 ORDER BY id DESC LIMIT 1`,
		job, string(model.RunOK))
}

// RunningRuns returns every run still marked running.
func RunningRuns(q db.Queryer) ([]model.Run, error) {
	return queryAll(q, scanRun, "SELECT "+runCols+" FROM runs WHERE state = ? ORDER BY id",
		string(model.RunRunning))
}

// AbortRunning marks every running run aborted at at and returns how many.
// At startup nothing can be running: the process that ran them is gone (J23).
func AbortRunning(q db.Queryer, at time.Time) (int64, error) {
	res, err := q.Exec("UPDATE runs SET state = ?, ended_at = ? WHERE state = ?",
		string(model.RunAborted), db.TimeStr(at), string(model.RunRunning))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecentRuns returns the newest n real runs of each job in jobs, newest
// first, without output: the ticks on the board.
func RecentRuns(q db.Queryer, jobs []string, n int) (map[string][]model.Run, error) {
	out := map[string][]model.Run{}
	if len(jobs) == 0 {
		return out, nil
	}

	args := append(anys(jobs), n)
	list, err := queryAll(q, scanRun, "SELECT "+runColsSlim+` FROM (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY job ORDER BY id DESC) AS rn
		FROM runs WHERE dry = 0 AND job IN (`+marks(len(jobs))+`))
		WHERE rn <= ? ORDER BY job, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		out[r.Job] = append(out[r.Job], r)
	}
	return out, nil
}

// PruneRuns deletes finished runs of job that started before olderThan or
// fall outside the newest keepMax (keepMax <= 0: no count limit) (J12, J18).
func PruneRuns(q db.Queryer, job string, olderThan time.Time, keepMax int) (int64, error) {
	if keepMax <= 0 {
		keepMax = -1 // SQLite: LIMIT -1 is no limit
	}
	res, err := q.Exec(`DELETE FROM runs WHERE job = ? AND state <> ? AND
		(started_at < ? OR id NOT IN
			(SELECT id FROM runs WHERE job = ? ORDER BY id DESC LIMIT ?))`,
		job, string(model.RunRunning), db.TimeStr(olderThan), job, keepMax)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneGone deletes finished runs older than olderThan of jobs that are not
// in live: removed jobs keep their history until the end of retention (J21).
func PruneGone(q db.Queryer, live []string, olderThan time.Time) (int64, error) {
	query := "DELETE FROM runs WHERE state <> ? AND started_at < ?"
	args := []any{string(model.RunRunning), db.TimeStr(olderThan)}
	if len(live) > 0 {
		query += " AND job NOT IN (" + marks(len(live)) + ")"
		args = append(args, anys(live)...)
	}

	res, err := q.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunsBetween returns real runs that started in [from, to), oldest first,
// without output: the timetable.
func RunsBetween(q db.Queryer, from, to time.Time) ([]model.Run, error) {
	return queryAll(q, scanRun, "SELECT "+runColsSlim+` FROM runs
		WHERE dry = 0 AND started_at >= ? AND started_at < ? ORDER BY started_at, id`,
		db.TimeStr(from), db.TimeStr(to))
}
