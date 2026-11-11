package repos

import (
	"database/sql"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

// OutboxItem is a message waiting to be sent: quiet hours, digest, retry,
// channels down (M6, M7, M9).
type OutboxItem struct {
	ID        int64
	Created   time.Time
	Due       time.Time
	Job       string
	RunID     *int64
	Event     model.Event
	Channel   string
	Digest    string // digest key, "" = at once
	Payload   string // JSON
	Attempts  int
	LastError string
}

const outboxCols = "id, created, due, job, run_id, event, channel, digest, payload, attempts, last_error"

func scanOutbox(s scanner) (OutboxItem, error) {
	var (
		o            OutboxItem
		created, due string
		runID        sql.NullInt64
		event        string
	)
	err := s.Scan(&o.ID, &created, &due, &o.Job, &runID, &event, &o.Channel,
		&o.Digest, &o.Payload, &o.Attempts, &o.LastError)
	if err != nil {
		return o, err
	}
	o.Event = model.Event(event)
	if runID.Valid {
		o.RunID = &runID.Int64
	}
	if o.Created, err = parseTime(created); err != nil {
		return o, err
	}
	o.Due, err = parseTime(due)
	return o, err
}

// AddOutbox queues a message and returns its id.
func AddOutbox(q db.Queryer, o OutboxItem) (int64, error) {
	var runID any
	if o.RunID != nil {
		runID = *o.RunID
	}
	res, err := q.Exec(`INSERT INTO outbox (created, due, job, run_id, event, channel, digest, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, db.TimeStr(o.Created), db.TimeStr(o.Due),
		o.Job, runID, string(o.Event), o.Channel, o.Digest, o.Payload)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueOutbox returns the messages due at now, oldest first.
func DueOutbox(q db.Queryer, now time.Time) ([]OutboxItem, error) {
	return queryAll(q, scanOutbox, "SELECT "+outboxCols+" FROM outbox WHERE due <= ? ORDER BY due, id",
		db.TimeStr(now))
}

// AttemptOutbox records a failed send: one more attempt, the error, the
// next try at nextDue.
func AttemptOutbox(q db.Queryer, id int64, sendErr string, nextDue time.Time) error {
	_, err := q.Exec(`UPDATE outbox SET attempts = attempts + 1, last_error = ?, due = ?
		WHERE id = ?`, sendErr, db.TimeStr(nextDue), id)
	return err
}

// DeleteOutbox removes a message: sent, or given up.
func DeleteOutbox(q db.Queryer, id int64) error {
	_, err := q.Exec("DELETE FROM outbox WHERE id = ?", id)
	return err
}

// AllOutbox returns every waiting message, for the page.
func AllOutbox(q db.Queryer) ([]OutboxItem, error) {
	return queryAll(q, scanOutbox, "SELECT "+outboxCols+" FROM outbox ORDER BY due, id")
}

// OutboxByChannel returns the messages waiting for channel.
func OutboxByChannel(q db.Queryer, channel string) ([]OutboxItem, error) {
	return queryAll(q, scanOutbox, "SELECT "+outboxCols+" FROM outbox WHERE channel = ? ORDER BY due, id",
		channel)
}
