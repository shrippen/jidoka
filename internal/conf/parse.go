package conf

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

const (
	hoursPerDay = 24
	dayUnit     = "d"
	hhmmLayout  = "15:04"
	remindNever = "never"
	chApprise   = "apprise"
	chMail      = "mail"
	dailyWord   = "daily"
	weeklyWord  = "weekly"
)

var (
	idRe     = regexp.MustCompile(`^[a-z0-9-]+$`)
	secretRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	dayRe    = regexp.MustCompile(`^(\d+)d$`)

	weekdays = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}

	// Events a [[notify]] rule may name; test is UI-only.
	knownEvents = map[model.Event]bool{
		model.EvFail: true, model.EvTimeout: true, model.EvMissed: true, model.EvRecover: true,
		model.EvSuccess: true, model.EvInvalid: true, model.EvReload: true, model.EvChange: true,
		model.EvStart: true, model.EvSlow: true,
	}
)

// parseDur reads Go durations plus whole days: "5m", "26h", "365d".
func parseDur(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if m := dayRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, i18n.M("conf.duration", "text", text)
		}

		return time.Duration(n) * hoursPerDay * time.Hour, nil
	}

	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, i18n.M("conf.duration", "text", text)
	}
	if d < 0 {
		return 0, i18n.M("conf.duration_negative", "text", text)
	}

	return d, nil
}

// parseWindow reads "22:00-07:00" in a zone.
func parseWindow(text string, loc *time.Location) (*model.Window, error) {
	from, to, ok := strings.Cut(text, "-")
	if !ok {
		return nil, i18n.M("conf.window", "text", text)
	}

	start, err := parseClock(from)
	if err != nil {
		return nil, err
	}
	end, err := parseClock(to)
	if err != nil {
		return nil, err
	}
	if start == end {
		return nil, i18n.M("conf.window_empty", "text", text)
	}

	return &model.Window{Start: start, End: end, Zone: loc}, nil
}

// parseClock reads HH:MM as time since midnight.
func parseClock(text string) (time.Duration, error) {
	t, err := time.Parse(hhmmLayout, strings.TrimSpace(text))
	if err != nil {
		return 0, i18n.M("conf.time", "text", text)
	}

	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// checkDigest accepts "daily 08:00" and "weekly Mon 08:00".
func checkDigest(text string) error {
	parts := strings.Fields(text)

	switch {
	case len(parts) == 2 && parts[0] == dailyWord:
		_, err := parseClock(parts[1])
		return err
	case len(parts) == 3 && parts[0] == weeklyWord:
		if !weekdays[parts[1]] {
			return i18n.M("conf.weekday", "day", parts[1])
		}
		_, err := parseClock(parts[2])
		return err
	}

	return i18n.M("conf.digest", "text", text)
}

// checkChannel accepts apprise, apprise:<tag> (tag from settings) and mail.
func checkChannel(ch string, tags map[string]string, check Check) error {
	if ch == chApprise || ch == chMail {
		return nil
	}

	sub, ok := strings.CutPrefix(ch, chApprise+":")
	if !ok {
		return i18n.M("conf.channel", "channel", ch)
	}
	if _, ok := tags[sub]; !ok && check == CheckFull {
		return i18n.M("conf.tag_undefined", "tag", sub)
	}

	return nil
}
