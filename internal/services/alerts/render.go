package alerts

import (
	"fmt"
	"strings"
	"time"

	"jidoka/internal/drivers/apprise"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

// item is one thing that happened; a message holds one or more (merged
// during quiet hours, collected in a digest). JSON, stored in the outbox.
type item struct {
	Job      string
	Name     string
	Event    model.Event
	At       time.Time
	Zone     string // IANA name of the job's zone
	Duration time.Duration
	Summary  string
	Message  string // JIDOKA_NOTIFY
	Detail   string // reason: run error, load error, reload list
	Tail     string
	Output   string // whole output, only when the rule attaches it
	RunID    int64
	Streak   int
	Since    time.Time
	Reminder bool
}

func (e *Engine) baseItem(set model.Set, sub subject, ev model.Event, at time.Time) item {
	loc := zoneOf(set)
	if sub.job != nil && sub.job.Zone != nil {
		loc = sub.job.Zone
	}
	return item{Job: sub.id, Name: sub.name, Event: ev, At: at, Zone: loc.String()}
}

// fillRun copies what M8 wants from the run: duration, summary, tail,
// optional whole output. Rule.Tail -1 = default, 0 = none.
func (e *Engine) fillRun(it *item, rule model.Rule, run model.Run, now time.Time) {
	it.RunID = run.ID
	it.Summary, it.Message = run.Summary, run.Message
	if !run.Started.IsZero() {
		it.Duration = run.Duration(now)
	}
	if it.Detail == "" {
		it.Detail = run.Error
	}

	n := rule.Tail
	if n < 0 {
		n = defaultTail
	}
	it.Tail = lastLines(run.Output, n)
	if rule.Attach {
		it.Output = run.Output
	}
}

// lastLines returns the last n lines; 0 or less gives "".
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\r\n")
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// localeOf is the language of messages: locale in jidoka.toml (D1).
func localeOf(set model.Set) string {
	if _, ok := texts[set.Settings.Locale]; !ok {
		return DefaultLocale
	}

	return set.Settings.Locale
}

// loadText is a load error in the language of messages; an error made
// without a catalog key keeps its text.
func loadText(le model.LoadError, lang string) string {
	if le.Key == "" {
		return le.Msg
	}

	return i18n.T(le.Key, i18n.Locale(lang), le.Params)
}

type locale struct {
	events    map[model.Event]string
	disturbed string // history verb for fail, timeout, missed, invalid
	recovered string
	state     string
	at        string
	duration  string
	summary   string
	message   string
	detail    string
	since     string
	streak    string
	history   string
	link      string
	tail      string
	reminder  string
	digest    string
	testBody  string
}

var texts = map[string]locale{
	"de": {
		events: map[model.Event]string{
			model.EvFail: "fehlgeschlagen", model.EvTimeout: "Zeitüberschreitung",
			model.EvMissed: "ausgeblieben", model.EvRecover: "erholt",
			model.EvSuccess: "erfolgreich", model.EvInvalid: "ungültig",
			model.EvReload: "neu geladen", model.EvChange: "Änderung",
			model.EvStart: "gestartet", model.EvSlow: "langsam", model.EvTest: "Test",
		},
		disturbed: "gestört", recovered: "erholt",
		state: "Zustand", at: "Zeit", duration: "Dauer", summary: "Zusammenfassung",
		message: "Meldung", detail: "Grund", since: "Seit", streak: "Termine in Folge",
		history: "Verlauf", link: "Link", tail: "Letzte %d Zeilen",
		reminder: "Erinnerung", digest: "Sammelmeldung (%d)",
		testBody: "Testmeldung von Jidoka.",
	},
	"en": {
		events: map[model.Event]string{
			model.EvFail: "failed", model.EvTimeout: "timed out",
			model.EvMissed: "missed", model.EvRecover: "recovered",
			model.EvSuccess: "succeeded", model.EvInvalid: "invalid",
			model.EvReload: "reloaded", model.EvChange: "changed",
			model.EvStart: "started", model.EvSlow: "slow", model.EvTest: "test",
		},
		disturbed: "disturbed", recovered: "recovered",
		state: "State", at: "Time", duration: "Duration", summary: "Summary",
		message: "Message", detail: "Reason", since: "Since", streak: "Slots in a row",
		history: "History", link: "Link", tail: "Last %d lines",
		reminder: "reminder", digest: "Digest (%d)",
		testBody: "Test message from Jidoka.",
	},
}

// appriseType maps an event to the Apprise type (M3).
func appriseType(ev model.Event) apprise.Type {
	switch ev {
	case model.EvFail, model.EvTimeout, model.EvInvalid:
		return apprise.TypeFailure
	case model.EvMissed:
		return apprise.TypeWarning
	case model.EvRecover, model.EvSuccess:
		return apprise.TypeSuccess
	}
	return apprise.TypeInfo
}

func loadZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (it item) local() time.Time { return it.At.In(loadZone(it.Zone)) }

// rendered is the text of one message, channel-neutral.
type rendered struct {
	Title  string
	Body   string
	Type   apprise.Type
	Attach *item // whole output goes to mail as attachment
}

// render builds title and body. Several items (quiet hours merge) show the
// last one in full and the course in one line:
//
//	Verlauf: gestört 02:15, erholt 03:15
//
// A digest lists one line per item.
func (e *Engine) render(set model.Set, p payload) rendered {
	l := texts[localeOf(set)]
	if p.Digest {
		return e.renderDigest(l, p.Items)
	}

	last := p.Items[len(p.Items)-1]
	title := last.Name + ": " + l.events[last.Event]
	if last.Reminder {
		title += " (" + l.reminder + ")"
	}

	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	line(l.state, l.events[last.Event])
	line(l.at, last.local().Format("2006-01-02 15:04 MST"))
	if last.Duration > 0 {
		line(l.duration, shortDur(last.Duration))
	}
	if !last.Since.IsZero() && last.Streak > 1 {
		line(l.since, last.Since.In(loadZone(last.Zone)).Format("2006-01-02 15:04 MST"))
		line(l.streak, fmt.Sprint(last.Streak))
	}
	line(l.summary, last.Summary)
	line(l.message, last.Message)
	line(l.detail, last.Detail)
	line(l.history, e.course(l, p.Items))
	line(l.link, link(set, last))

	if last.Tail != "" {
		n := strings.Count(last.Tail, "\n") + 1
		fmt.Fprintf(&b, "\n"+l.tail+":\n%s\n", n, last.Tail)
	}

	r := rendered{Title: title, Body: strings.TrimRight(b.String(), "\n"), Type: appriseType(last.Event)}
	if p.Attach && last.Output != "" {
		r.Attach = &last
	}
	return r
}

// course is the one-line history of merged items, "" for a single item.
func (e *Engine) course(l locale, items []item) string {
	var parts []string
	for _, it := range items {
		if it.Reminder {
			continue
		}
		word := l.events[it.Event]
		switch {
		case it.Event.Disturbing():
			word = l.disturbed
		case it.Event == model.EvRecover:
			word = l.recovered
		}
		parts = append(parts, word+" "+it.local().Format("15:04"))
	}
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts, ", ")
}

func (e *Engine) renderDigest(l locale, items []item) rendered {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "%s %s: %s", it.local().Format("2006-01-02 15:04"), it.Name, l.events[it.Event])
		if it.Summary != "" {
			b.WriteString(" - " + it.Summary)
		}
		b.WriteString("\n")
	}
	return rendered{
		Title: "Jidoka: " + fmt.Sprintf(l.digest, len(items)),
		Body:  strings.TrimRight(b.String(), "\n"),
		Type:  apprise.TypeInfo,
	}
}

// link points at the run, else at the base; "" without base_url.
func link(set model.Set, it item) string {
	base := strings.TrimRight(set.Settings.BaseURL, "/")
	if base == "" {
		return ""
	}
	if it.RunID == 0 {
		return base
	}
	return fmt.Sprintf("%s/runs/%d", base, it.RunID)
}

func shortDur(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}
