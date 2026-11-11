package conf

import (
	"time"

	"jidoka/internal/model"
)

// tailDefault is Rule.Tail when tail is not set (-1 = default, M8).
const tailDefault = -1

// rawRule is one [[notify]] or [[profiles.<name>]] table.
type rawRule struct {
	On           []string `toml:"on"`
	Channels     []string `toml:"channels"`
	To           []string `toml:"to"`
	After        int      `toml:"after"`
	Remind       string   `toml:"remind"`
	Urgent       bool     `toml:"urgent"`
	Tail         *int     `toml:"tail"`
	Digest       string   `toml:"digest"`
	AttachOutput bool     `toml:"attach_output"`
}

// buildRules validates the tables of one section (e.g. "notify").
func buildRules(is *issues, sect string, raws []rawRule, tags map[string]string) []model.Rule {
	var out []model.Rule

	for i, r := range raws {
		out = append(out, buildRule(is, sect, i, r, tags))
	}

	return out
}

func buildRule(is *issues, sect string, i int, r rawRule, tags map[string]string) model.Rule {
	rule := model.Rule{
		Channels: r.Channels,
		To:       r.To,
		After:    r.After,
		Urgent:   r.Urgent,
		Tail:     tailDefault,
		Digest:   r.Digest,
		Attach:   r.AttachOutput,
	}

	if len(r.On) == 0 {
		is.at(sect, i, "on", "on: at least one event required")
	}
	allDigestable := true
	for _, name := range r.On {
		ev := model.Event(name)
		if !knownEvents[ev] {
			is.at(sect, i, "on", "unknown event %q", name)
			continue
		}
		rule.On = append(rule.On, ev)
		allDigestable = allDigestable && digestEvents[ev]
	}

	if len(r.Channels) == 0 {
		is.at(sect, i, "channels", "channels: at least one required")
	}
	for _, ch := range r.Channels {
		if err := checkChannel(ch, tags); err != nil {
			is.at(sect, i, "channels", "%v", err)
		}
	}

	if r.After < 0 {
		is.at(sect, i, "after", "after must not be negative")
	}

	if r.Tail != nil {
		rule.Tail = *r.Tail
		if rule.Tail < 0 {
			is.at(sect, i, "tail", "tail must not be negative")
		}
	}

	rule.Remind = ruleRemind(is, sect, i, r.Remind)

	if r.Digest == "" {
		return rule
	}
	if err := checkDigest(r.Digest); err != nil {
		is.at(sect, i, "digest", "%v", err)
	}
	// Disturbances never wait in a digest (M7).
	if !allDigestable {
		is.at(sect, i, "digest", "digest only allowed with success, recover, change, reload")
	}

	return rule
}

func ruleRemind(is *issues, sect string, i int, text string) time.Duration {
	// Not set and "never" both mean no reminder.
	if text == "" || text == remindNever {
		return 0
	}

	d, err := parseDur(text)
	if err != nil {
		is.at(sect, i, "remind", "%v", err)
		return 0
	}
	if d == 0 {
		is.at(sect, i, "remind", `remind must be positive or "never"`)
	}

	return d
}
