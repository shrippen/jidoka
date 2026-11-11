package repos

import (
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

// LastOKRuns returns the newest successful real run of every job, in one
// query, without output.
func LastOKRuns(q db.Queryer) (map[string]model.Run, error) {
	list, err := queryAll(q, scanRun, "SELECT "+runColsSlim+` FROM runs WHERE id IN
		(SELECT MAX(id) FROM runs WHERE dry = 0 AND state = ? GROUP BY job)`, string(model.RunOK))
	if err != nil {
		return nil, err
	}

	out := make(map[string]model.Run, len(list))
	for _, r := range list {
		out[r.Job] = r
	}
	return out, nil
}

// LatestTails returns the last tailChars characters of the output of every
// job's newest real run, in one query. The cut happens in SQL, so a job
// with 256 KB of output does not move 256 KB per request.
func LatestTails(q db.Queryer, tailChars int) (map[string]string, error) {
	type row struct{ job, tail string }

	list, err := queryAll(q, func(s scanner) (row, error) {
		var r row
		err := s.Scan(&r.job, &r.tail)
		return r, err
	}, `SELECT job, substr(output, ?) FROM runs WHERE id IN
		(SELECT MAX(id) FROM runs WHERE dry = 0 GROUP BY job)`, -tailChars)
	if err != nil {
		return nil, err
	}

	out := make(map[string]string, len(list))
	for _, r := range list {
		out[r.job] = r.tail
	}
	return out, nil
}

// RunNeighbours returns the ids of the run before and after run id in the
// same job; 0 when there is none.
func RunNeighbours(q db.Queryer, job string, id int64) (older, newer int64, err error) {
	row := q.QueryRow(`SELECT
		COALESCE((SELECT MAX(id) FROM runs WHERE job = ? AND id < ?), 0),
		COALESCE((SELECT MIN(id) FROM runs WHERE job = ? AND id > ?), 0)`, job, id, job, id)
	err = row.Scan(&older, &newer)
	return older, newer, err
}

// JobRuns says how many runs one job id has stored and when the last began.
type JobRuns struct {
	Job   string
	Count int
	Last  time.Time
}

// RunJobs returns every job id that has runs, by id. The caller compares it
// with the loaded jobs to find the removed ones (J21).
func RunJobs(q db.Queryer) ([]JobRuns, error) {
	return queryAll(q, func(s scanner) (JobRuns, error) {
		var (
			j    JobRuns
			last string
		)
		if err := s.Scan(&j.Job, &j.Count, &last); err != nil {
			return j, err
		}

		var err error
		j.Last, err = parseTime(last)
		return j, err
	}, "SELECT job, COUNT(*), MAX(started_at) FROM runs GROUP BY job ORDER BY job")
}

// DeliveryCount is how many messages one channel took or refused.
type DeliveryCount struct {
	OK, Failed int
}

// DeliveryCounts counts the deliveries since a time, by channel name
// ("apprise:handy", "mail").
func DeliveryCounts(q db.Queryer, since time.Time) (map[string]DeliveryCount, error) {
	rows, err := q.Query(`SELECT channel, ok, COUNT(*) FROM deliveries WHERE at >= ?
		GROUP BY channel, ok`, db.TimeStr(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]DeliveryCount{}
	for rows.Next() {
		var (
			channel string
			ok, n   int
		)
		if err := rows.Scan(&channel, &ok, &n); err != nil {
			return nil, err
		}

		c := out[channel]
		if ok != 0 {
			c.OK += n
		} else {
			c.Failed += n
		}
		out[channel] = c
	}
	return out, rows.Err()
}

// LastStarts returns when every job's newest run (dry ones too) started.
func LastStarts(q db.Queryer) (map[string]time.Time, error) {
	type row struct {
		job     string
		started time.Time
	}

	list, err := queryAll(q, func(s scanner) (row, error) {
		var r row
		var started string
		if err := s.Scan(&r.job, &started); err != nil {
			return r, err
		}
		var err error
		r.started, err = parseTime(started)
		return r, err
	}, "SELECT job, MAX(started_at) FROM runs GROUP BY job")
	if err != nil {
		return nil, err
	}

	out := make(map[string]time.Time, len(list))
	for _, r := range list {
		out[r.job] = r.started
	}

	return out, nil
}
