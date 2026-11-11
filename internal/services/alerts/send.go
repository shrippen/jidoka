package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"jidoka/internal/drivers/apprise"
	"jidoka/internal/drivers/smtp"
	"jidoka/internal/model"
)

// Delivery policy (M9).
const (
	// maxTries per channel: tries at 0, +1 min, +5 min.
	maxTries = 3
	// probeEvery: how often a down channel gets one test try.
	probeEvery = 5 * time.Minute
	// parkEvery: how often a message that no channel takes is tried again.
	parkEvery = 15 * time.Minute
)

// retryGap is the wait after the n-th failed try (index n-1).
var retryGap = []time.Duration{time.Minute, 4 * time.Minute}

// ChannelHealth is what the UI shows at the top (M9, O8).
type ChannelHealth struct {
	Channel   string // ChanApprise or ChanMail
	Down      bool
	Since     time.Time // when it went down
	LastError string
	LastOK    time.Time
}

type chanHealth struct {
	ChannelHealth
	nextProbe time.Time
}

// Health lists both channels. State is kept in memory: after a restart a
// channel counts as up until it fails again.
func (e *Engine) Health() []ChannelHealth {
	e.hmu.Lock()
	defer e.hmu.Unlock()

	var out []ChannelHealth
	for _, k := range []string{ChanApprise, ChanMail} {
		out = append(out, e.healthOf(k).ChannelHealth)
	}
	return out
}

// healthOf needs e.hmu.
func (e *Engine) healthOf(kind string) *chanHealth {
	h := e.health[kind]
	if h == nil {
		h = &chanHealth{ChannelHealth: ChannelHealth{Channel: kind}}
		e.health[kind] = h
	}
	return h
}

type gate int

const (
	gateOpen  gate = iota // channel is up: normal tries
	gateProbe             // down, probe is due: one try
	gateShut              // down, not yet: skip
)

func (e *Engine) gateOf(kind string, now time.Time) gate {
	e.hmu.Lock()
	defer e.hmu.Unlock()

	h := e.healthOf(kind)
	switch {
	case !h.Down:
		return gateOpen
	case !now.Before(h.nextProbe):
		return gateProbe
	}
	return gateShut
}

func (e *Engine) markUp(kind string, now time.Time) {
	e.hmu.Lock()
	defer e.hmu.Unlock()

	h := e.healthOf(kind)
	h.Down, h.LastError, h.LastOK = false, "", now
}

func (e *Engine) markDown(kind string, now time.Time, err error) {
	e.hmu.Lock()
	defer e.hmu.Unlock()

	h := e.healthOf(kind)
	if !h.Down {
		h.Since = now
	}
	h.Down, h.LastError, h.nextProbe = true, err.Error(), now.Add(probeEvery)
}

func kindOf(name string) string {
	k, _, _ := strings.Cut(name, ":")
	return k
}

func otherKind(kind string) string {
	if kind == ChanMail {
		return ChanApprise
	}
	return ChanMail
}

func configured(set model.Set, kind string) bool {
	if kind == ChanMail {
		return set.Settings.Mail != nil
	}
	return set.Settings.Apprise != nil
}

// sendDue turns waiting rows that are due into ready rows, then sends every
// ready row that is due. One failing row never stops the others.
func (e *Engine) sendDue(ctx context.Context, now time.Time) error {
	e.tickMu.Lock()
	defer e.tickMu.Unlock()

	ready, err := e.release(now)
	if err != nil {
		return err
	}

	set := e.cfg.Set()
	flush := false
	var first error
	for _, q := range ready {
		sent, err := e.process(ctx, now, set, q)
		flush = flush || sent
		if err != nil && first == nil {
			first = err
		}
	}

	if flush {
		if err := e.unpark(now); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// release lists the due rows; held rows that are due become ready first,
// under e.mu so no item is added to a row that is about to go out.
func (e *Engine) release(now time.Time) ([]Queued, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rows, err := e.cfg.Store.OutboxList()
	if err != nil {
		return nil, err
	}

	var ready []Queued
	for _, q := range rows {
		if q.Due.After(now) {
			continue
		}
		if q.Digest != "" {
			q.Digest = ""
			if err := e.cfg.Store.OutboxSave(q); err != nil {
				return nil, err
			}
		}
		ready = append(ready, q)
	}

	sort.Slice(ready, func(i, j int) bool {
		if !ready[i].Due.Equal(ready[j].Due) {
			return ready[i].Due.Before(ready[j].Due)
		}
		return ready[i].ID < ready[j].ID
	})
	return ready, nil
}

// process tries every open target of one row and saves or deletes it.
// sent reports that something went out.
func (e *Engine) process(ctx context.Context, now time.Time, set model.Set, q Queued) (bool, error) {
	var p payload
	if err := json.Unmarshal([]byte(q.Payload), &p); err != nil {
		return false, err
	}

	sent := false
	for i := range p.Targets {
		t := &p.Targets[i]
		if t.Done || t.Next.After(now) {
			continue
		}
		ok, err := e.step(ctx, now, set, &q, &p, t)
		if err != nil {
			return sent, err
		}
		sent = sent || ok
	}

	return sent, e.settle(q, p)
}

// settle deletes a finished row, else stores it due at the earliest target.
func (e *Engine) settle(q Queued, p payload) error {
	var due time.Time
	tries := 0
	for _, t := range p.Targets {
		tries = max(tries, t.Tries)
		if t.Done {
			continue
		}
		if due.IsZero() || t.Next.Before(due) {
			due = t.Next
		}
	}

	if due.IsZero() {
		return e.cfg.Store.OutboxDelete(q.ID)
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	q.Payload, q.Due, q.Attempts = string(raw), due, tries
	return e.cfg.Store.OutboxSave(q)
}

// step moves one target forward:
//
//	own channel: up to 3 tries (now, +1 min, +5 min); a down channel gets one
//	             probe, or is skipped until the probe is due
//	other channel: same, unless a sibling target already uses it
//	nobody takes it: park for 15 min, then start again
//
// It reports whether a message went out.
func (e *Engine) step(ctx context.Context, now time.Time, set model.Set, q *Queued, p *payload, t *target) (bool, error) {
	for {
		name, ok := e.channelFor(set, p, t)
		if !ok {
			if t.Done {
				return false, nil
			}
			park(t, now)
			return false, nil
		}

		kind := kindOf(name)
		g := e.gateOf(kind, now)
		if g == gateShut {
			advance(t)
			continue
		}

		err := e.attempt(ctx, now, set, q, *p, name)
		if err == nil {
			e.markUp(kind, now)
			t.Done = true
			return true, nil
		}

		t.Tries++
		q.LastError = err.Error()

		// A probe is one try; a failure sends the message on its way.
		if g == gateProbe {
			e.markDown(kind, now, err)
			advance(t)
			continue
		}
		if t.Tries < maxTries {
			t.Next = now.Add(retryGap[t.Tries-1])
			return false, nil
		}

		e.markDown(kind, now, err)
		advance(t)
	}
}

// advance leaves the current channel: own → other, other → parked.
func advance(t *target) {
	t.Tries = 0
	t.Phase++
}

func park(t *target, now time.Time) {
	t.Phase, t.Tries, t.Next = phaseOwn, 0, now.Add(parkEvery)
}

// channelFor names the channel to use now. ok is false when there is none:
// the other channel is not configured, or a sibling target covers it (then
// t is marked done), or both were tried.
func (e *Engine) channelFor(set model.Set, p *payload, t *target) (string, bool) {
	if t.Phase == phaseOwn {
		return t.Name, true
	}
	if t.Phase > phaseOther {
		return "", false
	}

	alt := otherKind(kindOf(t.Name))
	if !configured(set, alt) {
		return "", false
	}

	for _, s := range p.Targets {
		if s.Name != t.Name && kindOf(s.Name) == alt {
			t.Done = true
			return "", false
		}
	}
	return alt, true
}

// attempt sends one message over one channel and records the Delivery.
func (e *Engine) attempt(ctx context.Context, now time.Time, set model.Set, q *Queued, p payload, name string) error {
	err := e.send(ctx, set, name, p)

	d := model.Delivery{
		Job: q.Job, RunID: q.RunID, Event: q.Event,
		Channel: name, At: now, OK: err == nil,
	}
	if err != nil {
		d.Error = err.Error()
	}

	// A lost log line must not turn a sent message into a failed one.
	_ = e.cfg.Store.AddDelivery(d)
	return err
}

// send renders and sends. name is "mail", "apprise" or "apprise:<sub>".
func (e *Engine) send(ctx context.Context, set model.Set, name string, p payload) error {
	r := e.render(set, p)
	kind, sub, _ := strings.Cut(name, ":")

	if kind == ChanMail {
		return e.sendMail(ctx, set, p, r)
	}
	return e.sendApprise(ctx, set, sub, r)
}

func (e *Engine) sendApprise(ctx context.Context, set model.Set, sub string, r rendered) error {
	cfg := set.Settings.Apprise
	if cfg == nil {
		return errors.New("apprise: not configured")
	}

	tag := ""
	if sub != "" {
		t, ok := cfg.Tags[sub]
		if !ok {
			return fmt.Errorf("apprise: unknown sub channel %q", sub)
		}
		tag = t
	}

	sec, err := e.cfg.Secrets(SecretAppriseKey)
	if err != nil {
		return err
	}
	key := sec[SecretAppriseKey]
	if key == "" {
		return errors.New("apprise: secret " + SecretAppriseKey + " missing")
	}

	return e.cfg.Apprise(ctx, cfg.URL, key, tag, apprise.Msg{
		Title: r.Title, Body: r.Body, Type: r.Type, Format: apprise.FormatText,
	})
}

func (e *Engine) sendMail(ctx context.Context, set model.Set, p payload, r rendered) error {
	cfg := set.Settings.Mail
	if cfg == nil {
		return errors.New("mail: not configured")
	}

	to := p.To
	if len(to) == 0 {
		to = cfg.To
	}

	// The password is optional (a relay without login), the user decides.
	pw := ""
	if cfg.User != "" {
		sec, err := e.cfg.Secrets(SecretSMTPPassword)
		if err != nil {
			return err
		}
		pw = sec[SecretSMTPPassword]
		if pw == "" {
			return errors.New("mail: secret " + SecretSMTPPassword + " missing")
		}
	}

	m := smtp.Mail{To: to, Subject: r.Title, Body: r.Body}
	if r.Attach != nil {
		m.Attach = &smtp.Attachment{Name: attachName(*r.Attach), Text: r.Attach.Output}
	}
	return e.cfg.Mail(ctx, *cfg, pw, m)
}

func attachName(it item) string {
	if it.RunID == 0 {
		return it.Job + ".txt"
	}
	return fmt.Sprintf("%s-%d.txt", it.Job, it.RunID)
}

// unpark makes every parked target due now. A send that worked proves a
// channel is up, so what waited for one goes out (M9).
func (e *Engine) unpark(now time.Time) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rows, err := e.cfg.Store.OutboxList()
	if err != nil {
		return err
	}

	for _, q := range rows {
		if q.Digest != "" || !q.Due.After(now) {
			continue
		}

		var p payload
		if err := json.Unmarshal([]byte(q.Payload), &p); err != nil {
			return err
		}
		if !wake(&p, now) {
			continue
		}

		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		q.Payload, q.Due = string(raw), now
		if err := e.cfg.Store.OutboxSave(q); err != nil {
			return err
		}
	}
	return nil
}

// wake sets parked targets (waiting for the park interval) due now.
func wake(p *payload, now time.Time) bool {
	woke := false
	for i := range p.Targets {
		t := &p.Targets[i]
		if t.Done || t.Tries != 0 || !t.Next.After(now) {
			continue
		}
		t.Next = now
		woke = true
	}
	return woke
}

// TestChannel sends a test message over one channel, at once, without
// retries or fallback, and returns the error as it is (M11).
func (e *Engine) TestChannel(ctx context.Context, channel string) error {
	set := e.cfg.Set()
	it := e.baseItem(set, subject{name: globalName}, model.EvTest, e.cfg.Now())
	it.Summary = texts[localeOf(set)].testBody
	return e.testSend(ctx, set, []string{channel}, payload{Items: []item{it}})
}

// TestRule sends a test message the way rule idx of the job would: its
// channels, recipients and tail (M11). Job "" tests the profile "standard".
func (e *Engine) TestRule(ctx context.Context, jobID string, idx int) error {
	set := e.cfg.Set()
	job := set.Jobs[jobID]
	rules := rulesOf(set, job)
	if idx < 0 || idx >= len(rules) {
		return fmt.Errorf("alerts: no rule %d", idx)
	}

	rule := rules[idx]
	sub := subjectOf(set, jobID, jobID)
	if sub.name == "" {
		sub.name = globalName
	}
	it := e.baseItem(set, sub, model.EvTest, e.cfg.Now())
	it.Summary = texts[localeOf(set)].testBody
	return e.testSend(ctx, set, rule.Channels, payload{Items: []item{it}, To: rule.To})
}

func (e *Engine) testSend(ctx context.Context, set model.Set, channels []string, p payload) error {
	var errs []error
	for _, c := range channels {
		err := e.send(ctx, set, c, p)

		d := model.Delivery{Event: model.EvTest, Channel: c, At: e.cfg.Now(), OK: err == nil}
		if err != nil {
			d.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", c, err))
		}
		if serr := e.cfg.Store.AddDelivery(d); serr != nil {
			errs = append(errs, serr)
		}
	}
	return errors.Join(errs...)
}
