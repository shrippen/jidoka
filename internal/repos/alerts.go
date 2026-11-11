package repos

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

// Alert is the ongoing disturbance of one job and what was already sent
// about it (M3, M5): a restart repeats no message.
type Alert struct {
	Job       string
	Since     time.Time
	Streak    int // slots in a row with disturbance
	FirstSent *time.Time
	LastSent  *time.Time
	Event     model.Event
}

const alertCols = "job, since, streak, first_sent, last_sent, event"

func scanAlert(s scanner) (Alert, error) {
	var (
		a           Alert
		since       string
		first, last sql.NullString
		event       string
	)
	if err := s.Scan(&a.Job, &since, &a.Streak, &first, &last, &event); err != nil {
		return a, err
	}
	a.Event = model.Event(event)

	var err error
	if a.Since, err = parseTime(since); err != nil {
		return a, err
	}
	if a.FirstSent, err = parseNull(first); err != nil {
		return a, err
	}
	a.LastSent, err = parseNull(last)
	return a, err
}

// GetAlert returns the open alert of job; ErrNotFound when there is none.
func GetAlert(q db.Queryer, job string) (Alert, error) {
	return queryOne(q, scanAlert, "SELECT "+alertCols+" FROM alerts WHERE job = ?", job)
}

// AllAlerts returns every open alert by job.
func AllAlerts(q db.Queryer) (map[string]Alert, error) {
	list, err := queryAll(q, scanAlert, "SELECT "+alertCols+" FROM alerts")
	if err != nil {
		return nil, err
	}

	out := make(map[string]Alert, len(list))
	for _, a := range list {
		out[a.Job] = a
	}
	return out, nil
}

// OpenAlert starts the alert of a job; one already open is replaced.
func OpenAlert(q db.Queryer, a Alert) error {
	_, err := q.Exec(`INSERT OR REPLACE INTO alerts (`+alertCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Job, db.TimeStr(a.Since), a.Streak, db.NullTimeStr(a.FirstSent),
		db.NullTimeStr(a.LastSent), string(a.Event))
	return err
}

// UpdateAlert changes streak, sent times and event of the open alert.
func UpdateAlert(q db.Queryer, a Alert) error {
	_, err := q.Exec(`UPDATE alerts SET streak = ?, first_sent = ?, last_sent = ?, event = ?
		WHERE job = ?`, a.Streak, db.NullTimeStr(a.FirstSent), db.NullTimeStr(a.LastSent),
		string(a.Event), a.Job)
	return err
}

// CloseAlert ends the alert of job: the disturbance is over.
func CloseAlert(q db.Queryer, job string) error {
	_, err := q.Exec("DELETE FROM alerts WHERE job = ?", job)
	return err
}
