package web

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"jidoka/internal/cron"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/status"
)

// Kante's data-state words (.pill, .status, .ticks, .ev).
const (
	keyOK   = "ok"
	keyWarn = "warn"
	keyBad  = "bad"
	keyRun  = "run"
	keyOff  = "off"
	keyMiss = "miss"
)

const (
	secondsPerHour = 3600
	hoursPerDay    = 24
	// secsShown: below it a duration reads in seconds ("45 s"), above in
	// minutes and seconds ("2 min 05 s").
	secsShown = 2 * time.Minute
	// minsShown: below it minutes ("1 min 30 s"), above hours ("3 h 05 min").
	minsShown = 2 * time.Hour

	// okTickY and okTickH make a success a short column, as Kante's CSS
	// does too: the strip tells ok from bad by height, not only by colour.
	okTickY = ".45"
	okTickH = ".55"

	// tipSummaryMax cuts the summary in a read-out so it stays on a tile.
	tipSummaryMax = 40
	// stripShort and stripLong are the runs a tile's strip shows: a
	// disturbed tile is wider (O1).
	stripShort = 16
	stripLong  = 30
)

// ui says times and states in the language and zone of one request. now
// is fixed per render, so "yesterday" is the same everywhere on a page.
type ui struct {
	locale i18n.Locale
	zone   *time.Location
	now    time.Time
}

// T is the text of key in the request's language.
func (u ui) T(key string, kv ...any) string {
	params := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		params[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return i18n.T(key, u.locale, params)
}

// word is the text of a key made of a family and a value: "run." + "ok".
func (u ui) word(family, value string) string {
	return u.T(family + value)
}

// funcs are the template funcs that need the request. placeholders()
// calls it on a zero ui to learn the names.
func (u ui) funcs() template.FuncMap {
	return template.FuncMap{
		"dur":      u.dur,
		"when":     u.when,
		"clock":    u.clock,
		"now":      func() time.Time { return u.now },
		"since":    func(t time.Time) time.Duration { return u.now.Sub(t) },
		"strip":    u.strip,
		"plan":     u.plan,
		"durs":     u.durations,
		"audit":    u.audit,
		"diff":     u.diff,
		"quiet":    u.quiet,
		"ruleText": u.ruleText,
		"loadMsg":  u.loadMsg,
		"words":    u.words,
		"jobKey":   jobKey,
		"runKey":   runKey,
		"eventKey": eventKey,
		"join":     strings.Join,
		"zoneName": func() string { return zoneOf(u.zone).Name },
		"hm":       quietClock,
	}
}

// loadMsg is a load error in the reader's language; one made without a
// catalog key keeps its English text.
func (u ui) loadMsg(le model.LoadError) string {
	if le.Key == "" {
		return le.Msg
	}
	return i18n.T(le.Key, u.locale, le.Params)
}

// words is a cron expression in words ("daily at 06:00"); the form it
// does not cover stays as written. The page puts the expression in a title.
func (u ui) words(expr string) string {
	text, ok := cron.Describe(expr, u.locale)
	if !ok {
		return expr
	}
	return text
}

// dur reads a length: "<1 s", "45 s", "2 min 05 s", "3 h 05 min".
func (u ui) dur(d time.Duration) string {
	switch {
	case d < 0:
		return "—"
	case d < time.Second:
		return "<1 s"
	case d < secsShown:
		return fmt.Sprintf("%d s", int(d.Seconds()+.5))
	case d < minsShown:
		sec := int(d.Seconds() + .5)
		return fmt.Sprintf("%d min %02d s", sec/60, sec%60)
	}

	mins := int(d.Minutes() + .5)
	if mins%60 == 0 {
		return fmt.Sprintf("%d h", mins/60)
	}
	return fmt.Sprintf("%d h %02d min", mins/60, mins%60)
}

// clock is the time of day in Jidoka's zone.
func (u ui) clock(t time.Time) string {
	return t.In(u.zone).Format("15:04")
}

// when is t as the reader wants it next to "now": "14:20", "yesterday
// 14:20", "tomorrow 14:20", else "02.01. 14:20". Zero is a dash. The
// caller puts {{tzMark}} behind it.
func (u ui) when(t time.Time) string {
	if t.IsZero() {
		return "—"
	}

	local, now := t.In(u.zone), u.now.In(u.zone)
	days := dayIndex(local) - dayIndex(now)
	switch days {
	case 0:
		return local.Format("15:04")
	case -1:
		return u.T("time.yesterday") + " " + local.Format("15:04")
	case 1:
		return u.T("time.tomorrow") + " " + local.Format("15:04")
	}

	if u.locale == i18n.LocaleDE {
		return local.Format("02.01. 15:04")
	}
	return local.Format("Jan 2 15:04")
}

// dayIndex counts calendar days, whatever the length of a day.
func dayIndex(t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / (secondsPerHour * hoursPerDay))
}

// jobKey is Kante's data-state of a job's state.
func jobKey(s model.JobState) string {
	switch s {
	case model.JobOK:
		return keyOK
	case model.JobFail, model.JobTimeout, model.JobInvalid:
		return keyBad
	case model.JobMissed:
		return keyWarn
	case model.JobRunning:
		return keyRun
	}
	return keyOff
}

// runKey is Kante's data-state of a run's state.
func runKey(s model.RunState) string {
	switch s {
	case model.RunOK:
		return keyOK
	case model.RunFail, model.RunTimeout:
		return keyBad
	case model.RunRunning:
		return keyRun
	case model.RunAborted:
		return keyWarn
	}
	return keyOff
}

// eventKey colours the trigger of a rule like the state it reports.
func eventKey(e model.Event) string {
	switch e {
	case model.EvFail, model.EvTimeout, model.EvInvalid:
		return keyBad
	case model.EvMissed, model.EvSlow:
		return keyWarn
	case model.EvRecover, model.EvSuccess:
		return keyOK
	}
	return keyOff
}

// stripCell is one run of a tile's strip.
type stripCell struct {
	X     int
	State string
	Y, H  string
	Tip   string
}

// stripView is the history ticks of one tile (O1).
type stripView struct {
	N     int
	Cells []stripCell
	Label string
}

// strip draws the last runs of t, newest right: stripLong columns on a
// disturbed tile, stripShort otherwise (O1). A watch that missed its
// window gets a hollow column after them.
func (u ui) strip(t status.Tile) stripView {
	n := stripShort
	if t.State.Broken() {
		n = stripLong
	}

	ticks := t.Ticks
	miss := t.State == model.JobMissed
	room := n
	if miss {
		room--
	}
	if len(ticks) > room {
		ticks = ticks[len(ticks)-room:]
	}

	x := 0

	v := stripView{N: n, Cells: []stripCell{}, Label: u.T("strip.label")}
	for _, tk := range ticks {
		c := stripCell{X: x, State: runKey(tk.State), Y: "0", H: "1", Tip: u.tickTip(tk)}
		if tk.State == model.RunOK {
			c.Y, c.H = okTickY, okTickH
		}
		v.Cells = append(v.Cells, c)
		x++
	}

	if miss {
		tip := u.T("strip.miss", "until", u.when(t.Due)+" "+zoneOf(u.zone).Name)
		v.Cells = append(v.Cells, stripCell{X: x, State: keyMiss, Y: "0", H: "1", Tip: tip})
	}
	return v
}

// tickTip is the read-out of a run: "#12 · 14:20 CET · ok · 3 s · text".
// Kante splits it at the first " · ".
func (u ui) tickTip(t status.Tick) string {
	parts := []string{
		"#" + strconv.FormatInt(t.ID, 10),
		u.when(t.Start) + " " + zoneOf(u.zone).Name,
		u.word("run.", string(t.State)),
		u.dur(t.Length),
	}
	if t.Summary != "" {
		parts = append(parts, cut(t.Summary, tipSummaryMax))
	}
	return strings.Join(parts, " · ")
}

// cut shortens s to n runes with an ellipsis.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// audit is the words of an audit action; an unknown one stays as it is.
func (u ui) audit(action string) string {
	key := "audit." + action
	if !i18n.Has(key) {
		return action
	}
	return u.T(key)
}

// diff is "2 new, 1 changed" for the last load; no change says so.
func (u ui) diff(d jobs.Diff) string {
	var parts []string
	add := func(key string, n int) {
		if n > 0 {
			parts = append(parts, u.T(key, "count", n))
		}
	}
	add("diff.added", len(d.Added))
	add("diff.changed", len(d.Changed))
	add("diff.removed", len(d.Removed))
	add("diff.invalid", len(d.Invalid))

	if len(parts) == 0 {
		return u.T("diff.none")
	}
	return strings.Join(parts, ", ")
}

// quiet is how long a watch has been silent: since its last success, else
// since its window ended.
func (u ui) quiet(t status.Tile) time.Duration {
	if t.LastOK != nil && !t.LastOK.End.IsZero() {
		return u.now.Sub(t.LastOK.End)
	}
	return u.now.Sub(t.Since)
}

// ruleText is what a rule does besides "when" and "where": delay,
// reminder, digest, priority.
func (u ui) ruleText(r model.Rule) string {
	var bits []string
	if r.After > 1 {
		bits = append(bits, u.T("rule.after", "n", r.After))
	}
	if r.Remind > 0 {
		bits = append(bits, u.T("rule.remind", "every", u.dur(r.Remind)))
	}
	if r.Digest != "" {
		bits = append(bits, u.T("rule.digest", "when", r.Digest))
	}
	if r.Urgent {
		bits = append(bits, u.T("rule.urgent"))
	}
	if r.Attach {
		bits = append(bits, u.T("rule.attach"))
	}
	if len(bits) == 0 {
		return u.T("rule.at_once")
	}
	return strings.Join(bits, " · ")
}
