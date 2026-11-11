package conf

import (
	"net/url"
	"time"

	_ "time/tzdata" // zone data in the binary (J3)

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

const (
	defaultZone     = "Europe/Berlin"
	defaultKeepDays = 180
	defaultKeepRuns = 500
	defaultParallel = 2
	defaultProfile  = "standard"
)

type rawApprise struct {
	URL  string            `toml:"url"`
	Tags map[string]string `toml:"tags"`
}

type rawMail struct {
	SMTP string   `toml:"smtp"`
	User string   `toml:"user"`
	From string   `toml:"from"`
	To   []string `toml:"to"`
}

type rawSettings struct {
	Zone        string `toml:"zone"`
	Locale      string `toml:"locale"`
	Quiet       string `toml:"quiet"`
	Heartbeat   string `toml:"heartbeat"`
	MaxParallel *int   `toml:"max_parallel"`
	Keep        string `toml:"keep"`
	KeepRuns    *int   `toml:"keep_runs"`
	AndonURL    string `toml:"andon_url"`
	BaseURL     string `toml:"base_url"`
	Channels    struct {
		Apprise *rawApprise `toml:"apprise"`
		Mail    *rawMail    `toml:"mail"`
	} `toml:"channels"`
	Profiles map[string][]rawRule `toml:"profiles"`
}

// defaults is what a missing or broken jidoka.toml leaves.
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

// buildSettings turns the decoded file into Settings, reporting every error.
func buildSettings(is *issues, raw rawSettings) model.Settings {
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
		s.Profiles[name] = buildRules(is, "profiles."+name, rules, tags)
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

func buildApprise(is *issues, raw *rawApprise) *model.Apprise {
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

func buildMail(is *issues, raw *rawMail) *model.Mail {
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
