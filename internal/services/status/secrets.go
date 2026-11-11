package status

import (
	"database/sql"
	"sort"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/andon"
	"jidoka/internal/services/beat"
)

// Own tells which part of Jidoka itself uses a secret (Z5).
type Own string

const (
	OwnNone      Own = ""
	OwnApprise   Own = "apprise"
	OwnMail      Own = "mail"
	OwnHeartbeat Own = "heartbeat"
	OwnAndon     Own = "andon"
)

// ownSecrets are the secrets Jidoka reads for itself, and when it needs
// them. A needed one that is not set counts as missing; ANDON_HOOK_URL is
// optional.
var ownSecrets = []struct {
	name   string
	use    Own
	needed func(model.Settings) bool
}{
	{alerts.SecretAppriseKey, OwnApprise, func(s model.Settings) bool { return s.Apprise != nil }},
	{alerts.SecretSMTPPassword, OwnMail, func(s model.Settings) bool { return s.Mail != nil && s.Mail.User != "" }},
	{beat.SecretURL, OwnHeartbeat, func(s model.Settings) bool { return s.Heartbeat > 0 }},
	{andon.SecretHookURL, OwnAndon, func(model.Settings) bool { return false }},
}

// SecretRow is one secret on the secrets page (O11). It never has a value.
type SecretRow struct {
	Name string
	// Set: a value is stored.
	Set       bool
	UpdatedAt time.Time
	UpdatedBy string
	// UsedBy are the ids of the jobs that name it, sorted.
	UsedBy []string
	// Own is the part of Jidoka that uses it, if any.
	Own Own
	// Missing: named (by a job, or needed by Jidoka) but not set.
	Missing bool
	// Unused: set, but no job names it and Jidoka does not use it.
	Unused bool
}

// SecretsPage is the secrets page (O11).
type SecretsPage struct {
	// Rows are all secrets that are set, named or used by Jidoka, by name.
	Rows []SecretRow
	// Missing and Unused are the names to show at the top.
	Missing, Unused []string
	// KeyReady: MASTER_KEY is set. Without it a value cannot be stored.
	KeyReady bool
	// Audit are the newest 50 changes, newest first: who, when, what name.
	Audit []repos.AuditEntry
}

// Secrets returns the secrets page.
func (s *Service) Secrets() (SecretsPage, error) {
	set := s.d.Jobs.Current()
	p := SecretsPage{Rows: []SecretRow{}, Missing: []string{}, Unused: []string{}, KeyReady: crypto.Ready()}

	rows := map[string]*SecretRow{}
	row := func(name string) *SecretRow {
		if rows[name] == nil {
			rows[name] = &SecretRow{Name: name, UsedBy: []string{}}
		}
		return rows[name]
	}

	if s.d.Secrets != nil {
		list, err := s.d.Secrets.List()
		if err != nil {
			return p, err
		}
		for _, i := range list {
			r := row(i.Name)
			r.Set, r.UpdatedAt, r.UpdatedBy = true, i.UpdatedAt, i.UpdatedBy
		}
	}

	for _, id := range sortedKeys(set.Jobs) {
		for _, name := range set.Jobs[id].Secrets {
			r := row(name)
			r.UsedBy = append(r.UsedBy, id)
		}
	}

	for _, o := range ownSecrets {
		if r := rows[o.name]; r == nil && !o.needed(set.Settings) {
			continue
		}
		row(o.name).Own = o.use
		if r := rows[o.name]; !r.Set && o.needed(set.Settings) {
			r.Missing = true
		}
	}

	for _, name := range sortedKeys(rows) {
		r := rows[name]
		if len(r.UsedBy) > 0 && !r.Set {
			r.Missing = true
		}
		r.Unused = r.Set && len(r.UsedBy) == 0 && r.Own == OwnNone

		p.Rows = append(p.Rows, *r)
		if r.Missing {
			p.Missing = append(p.Missing, name)
		}
		if r.Unused {
			p.Unused = append(p.Unused, name)
		}
	}
	sort.SliceStable(p.Rows, func(i, j int) bool { return p.Rows[i].Name < p.Rows[j].Name })

	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		p.Audit, err = repos.RecentAudit(tx, recentAudit)
		return err
	})
	if p.Audit == nil {
		p.Audit = []repos.AuditEntry{}
	}
	return p, err
}
