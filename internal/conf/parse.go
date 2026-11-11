package conf

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

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

	// Events a digest may carry (M7).
	digestEvents = map[model.Event]bool{
		model.EvSuccess: true, model.EvRecover: true, model.EvChange: true, model.EvReload: true,
	}
)

// parseDur reads Go durations plus whole days: "5m", "26h", "365d".
func parseDur(text string) (time.Duration, error) {
	text = strings.TrimSpace(text)
	if m := dayRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", text)
		}

		return time.Duration(n) * hoursPerDay * time.Hour, nil
	}

	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use 5m, 26h, 365d)", text)
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q is negative", text)
	}

	return d, nil
}

// parseWindow reads "22:00-07:00" in a zone.
func parseWindow(text string, loc *time.Location) (*model.Window, error) {
	from, to, ok := strings.Cut(text, "-")
	if !ok {
		return nil, fmt.Errorf("invalid window %q (use HH:MM-HH:MM)", text)
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
		return nil, fmt.Errorf("window %q is empty", text)
	}

	return &model.Window{Start: start, End: end, Zone: loc}, nil
}

// parseClock reads HH:MM as time since midnight.
func parseClock(text string) (time.Duration, error) {
	t, err := time.Parse(hhmmLayout, strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("invalid time %q (use HH:MM)", text)
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
			return fmt.Errorf("invalid weekday %q (use Mon..Sun)", parts[1])
		}
		_, err := parseClock(parts[2])
		return err
	}

	return fmt.Errorf("invalid digest %q (use \"daily HH:MM\" or \"weekly Mon HH:MM\")", text)
}

// checkChannel accepts apprise, apprise:<tag> (tag from settings) and mail.
func checkChannel(ch string, tags map[string]string) error {
	if ch == chApprise || ch == chMail {
		return nil
	}

	sub, ok := strings.CutPrefix(ch, chApprise+":")
	if !ok {
		return fmt.Errorf("unknown channel %q (apprise, apprise:<tag>, mail)", ch)
	}
	if _, ok := tags[sub]; !ok {
		return fmt.Errorf("apprise tag %q is not defined in jidoka.toml", sub)
	}

	return nil
}
