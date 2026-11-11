package settings

import (
	"database/sql"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jidoka/internal/conf"
	"jidoka/internal/db"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

// Catalog keys of errors the profile editor itself finds.
const (
	keyProfileName = "profiles.name_bad"
	keyProfileNone = "profiles.no_rule"
	keyNoStandard  = "profiles.no_standard"
	keyUsed        = "profiles.used"
)

const (
	fieldNoStandard = "profiles." + conf.StandardProfile
	auditProfiles   = "profiles"
	channelApprise  = "apprise"
	channelMail     = "mail"
	subPrefix       = channelApprise + ":"
)

// profileName is what a job's notify_profile can name: no dots or spaces,
// they would break the key of an error.
var profileName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// RuleForm is one rule of a profile, as text. The checkbox lists hold the
// names a rule has; the numbers and durations are checked by conf.
type RuleForm struct {
	On, Channels   []string
	To             string // one address per line
	After          string
	Remind         string
	Tail           string
	Digest         string
	Urgent, Attach bool
}

// ProfileForm is a named list of rules.
type ProfileForm struct {
	Name  string
	Rules []RuleForm
}

// ProfilesView is the profile editor: the profiles as stored and what the
// checkboxes offer.
type ProfilesView struct {
	Profiles []ProfileForm
	// Events a rule may name, in the order of the form.
	Events []string
	// Channels that are set up: apprise, apprise:<sub>, mail.
	Channels []string
	// Broken is why the stored document did not load; the editor then
	// shows the defaults and a save replaces the document.
	Broken *model.LoadError
}

// ChannelsFor are the choices for a rule: the set up channels, and any
// other the rule names (a sub channel since removed), so a save does not
// drop it unseen. conf then reports it.
func (v ProfilesView) ChannelsFor(r RuleForm) []string {
	out := slices.Clone(v.Channels)
	for _, ch := range r.Channels {
		if !slices.Contains(out, ch) {
			out = append(out, ch)
		}
	}

	return out
}

// usedField is the key of the error of a profile that a job still names.
func usedField(name string) string {
	return "profiles." + name + ".used"
}

// Profiles returns the editor for the stored settings.
func (s *Service) Profiles() (ProfilesView, error) {
	d, broken, err := s.stored()
	if err != nil {
		return ProfilesView{}, err
	}

	v := ProfilesView{Profiles: profileForms(d.Profiles), Channels: channelNames(d), Broken: broken}
	for _, ev := range conf.RuleEvents() {
		v.Events = append(v.Events, string(ev))
	}

	return v, nil
}

// SaveProfiles checks ps and stores them as the profiles; the rest of the
// document stays. With errors nothing is stored and they come back by
// field ("profiles.<name>.<i>.<field>"). A profile a job names cannot go,
// and standard stays. Afterwards every job is checked against the new
// settings.
func (s *Service) SaveProfiles(ps []ProfileForm, actor string) (Errors, error) {
	d, _, err := s.stored()
	if err != nil {
		return nil, err
	}

	errs := Errors{}
	removed := removedNames(d.Profiles, ps)
	d.Profiles = profilesOf(errs, ps)

	if _, ok := d.Profiles[conf.StandardProfile]; !ok {
		errs[fieldNoStandard] = fieldErrM(fieldNoStandard, i18n.M(keyNoStandard))
	}
	s.refuseUsed(errs, removed)

	// conf's own "standard missing" is said above, once.
	if _, list := conf.CheckDraft(d); len(list) > 0 {
		for _, le := range list {
			if _, dup := errs[le.Field]; !dup && le.Field != fieldNoStandard {
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
		return repos.AddAudit(tx, actor, auditAction, auditProfiles, s.cfg.Now())
	})
	if err != nil {
		return nil, err
	}

	_, err = s.cfg.Jobs.Revalidate()
	return nil, err
}

// removedNames are the stored profiles the form no longer has by name:
// deleted or renamed. Jobs that name them would turn invalid.
func removedNames(stored map[string][]conf.DraftRule, ps []ProfileForm) []string {
	var out []string

	for name := range stored {
		gone := !slices.ContainsFunc(ps, func(p ProfileForm) bool { return strings.TrimSpace(p.Name) == name })
		if gone {
			out = append(out, name)
		}
	}
	sort.Strings(out)

	return out
}

// refuseUsed reports each removed profile that a job names, with the jobs.
func (s *Service) refuseUsed(errs Errors, removed []string) {
	jobs := s.cfg.Jobs.Current().Jobs

	for _, name := range removed {
		var ids []string
		for id, j := range jobs {
			if j.Profile == name {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}

		sort.Strings(ids)
		field := usedField(name)
		errs[field] = fieldErrM(field, i18n.M(keyUsed, "profile", name, "jobs", strings.Join(ids, ", ")))
	}
}

// profilesOf is the document's profiles; the errors are those of the text
// itself (a name, a number that is none).
func profilesOf(errs Errors, ps []ProfileForm) map[string][]conf.DraftRule {
	out := map[string][]conf.DraftRule{}

	for _, p := range ps {
		name := strings.TrimSpace(p.Name)
		nameField := "profiles." + name + ".name"

		_, dup := out[name]
		if !profileName.MatchString(name) || dup {
			errs[nameField] = fieldErrM(nameField, i18n.M(keyProfileName))
			continue
		}

		if len(p.Rules) == 0 {
			field := "profiles." + name + ".rules"
			errs[field] = fieldErrM(field, i18n.M(keyProfileNone))
		}

		rules := make([]conf.DraftRule, 0, len(p.Rules))
		for i, r := range p.Rules {
			rules = append(rules, ruleOf(errs, "profiles."+name+"."+strconv.Itoa(i), r))
		}
		out[name] = rules
	}

	return out
}

// ruleOf is the document's rule; prefix is the key of its errors.
func ruleOf(errs Errors, prefix string, r RuleForm) conf.DraftRule {
	rule := conf.DraftRule{
		On: r.On, Channels: r.Channels, Urgent: r.Urgent, AttachOutput: r.Attach,
		Remind: strings.TrimSpace(r.Remind), Digest: strings.TrimSpace(r.Digest),
	}
	for _, line := range strings.Split(r.To, "\n") {
		if to := strings.TrimSpace(line); to != "" {
			rule.To = append(rule.To, to)
		}
	}

	rule.After = intField(errs, prefix+".after", r.After)
	tail := intField(errs, prefix+".tail", r.Tail)
	if strings.TrimSpace(r.Tail) != "" {
		rule.Tail = &tail
	}

	return rule
}

// intField reads an optional whole number; empty is 0. A negative one is
// left for conf to refuse.
func intField(errs Errors, field, text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}

	n, err := strconv.Atoi(text)
	if err != nil {
		errs[field] = fieldErr(field, keyInt)
		return 0
	}

	return n
}

// profileForms are the profiles as text: standard first, then by name.
func profileForms(profiles map[string][]conf.DraftRule) []ProfileForm {
	var out []ProfileForm

	for name, rules := range profiles {
		p := ProfileForm{Name: name}
		for _, r := range rules {
			p.Rules = append(p.Rules, ruleForm(r))
		}
		out = append(out, p)
	}

	sort.Slice(out, func(a, b int) bool {
		if sa, sb := out[a].Name == conf.StandardProfile, out[b].Name == conf.StandardProfile; sa != sb {
			return sa
		}

		return out[a].Name < out[b].Name
	})

	return out
}

func ruleForm(r conf.DraftRule) RuleForm {
	f := RuleForm{
		On: r.On, Channels: r.Channels, To: strings.Join(r.To, "\n"),
		Remind: r.Remind, Digest: r.Digest, Urgent: r.Urgent, Attach: r.AttachOutput,
	}
	if r.After > 0 {
		f.After = strconv.Itoa(r.After)
	}
	if r.Tail != nil {
		f.Tail = strconv.Itoa(*r.Tail)
	}

	return f
}

// channelNames are the channels a rule can name: set up ones only.
func channelNames(d conf.Draft) []string {
	var out []string

	if a := d.Channels.Apprise; a != nil {
		out = append(out, channelApprise)
		var subs []string
		for name := range a.Tags {
			subs = append(subs, subPrefix+name)
		}
		sort.Strings(subs)
		out = append(out, subs...)
	}
	if d.Channels.Mail != nil {
		out = append(out, channelMail)
	}

	return out
}

// EditKind is a button of the editor that changes the form, not the
// stored profiles.
type EditKind int

const (
	EditNone EditKind = iota
	EditAddProfile
	EditDelProfile
	EditAddRule
	EditDelRule
)

// Edit is one such button press: Profile and Rule are positions in the form.
type Edit struct {
	Kind          EditKind
	Profile, Rule int
}

// ApplyEdit is ps after e; ps stays as it is. A position that is not
// there changes nothing.
func ApplyEdit(ps []ProfileForm, e Edit) []ProfileForm {
	out := slices.Clone(ps)

	switch e.Kind {
	case EditAddProfile:
		return append(out, ProfileForm{Rules: []RuleForm{{}}})
	case EditDelProfile:
		if e.Profile < 0 || e.Profile >= len(out) {
			return out
		}
		return slices.Delete(out, e.Profile, e.Profile+1)
	}

	if e.Profile < 0 || e.Profile >= len(out) {
		return out
	}

	rules := slices.Clone(out[e.Profile].Rules)
	switch e.Kind {
	case EditAddRule:
		rules = append(rules, RuleForm{})
	case EditDelRule:
		if e.Rule < 0 || e.Rule >= len(rules) {
			return out
		}
		rules = slices.Delete(rules, e.Rule, e.Rule+1)
	}
	out[e.Profile].Rules = rules

	return out
}
