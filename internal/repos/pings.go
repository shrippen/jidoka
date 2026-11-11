package repos

import (
	"time"

	"jidoka/internal/db"
)

// Ping is the ping token of one watch: its hash finds the job, the sealed
// token is shown again on the page (W3).
type Ping struct {
	Job     string
	Hash    string
	Enc     []byte
	Created time.Time
}

const pingCols = "job, token_hash, token_enc, created_at"

func scanPing(s scanner) (Ping, error) {
	var (
		p       Ping
		created string
	)
	if err := s.Scan(&p.Job, &p.Hash, &p.Enc, &created); err != nil {
		return p, err
	}

	var err error
	p.Created, err = parseTime(created)
	return p, err
}

// PutPing stores the token of job, replacing the old one (renew).
func PutPing(q db.Queryer, job, hash string, enc []byte, at time.Time) error {
	_, err := q.Exec(`INSERT INTO pings (job, token_hash, token_enc, created_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (job) DO UPDATE SET token_hash = excluded.token_hash,
		token_enc = excluded.token_enc, created_at = excluded.created_at`,
		job, hash, enc, db.TimeStr(at))
	return err
}

// JobByPing returns the job a token hash belongs to; ErrNotFound when
// unknown.
func JobByPing(q db.Queryer, hash string) (string, error) {
	return queryOne(q, func(s scanner) (string, error) {
		var job string
		err := s.Scan(&job)
		return job, err
	}, "SELECT job FROM pings WHERE token_hash = ?", hash)
}

// GetPing returns the token of job; ErrNotFound when none.
func GetPing(q db.Queryer, job string) (Ping, error) {
	return queryOne(q, scanPing, "SELECT "+pingCols+" FROM pings WHERE job = ?", job)
}

// AllPings returns every ping token (key rotation).
func AllPings(q db.Queryer) ([]Ping, error) {
	return queryAll(q, scanPing, "SELECT "+pingCols+" FROM pings ORDER BY job")
}

// ReplacePingEnc swaps the sealed token only (key rotation).
func ReplacePingEnc(q db.Queryer, job string, enc []byte) error {
	_, err := q.Exec("UPDATE pings SET token_enc = ? WHERE job = ?", enc, job)
	return err
}

// DeletePing removes the token of job.
func DeletePing(q db.Queryer, job string) error {
	_, err := q.Exec("DELETE FROM pings WHERE job = ?", job)
	return err
}
