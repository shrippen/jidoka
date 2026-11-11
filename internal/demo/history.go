//go:build !release

package demo

import (
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"jidoka/internal/cron"
	"jidoka/internal/model"
)

const (
	// histDays is how far back the seeded history reaches.
	histDays = 30

	timeLayout  = "15:04:05"
	dateLayout  = "2006-01-02"
	clockLayout = "15:04"

	placeStart = "{start}"
	placeEnd   = "{end}"
	placeDate  = "{date}"

	// Salts keep the pseudo random choices of one slot independent.
	saltLag      = "lag"
	saltDuration = "duration"
	saltSummary  = "summary"
	saltJitter   = "jitter"

	// rangeLen is the length of a [min, max] range of seconds.
	rangeLen = 2

	defaultFailExit = 1
)

// builder turns the world's run patterns into runs, relative to now.
//
//	slot (cron) ──► failure? ──► attempts ──► Run{start, end, state, texts}
//	                change?  ──► summary, message
type builder struct {
	now      time.Time
	keepRuns int
}

// history is the seeded part of one job.
type history struct {
	runs []model.Run
	// last is the slot the planner counts as handled; zero for watches.
	last time.Time
}

// build computes the runs of one job; the newest ones, at most keepRuns.
func (b builder) build(def jobDef, job *model.Job) (history, error) {
	var out history

	sched, err := cron.Parse(job.Schedule, job.Zone)
	if err != nil {
		return out, err
	}

	until := b.now
	if def.History.Until != "" {
		if until, err = offset(def.History.Until, b.now); err != nil {
			return out, err
		}
	}

	// The newest slot is left out when it is running now: the planner
	// catches it up at the start (J10).
	newest := sched.Last(b.now)
	if def.History.Running {
		out.last = sched.Last(newest.Add(-time.Minute))
	}
	if !def.History.Running && job.Kind == model.KindJob {
		out.last = newest
	}

	used := map[int]bool{}
	from := b.now.AddDate(0, 0, -histDays)
	for slot := sched.Next(from); !slot.IsZero() && !slot.After(until); slot = sched.Next(slot) {
		if def.History.Running && slot.Equal(newest) {
			break
		}

		runs, err := b.slot(def, job, slot, used)
		if err != nil {
			return out, err
		}
		out.runs = append(out.runs, runs...)
	}

	if len(out.runs) > b.keepRuns {
		out.runs = out.runs[len(out.runs)-b.keepRuns:]
	}

	return out, nil
}

// slot makes the runs of one slot: one, or one per attempt (J13).
func (b builder) slot(def jobDef, job *model.Job, slot time.Time, used map[int]bool) ([]model.Run, error) {
	h := def.History

	fail, err := b.failure(h, job, slot)
	if err != nil {
		return nil, err
	}

	attempts := 1
	if fail != nil && job.Kind == model.KindJob {
		attempts = job.Retries + 1
	}

	var runs []model.Run
	for n := 1; n <= attempts; n++ {
		state := model.RunOK
		if fail != nil && !(fail.Recover && n == attempts) {
			state = model.RunState(fail.State)
		}

		start := b.start(def, job, slot, n, runs)
		run := model.Run{
			Job:     job.ID,
			Trigger: trigger(job, n),
			State:   state,
			Slot:    slot,
			Attempt: n,
			Started: start,
		}
		run.Ended = start.Add(b.length(h, job, state, slot, n))

		b.fill(&run, def, job, fail, used)
		runs = append(runs, run)

		if state == model.RunOK {
			break
		}
	}

	return runs, nil
}

func trigger(job *model.Job, attempt int) model.Trigger {
	switch {
	case job.Kind == model.KindWatch:
		return model.TrigPing
	case attempt > 1:
		return model.TrigRetry
	}
	return model.TrigSchedule
}

// start is when attempt n begins: after jitter, or after the retry gap.
func (b builder) start(def jobDef, job *model.Job, slot time.Time, n int, prev []model.Run) time.Time {
	if n > 1 {
		return prev[len(prev)-1].Ended.Add(job.RetryGap)
	}

	if job.Kind == model.KindWatch {
		return slot.Add(spread(def.History.PingLag, hash(job.ID, saltLag, slot.String())))
	}

	if job.Jitter > 0 {
		span := uint64(job.Jitter / time.Second)
		if span > 0 {
			return slot.Add(time.Duration(hash(job.ID, saltJitter, slot.String())%span) * time.Second)
		}
	}

	return slot
}

// length is how long a run took: the timeout for a timeout, else a
// value in the pattern's range.
func (b builder) length(h historyDef, job *model.Job, state model.RunState, slot time.Time, n int) time.Duration {
	seed := hash(job.ID, saltDuration, slot.String(), strconv.Itoa(n))

	switch {
	case state == model.RunTimeout:
		return job.Timeout
	case state != model.RunOK && len(h.FailDuration) == rangeLen:
		return spread(h.FailDuration, seed)
	case state != model.RunOK && len(h.Duration) == rangeLen:
		return spread(h.Duration[:1], seed)
	}

	return spread(h.Duration, seed)
}

// fill sets summary, message, output and exit code of a run.
func (b builder) fill(run *model.Run, def jobDef, job *model.Job, fail *failureDef, used map[int]bool) {
	h := def.History
	zone := job.Zone

	lines := h.Output
	if run.State == model.RunOK {
		if c := b.change(h, job, run.Slot, used); c != nil {
			run.Summary, run.Message = c.Summary, c.Message
			if len(c.Output) > 0 {
				lines = c.Output
			}
		}
		if run.Summary == "" && len(h.Summaries) > 0 {
			run.Summary = h.Summaries[hash(job.ID, saltSummary, run.Slot.String())%uint64(len(h.Summaries))]
		}
		zero := 0
		run.Exit = &zero
	}

	if run.State == model.RunFail {
		code := fail.Exit
		if code == 0 {
			code = defaultFailExit
		}
		run.Exit = &code
	}
	if run.State.Broken() {
		lines = h.FailureOutput
	}

	run.Output = render(lines, run, zone)
}

// render fills the placeholders and joins the lines.
func render(lines []string, run *model.Run, zone *time.Location) string {
	var sb strings.Builder

	r := strings.NewReplacer(
		placeStart, run.Started.In(zone).Format(timeLayout),
		placeEnd, run.Ended.In(zone).Format(timeLayout),
		placeDate, run.Slot.In(zone).Format(dateLayout),
	)
	for _, l := range lines {
		sb.WriteString(r.Replace(l))
		sb.WriteString("\n")
	}

	return sb.String()
}

// failure finds the pattern that makes this slot fail, if any.
func (b builder) failure(h historyDef, job *model.Job, slot time.Time) (*failureDef, error) {
	for i := range h.Failures {
		f := &h.Failures[i]

		if f.Day != nil && b.sameDay(slot, *f.Day) {
			return f, nil
		}
		if f.Since == "" {
			continue
		}

		since, err := offset(f.Since, b.now)
		if err != nil {
			return nil, err
		}
		if !slot.Before(since) {
			return f, nil
		}
	}

	return nil, nil
}

// change finds the special pattern of this slot; each applies once.
func (b builder) change(h historyDef, job *model.Job, slot time.Time, used map[int]bool) *changeDef {
	for i := range h.Changes {
		c := &h.Changes[i]
		if used[i] || !b.sameDay(slot, c.Day) {
			continue
		}

		if c.At != "" {
			at, err := time.ParseInLocation(clockLayout, c.At, job.Zone)
			if err != nil {
				continue
			}
			local := slot.In(job.Zone)
			if local.Format(clockLayout) < at.Format(clockLayout) {
				continue
			}
		}

		used[i] = true
		return c
	}

	return nil
}

// sameDay: slot falls on the day the world names as "@date-N", N days
// from today, in UTC (demo/README.md, Values).
func (b builder) sameDay(slot time.Time, day string) bool {
	d, err := dayOffset(day)
	if err != nil {
		return false
	}

	want := b.now.UTC().AddDate(0, 0, d).Format(dateLayout)
	return slot.UTC().Format(dateLayout) == want
}

func hash(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}

	return h.Sum64()
}

// spread picks a duration in [min, max] seconds from a hash; one value
// is a fixed length, no range is zero.
func spread(r []int, h uint64) time.Duration {
	switch len(r) {
	case 0:
		return 0
	case 1:
		return time.Duration(r[0]) * time.Second
	}

	span := uint64(r[1] - r[0] + 1)
	return time.Duration(r[0]+int(h%span)) * time.Second
}
