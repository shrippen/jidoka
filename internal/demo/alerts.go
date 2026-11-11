//go:build !release

package demo

import (
	"errors"
	"fmt"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
)

const defaultProfile = "standard"

// seedAlerts opens the disturbances the world lists, as the alert engine
// would have: with the messages already sent, so a start repeats none.
func seedAlerts(q db.Queryer, d data, set model.Set, runs map[string][]model.Run, now time.Time) error {
	for _, def := range d.Alerts {
		a, err := buildAlert(def, set, runs[def.Job], now)
		if err != nil {
			return fmt.Errorf("alert %s: %w", def.Job, err)
		}
		if err := repos.OpenAlert(q, a); err != nil {
			return err
		}
	}

	return nil
}

func buildAlert(def alertDef, set model.Set, runs []model.Run, now time.Time) (repos.Alert, error) {
	ev := model.Event(def.Event)
	a := repos.Alert{Job: def.Job, Event: ev, Name: def.Job, Streak: 1, Sent: map[int]time.Time{}}

	job := set.Jobs[def.Job]
	if job != nil && job.Name != "" {
		a.Name = job.Name
	}

	switch ev {
	case model.EvFail, model.EvTimeout:
		trailBroken(&a, runs)
	case model.EvInvalid:
		for _, le := range set.Invalid {
			if le.Job != def.Job {
				continue
			}
			a.Detail = le.File
			if le.Line > 0 {
				a.Detail += fmt.Sprintf(":%d", le.Line)
			}
			a.Detail += ": " + le.Msg
		}
	}

	if def.Since != "" {
		since, err := offset(def.Since, now)
		if err != nil {
			return a, err
		}
		a.Since = since
	}
	if a.Since.IsZero() {
		return a, errors.New("no start of the disturbance")
	}

	for i, rule := range rulesOf(set, job) {
		if !hasEvent(rule, ev) || a.Streak < threshold(rule, ev) {
			continue
		}

		// A rule with reminders counts from the last message; "now"
		// keeps the first reminder a full period away, so a demo that
		// runs for a while sends none.
		a.Sent[i] = a.Since
		if rule.Remind > 0 {
			a.Sent[i] = now
		}
	}

	return a, nil
}

// trailBroken sets streak, last slot, last run and start from the
// failed runs at the end of the history.
func trailBroken(a *repos.Alert, runs []model.Run) {
	start := len(runs)
	for start > 0 && runs[start-1].State.Broken() {
		start--
	}
	if start == len(runs) {
		return
	}

	trail := runs[start:]
	first := trail[0].Slot

	a.Streak = 0
	var last model.Run
	for _, r := range trail {
		if r.Slot != last.Slot {
			a.Streak++
		}
		last = r
		if r.Slot.Equal(first) {
			a.Since = r.Ended
		}
	}
	a.LastSlot, a.RunID = last.Slot, last.ID
}

// rulesOf: the job's own rules replace the profile (M3, M4).
func rulesOf(set model.Set, job *model.Job) []model.Rule {
	if job != nil && job.Rules != nil {
		return job.Rules
	}

	name := defaultProfile
	if job != nil && job.Profile != "" {
		name = job.Profile
	}
	return set.Settings.Profiles[name]
}

func hasEvent(r model.Rule, ev model.Event) bool {
	for _, e := range r.On {
		if e == ev {
			return true
		}
	}
	return false
}

// threshold: `after` counts slots, so it only holds back fail and timeout.
func threshold(r model.Rule, ev model.Event) int {
	if ev != model.EvFail && ev != model.EvTimeout {
		return 1
	}
	return max(r.After, 1)
}
