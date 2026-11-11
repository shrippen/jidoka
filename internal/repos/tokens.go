package repos

import (
	"database/sql"
	"strings"
	"time"

	"jidoka/internal/db"
)

// Token is an API token as stored: only its hash (Z2).
type Token struct {
	ID       int64
	Name     string
	Hash     string
	Rights   []string // read, run, reload
	Created  time.Time
	LastUsed *time.Time
}

const tokenCols = "id, name, hash, rights, created_at, last_used"

func scanToken(s scanner) (Token, error) {
	var (
		t       Token
		rights  string
		created string
		used    sql.NullString
	)
	if err := s.Scan(&t.ID, &t.Name, &t.Hash, &rights, &created, &used); err != nil {
		return t, err
	}
	if rights != "" {
		t.Rights = strings.Split(rights, ",")
	}

	var err error
	if t.Created, err = parseTime(created); err != nil {
		return t, err
	}
	t.LastUsed, err = parseNull(used)
	return t, err
}

// AddToken stores a token by its hash and returns its id.
func AddToken(q db.Queryer, name, hash string, rights []string, at time.Time) (int64, error) {
	res, err := q.Exec("INSERT INTO tokens (name, hash, rights, created_at) VALUES (?, ?, ?, ?)",
		name, hash, strings.Join(rights, ","), db.TimeStr(at))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TokenByHash returns the token with hash; ErrNotFound when unknown.
func TokenByHash(q db.Queryer, hash string) (Token, error) {
	return queryOne(q, scanToken, "SELECT "+tokenCols+" FROM tokens WHERE hash = ?", hash)
}

// ListTokens returns all tokens, oldest first.
func ListTokens(q db.Queryer) ([]Token, error) {
	return queryAll(q, scanToken, "SELECT "+tokenCols+" FROM tokens ORDER BY id")
}

// DeleteToken revokes a token.
func DeleteToken(q db.Queryer, id int64) error {
	_, err := q.Exec("DELETE FROM tokens WHERE id = ?", id)
	return err
}

// TouchToken records the time of the last use.
func TouchToken(q db.Queryer, id int64, at time.Time) error {
	_, err := q.Exec("UPDATE tokens SET last_used = ? WHERE id = ?", db.TimeStr(at), id)
	return err
}
