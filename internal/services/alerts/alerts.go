// Package alerts is the notification engine (M1-M11).
//
//	runs/watch/jobs ──► RunDone, Started, Missed, Invalid, Reloaded
//	                       │ derive events, apply rules, after/mute/ack
//	                       ▼
//	                    deliver ──► now ────────────► outbox (due = now)
//	                       │    ├─► quiet hours ───► outbox (held, due = end)
//	                       │    └─► digest ────────► outbox (held, due = window)
//	planner, 1/s ──► Tick: reminders, then send due outbox rows
//	                       │ 3 tries, then other channel, then park
//	                       ▼
//	                    drivers/apprise, drivers/smtp, Delivery per try
//
// The event methods only write to the store; only Tick sends. A slow
// Apprise server therefore never blocks a run.
package alerts

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"jidoka/internal/drivers/apprise"
	"jidoka/internal/drivers/smtp"
	"jidoka/internal/model"
)

// Secret names the engine asks for (Z5).
const (
	SecretAppriseKey   = "APPRISE_KEY"
	SecretSMTPPassword = "SMTP_PASSWORD"
)

// Channel kinds, as written in rules. "apprise:handy" is kind apprise, tag key handy.
const (
	ChanApprise = "apprise"
	ChanMail    = "mail"
)

const (
	// KeyGlobal is the disturbance key of a broken jidoka.toml.
	KeyGlobal = "_settings"

	defaultProfile = "standard"
	defaultTail    = 20
	// DefaultLocale is used when Config.Locale is empty or unknown; the
	// settings file has no locale.
	DefaultLocale = "de"
	globalName    = "Jidoka"
)

// Attempt tells whether a run ended its slot (J13).
type Attempt int

const (
	// AttemptFinal: last try of the slot, or a run that needs no retry.
	AttemptFinal Attempt = iota
	// AttemptRetry: failed, another try follows; nothing is reported.
	AttemptRetry
)

// Reload says what a reload changed (job ids).
type Reload struct {
	Added, Changed, Removed []string
	// Fixed lists ids whose load error is gone; their "invalid"
	// disturbance ends with a recover message.
	Fixed []string
}

// AppriseFunc and MailFunc are the drivers; tests replace them.
type (
	AppriseFunc func(ctx context.Context, baseURL, key, tag string, m apprise.Msg) error
	MailFunc    func(ctx context.Context, cfg model.Mail, password string, m smtp.Mail) error
)

// Config wires the engine. Store, Set and Secrets are required.
type Config struct {
	Store Store
	// Set returns the current jobs and settings; called per event and tick.
	Set func() model.Set
	// Secrets returns the named secrets; a missing one is absent or empty.
	Secrets func(names ...string) (map[string]string, error)
	// Now defaults to time.Now.
	Now func() time.Time
	// Locale "de" or "en"; default DefaultLocale.
	Locale string
	// Apprise and Mail default to the drivers.
	Apprise AppriseFunc
	Mail    MailFunc
}

// Engine decides, queues and sends messages. Create with New.
type Engine struct {
	cfg Config

	// mu orders changes to disturbances and to outbox rows that wait.
	mu sync.Mutex
	// tickMu lets one Tick run at a time; it spans network sends.
	tickMu sync.Mutex

	hmu    sync.Mutex
	health map[string]*chanHealth
}

// New builds an engine.
func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Apprise == nil {
		cfg.Apprise = apprise.Send
	}
	if cfg.Mail == nil {
		cfg.Mail = smtp.Send
	}
	if _, ok := texts[cfg.Locale]; !ok {
		cfg.Locale = DefaultLocale
	}
	return &Engine{cfg: cfg, health: map[string]*chanHealth{}}
}

// subject is who a message is about.
type subject struct {
	id   string
	name string
	job  *model.Job // nil: unknown, removed or global
}

func subjectOf(set model.Set, id, name string) subject {
	job := set.Jobs[id]
	if job != nil && job.Name != "" {
		name = job.Name
	}
	if name == "" {
		name = id
	}
	return subject{id: id, name: name, job: job}
}

// rulesOf: the job's own rules replace the profile, never merge (M3, M4).
// Rules == nil means "use the profile"; an empty non-nil slice means none.
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

// Started reports that a run began (M2, S). Only the first attempt of a slot.
func (e *Engine) Started(job *model.Job, run model.Run) error {
	if run.Dry || run.Attempt > 1 {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	set := e.cfg.Set()
	sub := subjectOf(set, job.ID, job.Name)
	it := e.baseItem(set, sub, model.EvStart, run.Started)
	it.RunID = run.ID
	return e.fire(set, sub, it, &run)
}

// RunDone derives events from a finished run (M2):
//
//	fail/timeout  final attempt only; opens or continues the disturbance
//	recover       first ok after a disturbance that was announced
//	change        run.Message set
//	success       every ok
//	slow          ok, but longer than job.Slow
//
// A rule gets at most one message per run, in the order recover, change,
// success, slow. prev is not used: the store knows the disturbance.
// Dry, aborted and skipped runs are silent (J23).
func (e *Engine) RunDone(job *model.Job, run model.Run, prev model.JobState, att Attempt) error {
	_ = prev
	if run.Dry {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	set := e.cfg.Set()
	sub := subjectOf(set, job.ID, job.Name)

	switch {
	case run.State.Broken():
		if att == AttemptRetry {
			return nil
		}
		ev := model.EvFail
		if run.State == model.RunTimeout {
			ev = model.EvTimeout
		}
		return e.disturb(set, sub, ev, &run, "")
	case run.State == model.RunOK:
		return e.okRun(set, sub, run)
	}
	return nil
}

// Missed reports that the watch found no run in its window (W1). Repeated
// calls while the job stays disturbed do nothing; reminders come from Tick.
func (e *Engine) Missed(job *model.Job, since time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	set := e.cfg.Set()
	sub := subjectOf(set, job.ID, job.Name)
	run := model.Run{Ended: since}
	return e.disturb(set, sub, model.EvMissed, &run, "")
}

// Invalid reports a file that did not load (O9).
func (e *Engine) Invalid(le model.LoadError) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	set := e.cfg.Set()
	id, name := le.Job, le.Job
	if id == "" {
		id, name = KeyGlobal, le.File
	}
	sub := subjectOf(set, id, name)

	detail := le.File
	if le.Line > 0 {
		detail += ":" + strconv.Itoa(le.Line)
	}
	detail += ": " + le.Msg

	run := model.Run{Ended: e.cfg.Now()}
	return e.disturb(set, sub, model.EvInvalid, &run, detail)
}

// Reloaded reports a reload (M2): one reload message per rule, and a
// recover message for every file that works again.
func (e *Engine) Reloaded(r Reload) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	set := e.cfg.Set()
	for _, id := range r.Fixed {
		sub := subjectOf(set, id, id)
		if err := e.recoverOf(set, sub, e.cfg.Now(), nil); err != nil {
			return err
		}
	}

	if len(r.Added)+len(r.Changed)+len(r.Removed) == 0 {
		return nil
	}

	sub := subject{name: globalName}
	it := e.baseItem(set, sub, model.EvReload, e.cfg.Now())
	it.Detail = reloadDetail(r)
	return e.fire(set, sub, it, nil)
}

func reloadDetail(r Reload) string {
	var parts []string
	for _, g := range []struct {
		sign string
		ids  []string
	}{{"+", r.Added}, {"~", r.Changed}, {"-", r.Removed}} {
		for _, id := range g.ids {
			parts = append(parts, g.sign+id)
		}
	}
	return strings.Join(parts, " ")
}

// okRun handles an ok run: recover, change, success, slow.
func (e *Engine) okRun(set model.Set, sub subject, run model.Run) error {
	now := e.cfg.Now()

	var evs []model.Event
	recovered, err := e.recoverable(sub.id)
	if err != nil {
		return err
	}
	if recovered {
		evs = append(evs, model.EvRecover)
	}
	if run.Message != "" {
		evs = append(evs, model.EvChange)
	}
	evs = append(evs, model.EvSuccess)
	if sub.job != nil && sub.job.Slow > 0 && run.Duration(now) > sub.job.Slow {
		evs = append(evs, model.EvSlow)
	}

	// Flags are read before they are reset, so mute-until-recovery still
	// decides the recover message.
	flags, err := e.cfg.Store.Flags(sub.id)
	if err != nil {
		return err
	}

	rules := rulesOf(set, sub.job)
	for _, rule := range rules {
		ev, ok := firstOf(rule, evs)
		if !ok {
			continue
		}

		it := e.baseItem(set, sub, ev, endOf(run, now))
		e.fillRun(&it, rule, run, now)
		if e.muted(flags, ev, now) {
			continue
		}
		if err := e.deliver(set, sub, rule, it, now); err != nil {
			return err
		}
	}

	if !recovered {
		return nil
	}
	if err := e.cfg.Store.CloseDisturbance(sub.id); err != nil {
		return err
	}
	return e.cfg.Store.ResetFlags(sub.id)
}

// recoverable: a disturbance is open and at least one rule announced it.
// An unannounced one (after not reached, muted) is closed silently.
func (e *Engine) recoverable(id string) (bool, error) {
	d, err := e.cfg.Store.Disturbance(id)
	if err != nil || d == nil {
		return false, err
	}
	return len(d.Sent) > 0, nil
}

// recoverOf closes a disturbance and sends recover (invalid file fixed).
func (e *Engine) recoverOf(set model.Set, sub subject, now time.Time, run *model.Run) error {
	d, err := e.cfg.Store.Disturbance(sub.id)
	if err != nil || d == nil {
		return err
	}

	if len(d.Sent) > 0 {
		it := e.baseItem(set, sub, model.EvRecover, now)
		if err := e.fire(set, sub, it, run); err != nil {
			return err
		}
	}
	if err := e.cfg.Store.CloseDisturbance(sub.id); err != nil {
		return err
	}
	return e.cfg.Store.ResetFlags(sub.id)
}

// firstOf picks the first event, in the given order, the rule listens to.
func firstOf(r model.Rule, evs []model.Event) (model.Event, bool) {
	for _, ev := range evs {
		if hasEvent(r, ev) {
			return ev, true
		}
	}
	return "", false
}

func endOf(run model.Run, now time.Time) time.Time {
	if !run.Ended.IsZero() {
		return run.Ended
	}
	return now
}

// fire sends a non-disturbance event to every rule that listens to it.
func (e *Engine) fire(set model.Set, sub subject, it item, run *model.Run) error {
	flags, err := e.flagsOf(sub.id)
	if err != nil {
		return err
	}
	now := e.cfg.Now()
	if e.muted(flags, it.Event, now) {
		return nil
	}

	for _, rule := range rulesOf(set, sub.job) {
		if !hasEvent(rule, it.Event) {
			continue
		}

		ri := it
		if run != nil {
			e.fillRun(&ri, rule, *run, now)
		}
		if err := e.deliver(set, sub, rule, ri, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) flagsOf(id string) (model.Flags, error) {
	if id == "" || id == KeyGlobal {
		return model.Flags{}, nil
	}
	return e.cfg.Store.Flags(id)
}

// muted: MutedUntil silences everything of the job until then. MuteRecover
// silences everything but the recover message, which ends the mute.
func (e *Engine) muted(f model.Flags, ev model.Event, now time.Time) bool {
	if f.MutedUntil != nil && now.Before(*f.MutedUntil) {
		return true
	}
	return f.MuteRecover && ev != model.EvRecover
}

// disturb opens or continues a disturbance and queues the messages that are
// due: the first one when the streak reaches the rule's after, none if the
// rule has spoken already. Reminders are Tick's job.
func (e *Engine) disturb(set model.Set, sub subject, ev model.Event, run *model.Run, detail string) error {
	now := e.cfg.Now()
	d, err := e.cfg.Store.Disturbance(sub.id)
	if err != nil {
		return err
	}

	// Missed and invalid are one slot long; a repeat adds nothing.
	oneShot := ev == model.EvMissed || ev == model.EvInvalid
	if d != nil && oneShot {
		return nil
	}

	if d == nil {
		d = &Disturbance{Job: sub.id, Since: endOf(*run, now), Sent: map[int]time.Time{}}
	}
	d.Event, d.Name, d.Detail = ev, sub.name, detail
	if run.ID != 0 {
		d.RunID = run.ID
	}

	// A slot counts once, however often its final result is reported.
	sameSlot := !run.Slot.IsZero() && run.Slot.Equal(d.LastSlot)
	if !sameSlot {
		d.Streak++
		d.LastSlot = run.Slot
	}

	flags, err := e.flagsOf(sub.id)
	if err != nil {
		return err
	}

	if !e.muted(flags, ev, now) {
		if err := e.firstMessages(set, sub, d, run, now); err != nil {
			return err
		}
	}
	return e.cfg.Store.SaveDisturbance(*d)
}

func (e *Engine) firstMessages(set model.Set, sub subject, d *Disturbance, run *model.Run, now time.Time) error {
	for i, rule := range rulesOf(set, sub.job) {
		if !hasEvent(rule, d.Event) {
			continue
		}
		if _, spoke := d.Sent[i]; spoke {
			continue
		}
		if d.Streak < threshold(rule, d.Event) {
			continue
		}

		it := e.baseItem(set, sub, d.Event, endOf(*run, now))
		it.Detail, it.Streak, it.Since = d.Detail, d.Streak, d.Since
		e.fillRun(&it, rule, *run, now)
		if err := e.deliver(set, sub, rule, it, now); err != nil {
			return err
		}
		d.Sent[i] = now
	}
	return nil
}

// threshold: after counts slots, so it only holds back fail and timeout.
func threshold(r model.Rule, ev model.Event) int {
	if ev != model.EvFail && ev != model.EvTimeout {
		return 1
	}
	return max(r.After, 1)
}

// Remind queues reminders for open disturbances. Tick calls it.
func (e *Engine) remind(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	ds, err := e.cfg.Store.Disturbances()
	if err != nil {
		return err
	}

	set := e.cfg.Set()
	for _, d := range ds {
		if err := e.remindOne(set, d, now); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) remindOne(set model.Set, d Disturbance, now time.Time) error {
	flags, err := e.flagsOf(d.Job)
	if err != nil {
		return err
	}

	// Acknowledged: no reminders until recovery (M10).
	if flags.AckedAt != nil || e.muted(flags, d.Event, now) {
		return nil
	}

	sub := subjectOf(set, d.Job, d.Name)
	changed := false
	for i, rule := range rulesOf(set, sub.job) {
		last, spoke := d.Sent[i]
		if !spoke || rule.Remind <= 0 || !hasEvent(rule, d.Event) {
			continue
		}
		if now.Before(last.Add(rule.Remind)) {
			continue
		}

		it := e.baseItem(set, sub, d.Event, now)
		it.Detail, it.Streak, it.Since, it.Reminder = d.Detail, d.Streak, d.Since, true
		it.RunID = d.RunID
		if err := e.deliver(set, sub, rule, it, now); err != nil {
			return err
		}
		d.Sent[i] = now
		changed = true
	}

	if !changed {
		return nil
	}
	return e.cfg.Store.SaveDisturbance(d)
}

// Tick is the planner's second beat: queue reminders, then send what is due.
func (e *Engine) Tick(ctx context.Context, now time.Time) error {
	if err := e.remind(now); err != nil {
		return err
	}
	return e.sendDue(ctx, now)
}
