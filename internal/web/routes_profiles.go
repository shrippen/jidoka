package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"jidoka/internal/conf"
	"jidoka/internal/model"
	"jidoka/internal/services/settings"
)

// The profile editor (M4): a form per rule. Every signed-in user may edit.
//
//	GET  /settings/profiles   the form
//	POST /settings/profiles   an edit button (add or delete a row): the form
//	                          again, changed, nothing stored; else check: errors,
//	                          400 and the form again; saved, back with a notice
//
// Fields are named by position, which only lives for one post:
//
//	profile=0 profile=1            the profiles, in order
//	p0_name  p0_rule=0 p0_rule=1   name and rules of profile 0
//	p0_r1_on p0_r1_channels …      fields of rule 1 (a list: one value per tick)
//	act=add_rule:0                 the button pressed
const (
	profilesPath     = "/settings/profiles"
	profilesTemplate = "settings_profiles"

	fieldProfile = "profile"
	fieldAct     = "act"
	actSep       = ":"
	valueYes     = "1"
)

// The buttons of the form; the text after the name is the position.
const (
	actAddProfile = "add_profile"
	actDelProfile = "del_profile"
	actAddRule    = "add_rule"
	actDelRule    = "del_rule"
)

// ruleFields are the fields of one rule, for the errors shown at them.
var ruleFields = []string{"on", "channels", "to", "after", "remind", "tail", "digest"}

func (s *Server) registerProfiles() {
	s.mux.HandleFunc("GET "+profilesPath, s.authed(s.pageProfiles))
	s.mux.HandleFunc("POST "+profilesPath, s.authed(s.actProfiles))
}

// ruleRow is one rule of the page.
type ruleRow struct {
	P, Idx   int
	Form     settings.RuleForm
	Channels []string
	errs     settings.Errors
	key      string // "profiles.<name>.<i>."
}

// N is the number shown for the rule.
func (r ruleRow) N() int {
	return r.Idx + 1
}

// Err is the error of a field of the rule; the zero value when none.
func (r ruleRow) Err(field string) model.LoadError {
	return r.errs[r.key+field]
}

// Bad marks the input of a field with an error.
func (r ruleRow) Bad(field string) template.HTMLAttr {
	if _, ok := r.errs[r.key+field]; ok {
		return `aria-invalid="true"`
	}

	return ""
}

// Has reports whether a list of the rule holds name.
func (r ruleRow) Has(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}

	return false
}

// profileRow is one profile of the page.
type profileRow struct {
	Idx      int
	Name     string
	Standard bool
	Rules    []ruleRow
	errs     settings.Errors
}

// Err is the error of the profile's name.
func (p profileRow) Err() model.LoadError {
	return p.errs["profiles."+p.Name+".name"]
}

// NoRule is the error of a profile without a rule.
func (p profileRow) NoRule() model.LoadError {
	return p.errs["profiles."+p.Name+".rules"]
}

// Bad marks the name input when it has an error.
func (p profileRow) Bad() template.HTMLAttr {
	if _, ok := p.errs["profiles."+p.Name+".name"]; ok {
		return `aria-invalid="true"`
	}

	return ""
}

// profilesData is the data of settings_profiles.html.
type profilesData struct {
	View    settings.ProfilesView
	Rows    []profileRow
	Rest    []model.LoadError
	Errors  settings.Errors
	Saved   bool
	Unsaved bool
}

// Next is the position the next added profile gets.
func (d profilesData) Next() int {
	return len(d.Rows)
}

// profilesView lays the form out for the template; errs may be nil.
func profilesView(v settings.ProfilesView, ps []settings.ProfileForm, errs settings.Errors) profilesData {
	d := profilesData{View: v, Errors: errs}
	shown := map[string]bool{}

	for i, p := range ps {
		name := strings.TrimSpace(p.Name)
		row := profileRow{Idx: i, Name: name, Standard: name == conf.StandardProfile, errs: errs}
		shown["profiles."+name+".name"] = true
		shown["profiles."+name+".rules"] = true

		for j, r := range p.Rules {
			key := "profiles." + name + "." + strconv.Itoa(j) + "."
			row.Rules = append(row.Rules, ruleRow{P: i, Idx: j, Form: r, Channels: v.ChannelsFor(r), errs: errs, key: key})
			for _, f := range ruleFields {
				shown[key+f] = true
			}
		}
		d.Rows = append(d.Rows, row)
	}

	for field, le := range errs {
		if !shown[field] {
			d.Rest = append(d.Rest, le)
		}
	}

	return d
}

func (s *Server) pageProfiles(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Config == nil {
		s.missing(w, r, sess)
		return
	}

	v, err := s.deps.Config.Profiles()
	if err != nil {
		s.log.Error("profiles", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}

	d := profilesView(v, v.Profiles, nil)
	d.Saved = r.URL.Query().Get(paramSaved) == savedYes
	s.page(w, r, sess, profilesTemplate, http.StatusOK, d)
}

func (s *Server) actProfiles(w http.ResponseWriter, r *http.Request, sess *session) {
	if s.deps.Config == nil {
		s.missing(w, r, sess)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, sess, http.StatusBadRequest, errKeyBad)
		return
	}

	ps := profilesFormOf(r)
	v, err := s.deps.Config.Profiles()
	if err != nil {
		s.log.Error("profiles", "err", err)
		s.fail(w, r, sess, http.StatusInternalServerError, errKeyInternal)
		return
	}

	// An edit button only changes the form; the save comes with its own.
	if e := editOf(r.PostForm.Get(fieldAct)); e.Kind != settings.EditNone {
		d := profilesView(v, settings.ApplyEdit(ps, e), nil)
		d.Unsaved = true
		s.page(w, r, sess, profilesTemplate, http.StatusOK, d)
		return
	}

	errs, err := s.deps.Config.SaveProfiles(ps, actor(sess))
	if err != nil {
		s.actionFail(w, r, sess, err)
		return
	}
	if len(errs) == 0 {
		http.Redirect(w, r, profilesPath+"?"+paramSaved+"="+savedYes, http.StatusSeeOther)
		return
	}

	// As entered: the page adds no row of its own, so a rejected save
	// does not grow the form.
	s.page(w, r, sess, profilesTemplate, http.StatusBadRequest, profilesView(v, ps, errs))
}

// profilesFormOf reads the posted profiles in the order of their positions.
func profilesFormOf(r *http.Request) []settings.ProfileForm {
	v := r.PostForm
	var out []settings.ProfileForm

	for _, pi := range v[fieldProfile] {
		pre := "p" + pi + "_"
		p := settings.ProfileForm{Name: v.Get(pre + "name")}

		for _, ri := range v[pre+"rule"] {
			rp := pre + "r" + ri + "_"
			p.Rules = append(p.Rules, settings.RuleForm{
				On: v[rp+"on"], Channels: v[rp+"channels"], To: v.Get(rp + "to"),
				After: v.Get(rp + "after"), Remind: v.Get(rp + "remind"),
				Tail: v.Get(rp + "tail"), Digest: v.Get(rp + "digest"),
				Urgent: v.Get(rp+"urgent") == valueYes, Attach: v.Get(rp+"attach") == valueYes,
			})
		}
		out = append(out, p)
	}

	return out
}

// editOf reads a button value such as "del_rule:1:0"; anything else is no edit.
func editOf(act string) settings.Edit {
	parts := strings.Split(act, actSep)
	pos := func(i int) int {
		if i >= len(parts) {
			return -1
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return -1
		}

		return n
	}

	switch parts[0] {
	case actAddProfile:
		return settings.Edit{Kind: settings.EditAddProfile}
	case actDelProfile:
		return settings.Edit{Kind: settings.EditDelProfile, Profile: pos(1)}
	case actAddRule:
		return settings.Edit{Kind: settings.EditAddRule, Profile: pos(1)}
	case actDelRule:
		return settings.Edit{Kind: settings.EditDelRule, Profile: pos(1), Rule: pos(2)}
	}

	return settings.Edit{}
}
