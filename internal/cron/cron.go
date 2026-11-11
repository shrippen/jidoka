// Package cron parses 5-field cron expressions and finds slots in a zone.
// Pure: no I/O.
//
// Slots are matched on the wall clock of the zone ("civil time"), then
// mapped to an instant with the DST rules of J3:
//
//	gap      02:30 Europe/Berlin, last Sunday of March, does not exist
//	         -> fires once at 03:00, the first valid instant after the gap
//	overlap  02:30 exists twice in October -> fires at the first one only
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	fieldCount = 5
	// searchYears bounds the search, e.g. for "0 0 31 2 *" which never fires.
	searchYears = 8
	// overlapSlack covers the longest DST overlap when Last starts from a wall clock.
	overlapSlack = 2 * time.Hour
)

// field describes one cron field: name, value range and names.
type field struct {
	name     string
	min, max int
	names    []string // names[i] is the name of value min+i
}

var (
	fMinute = field{name: "minute", min: 0, max: 59}
	fHour   = field{name: "hour", min: 0, max: 23}
	fDom    = field{name: "day of month", min: 1, max: 31}
	fMonth  = field{name: "month", min: 1, max: 12, names: []string{
		"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}}
	// Day of week: 0 and 7 are Sunday.
	fDow = field{name: "day of week", min: 0, max: 7, names: []string{
		"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT", "SUN"}}
)

// Schedule is a parsed cron expression bound to a zone.
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bit i set = value i matches
	domStar, dowStar              bool   // field started with "*": Vixie AND/OR rule
	loc                           *time.Location
}

// Parse reads "m h dom mon dow". A nil loc means UTC.
func Parse(expr string, loc *time.Location) (*Schedule, error) {
	if loc == nil {
		loc = time.UTC
	}

	parts := strings.Fields(expr)
	if len(parts) != fieldCount {
		return nil, fmt.Errorf("need %d fields (minute hour day month weekday), got %d", fieldCount, len(parts))
	}

	s := &Schedule{loc: loc}
	targets := []struct {
		dst *uint64
		f   field
	}{{&s.minute, fMinute}, {&s.hour, fHour}, {&s.dom, fDom}, {&s.month, fMonth}, {&s.dow, fDow}}

	for i, t := range targets {
		bits, err := parseField(parts[i], t.f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.f.name, err)
		}
		*t.dst = bits
	}

	// Sunday may be written 7; fold it onto 0.
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domStar = strings.HasPrefix(parts[2], "*")
	s.dowStar = strings.HasPrefix(parts[4], "*")

	return s, nil
}

// parseField handles lists "1,5", ranges "1-5", steps "*/5", "1-10/2", names.
func parseField(text string, f field) (uint64, error) {
	var bits uint64

	for _, item := range strings.Split(text, ",") {
		b, err := parseItem(item, f)
		if err != nil {
			return 0, err
		}
		bits |= b
	}

	return bits, nil
}

func parseItem(item string, f field) (uint64, error) {
	rng, stepText, hasStep := strings.Cut(item, "/")

	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("invalid step %q", stepText)
		}
		step = n
	}

	lo, hi := f.min, f.max
	switch {
	case rng == "*":
	case strings.Contains(rng, "-"):
		a, b, _ := strings.Cut(rng, "-")
		var err error
		if lo, err = parseValue(a, f); err != nil {
			return 0, err
		}
		if hi, err = parseValue(b, f); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("range %q runs backwards", rng)
		}
	default:
		v, err := parseValue(rng, f)
		if err != nil {
			return 0, err
		}
		lo, hi = v, v
		// "5/15" means "5-max/15".
		if hasStep {
			hi = f.max
		}
	}

	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}

	return bits, nil
}

func parseValue(text string, f field) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(text, name) {
			return f.min + i, nil
		}
	}

	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", text)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("value %d out of range %d-%d", v, f.min, f.max)
	}

	return v, nil
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }

// dayOK applies Vixie's rule: both day fields restricted = either matches.
func (s *Schedule) dayOK(c time.Time) bool {
	domOK := has(s.dom, c.Day())
	dowOK := has(s.dow, int(c.Weekday()))
	if s.domStar || s.dowStar {
		return domOK && dowOK
	}

	return domOK || dowOK
}

// Next returns the first slot strictly after the given instant, or the
// zero time if there is none within searchYears.
func (s *Schedule) Next(after time.Time) time.Time {
	c := civil(after.In(s.loc)).Add(time.Minute)
	limit := c.AddDate(searchYears, 0, 0)

	for c.Before(limit) {
		c = s.nextCivil(c, limit)
		if c.IsZero() {
			return time.Time{}
		}

		inst := s.instant(c)
		if inst.After(after) {
			return inst
		}
		c = c.Add(time.Minute)
	}

	return time.Time{}
}

// Last returns the latest slot at or before the given instant (for
// catchup, J10), or the zero time if there is none.
func (s *Schedule) Last(before time.Time) time.Time {
	c := civil(before.In(s.loc)).Add(overlapSlack)
	limit := c.AddDate(-searchYears, 0, 0)

	for c.After(limit) {
		c = s.prevCivil(c, limit)
		if c.IsZero() {
			return time.Time{}
		}

		inst := s.instant(c)
		if !inst.After(before) {
			return inst
		}
		c = c.Add(-time.Minute)
	}

	return time.Time{}
}

// civil keeps the wall clock of t, truncated to the minute, as a UTC time.
func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}

// nextCivil returns the first matching wall-clock minute >= c.
func (s *Schedule) nextCivil(c, limit time.Time) time.Time {
	for c.Before(limit) {
		switch {
		case !has(s.month, int(c.Month())):
			c = time.Date(c.Year(), c.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayOK(c):
			c = time.Date(c.Year(), c.Month(), c.Day()+1, 0, 0, 0, 0, time.UTC)
		case !has(s.hour, c.Hour()):
			c = c.Truncate(time.Hour).Add(time.Hour)
		case !has(s.minute, c.Minute()):
			c = c.Add(time.Minute)
		default:
			return c
		}
	}

	return time.Time{}
}

// prevCivil returns the last matching wall-clock minute <= c.
func (s *Schedule) prevCivil(c, limit time.Time) time.Time {
	for c.After(limit) {
		switch {
		case !has(s.month, int(c.Month())):
			c = time.Date(c.Year(), c.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-time.Minute)
		case !s.dayOK(c):
			c = time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC).Add(-time.Minute)
		case !has(s.hour, c.Hour()):
			c = c.Truncate(time.Hour).Add(-time.Minute)
		case !has(s.minute, c.Minute()):
			c = c.Add(-time.Minute)
		default:
			return c
		}
	}

	return time.Time{}
}

// instant maps a wall-clock minute to a real instant (J3).
func (s *Schedule) instant(c time.Time) time.Time {
	// Offsets a day before and after bracket any transition near c.
	var best time.Time

	for _, probe := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		_, off := c.Add(probe).In(s.loc).Zone()
		inst := c.Add(-time.Duration(off) * time.Second)

		wall := inst.In(s.loc)
		if !civil(wall).Equal(c) {
			continue
		}
		// Overlap: two valid instants, take the first.
		if best.IsZero() || inst.Before(best) {
			best = inst
		}
	}
	if !best.IsZero() {
		return best
	}

	// Gap: the wall time does not exist. Use the instant the new offset starts.
	_, off := c.Add(-24 * time.Hour).In(s.loc).Zone()
	start, _ := c.Add(-time.Duration(off) * time.Second).In(s.loc).ZoneBounds()

	return start
}
