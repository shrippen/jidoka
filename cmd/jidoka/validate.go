package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"jidoka/internal/conf"
	"jidoka/internal/cron"
	"jidoka/internal/model"
)

const (
	nextSlots  = 3
	slotLayout = "Mon 2006-01-02 15:04 MST"
)

// validate checks a jobs folder (T1): lists every job with its next
// slots on out, every error as file:line: msg on errOut. It returns 1 if
// anything is broken, so it can run as CI. It knows no stored settings:
// the defaults apply, and profile names and Apprise tags are not checked
// (the server does, when it loads the jobs).
func validate(dir string, out, errOut io.Writer) int {
	set := conf.Load(dir, conf.DefaultSettings(), conf.CheckOffline)
	broken := false

	for _, e := range set.Invalid {
		broken = true
		fmt.Fprintln(errOut, formatErr(e))
	}

	ids := make([]string, 0, len(set.Jobs))
	for id := range set.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		printJob(out, set.Jobs[id], set.LoadedAt)
	}

	fmt.Fprintf(out, "%d valid, %d invalid\n", len(set.Jobs), invalidCount(set))

	if broken {
		return exitInvalid
	}

	return exitOK
}

// invalidCount counts broken jobs, not errors (one job may have several).
func invalidCount(set model.Set) int {
	seen := map[string]bool{}
	for _, e := range set.Invalid {
		seen[e.File] = true
	}

	return len(seen)
}

func formatErr(e model.LoadError) string {
	if e.Line == 0 {
		return fmt.Sprintf("%s: %s", e.File, e.Msg)
	}

	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
}

func printJob(out io.Writer, j *model.Job, now time.Time) {
	fmt.Fprintf(out, "%s (%s) %s\n", j.ID, j.Kind, j.Name)

	if j.Schedule == "" {
		fmt.Fprintf(out, "  every %s, grace %s\n", j.Period, j.Grace)
		return
	}

	s, err := cron.Parse(j.Schedule, j.Zone)
	if err != nil {
		return
	}

	var slots []string
	at := now
	for i := 0; i < nextSlots; i++ {
		at = s.Next(at)
		if at.IsZero() {
			break
		}
		slots = append(slots, at.In(j.Zone).Format(slotLayout))
	}
	fmt.Fprintf(out, "  %s [%s]: %s\n", j.Schedule, j.Zone, strings.Join(slots, ", "))
}
