package repos

import (
	"database/sql"

	"jidoka/internal/db"
	"jidoka/internal/model"
)

const deliveryCols = "id, job, run_id, event, channel, at, ok, error"

func scanDelivery(s scanner) (model.Delivery, error) {
	var (
		d     model.Delivery
		runID sql.NullInt64
		event string
		at    string
		ok    int
	)
	if err := s.Scan(&d.ID, &d.Job, &runID, &event, &d.Channel, &at, &ok, &d.Error); err != nil {
		return d, err
	}
	d.Event, d.OK = model.Event(event), ok != 0
	if runID.Valid {
		d.RunID = &runID.Int64
	}

	var err error
	d.At, err = parseTime(at)
	return d, err
}

// AddDelivery records a message sent or tried and returns its id.
func AddDelivery(q db.Queryer, d model.Delivery) (int64, error) {
	var runID any
	if d.RunID != nil {
		runID = *d.RunID
	}
	res, err := q.Exec(`INSERT INTO deliveries (job, run_id, event, channel, at, ok, error)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, d.Job, runID, string(d.Event), d.Channel,
		db.TimeStr(d.At), boolInt(d.OK), d.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeliveriesByRun returns what was sent for one run, oldest first.
func DeliveriesByRun(q db.Queryer, runID int64) ([]model.Delivery, error) {
	return queryAll(q, scanDelivery, "SELECT "+deliveryCols+" FROM deliveries WHERE run_id = ? ORDER BY id",
		runID)
}

// RecentDeliveries returns the newest limit deliveries.
func RecentDeliveries(q db.Queryer, limit int) ([]model.Delivery, error) {
	return queryAll(q, scanDelivery, "SELECT "+deliveryCols+" FROM deliveries ORDER BY at DESC, id DESC LIMIT ?",
		limit)
}

// LastDeliveries returns the newest delivery of each channel (O8: is the
// channel healthy).
func LastDeliveries(q db.Queryer) (map[string]model.Delivery, error) {
	list, err := queryAll(q, scanDelivery, "SELECT "+deliveryCols+` FROM deliveries WHERE id IN
		(SELECT MAX(id) FROM deliveries GROUP BY channel)`)
	if err != nil {
		return nil, err
	}

	out := make(map[string]model.Delivery, len(list))
	for _, d := range list {
		out[d.Channel] = d
	}
	return out, nil
}
