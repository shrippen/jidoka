package settings

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/secrets"
)

type rig struct {
	t    *testing.T
	db   *sql.DB
	dir  string
	jobs *jobs.Service
	sec  *secrets.Service
	svc  *Service
}

func newRig(t *testing.T) *rig {
	t.Helper()

	crypto.Init(crypto.Derive("test-key", []byte("test-salt-0123456789")))
	t.Cleanup(func() { crypto.Init(nil) })

	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	r := &rig{t: t, db: d, dir: t.TempDir()}
	r.jobs = jobs.New(d, r.dir)
	r.sec = secrets.New(d)
	r.svc = New(Config{DB: d, Jobs: r.jobs, Secrets: r.sec})

	return r
}

func (r *rig) job(id, extra string) {
	r.t.Helper()

	dir := filepath.Join(r.dir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	toml := "run = \"run.sh\"\nschedule = \"0 6 * * *\"\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "job.toml"), []byte(toml), 0o644); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		r.t.Fatal(err)
	}
}

// stored is the document in the database; "" when none.
func (r *rig) stored() string {
	r.t.Helper()

	doc, _, err := repos.GetSettings(r.db)
	if err != nil {
		r.t.Fatal(err)
	}

	return doc
}

// form is the form as the page shows it now.
func (r *rig) form() Form {
	r.t.Helper()

	v, err := r.svc.Get()
	if err != nil {
		r.t.Fatal(err)
	}

	return v.Form
}

func (r *rig) save(f Form) Errors {
	r.t.Helper()

	errs, err := r.svc.Save(f, "tester")
	if err != nil {
		r.t.Fatal(err)
	}

	return errs
}

// A valid form is stored and every job is checked against it at once.
func TestSaveStoresAndRevalidates(t *testing.T) {
	r := newRig(t)
	r.job("a", "")
	if err := r.jobs.Load(); err != nil {
		t.Fatal(err)
	}

	f := r.form()
	f.Zone, f.MaxParallel, f.Heartbeat = "UTC", "5", "10m"
	f.QuietFrom, f.QuietTo = "22:00", "07:00"
	f.AppriseURL = "http://apprise:8000/notify"
	f.Tags = []TagRow{{Name: "handy", Tag: "phone"}}
	f.SMTP, f.From, f.ToList = "smtp:587", "j@example.org", "a@example.org\n\n b@example.org \n"

	var heard bool
	r.jobs.OnReload(func(jobs.Diff, model.Set) { heard = true })

	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}

	cur := r.jobs.Current().Settings
	if cur.Zone.String() != "UTC" || cur.MaxParallel != 5 || cur.Heartbeat.Minutes() != 10 ||
		cur.Quiet == nil || cur.Apprise.Tags["handy"] != "phone" || len(cur.Mail.To) != 2 {
		t.Errorf("settings in force: %+v", cur)
	}
	if !heard {
		t.Error("reload hooks not called")
	}
	if !strings.Contains(r.stored(), `max_parallel = 5`) {
		t.Errorf("stored: %s", r.stored())
	}

	got := r.form()
	if got.Zone != "UTC" || got.QuietFrom != "22:00" || got.ToList != "a@example.org\nb@example.org" {
		t.Errorf("form: %+v", got)
	}

	entries, err := repos.RecentAudit(r.db, 5)
	if err != nil || len(entries) != 1 || entries[0].Actor != "tester" || entries[0].Action != "settings" {
		t.Errorf("audit: %+v %v", entries, err)
	}
}

// Errors come back by field and nothing is stored, not even the valid part.
func TestSaveRefusesBadValues(t *testing.T) {
	r := newRig(t)

	f := r.form()
	f.Zone, f.Keep, f.MaxParallel = "Mars/Base", "0s", "many"
	f.AppriseURL = "nope"
	f.Tags = []TagRow{{Name: "x"}}
	f.SMTP = "smtp:587"
	f.Heartbeat = "5m"

	errs := r.save(f)
	for _, field := range []string{"zone", "keep", "max_parallel", "channels.apprise.url", "channels.apprise.tags", "channels.mail.from"} {
		le, ok := errs[field]
		if !ok || le.Key == "" || le.Msg == "" {
			t.Errorf("no error on %s: %+v", field, errs)
		}
	}
	if len(errs) != 6 {
		t.Errorf("errors: %+v", errs)
	}
	if r.stored() != "" {
		t.Errorf("stored despite errors: %s", r.stored())
	}
	if got := r.jobs.Current().Settings.Heartbeat; got != 0 {
		t.Errorf("heartbeat %v applied", got)
	}
}

// The form's own error keys exist in both catalogs.
func TestFormKeysInCatalog(t *testing.T) {
	for _, key := range []string{keyInt, keyTag} {
		if !i18n.Has(key) {
			t.Errorf("no catalog text for %s", key)
		}
	}
}

// Profiles are not on the form; saving keeps them as they are.
func TestSaveKeepsProfiles(t *testing.T) {
	r := newRig(t)
	doc := "[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n" +
		"[[profiles.leise]]\non = [\"fail\"]\nchannels = [\"mail\"]\nafter = 3\n"
	if err := db.WithTx(r.db, func(tx *sql.Tx) error { return repos.PutSettings(tx, doc) }); err != nil {
		t.Fatal(err)
	}

	f := r.form()
	f.Zone = "UTC"
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}

	if !strings.Contains(r.stored(), "leise") || !strings.Contains(r.stored(), "after = 3") {
		t.Errorf("profile lost: %s", r.stored())
	}

	v, err := r.svc.Get()
	if err != nil || len(v.Profiles["leise"]) != 1 {
		t.Errorf("profiles: %+v %v", v.Profiles, err)
	}
}

// Removing what a job names turns the job invalid at once: here the
// Apprise sub channel its rule sends to.
func TestSaveRevalidatesJobs(t *testing.T) {
	r := newRig(t)
	r.job("a", "[[notify]]\non = [\"fail\"]\nchannels = [\"apprise:handy\"]\n")

	f := r.form()
	f.AppriseURL = "http://apprise:8000/notify"
	f.Tags = []TagRow{{Name: "handy", Tag: "phone"}}
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}
	if err := r.jobs.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.jobs.Job("a"); !ok {
		t.Fatalf("job invalid at first: %+v", r.jobs.Current().Invalid)
	}

	f.Tags = nil
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}

	if _, ok := r.jobs.Job("a"); ok {
		t.Fatal("job still valid without its sub channel")
	}
	var ids []string
	for _, le := range r.jobs.Current().Invalid {
		ids = append(ids, le.Job)
	}
	if !slices.Contains(ids, "a") {
		t.Errorf("invalid: %+v", r.jobs.Current().Invalid)
	}
}

// A tag a profile uses cannot be removed: the error is not on a form field.
func TestSaveProfileNeedsTag(t *testing.T) {
	r := newRig(t)
	doc := "[channels.apprise]\nurl = \"http://a/notify\"\ntags = {handy = \"p\"}\n" +
		"[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"apprise:handy\"]\n"
	if err := db.WithTx(r.db, func(tx *sql.Tx) error { return repos.PutSettings(tx, doc) }); err != nil {
		t.Fatal(err)
	}

	f := r.form()
	f.Tags = nil
	errs := r.save(f)
	if _, ok := errs["profiles.standard.0.channels"]; !ok || len(errs) != 1 {
		t.Errorf("errors: %+v", errs)
	}
	if r.stored() != doc {
		t.Errorf("document changed: %s", r.stored())
	}
}

// secrets lists the names of the secrets that are set.
func (r *rig) secretNames() []string {
	r.t.Helper()

	infos, err := r.sec.List()
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, in := range infos {
		out = append(out, in.Name)
	}

	return out
}

// A password is set, kept by an empty field, replaced and removed; the
// view only says whether it is set.
func TestSaveSecrets(t *testing.T) {
	r := newRig(t)
	f := r.form()

	f.Secrets = map[string]SecretEdit{
		SecretSMTPPassword: {Value: "pw-one"},
		SecretAppriseKey:   {Value: "key-one"},
	}
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}

	v, err := r.svc.Get()
	if err != nil || !v.SecretSet[SecretSMTPPassword] || !v.SecretSet[SecretAppriseKey] || v.SecretSet[SecretHeartbeat] {
		t.Fatalf("set: %+v %v", v.SecretSet, err)
	}
	got, _, err := r.sec.Resolve([]string{SecretSMTPPassword})
	if err != nil || got[SecretSMTPPassword] != "pw-one" {
		t.Errorf("resolve: %v %v", got, err)
	}
	if strings.Contains(r.stored(), "pw-one") || strings.Contains(r.stored(), "key-one") {
		t.Errorf("secret in the document: %s", r.stored())
	}

	// Empty keeps; a new value replaces; Clear removes.
	f.Secrets = map[string]SecretEdit{SecretSMTPPassword: {}, SecretAppriseKey: {Value: "key-two"}}
	r.save(f)
	got, _, _ = r.sec.Resolve([]string{SecretSMTPPassword, SecretAppriseKey})
	if got[SecretSMTPPassword] != "pw-one" || got[SecretAppriseKey] != "key-two" {
		t.Errorf("keep/replace: %v", got)
	}

	f.Secrets = map[string]SecretEdit{SecretSMTPPassword: {Clear: true}, SecretHeartbeat: {Clear: true}}
	r.save(f)
	if names := r.secretNames(); !slices.Equal(names, []string{SecretAppriseKey}) {
		t.Errorf("after clear: %v", names)
	}
}

// Without MASTER_KEY a value cannot be sealed; nothing else is saved either.
func TestSaveSecretNeedsKey(t *testing.T) {
	r := newRig(t)
	crypto.Init(nil)

	f := r.form()
	f.Zone = "UTC"
	f.Secrets = map[string]SecretEdit{SecretSMTPPassword: {Value: "x"}}
	if _, err := r.svc.Save(f, "tester"); !errors.Is(err, crypto.ErrMissingKey) {
		t.Fatalf("err %v", err)
	}
	if r.stored() != "" {
		t.Errorf("stored: %s", r.stored())
	}
}

// BASE_URL wins: the stored value stays and the form's is ignored.
func TestBaseURLEnvKeepsStored(t *testing.T) {
	r := newRig(t)
	r.svc = New(Config{DB: r.db, Jobs: r.jobs, Secrets: r.sec, BaseURLEnv: "https://env.example"})
	if err := db.WithTx(r.db, func(tx *sql.Tx) error {
		return repos.PutSettings(tx, "base_url = \"https://stored.example\"\n[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n")
	}); err != nil {
		t.Fatal(err)
	}

	f := r.form()
	f.BaseURL = "https://typed.example"
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}
	if !strings.Contains(r.stored(), "stored.example") || strings.Contains(r.stored(), "typed.example") {
		t.Errorf("stored: %s", r.stored())
	}

	v, _ := r.svc.Get()
	if v.BaseURLEnv != "https://env.example" {
		t.Errorf("view: %+v", v)
	}
}

// A stored document that does not load comes back as the defaults and the
// reason; saving replaces it.
func TestGetBrokenDocument(t *testing.T) {
	r := newRig(t)
	if err := db.WithTx(r.db, func(tx *sql.Tx) error { return repos.PutSettings(tx, "zone = [\n") }); err != nil {
		t.Fatal(err)
	}

	v, err := r.svc.Get()
	if err != nil || v.Broken == nil || len(v.Profiles["standard"]) != 1 {
		t.Fatalf("view: %+v %v", v, err)
	}
	if errs := r.save(v.Form); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}
	if v, _ := r.svc.Get(); v.Broken != nil {
		t.Errorf("still broken: %+v", v.Broken)
	}
}
