package repos

import (
	"time"

	"jidoka/internal/db"
)

// AuditEntry is one change: who, what, on what. Never a value (O11).
type AuditEntry struct {
	ID     int64
	At     time.Time
	Actor  string
	Action string
	Target string
}

// AddAudit appends an entry.
func AddAudit(q db.Queryer, actor, action, target string, at time.Time) error {
	_, err := q.Exec("INSERT INTO audit (at, actor, action, target) VALUES (?, ?, ?, ?)",
		db.TimeStr(at), actor, action, target)
	return err
}

// RecentAudit returns the newest limit entries, newest first.
func RecentAudit(q db.Queryer, limit int) ([]AuditEntry, error) {
	return queryAll(q, func(s scanner) (AuditEntry, error) {
		var (
			e  AuditEntry
			at string
		)
		if err := s.Scan(&e.ID, &at, &e.Actor, &e.Action, &e.Target); err != nil {
			return e, err
		}

		var err error
		e.At, err = parseTime(at)
		return e, err
	}, "SELECT id, at, actor, action, target FROM audit ORDER BY id DESC LIMIT ?", limit)
}
