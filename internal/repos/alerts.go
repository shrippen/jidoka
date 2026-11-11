package repos

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

// Alert is the ongoing disturbance of one job and what was already sent
// about it (M3, M5): a restart repeats no message.
type Alert struct {
	Job      string
	Event    model.Event
	Since    time.Time
	Streak   int       // slots in a row with disturbance
	LastSlot time.Time // zero = none
	RunID    int64     // 0 = none
	Name     string
	Detail   string
	// Prev is the alert as it was before "invalid" took over; nil = none.
	// It is stored as JSON in one column and holds no Prev itself.
	Prev *Alert
	Sent map[int]time.Time // rule index -> last message
}

const alertCols = "job, event, since, streak, last_slot, run_id, name, detail, sent, prev"

func scanAlert(s scanner) (Alert, error) {
	var (
		a            Alert
		event, since string
		slot         sql.NullString
		runID        sql.NullInt64
		sent, prev   string
	)
	err := s.Scan(&a.Job, &event, &since, &a.Streak, &slot, &runID, &a.Name, &a.Detail, &sent, &prev)
	if err != nil {
		return a, err
	}
	a.Event, a.RunID = model.Event(event), runID.Int64

	if a.Since, err = parseTime(since); err != nil {
		return a, err
	}
	last, err := parseNull(slot)
	if err != nil {
		return a, err
	}
	if last != nil {
		a.LastSlot = *last
	}
	if a.Sent, err = decodeSent(sent); err != nil {
		return a, err
	}
	a.Prev, err = decodePrev(prev)
	return a, err
}

// encodeSent turns the per-rule times into the JSON column.
func encodeSent(m map[int]time.Time) (string, error) {
	out := make(map[string]string, len(m))
	for i, t := range m {
		out[strconv.Itoa(i)] = db.TimeStr(t)
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// decodeSent reads the JSON column; never returns a nil map.
func decodeSent(s string) (map[int]time.Time, error) {
	var raw map[string]string
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, err
	}

	out := make(map[int]time.Time, len(raw))
	for k, v := range raw {
		i, err := strconv.Atoi(k)
		if err != nil {
			return nil, err
		}
		if out[i], err = parseTime(v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// encodePrev turns the earlier alert into the JSON column; "" for none.
func encodePrev(p *Alert) (string, error) {
	if p == nil {
		return "", nil
	}
	b, err := json.Marshal(p)
	return string(b), err
}

// decodePrev reads the JSON column.
func decodePrev(s string) (*Alert, error) {
	if s == "" {
		return nil, nil
	}
	var p Alert
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	if p.Sent == nil {
		p.Sent = map[int]time.Time{}
	}
	return &p, nil
}

// alertArgs lists the column values of a in alertCols order.
func alertArgs(a Alert) ([]any, error) {
	sent, err := encodeSent(a.Sent)
	if err != nil {
		return nil, err
	}

	prev, err := encodePrev(a.Prev)
	if err != nil {
		return nil, err
	}

	var slot, runID any
	if !a.LastSlot.IsZero() {
		slot = db.TimeStr(a.LastSlot)
	}
	if a.RunID != 0 {
		runID = a.RunID
	}
	return []any{a.Job, string(a.Event), db.TimeStr(a.Since), a.Streak, slot, runID,
		a.Name, a.Detail, sent, prev}, nil
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

// OpenAlert stores the alert of a job; one already open is replaced.
func OpenAlert(q db.Queryer, a Alert) error {
	args, err := alertArgs(a)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT OR REPLACE INTO alerts (`+alertCols+`) VALUES (`+marks(len(args))+`)`, args...)
	return err
}

// CloseAlert ends the alert of job: the disturbance is over.
func CloseAlert(q db.Queryer, job string) error {
	_, err := q.Exec("DELETE FROM alerts WHERE job = ?", job)
	return err
}
