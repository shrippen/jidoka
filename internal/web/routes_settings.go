package web

import (
	"html/template"
	"net/http"
	"strings"

	"jidoka/internal/model"
	"jidoka/internal/services/settings"
)

// The settings page (Z5, M1): every signed-in user may edit.
//
//	GET  /settings          the form
//	POST /settings          check; errors: 400 and the form again; else back to
//	                        /settings?saved=1 (post, redirect, get)
const (
	settingsPath   = "/settings"
	paramSaved     = "saved"
	savedYes       = "1"
	fieldSecretPre = "secret_"
	fieldClearPre  = "clear_"
	fieldTagName   = "tag_name"
	fieldTagTag    = "tag_tag"
)

func (s *Server) registerSettings() {
	s.mux.HandleFunc("GET "+settingsPath, s.authed(s.pageSettings))
	s.mux.HandleFunc("POST "+settingsPath, s.authed(s.actSettings))
}

// formFields are the fields of settings.html; an error about anything
// else (a profile that names a removed tag) shows above the form.
var formFields = map[string]bool{
	"zone": true, "locale": true, "quiet": true, "heartbeat": true, "max_parallel": true,
	"keep": true, "keep_runs": true, "andon_url": true, "base_url": true,
	"channels.apprise.url": true, "channels.apprise.tags": true,
	"channels.mail.smtp": true, "channels.mail.security": true, "channels.mail.from": true,
}

// settingsPage is the data of settings.html.
type settingsPage struct {
	settings.View
	Errors  settings.Errors
	Rest    []model.LoadError
	Saved   bool
	Secrets []string
}

// Err is the error of a field; the zero value when it has none.
func (p settingsPage) Err(field string) model.LoadError {
	return p.Errors[field]
}

// Bad marks the input of a field with an error.
func (p settingsPage) Bad(field string) template.HTMLAttr {
	if _, ok := p.Errors[field]; ok {
		return `aria-invalid="true"`
	}

	return ""
}

// secretView is one password field of the page: whether a value is stored
// (never the value) and the edit to show again after an error.
type secretView struct {
	Name       string
	Key        string // lower case, for the catalog
	Set, Clear bool
}

// Secret is the field of secret name.
func (p settingsPage) Secret(name string) secretView {
	return secretView{Name: name, Key: strings.ToLower(name), Set: p.SecretSet[name], Clear: p.Form.Secrets[name].Clear}
}

// Blank is an empty sub channel row, for the template of "add row".
func (p settingsPage) Blank() settings.TagRow {
	return settings.TagRow{}
}

func (s *Server) settingsView(errs settings.Errors) (settingsPage, error) {
	v, err := s.deps.Config.Get()
	p := settingsPage{View: v, Errors: errs, Secrets: settings.Secrets()}

	for field, le := range errs {
		if !formFields[field] {
			p.Rest = append(p.Rest, le)
		}
	}

	return p, err
}

func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Config == nil {
		s.missing(w, r, sess)
		return
	}

	p, err := s.settingsView(nil)
	if err != nil {
		s.log.Error("settings", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}

	p.Saved = r.URL.Query().Get(paramSaved) == savedYes
	s.page(w, r, sess, "settings", http.StatusOK, p)
}

func (s *Server) actSettings(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Config == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	f := formOf(r)
	errs, err := s.deps.Config.Save(f, actor(sess))
	if err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	if len(errs) == 0 {
		http.Redirect(w, r, settingsPath+"?"+paramSaved+"="+savedYes, http.StatusSeeOther)
		return
	}

	// The form comes back as entered, except the secrets: they are never
	// rendered.
	p, err := s.settingsView(errs)
	if err != nil {
		s.log.Error("settings", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}
	for name, e := range f.Secrets {
		e.Value = ""
		f.Secrets[name] = e
	}
	p.Form = f

	s.page(w, r, sess, "settings", http.StatusBadRequest, p)
}

// formOf reads the posted fields.
func formOf(r *http.Request) settings.Form {
	v := r.PostForm
	f := settings.Form{
		Zone: v.Get("zone"), Locale: v.Get("locale"),
		QuietFrom: v.Get("quiet_from"), QuietTo: v.Get("quiet_to"),
		Heartbeat: v.Get("heartbeat"), MaxParallel: v.Get("max_parallel"),
		Keep: v.Get("keep"), KeepRuns: v.Get("keep_runs"),
		AndonURL: v.Get("andon_url"), BaseURL: v.Get("base_url"),
		AppriseURL: v.Get("apprise_url"),
		SMTP:       v.Get("smtp"), Security: v.Get("mail_security"), User: v.Get("mail_user"), From: v.Get("mail_from"),
		ToList:  v.Get("mail_to"),
		Secrets: map[string]settings.SecretEdit{},
	}

	tags := v[fieldTagTag]
	for i, name := range v[fieldTagName] {
		row := settings.TagRow{Name: name}
		if i < len(tags) {
			row.Tag = tags[i]
		}

		// The page adds a blank row itself; keeping posted ones
		// would grow the form with every rejected save.
		if row.Name == "" && row.Tag == "" {
			continue
		}
		f.Tags = append(f.Tags, row)
	}

	for _, name := range settings.Secrets() {
		f.Secrets[name] = settings.SecretEdit{
			Value: strings.TrimSpace(v.Get(fieldSecretPre + name)),
			Clear: v.Get(fieldClearPre+name) != "",
		}
	}

	return f
}
