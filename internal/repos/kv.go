package repos

import "jidoka/internal/db"

// GetKV returns the value of key; false when unset.
func GetKV(q db.Queryer, key string) (string, bool, error) {
	v, err := queryOne(q, func(s scanner) (string, error) {
		var v string
		err := s.Scan(&v)
		return v, err
	}, "SELECT value FROM kv WHERE key = ?", key)
	if err == ErrNotFound {
		return "", false, nil
	}
	return v, err == nil, err
}

// SetKV stores value under key.
func SetKV(q db.Queryer, key, value string) error {
	_, err := q.Exec(`INSERT INTO kv (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
