package settings

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"jidoka/internal/i18n"
)

const (
	leise = "leise"
	mail  = "mail"
	fail  = "fail"
)

// view is the profile editor as the page shows it now.
func (r *rig) view() ProfilesView {
	r.t.Helper()

	v, err := r.svc.Profiles()
	if err != nil {
		r.t.Fatal(err)
	}

	return v
}

func (r *rig) saveProfiles(ps []ProfileForm) Errors {
	r.t.Helper()

	errs, err := r.svc.SaveProfiles(ps, "tester")
	if err != nil {
		r.t.Fatal(err)
	}

	return errs
}

func (r *rig) profile(name string) (ProfileForm, bool) {
	for _, p := range r.view().Profiles {
		if p.Name == name {
			return p, true
		}
	}

	return ProfileForm{}, false
}

func simple(name string) ProfileForm {
	return ProfileForm{Name: name, Rules: []RuleForm{{On: []string{fail}, Channels: []string{mail}}}}
}

// A fresh install shows the standard profile with its default rule.
func TestProfilesDefault(t *testing.T) {
	r := newRig(t)
	v := r.view()

	if len(v.Profiles) != 1 || v.Profiles[0].Name != "standard" || len(v.Profiles[0].Rules) != 1 {
		t.Fatalf("profiles: %+v", v.Profiles)
	}
	if rule := v.Profiles[0].Rules[0]; rule.Remind != "24h" || !slices.Contains(rule.On, "timeout") {
		t.Errorf("rule: %+v", rule)
	}
	if !slices.Contains(v.Events, "change") || !slices.Contains(v.Events, "slow") {
		t.Errorf("events: %v", v.Events)
	}
}

// Every rule field survives a save and a read.
func TestProfileRuleRoundTrip(t *testing.T) {
	r := newRig(t)
	f := r.form()
	f.AppriseURL = "http://a/notify"
	f.Tags = []TagRow{{Name: "handy", Tag: "phone"}}
	f.SMTP, f.From = "smtp:587", "j@example.org"
	if errs := r.save(f); len(errs) != 0 {
		t.Fatalf("settings: %+v", errs)
	}

	rule := RuleForm{
		On: []string{"fail", "timeout"}, Channels: []string{"apprise:handy", "mail"},
		To: "a@example.org\nb@example.org", After: "3", Remind: "6h", Tail: "0", Urgent: true, Attach: true,
	}
	digest := RuleForm{On: []string{"success", "recover"}, Channels: []string{"apprise"}, Digest: "weekly Mon 08:00"}
	ps := append(r.view().Profiles, ProfileForm{Name: leise, Rules: []RuleForm{rule, digest}})

	if errs := r.saveProfiles(ps); len(errs) != 0 {
		t.Fatalf("errors: %+v", errs)
	}

	got, ok := r.profile(leise)
	if !ok || len(got.Rules) != 2 {
		t.Fatalf("profile: %+v", got)
	}
	if !slices.Equal(got.Rules[0].On, rule.On) || !slices.Equal(got.Rules[0].Channels, rule.Channels) ||
		got.Rules[0].To != rule.To || got.Rules[0].After != "3" || got.Rules[0].Remind != "6h" ||
		got.Rules[0].Tail != "0" || !got.Rules[0].Urgent || !got.Rules[0].Attach {
		t.Errorf("rule 0: %+v", got.Rules[0])
	}
	if got.Rules[1].Digest != "weekly Mon 08:00" || got.Rules[1].Tail != "" || got.Rules[1].After != "" {
		t.Errorf("rule 1: %+v", got.Rules[1])
	}

	// Seen by the jobs: the new profile is in force at once.
	if rules := r.jobs.Current().Settings.Profiles[leise]; len(rules) != 2 || rules[0].After != 3 || !rules[0].Urgent {
		t.Errorf("in force: %+v", rules)
	}
	// The rest of the document stays.
	if st := r.jobs.Current().Settings; st.Apprise.Tags["handy"] != "phone" || st.Mail.SMTP != "smtp:587" {
		t.Errorf("channels lost: %+v", st)
	}
}

// Add, rename and delete a profile.
func TestProfileAddRenameDelete(t *testing.T) {
	r := newRig(t)

	ps := append(r.view().Profiles, simple(leise))
	if errs := r.saveProfiles(ps); len(errs) != 0 {
		t.Fatalf("add: %+v", errs)
	}

	ps = r.view().Profiles
	for i := range ps {
		if ps[i].Name == leise {
			ps[i].Name = "ruhig"
		}
	}
	if errs := r.saveProfiles(ps); len(errs) != 0 {
		t.Fatalf("rename: %+v", errs)
	}
	if _, ok := r.profile(leise); ok {
		t.Error("old name still there")
	}
	if _, ok := r.profile("ruhig"); !ok {
		t.Error("new name missing")
	}

	var keep []ProfileForm
	for _, p := range r.view().Profiles {
		if p.Name != "ruhig" {
			keep = append(keep, p)
		}
	}
	if errs := r.saveProfiles(keep); len(errs) != 0 {
		t.Fatalf("delete: %+v", errs)
	}
	if _, ok := r.profile("ruhig"); ok {
		t.Error("deleted profile still there")
	}
}

// standard is required: without it nothing is stored.
func TestProfileRefusesNoStandard(t *testing.T) {
	r := newRig(t)
	before := r.stored()

	errs := r.saveProfiles([]ProfileForm{simple(leise)})
	if _, ok := errs[fieldNoStandard]; !ok || len(errs) != 1 {
		t.Errorf("errors: %+v", errs)
	}
	if r.stored() != before {
		t.Errorf("stored: %s", r.stored())
	}
}

// A profile a job names is not deleted or renamed: the error names the jobs.
func TestProfileRefusesUsed(t *testing.T) {
	r := newRig(t)
	if errs := r.saveProfiles(append(r.view().Profiles, simple(leise))); len(errs) != 0 {
		t.Fatal(errs)
	}
	r.job("b", "notify_profile = \""+leise+"\"\n")
	r.job("a", "notify_profile = \""+leise+"\"\n")
	if err := r.jobs.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.jobs.Job("a"); !ok {
		t.Fatalf("job a invalid: %+v", r.jobs.Current().Invalid)
	}
	before := r.stored()

	std, _ := r.profile("standard")
	for name, ps := range map[string][]ProfileForm{
		"delete": {std},
		"rename": {std, simple("ruhig")},
	} {
		errs := r.saveProfiles(ps)
		le, ok := errs[usedField(leise)]
		if !ok || len(errs) != 1 || le.Params["jobs"] != "a, b" {
			t.Errorf("%s: errors %+v", name, errs)
		}
		if r.stored() != before {
			t.Errorf("%s: stored changed", name)
		}
	}
	if _, ok := r.jobs.Job("a"); !ok {
		t.Error("job a turned invalid")
	}
}

// Errors come back at profiles.<name>.<i>.<field>; nothing is stored.
func TestProfileFieldErrors(t *testing.T) {
	r := newRig(t)
	before := r.stored()

	std, _ := r.profile("standard")
	bad := ProfileForm{Name: leise, Rules: []RuleForm{
		{On: []string{fail}, Channels: []string{mail}},
		{On: []string{fail}, Channels: []string{mail}, After: "x", Tail: "-1", Remind: "soon", Digest: "daily 08:00"},
		{Channels: []string{"apprise:nope"}},
	}}

	errs := r.saveProfiles([]ProfileForm{std, bad})
	for _, field := range []string{
		"profiles.leise.1.after", "profiles.leise.1.tail", "profiles.leise.1.remind", "profiles.leise.1.digest",
		"profiles.leise.2.on", "profiles.leise.2.channels",
	} {
		le, ok := errs[field]
		if !ok || le.Key == "" || le.Msg == "" {
			t.Errorf("no error on %s: %+v", field, errs)
		}
	}
	if _, ok := errs["profiles.leise.0.on"]; ok {
		t.Errorf("rule 0 is fine: %+v", errs)
	}
	if r.stored() != before {
		t.Errorf("stored despite errors: %s", r.stored())
	}
}

// Names: letters, digits, - and _; each once; not empty. A profile has a rule.
func TestProfileNameErrors(t *testing.T) {
	r := newRig(t)
	std, _ := r.profile("standard")

	tests := map[string][]ProfileForm{
		"profiles.a b.name":      {std, simple("a b")},
		"profiles.standard.name": {std, simple("standard")},
		"profiles..name":         {std, simple(" ")},
		"profiles.leer.rules":    {std, {Name: "leer"}},
	}
	for field, ps := range tests {
		errs := r.saveProfiles(ps)
		if le, ok := errs[field]; !ok || le.Key == "" {
			t.Errorf("%s: errors %+v", field, errs)
		}
		if r.stored() != "" {
			t.Errorf("%s: stored %s", field, r.stored())
		}
	}
}

// The edit buttons change the form, nothing else.
func TestApplyEdit(t *testing.T) {
	ps := []ProfileForm{simple("a"), simple("b")}

	got := ApplyEdit(ps, Edit{Kind: EditAddRule, Profile: 1})
	if len(got[1].Rules) != 2 || len(got[0].Rules) != 1 || len(ps[1].Rules) != 1 {
		t.Errorf("add rule: %+v", got)
	}

	got = ApplyEdit(got, Edit{Kind: EditDelRule, Profile: 1, Rule: 0})
	if len(got[1].Rules) != 1 || len(got[1].Rules[0].On) != 0 {
		t.Errorf("del rule: %+v", got)
	}

	got = ApplyEdit(got, Edit{Kind: EditAddProfile})
	if len(got) != 3 || got[2].Name != "" || len(got[2].Rules) != 1 {
		t.Errorf("add profile: %+v", got)
	}

	got = ApplyEdit(got, Edit{Kind: EditDelProfile, Profile: 0})
	if len(got) != 2 || got[0].Name != "b" {
		t.Errorf("del profile: %+v", got)
	}

	if same := ApplyEdit(got, Edit{Kind: EditDelRule, Profile: 9, Rule: 9}); len(same) != 2 {
		t.Errorf("out of range: %+v", same)
	}
}

// Channel choices: those configured, plus one a rule names that is gone.
func TestChannelChoices(t *testing.T) {
	r := newRig(t)
	f := r.form()
	f.AppriseURL = "http://a/notify"
	f.Tags = []TagRow{{Name: "handy", Tag: "p"}, {Name: "arbeit", Tag: "w"}}
	f.SMTP, f.From = "s:25", "j@example.org"
	if errs := r.save(f); len(errs) != 0 {
		t.Fatal(errs)
	}

	v := r.view()
	if want := []string{"apprise", "apprise:arbeit", "apprise:handy", "mail"}; !slices.Equal(v.Channels, want) {
		t.Errorf("channels: %v", v.Channels)
	}

	got := v.ChannelsFor(RuleForm{Channels: []string{"mail", "apprise:weg"}})
	if want := []string{"apprise", "apprise:arbeit", "apprise:handy", "mail", "apprise:weg"}; !slices.Equal(got, want) {
		t.Errorf("for rule: %v", got)
	}
}

func TestProfileKeysInCatalog(t *testing.T) {
	for _, key := range []string{keyProfileName, keyProfileNone, keyNoStandard, keyUsed} {
		if !i18n.Has(key) {
			t.Errorf("no catalog text for %s", key)
		}
	}
	if !strings.Contains(usedField(leise), leise) {
		t.Error("used field")
	}
}

// Saving settings and profiles at the same time keeps both: each save
// reads and writes the one document, so the second must see the first.
func TestConcurrentSavesKeepBoth(t *testing.T) {
	r := newRig(t)
	const rounds = 30

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range rounds {
			f := r.form()
			f.AndonURL = fmt.Sprintf("https://andon%d.test", i)
			if _, err := r.svc.Save(f, "tester"); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range rounds {
			v, err := r.svc.Profiles()
			if err != nil {
				t.Error(err)
				return
			}
			ps := append(v.Profiles, ProfileForm{Name: fmt.Sprintf("p%d", i),
				Rules: []RuleForm{{On: []string{fail}, Channels: []string{mail}}}})
			if _, err := r.svc.SaveProfiles(ps, "tester"); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()

	doc := r.stored()
	for i := range rounds {
		if !strings.Contains(doc, fmt.Sprintf("[[profiles.p%d]]", i)) {
			t.Errorf("profile p%d lost", i)
		}
	}
	if !strings.Contains(doc, fmt.Sprintf("andon%d.test", rounds-1)) {
		t.Errorf("last andon_url lost")
	}
}
