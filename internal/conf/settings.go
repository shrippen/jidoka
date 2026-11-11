package conf

import (
	"bytes"
	"net/url"
	"time"

	"github.com/BurntSushi/toml"

	_ "time/tzdata" // zone data in the binary (J3)

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

// settingsFile names the settings document in syntax errors.
const settingsFile = "settings"

const (
	defaultZone     = "Europe/Berlin"
	defaultKeepDays = 180
	defaultKeepRuns = 500
	defaultParallel = 2
	defaultProfile  = "standard"
	defaultRemind   = "24h"
)

// DraftApprise is the Apprise channel as written in the settings document.
type DraftApprise struct {
	URL  string            `toml:"url"`
	Tags map[string]string `toml:"tags,omitempty"`
}

// DraftMail is the mail channel as written in the settings document.
type DraftMail struct {
	SMTP string   `toml:"smtp"`
	User string   `toml:"user,omitempty"`
	From string   `toml:"from"`
	To   []string `toml:"to,omitempty"`
}

// Draft is the settings document (TOML) in editable form: what the
// settings page changes before CheckDraft turns it into model.Settings.
// Profiles pass through untouched until their own editor exists.
type Draft struct {
	Zone        string `toml:"zone,omitempty"`
	Locale      string `toml:"locale,omitempty"`
	Quiet       string `toml:"quiet,omitempty"`
	Heartbeat   string `toml:"heartbeat,omitempty"`
	MaxParallel *int   `toml:"max_parallel,omitempty"`
	Keep        string `toml:"keep,omitempty"`
	KeepRuns    *int   `toml:"keep_runs,omitempty"`
	AndonURL    string `toml:"andon_url,omitempty"`
	BaseURL     string `toml:"base_url,omitempty"`
	Channels    struct {
		Apprise *DraftApprise `toml:"apprise,omitempty"`
		Mail    *DraftMail    `toml:"mail,omitempty"`
	} `toml:"channels"`
	Profiles map[string][]rawRule `toml:"profiles,omitempty"`
}

// defaults is what no stored settings leave.
func defaults() model.Settings {
	loc, err := time.LoadLocation(defaultZone)
	if err != nil {
		loc = time.UTC
	}

	return model.Settings{
		Zone:        loc,
		Locale:      string(i18n.DefaultLocale),
		MaxParallel: defaultParallel,
		Keep:        defaultKeepDays * hoursPerDay * time.Hour,
		KeepRuns:    defaultKeepRuns,
	}
}

// ParseDraft decodes a settings document strictly (syntax, types, unknown
// keys); the values are not checked yet.
func ParseDraft(src []byte) (Draft, *model.LoadError) {
	var d Draft
	is := decode(newIssues("", settingsFile, string(src)), string(src), &d)
	if len(is.list) > 0 {
		return Draft{}, &is.list[0]
	}

	return d, nil
}

// EncodeDraft is the document of d.
func EncodeDraft(d Draft) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(d); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// CheckDraft is the one validation of the settings: every value is
// checked and every error returned. The Settings are only good without
// errors.
func CheckDraft(d Draft) (model.Settings, []model.LoadError) {
	is := newIssues("", settingsFile, "")
	s := buildSettings(is, d)
	if len(is.list) > 0 {
		return defaults(), is.list
	}

	return s, nil
}

// ParseSettings parses and checks a settings document; the first error
// is returned.
func ParseSettings(src []byte) (model.Settings, *model.LoadError) {
	d, lerr := ParseDraft(src)
	if lerr != nil {
		return defaults(), lerr
	}

	s, errs := CheckDraft(d)
	if len(errs) > 0 {
		return defaults(), &errs[0]
	}

	return s, nil
}

// DefaultDraft is the document of a fresh install: the defaults and the
// standard profile every fallback goes through (B8).
func DefaultDraft() Draft {
	d := Draft{Profiles: map[string][]rawRule{
		defaultProfile: {{
			On:       []string{"fail", "timeout", "missed", "invalid"},
			Channels: []string{chApprise, chMail},
			Remind:   defaultRemind,
		}},
	}}

	return d
}

// DefaultSettings are the settings of a fresh install.
func DefaultSettings() model.Settings {
	s, _ := CheckDraft(DefaultDraft())

	return s
}

// buildSettings turns the decoded document into Settings, reporting every error.
func buildSettings(is *issues, raw Draft) model.Settings {
	s := defaults()

	if raw.Zone != "" {
		loc, err := time.LoadLocation(raw.Zone)
		if err != nil {
			is.at("", 0, "zone", i18n.M("conf.zone", "zone", raw.Zone))
		} else {
			s.Zone = loc
		}
	}

	if raw.Locale != "" {
		s.Locale = checkLocale(is, raw.Locale, s.Locale)
	}

	if raw.Quiet != "" {
		w, err := parseWindow(raw.Quiet, s.Zone)
		if err != nil {
			is.atErr("", 0, "quiet", err)
		}
		s.Quiet = w
	}

	if raw.Heartbeat != "" {
		d, err := parseDur(raw.Heartbeat)
		if err != nil {
			is.atErr("", 0, "heartbeat", err)
		}
		s.Heartbeat = d
	}

	if raw.Keep != "" {
		d, err := parseDur(raw.Keep)
		if err != nil || d == 0 {
			is.at("", 0, "keep", i18n.M("conf.keep_positive"))
		} else {
			s.Keep = d
		}
	}

	s.MaxParallel = positive(is, "max_parallel", raw.MaxParallel, s.MaxParallel)
	s.KeepRuns = positive(is, "keep_runs", raw.KeepRuns, s.KeepRuns)

	s.AndonURL = checkURL(is, "", 0, "andon_url", raw.AndonURL)
	s.BaseURL = checkURL(is, "", 0, "base_url", raw.BaseURL)

	s.Apprise = buildApprise(is, raw.Channels.Apprise)
	s.Mail = buildMail(is, raw.Channels.Mail)

	var tags map[string]string
	if s.Apprise != nil {
		tags = s.Apprise.Tags
	}
	s.Profiles = map[string][]model.Rule{}
	for name, rules := range raw.Profiles {
		s.Profiles[name] = buildRules(is, "profiles."+name, rules, tags, CheckFull)
	}

	// Every fallback (a job without [[notify]], reload, invalid, a failed
	// repo fetch) goes through the standard profile (B8).
	if len(s.Profiles[defaultProfile]) == 0 {
		is.at("profiles", anyIdx, defaultProfile, i18n.M("conf.no_standard"))
	}

	return s
}

// checkLocale accepts the languages with a catalog.
func checkLocale(is *issues, text, def string) string {
	switch i18n.Locale(text) {
	case i18n.LocaleDE, i18n.LocaleEN:
		return text
	}
	is.at("", 0, "locale", i18n.M("conf.locale"))

	return def
}

func positive(is *issues, key string, v *int, def int) int {
	if v == nil {
		return def
	}
	if *v < 1 {
		is.at("", 0, key, i18n.M("conf.min_one", "key", key))
		return def
	}

	return *v
}

func checkURL(is *issues, sect string, idx int, key, text string) string {
	if text == "" {
		return ""
	}

	u, err := url.Parse(text)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		is.at(sect, idx, key, i18n.M("conf.url", "key", key))
		return ""
	}

	return text
}

func buildApprise(is *issues, raw *DraftApprise) *model.Apprise {
	if raw == nil {
		return nil
	}

	const sect = "channels.apprise"
	if raw.URL == "" {
		is.at(sect, 0, "url", i18n.M("conf.url_required"))
	}
	checkURL(is, sect, 0, "url", raw.URL)

	return &model.Apprise{URL: raw.URL, Tags: raw.Tags}
}

func buildMail(is *issues, raw *DraftMail) *model.Mail {
	if raw == nil {
		return nil
	}

	const sect = "channels.mail"
	if raw.SMTP == "" {
		is.at(sect, 0, "smtp", i18n.M("conf.smtp_required"))
	}
	if raw.From == "" {
		is.at(sect, 0, "from", i18n.M("conf.from_required"))
	}

	return &model.Mail{SMTP: raw.SMTP, User: raw.User, From: raw.From, To: raw.To}
}
