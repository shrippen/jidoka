package conf

import (
	"net/url"
	"time"

	_ "time/tzdata" // zone data in the binary (J3)

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
			is.at("", 0, "zone", "unknown zone %q", raw.Zone)
		} else {
			s.Zone = loc
		}
	}

	if raw.Quiet != "" {
		w, err := parseWindow(raw.Quiet, s.Zone)
		if err != nil {
			is.at("", 0, "quiet", "%v", err)
		}
		s.Quiet = w
	}

	if raw.Heartbeat != "" {
		d, err := parseDur(raw.Heartbeat)
		if err != nil {
			is.at("", 0, "heartbeat", "%v", err)
		}
		s.Heartbeat = d
	}

	if raw.Keep != "" {
		d, err := parseDur(raw.Keep)
		if err != nil || d == 0 {
			is.at("", 0, "keep", "keep must be a positive duration")
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

	return s
}

func positive(is *issues, key string, v *int, def int) int {
	if v == nil {
		return def
	}
	if *v < 1 {
		is.at("", 0, key, "%s must be at least 1", key)
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
		is.at(sect, idx, key, "%s must be an http(s) URL", key)
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
		is.at(sect, 0, "url", "url required")
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
		is.at(sect, 0, "smtp", "smtp required (host:port)")
	}
	if raw.From == "" {
		is.at(sect, 0, "from", "from required")
	}

	return &model.Mail{SMTP: raw.SMTP, User: raw.User, From: raw.From, To: raw.To}
}
