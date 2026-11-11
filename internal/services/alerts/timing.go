package alerts

import (
	"strconv"
	"strings"
	"time"

	"jidoka/internal/model"
)

const (
	minutesPerHour = 60
	daysPerWeek    = 7
)

// zoneOf is the zone for settings-wide times: the settings, else UTC.
func zoneOf(set model.Set) *time.Location {
	if set.Settings.Zone != nil {
		return set.Settings.Zone
	}
	return time.UTC
}

// quietOf: the job's own quiet hours replace the global ones (M6).
func quietOf(set model.Set, job *model.Job) *model.Window {
	if job != nil && job.Quiet != nil {
		return job.Quiet
	}
	return set.Settings.Quiet
}

// quietEnd reports whether now lies in the window and when it ends.
//
//	22:00-07:00, now 02:15  → in, ends 07:00 the same day
//	22:00-07:00, now 23:00  → in, ends 07:00 the next day
//	09:00-17:00, now 08:00  → out
//
// Start == End means no quiet hours.
func quietEnd(w *model.Window, now time.Time, def *time.Location) (time.Time, bool) {
	if w == nil || w.Start == w.End {
		return time.Time{}, false
	}

	loc := w.Zone
	if loc == nil {
		loc = def
	}
	t := now.In(loc)
	since := t.Hour()*minutesPerHour + t.Minute() // minutes since local midnight
	start, end := int(w.Start/time.Minute), int(w.End/time.Minute)

	at := func(dayOffset int) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day()+dayOffset, end/minutesPerHour, end%minutesPerHour, 0, 0, loc)
	}

	if start < end {
		if since >= start && since < end {
			return at(0), true
		}
		return time.Time{}, false
	}

	// Wraps past midnight.
	if since >= start {
		return at(1), true
	}
	if since < end {
		return at(0), true
	}
	return time.Time{}, false
}

// nextDigest returns the first digest time after now: "daily 08:00" or
// "weekly Mon 08:00", in the zone of the settings. ok is false for a spec
// it cannot read; the message then goes at once.
func nextDigest(spec string, now time.Time, loc *time.Location) (time.Time, bool) {
	f := strings.Fields(spec)
	t := now.In(loc)

	switch {
	case len(f) == 2 && f[0] == "daily":
		h, m, ok := clock(f[1])
		if !ok {
			return time.Time{}, false
		}
		return firstAfter(t, h, m, -1, loc), true
	case len(f) == 3 && f[0] == "weekly":
		h, m, ok := clock(f[2])
		wd, wok := weekday(f[1])
		if !ok || !wok {
			return time.Time{}, false
		}
		return firstAfter(t, h, m, int(wd), loc), true
	}
	return time.Time{}, false
}

// firstAfter: next h:m after t on weekday wd (-1 = any day).
func firstAfter(t time.Time, h, m, wd int, loc *time.Location) time.Time {
	for d := 0; d <= daysPerWeek; d++ {
		c := time.Date(t.Year(), t.Month(), t.Day()+d, h, m, 0, 0, loc)
		if !c.After(t) {
			continue
		}
		if wd >= 0 && int(c.Weekday()) != wd {
			continue
		}
		return c
	}
	return t
}

func clock(s string) (int, int, bool) {
	hs, ms, ok := strings.Cut(s, ":")
	h, herr := strconv.Atoi(hs)
	m, merr := strconv.Atoi(ms)
	if !ok || herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func weekday(s string) (time.Weekday, bool) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(d.String()[:3], s) {
			return d, true
		}
	}
	return 0, false
}
