package repos

import (
	"time"

	"jidoka/internal/db"
)

// SecretInfo is a secret without its value.
type SecretInfo struct {
	Name    string
	Updated time.Time
	By      string
}

// SecretEnc is a secret with its sealed value.
type SecretEnc struct {
	Name string
	Enc  []byte
}

// PutSecret stores the sealed value under name, replacing an older one.
func PutSecret(q db.Queryer, name string, enc []byte, by string, at time.Time) error {
	_, err := q.Exec(`INSERT INTO secrets (name, value_enc, updated_at, updated_by)
		VALUES (?, ?, ?, ?) ON CONFLICT (name) DO UPDATE SET value_enc = excluded.value_enc,
		updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		name, enc, db.TimeStr(at), by)
	return err
}

// ReplaceSecretEnc swaps the sealed value only (key rotation): who and when
// stay.
func ReplaceSecretEnc(q db.Queryer, name string, enc []byte) error {
	_, err := q.Exec("UPDATE secrets SET value_enc = ? WHERE name = ?", enc, name)
	return err
}

// DeleteSecret removes name; ErrNotFound when there was none.
func DeleteSecret(q db.Queryer, name string) error {
	res, err := q.Exec("DELETE FROM secrets WHERE name = ?", name)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSecrets returns names, times and authors by name, never a value.
func ListSecrets(q db.Queryer) ([]SecretInfo, error) {
	return queryAll(q, func(s scanner) (SecretInfo, error) {
		var (
			i  SecretInfo
			at string
		)
		if err := s.Scan(&i.Name, &at, &i.By); err != nil {
			return i, err
		}

		var err error
		i.Updated, err = parseTime(at)
		return i, err
	}, "SELECT name, updated_at, updated_by FROM secrets ORDER BY name")
}

// GetSecretEnc returns the sealed value of name; ErrNotFound when missing.
func GetSecretEnc(q db.Queryer, name string) ([]byte, error) {
	return queryOne(q, func(s scanner) ([]byte, error) {
		var enc []byte
		err := s.Scan(&enc)
		return enc, err
	}, "SELECT value_enc FROM secrets WHERE name = ?", name)
}

// AllSecretsEnc returns every sealed value (redaction, key rotation).
func AllSecretsEnc(q db.Queryer) ([]SecretEnc, error) {
	return queryAll(q, func(s scanner) (SecretEnc, error) {
		var e SecretEnc
		err := s.Scan(&e.Name, &e.Enc)
		return e, err
	}, "SELECT name, value_enc FROM secrets ORDER BY name")
}
