// Package secrets keeps the secrets of jobs and of Jidoka itself (J7, Z5,
// Z7, O11): sealed in the database, written through the page, handed to a
// run as environment variables, never shown again.
//
//	Set ──► crypto.Encrypt(PurposeSecret) ──► secrets.value_enc  (+ audit: name, never value)
//	Resolve(names) ◄── crypto.Decrypt ◄───────┘
package secrets

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/repos"
)

// minRedact is the shortest value the redactor masks (J7): shorter ones
// would mask ordinary text.
const minRedact = 6

// Audit actions (O11).
const (
	actionSet     = "set"
	actionReplace = "replace"
	actionDelete  = "delete"
	actionRotate  = "rotate-key"
)

// rotateActor and rotateTarget describe the offline rotate-key command.
const (
	rotateActor  = "cli"
	rotateTarget = "all"
)

// ErrBadName means a secret name is not [A-Z][A-Z0-9_]*.
var ErrBadName = errors.New("secrets: name must match [A-Z][A-Z0-9_]*")

// ErrEmpty means an empty value.
var ErrEmpty = errors.New("secrets: empty value")

// ErrNotFound means the secret does not exist.
var ErrNotFound = errors.New("secrets: not found")

var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// Info is a secret as the page lists it: no value.
type Info struct {
	Name      string
	UpdatedAt time.Time
	UpdatedBy string
}

// Service is the secrets service.
type Service struct {
	db  *sql.DB
	now func() time.Time
}

// New returns a service over d.
func New(d *sql.DB) *Service {
	return &Service{db: d, now: time.Now}
}

// Set stores value under name, replacing an older one, and audits it.
func (s *Service) Set(name, value, actor string) error {
	if !nameRe.MatchString(name) {
		return ErrBadName
	}
	if value == "" {
		return ErrEmpty
	}
	enc, err := crypto.Encrypt(value, crypto.PurposeSecret, nil)
	if err != nil {
		return err
	}

	return db.WithTx(s.db, func(tx *sql.Tx) error {
		action := actionSet
		_, err := repos.GetSecretEnc(tx, name)
		if err == nil {
			action = actionReplace
		}
		if err != nil && !errors.Is(err, repos.ErrNotFound) {
			return err
		}

		at := s.now()
		if err := repos.PutSecret(tx, name, enc, actor, at); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, action, name, at)
	})
}

// Delete removes name and audits it; ErrNotFound when it does not exist.
func (s *Service) Delete(name, actor string) error {
	return db.WithTx(s.db, func(tx *sql.Tx) error {
		err := repos.DeleteSecret(tx, name)
		if errors.Is(err, repos.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionDelete, name, s.now())
	})
}

// List returns every secret by name, without values.
func (s *Service) List() ([]Info, error) {
	var out []Info
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		rows, err := repos.ListSecrets(tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, Info{Name: r.Name, UpdatedAt: r.Updated, UpdatedBy: r.By})
		}
		return nil
	})
	return out, err
}

// Resolve returns the values of names for one run (J7). Names that do not
// exist come back in missing: the run then fails without starting the
// script. Without a master key it returns crypto.ErrMissingKey, so the
// run can say that, rather than blame a secret.
func (s *Service) Resolve(names []string) (map[string]string, []string, error) {
	values := map[string]string{}
	if len(names) == 0 {
		return values, nil, nil
	}
	if !crypto.Ready() {
		return nil, nil, crypto.ErrMissingKey
	}

	var missing []string
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		for _, name := range names {
			enc, err := repos.GetSecretEnc(tx, name)
			if errors.Is(err, repos.ErrNotFound) {
				missing = append(missing, name)
				continue
			}
			if err != nil {
				return err
			}

			// The error names the secret, never the value.
			v, err := crypto.Decrypt(enc, crypto.PurposeSecret)
			if err != nil {
				return fmt.Errorf("secret %s: %w", name, err)
			}
			values[name] = v
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return values, missing, nil
}

// Values returns every value of minRedact characters or more, for the
// redactor (J7). Without a master key there is nothing to mask: it returns
// crypto.ErrMissingKey only when secrets exist.
func (s *Service) Values() ([]string, error) {
	var out []string
	err := db.WithRead(s.db, func(tx *sql.Tx) error {
		all, err := repos.AllSecretsEnc(tx)
		if err != nil {
			return err
		}
		for _, e := range all {
			v, err := crypto.Decrypt(e.Enc, crypto.PurposeSecret)
			if err != nil {
				return fmt.Errorf("secret %s: %w", e.Name, err)
			}
			if len(v) >= minRedact {
				out = append(out, v)
			}
		}
		return nil
	})
	return out, err
}

// Rotate re-encrypts every sealed value, secrets and ping tokens, from the
// process master key to newMaster in one transaction (Z7 rotate-key). The
// operator then restarts with the new MASTER_KEY.
func (s *Service) Rotate(newMaster []byte) error {
	if !crypto.Ready() {
		return crypto.ErrMissingKey
	}

	return db.WithTx(s.db, func(tx *sql.Tx) error {
		if err := rotateSecrets(tx, newMaster); err != nil {
			return err
		}
		if err := rotatePings(tx, newMaster); err != nil {
			return err
		}
		return repos.AddAudit(tx, rotateActor, actionRotate, rotateTarget, s.now())
	})
}

func rotateSecrets(tx *sql.Tx, newMaster []byte) error {
	all, err := repos.AllSecretsEnc(tx)
	if err != nil {
		return err
	}
	for _, e := range all {
		enc, err := crypto.Reseal(e.Enc, crypto.PurposeSecret, newMaster)
		if err != nil {
			return fmt.Errorf("secret %s: %w", e.Name, err)
		}
		if err := repos.ReplaceSecretEnc(tx, e.Name, enc); err != nil {
			return err
		}
	}
	return nil
}

func rotatePings(tx *sql.Tx, newMaster []byte) error {
	all, err := repos.AllPings(tx)
	if err != nil {
		return err
	}
	for _, p := range all {
		enc, err := crypto.Reseal(p.Enc, crypto.PurposePing, newMaster)
		if err != nil {
			return fmt.Errorf("ping %s: %w", p.Job, err)
		}
		if err := repos.ReplacePingEnc(tx, p.Job, enc); err != nil {
			return err
		}
	}
	return nil
}
