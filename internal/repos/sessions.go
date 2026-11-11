package repos

import (
	"time"

	"jidoka/internal/db"
)

// Session is a browser login as stored: only the hash of its token (Z1).
type Session struct {
	Hash     string
	Sub      string
	Name     string
	Created  time.Time
	LastSeen time.Time
	Expires  time.Time
}

const sessionCols = "hash, sub, name, created, last_seen, expires"

func scanSession(s scanner) (Session, error) {
	var (
		v                         Session
		created, lastSeen, expiry string
	)
	if err := s.Scan(&v.Hash, &v.Sub, &v.Name, &created, &lastSeen, &expiry); err != nil {
		return v, err
	}

	var err error
	if v.Created, err = parseTime(created); err != nil {
		return v, err
	}
	if v.LastSeen, err = parseTime(lastSeen); err != nil {
		return v, err
	}
	v.Expires, err = parseTime(expiry)
	return v, err
}

// AddSession stores a session.
func AddSession(q db.Queryer, s Session) error {
	_, err := q.Exec("INSERT INTO sessions ("+sessionCols+") VALUES (?, ?, ?, ?, ?, ?)",
		s.Hash, s.Sub, s.Name, db.TimeStr(s.Created), db.TimeStr(s.LastSeen), db.TimeStr(s.Expires))
	return err
}

// SessionByHash returns the session; ErrNotFound when unknown.
func SessionByHash(q db.Queryer, hash string) (Session, error) {
	return queryOne(q, scanSession, "SELECT "+sessionCols+" FROM sessions WHERE hash = ?", hash)
}

// TouchSession records a use and the moved expiry.
func TouchSession(q db.Queryer, hash string, seen, expires time.Time) error {
	_, err := q.Exec("UPDATE sessions SET last_seen = ?, expires = ? WHERE hash = ?",
		db.TimeStr(seen), db.TimeStr(expires), hash)
	return err
}

// DeleteSession ends a session.
func DeleteSession(q db.Queryer, hash string) error {
	_, err := q.Exec("DELETE FROM sessions WHERE hash = ?", hash)
	return err
}

// DeleteExpiredSessions drops sessions that ended before now.
func DeleteExpiredSessions(q db.Queryer, now time.Time) error {
	_, err := q.Exec("DELETE FROM sessions WHERE expires < ?", db.TimeStr(now))
	return err
}
