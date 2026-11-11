package conf

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"jidoka/internal/cron"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

const (
	defaultTimeout  = 10 * time.Minute
	defaultRetryGap = 5 * time.Minute
	// watchSlots is how many slots of a watch schedule are measured for Expect.
	watchSlots = 8
)

type rawJob struct {
	Name          string            `toml:"name"`
	Description   string            `toml:"description"`
	Link          string            `toml:"link"`
	Tags          []string          `toml:"tags"`
	Run           string            `toml:"run"`
	Args          []string          `toml:"args"`
	Schedule      string            `toml:"schedule"`
	Zone          string            `toml:"zone"`
	Jitter        string            `toml:"jitter"`
	Timeout       string            `toml:"timeout"`
	Slow          string            `toml:"slow"`
	Retries       int               `toml:"retries"`
	RetryDelay    string            `toml:"retry_delay"`
	Catchup       bool              `toml:"catchup"`
	Lock          string            `toml:"lock"`
	Expect        string            `toml:"expect"`
	Keep          string            `toml:"keep"`
	Quiet         string            `toml:"quiet"`
	Secrets       []string          `toml:"secrets"`
	Env           map[string]string `toml:"env"`
	Enabled       *bool             `toml:"enabled"`
	NotifyProfile string            `toml:"notify_profile"`
	Notify        []rawRule         `toml:"notify"`
}

type rawWatch struct {
	Name          string    `toml:"name"`
	Description   string    `toml:"description"`
	Link          string    `toml:"link"`
	Tags          []string  `toml:"tags"`
	Period        string    `toml:"period"`
	Schedule      string    `toml:"schedule"`
	Zone          string    `toml:"zone"`
	Grace         string    `toml:"grace"`
	Enabled       *bool     `toml:"enabled"`
	NotifyProfile string    `toml:"notify_profile"`
	Notify        []rawRule `toml:"notify"`
}

// jobCtx is what building one job needs.
type jobCtx struct {
	is  *issues
	set *model.Settings
	now time.Time
}

// common fills the fields jobs and watches share.
func (c jobCtx) common(j *model.Job, name, desc, link string, tags []string, zone string,
	enabled *bool, profile string, rules []rawRule) {
	is := c.is

	j.Name = name
	if j.Name == "" {
		j.Name = j.ID
	}
	j.Description = desc
	j.Tags = tags
	j.Link = checkURL(is, "", 0, "link", link)
	j.Enabled = enabled == nil || *enabled

	j.Zone = c.set.Zone
	if zone != "" {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			is.at("", 0, "zone", i18n.M("conf.zone", "zone", zone))
		} else {
			j.Zone = loc
		}
	}

	var tagMap map[string]string
	if c.set.Apprise != nil {
		tagMap = c.set.Apprise.Tags
	}
	j.Rules = buildRules(is, "notify", rules, tagMap)

	j.Profile = defaultProfile
	if profile == "" {
		return
	}
	j.Profile = profile
	if _, ok := c.set.Profiles[profile]; !ok {
		is.at("", 0, "notify_profile", i18n.M("conf.unknown_profile", "profile", profile))
	}
}

// dur parses an optional duration key; min is the smallest allowed value.
func (c jobCtx) dur(key, text string, min time.Duration) time.Duration {
	if text == "" {
		return 0
	}

	d, err := parseDur(text)
	if err != nil {
		c.is.atErr("", 0, key, err)
		return 0
	}
	if d < min {
		c.is.at("", 0, key, i18n.M("conf.positive", "key", key))
	}

	return d
}

func (c jobCtx) schedule(j *model.Job, key string) {
	_, err := cron.Parse(j.Schedule, j.Zone)
	if err != nil {
		c.is.at("", 0, key, i18n.M("conf.schedule_invalid", "detail", msgOf(err)))
	}
}

// buildJob validates a job.toml.
func (c jobCtx) buildJob(j *model.Job, dir string, raw rawJob) {
	is := c.is
	j.Kind = model.KindJob
	c.common(j, raw.Name, raw.Description, raw.Link, raw.Tags, raw.Zone, raw.Enabled, raw.NotifyProfile, raw.Notify)

	j.Run, j.Args, j.Lock, j.Catchup, j.Retries = raw.Run, raw.Args, raw.Lock, raw.Catchup, raw.Retries
	j.Env = raw.Env

	c.checkRun(dir, raw.Run)

	j.Schedule = raw.Schedule
	if raw.Schedule == "" {
		is.at("", 0, "schedule", i18n.M("conf.schedule_required"))
	} else {
		c.schedule(j, "schedule")
	}

	j.Timeout = defaultTimeout
	if raw.Timeout != "" {
		j.Timeout = c.dur("timeout", raw.Timeout, 1)
	}
	j.Jitter = c.dur("jitter", raw.Jitter, 0)
	j.Slow = c.dur("slow", raw.Slow, 0)
	// Retries without retry_delay wait defaultRetryGap.
	j.RetryGap = defaultRetryGap
	if raw.RetryDelay != "" {
		j.RetryGap = c.dur("retry_delay", raw.RetryDelay, 1)
	}
	j.Expect = c.dur("expect", raw.Expect, 1)
	j.Keep = c.dur("keep", raw.Keep, 1)

	if raw.Retries < 0 {
		is.at("", 0, "retries", i18n.M("conf.retries_negative"))
	}

	if raw.Quiet != "" {
		w, err := parseWindow(raw.Quiet, j.Zone)
		if err != nil {
			is.atErr("", 0, "quiet", err)
		}
		j.Quiet = w
	}

	j.Secrets = raw.Secrets
	for _, name := range raw.Secrets {
		if !secretRe.MatchString(name) {
			is.at("", 0, "secrets", i18n.M("conf.secret_name", "name", name))
		}
	}
}

// checkRun requires run to be a regular file inside the job folder.
func (c jobCtx) checkRun(dir, run string) {
	if run == "" {
		c.is.at("", 0, "run", i18n.M("conf.run_required"))
		return
	}

	rel := filepath.Clean(run)
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		c.is.at("", 0, "run", i18n.M("conf.run_outside", "run", run))
		return
	}

	info, err := os.Stat(filepath.Join(dir, rel))
	if err != nil || !info.Mode().IsRegular() {
		c.is.at("", 0, "run", i18n.M("conf.run_missing", "run", run))
	}
}

// buildWatch validates a watch.toml.
//
// Expect (one field for the watcher, W1/W2) is the longest time allowed
// without a ping: period + grace, or for a schedule the longest gap
// between its next watchSlots slots + grace.
func (c jobCtx) buildWatch(j *model.Job, raw rawWatch) {
	is := c.is
	j.Kind = model.KindWatch
	c.common(j, raw.Name, raw.Description, raw.Link, raw.Tags, raw.Zone, raw.Enabled, raw.NotifyProfile, raw.Notify)

	j.Schedule = raw.Schedule
	j.Period = c.dur("period", raw.Period, 1)
	j.Grace = c.dur("grace", raw.Grace, 1)

	if raw.Grace == "" {
		is.at("", 0, "grace", i18n.M("conf.grace_required"))
	}

	switch {
	case raw.Period == "" && raw.Schedule == "":
		is.at("", 0, "", i18n.M("conf.period_or_schedule"))
		return
	case raw.Period != "" && raw.Schedule != "":
		is.at("", 0, "schedule", i18n.M("conf.period_and_schedule"))
		return
	}

	if raw.Period != "" {
		j.Expect = j.Period + j.Grace
		return
	}

	sched, err := cron.Parse(raw.Schedule, j.Zone)
	if err != nil {
		is.at("", 0, "schedule", i18n.M("conf.schedule_invalid", "detail", msgOf(err)))
		return
	}
	j.Expect = maxGap(sched, c.now) + j.Grace
}

// maxGap is the longest gap between the next watchSlots slots.
func maxGap(s *cron.Schedule, now time.Time) time.Duration {
	var longest time.Duration

	prev := s.Next(now)
	for i := 1; i < watchSlots && !prev.IsZero(); i++ {
		next := s.Next(prev)
		if next.IsZero() {
			break
		}
		longest = max(longest, next.Sub(prev))
		prev = next
	}

	return longest
}
