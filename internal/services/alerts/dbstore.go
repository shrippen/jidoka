package alerts

import (
	"database/sql"
	"errors"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

// dbStore is the Store on the database.
type dbStore struct {
	d *sql.DB
}

// NewDBStore returns the Store backed by d.
func NewDBStore(d *sql.DB) Store {
	return &dbStore{d: d}
}

func fromAlert(a repos.Alert) Disturbance {
	return Disturbance{
		Job: a.Job, Event: a.Event, Since: a.Since, Streak: a.Streak, LastSlot: a.LastSlot,
		RunID: a.RunID, Name: a.Name, Detail: a.Detail, Sent: a.Sent,
	}
}

func toAlert(d Disturbance) repos.Alert {
	return repos.Alert{
		Job: d.Job, Event: d.Event, Since: d.Since, Streak: d.Streak, LastSlot: d.LastSlot,
		RunID: d.RunID, Name: d.Name, Detail: d.Detail, Sent: d.Sent,
	}
}

func fromItem(o repos.OutboxItem) Queued {
	return Queued{
		ID: o.ID, Created: o.Created, Due: o.Due, Job: o.Job, RunID: o.RunID, Event: o.Event,
		Channel: o.Channel, Digest: o.Digest, Payload: o.Payload, Attempts: o.Attempts,
		LastError: o.LastError,
	}
}

func toItem(q Queued) repos.OutboxItem {
	return repos.OutboxItem{
		ID: q.ID, Created: q.Created, Due: q.Due, Job: q.Job, RunID: q.RunID, Event: q.Event,
		Channel: q.Channel, Digest: q.Digest, Payload: q.Payload, Attempts: q.Attempts,
		LastError: q.LastError,
	}
}

func (s *dbStore) Disturbance(job string) (*Disturbance, error) {
	var out *Disturbance
	err := db.WithRead(s.d, func(tx *sql.Tx) error {
		a, err := repos.GetAlert(tx, job)
		if errors.Is(err, repos.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		d := fromAlert(a)
		out = &d
		return nil
	})
	return out, err
}

func (s *dbStore) Disturbances() ([]Disturbance, error) {
	var out []Disturbance
	err := db.WithRead(s.d, func(tx *sql.Tx) error {
		all, err := repos.AllAlerts(tx)
		for _, a := range all {
			out = append(out, fromAlert(a))
		}
		return err
	})
	return out, err
}

func (s *dbStore) SaveDisturbance(d Disturbance) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		return repos.OpenAlert(tx, toAlert(d))
	})
}

func (s *dbStore) CloseDisturbance(job string) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		return repos.CloseAlert(tx, job)
	})
}

func (s *dbStore) Flags(job string) (model.Flags, error) {
	var f model.Flags
	err := db.WithRead(s.d, func(tx *sql.Tx) error {
		var err error
		f, err = repos.GetFlags(tx, job)
		return err
	})
	return f, err
}

func (s *dbStore) ResetFlags(job string) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		return repos.ResetRecover(tx, job)
	})
}

func (s *dbStore) OutboxAdd(q Queued) (int64, error) {
	var id int64
	err := db.WithTx(s.d, func(tx *sql.Tx) error {
		var err error
		id, err = repos.AddOutbox(tx, toItem(q))
		return err
	})
	return id, err
}

func (s *dbStore) OutboxList() ([]Queued, error) {
	var out []Queued
	err := db.WithRead(s.d, func(tx *sql.Tx) error {
		items, err := repos.AllOutbox(tx)
		for _, o := range items {
			out = append(out, fromItem(o))
		}
		return err
	})
	return out, err
}

func (s *dbStore) OutboxSave(q Queued) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		return repos.SaveOutbox(tx, toItem(q))
	})
}

func (s *dbStore) OutboxDelete(id int64) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		return repos.DeleteOutbox(tx, id)
	})
}

func (s *dbStore) AddDelivery(d model.Delivery) error {
	return db.WithTx(s.d, func(tx *sql.Tx) error {
		_, err := repos.AddDelivery(tx, d)
		return err
	})
}
