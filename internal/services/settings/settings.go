// Package settings edits Jidoka's global settings: one TOML document in
// the database, changed on the settings page (Z5, M1).
//
//	Form (text) ──► Draft ──► conf.CheckDraft ──► errors per field: nothing stored
//	                              │ valid
//	                              ▼
//	        document ─► kv, secrets ─► audit ─► jobs.Revalidate (every job against the new settings)
//
// Passwords and keys do not belong to the document; they go through the
// secrets service and are never read back (Z7).
package settings

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/beat"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/secrets"
)

// Secret names the settings page writes.
const (
	SecretAppriseKey   = alerts.SecretAppriseKey
	SecretSMTPPassword = alerts.SecretSMTPPassword
	SecretHeartbeat    = beat.SecretURL
)

// Secrets lists the secrets of the page, in its order.
func Secrets() []string {
	return []string{SecretAppriseKey, SecretSMTPPassword, SecretHeartbeat}
}

// Audit entry of a saved change (O11): no values.
const (
	auditAction = "settings"
	auditTarget = "settings"
)

// Catalog keys of errors the form itself finds.
const (
	keyInt = "settings.int"
	keyTag = "settings.tag_row"
)

// Field names of errors the form itself finds; the others come from conf.
const (
	fieldTags = "channels.apprise.tags"
)

// Reloader checks every job against the settings in force.
type Reloader interface {
	Revalidate() (jobs.Diff, error)
}

// Config wires the service.
type Config struct {
	DB      *sql.DB
	Jobs    Reloader
	Secrets *secrets.Service
	// BaseURLEnv is BASE_URL: it overrides the stored base_url.
	BaseURLEnv string
	Now        func() time.Time
}

// Service is the settings service.
type Service struct {
	cfg Config
}

// New returns a service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}
}

// TagRow is one sub channel of Apprise: a name and its Apprise tag.
type TagRow struct {
	Name, Tag string
}

// SecretEdit is what the page says about one secret: a new value, or
// remove the stored one. An empty value without Clear keeps it.
type SecretEdit struct {
	Value string
	Clear bool
}

// Form is what the page edits, all as text.
type Form struct {
	Zone, Locale             string
	QuietFrom, QuietTo       string
	Heartbeat                string
	MaxParallel, KeepRuns    string
	Keep                     string
	AndonURL, BaseURL        string
	AppriseURL               string
	Tags                     []TagRow
	SMTP, User, From, ToList string // ToList: one address per line
	Secrets                  map[string]SecretEdit
}

// Errors are the errors of a form by field (conf's field names).
type Errors map[string]model.LoadError

// View is the settings page: the form as stored, which secrets are set,
// and what the page only shows.
type View struct {
	Form      Form
	SecretSet map[string]bool
	// Profiles are shown, not edited.
	Profiles map[string][]model.Rule
	// BaseURLEnv is BASE_URL when set; the field is then read-only.
	BaseURLEnv string
	// Broken is why the stored document did not load; the form then shows
	// the defaults and a save replaces the document.
	Broken *model.LoadError
	// NoChannel is set when neither Apprise nor mail is stored: alerts
	// would be lost.
	NoChannel bool
}

// Get returns the page for the stored settings.
func (s *Service) Get() (View, error) {
	d, broken, err := s.stored()
	if err != nil {
		return View{}, err
	}

	v := View{Form: formOf(d), Broken: broken, BaseURLEnv: s.cfg.BaseURLEnv}
	v.NoChannel = v.Form.AppriseURL == "" && v.Form.SMTP == ""
	v.SecretSet, err = s.secretSet()
	if err != nil {
		return View{}, err
	}

	st, _ := conf.CheckDraft(d)
	v.Profiles = st.Profiles

	return v, nil
}

// secretSet says for each secret of the page whether it holds a value.
func (s *Service) secretSet() (map[string]bool, error) {
	infos, err := s.cfg.Secrets.List()
	if err != nil {
		return nil, err
	}

	out := map[string]bool{}
	for _, in := range infos {
		out[in.Name] = true
	}

	return out, nil
}

// stored reads the document; the defaults of a fresh install when there
// is none. A document that does not load comes back as the defaults and
// the reason.
func (s *Service) stored() (conf.Draft, *model.LoadError, error) {
	var doc string
	var ok bool
	err := db.WithRead(s.cfg.DB, func(tx *sql.Tx) error {
		var err error
		doc, ok, err = repos.GetSettings(tx)
		return err
	})
	if err != nil {
		return conf.Draft{}, nil, err
	}
	if !ok {
		return conf.DefaultDraft(), nil, nil
	}

	d, lerr := conf.ParseDraft([]byte(doc))
	if lerr != nil {
		return conf.DefaultDraft(), lerr, nil
	}
	if _, errs := conf.CheckDraft(d); len(errs) > 0 {
		return conf.DefaultDraft(), &errs[0], nil
	}

	return d, nil, nil
}

// Save checks f and stores it. With errors in f nothing is stored and
// they come back by field. Afterwards every job is checked against the
// new settings; a job that names a profile no longer there turns invalid.
func (s *Service) Save(f Form, actor string) (Errors, error) {
	// A value that cannot be sealed must not leave the rest half saved.
	if !crypto.Ready() && wantsSeal(f.Secrets) {
		return nil, crypto.ErrMissingKey
	}

	base, _, err := s.stored()
	if err != nil {
		return nil, err
	}

	d, errs := s.draftOf(base, f)
	if _, list := conf.CheckDraft(d); len(list) > 0 {
		for _, le := range list {
			if _, dup := errs[le.Field]; !dup {
				errs[le.Field] = le
			}
		}
	}
	if len(errs) > 0 {
		return errs, nil
	}

	doc, err := conf.EncodeDraft(d)
	if err != nil {
		return nil, err
	}

	err = db.WithTx(s.cfg.DB, func(tx *sql.Tx) error {
		if err := repos.PutSettings(tx, string(doc)); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, auditAction, auditTarget, s.cfg.Now())
	})
	if err != nil {
		return nil, err
	}

	if err := s.saveSecrets(f.Secrets, actor); err != nil {
		return nil, err
	}

	_, err = s.cfg.Jobs.Revalidate()
	return nil, err
}

// wantsSeal reports whether an edit sets a value.
func wantsSeal(edits map[string]SecretEdit) bool {
	for _, e := range edits {
		if e.Value != "" && !e.Clear {
			return true
		}
	}

	return false
}

// saveSecrets applies the edits of the page.
func (s *Service) saveSecrets(edits map[string]SecretEdit, actor string) error {
	for _, name := range Secrets() {
		e := edits[name]

		if e.Clear {
			err := s.cfg.Secrets.Delete(name, actor)
			if err != nil && !errors.Is(err, secrets.ErrNotFound) {
				return err
			}
			continue
		}
		if e.Value == "" {
			continue
		}
		if err := s.cfg.Secrets.Set(name, e.Value, actor); err != nil {
			return err
		}
	}

	return nil
}

// formOf is the text of a document.
func formOf(d conf.Draft) Form {
	f := Form{
		Zone: d.Zone, Locale: d.Locale, Heartbeat: d.Heartbeat, Keep: d.Keep,
		AndonURL: d.AndonURL, BaseURL: d.BaseURL,
		MaxParallel: intText(d.MaxParallel), KeepRuns: intText(d.KeepRuns),
	}
	f.QuietFrom, f.QuietTo, _ = strings.Cut(d.Quiet, "-")

	if a := d.Channels.Apprise; a != nil {
		f.AppriseURL = a.URL
		for name, tag := range a.Tags {
			f.Tags = append(f.Tags, TagRow{Name: name, Tag: tag})
		}
		sortTags(f.Tags)
	}
	if m := d.Channels.Mail; m != nil {
		f.SMTP, f.User, f.From = m.SMTP, m.User, m.From
		f.ToList = strings.Join(m.To, "\n")
	}

	return f
}
