// Adapted from andon internal/db/salt.go (468474a).

package db

// The install's key salt lives next to the database file, in hex:
//
//	jidoka.db       plain SQLite, secrets sealed in *_enc columns
//	jidoka.db.salt  16 random bytes, not secret, but needed to derive the key
//
// No salt yet means a fresh install: the caller creates one.

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	saltSuffix = ".salt"
	fileMode   = 0o600
)

// ReadSalt returns the salt next to the database at path, nil when there
// is none.
func ReadSalt(path string) ([]byte, error) {
	raw, err := os.ReadFile(path + saltSuffix)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimSpace(string(raw)))
}

// WriteSalt stores salt next to the database at path, durably: a synced
// temporary file renamed into place, then the directory synced. Once it
// returns, the next start derives the key with it.
func WriteSalt(path string, salt []byte) error {
	tmp := path + saltSuffix + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(hex.EncodeToString(salt) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path+saltSuffix); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
