package cron

import (
	"fmt"
	"strconv"
	"strings"

	"jidoka/internal/i18n"
)

const (
	hoursPerDay   = 24
	minutesInHour = 60
	daysInWeek    = 7
	listSep       = ", "
	dowKey        = "cron.dow."
	// Monday and Friday bound the weekday range "1-5".
	monday = 1
	friday = 5
)

// Describe says a schedule in words, "daily at 06:00", in locale. ok is
// false for forms it does not cover (and for invalid ones); the caller
// then shows the raw expression. Pure; the cron text stays the truth.
//
//	*/5 * * * *      every 5 minutes
//	15 * * * *       hourly at :15
//	0 6,18 * * *     daily at 06:00, 18:00
//	30 8 * * 1-5     weekdays at 08:30
//	0 3 * * 0,3      weekly on Wed, Sun at 03:00
//	0 4 1 * *        monthly on day 1 at 04:00
func Describe(expr string, locale i18n.Locale) (text string, ok bool) {
	f := strings.Fields(expr)
	if len(f) != fieldCount || f[3] != "*" {
		return "", false
	}

	minute, dom, dow := f[0], f[2], f[4]
	if minute == "*" && f[1] == "*" && dom == "*" && dow == "*" {
		return i18n.T("cron.every_min", locale, nil), true
	}

	if n, found := strings.CutPrefix(minute, "*/"); found {
		return stepText(n, f[1:], locale)
	}

	min, err := strconv.Atoi(minute)
	if err != nil || min < 0 || min >= minutesInHour {
		return "", false
	}
	if f[1] == "*" && dom == "*" && dow == "*" {
		return i18n.T("cron.hourly", locale, map[string]any{"min": pad(min)}), true
	}

	hours, found := intList(f[1], 0, hoursPerDay-1)
	if !found {
		return "", false
	}
	times := make([]string, len(hours))
	for i, h := range hours {
		times[i] = pad(h) + ":" + pad(min)
	}
	p := map[string]any{"times": strings.Join(times, listSep)}

	switch {
	case dom == "*" && dow == "*":
		return i18n.T("cron.daily", locale, p), true
	case dom == "*":
		return weekly(dow, p, locale)
	case dow == "*":
		days, found := intList(dom, 1, 31)
		if !found || len(days) != 1 {
			return "", false
		}
		p["day"] = days[0]
		return i18n.T("cron.monthly", locale, p), true
	}

	return "", false
}

// stepText covers "*/N * * * *"; the other fields must be open.
func stepText(n string, rest []string, locale i18n.Locale) (string, bool) {
	step, err := strconv.Atoi(n)
	if err != nil || step < 1 || step >= minutesInHour {
		return "", false
	}
	for _, r := range rest {
		if r != "*" {
			return "", false
		}
	}
	if step == 1 {
		return i18n.T("cron.every_min", locale, nil), true
	}

	return i18n.T("cron.every_n_min", locale, map[string]any{"n": step}), true
}

// weekly words a weekday list: Mon-Fri is "weekdays".
func weekly(dow string, p map[string]any, locale i18n.Locale) (string, bool) {
	days, ok := dowList(dow)
	if !ok {
		return "", false
	}

	if len(days) == friday && days[0] == monday && days[friday-1] == friday {
		return i18n.T("cron.weekdays", locale, p), true
	}

	names := make([]string, len(days))
	for i, d := range days {
		names[i] = i18n.T(dowKey+strconv.Itoa(d), locale, nil)
	}
	p["days"] = strings.Join(names, listSep)

	return i18n.T("cron.weekly", locale, p), true
}

// dowList reads "1-5", "0,3", "MON,WED" as sorted days 0-6 (7 is 0).
func dowList(text string) ([]int, bool) {
	var raw []string
	for _, part := range strings.Split(text, ",") {
		a, b, isRange := strings.Cut(part, "-")
		if !isRange {
			raw = append(raw, part)
			continue
		}
		lo, ok1 := dowValue(a)
		hi, ok2 := dowValue(b)
		if !ok1 || !ok2 || lo > hi {
			return nil, false
		}
		for d := lo; d <= hi; d++ {
			raw = append(raw, strconv.Itoa(d))
		}
	}

	seen := map[int]bool{}
	for _, r := range raw {
		d, ok := dowValue(r)
		if !ok {
			return nil, false
		}
		seen[d%daysInWeek] = true
	}

	var out []int
	for d := 0; d < daysInWeek; d++ {
		if seen[d] {
			out = append(out, d)
		}
	}
	// Monday-first order reads naturally: Mon..Sat, then Sun.
	if len(out) > 0 && out[0] == 0 {
		out = append(out[1:], 0)
	}

	return out, len(out) > 0
}

func dowValue(text string) (int, bool) {
	for i, name := range fDow.names {
		if strings.EqualFold(text, name) {
			return fDow.min + i, true
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil || v < fDow.min || v > fDow.max {
		return 0, false
	}

	return v, true
}

// intList reads "6" or "6,18" as values in [lo, hi].
func intList(text string, lo, hi int) ([]int, bool) {
	var out []int
	for _, part := range strings.Split(text, ",") {
		v, err := strconv.Atoi(part)
		if err != nil || v < lo || v > hi {
			return nil, false
		}
		out = append(out, v)
	}

	return out, true
}

func pad(n int) string {
	return fmt.Sprintf("%02d", n)
}
