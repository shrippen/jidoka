package alerts

import (
	"encoding/json"
	"strings"
	"time"

	"jidoka/internal/model"
)

// phase tells where a target is on its way: its own channel, or the other.
type phase int

const (
	phaseOwn phase = iota
	phaseOther
)

// target is one channel a message must reach.
type target struct {
	Name  string // as in the rule: "apprise", "apprise:handy", "mail"
	Done  bool
	Phase phase
	Tries int       // failed tries in the current phase
	Next  time.Time // not before
}

// payload is the outbox JSON.
type payload struct {
	Items   []item
	Targets []target
	To      []string
	Attach  bool
	Digest  bool
}

// Keys of rows that wait, in Queued.Digest.
const (
	holdQuiet = "quiet"
	sepKey    = "|"
)

// deliver routes one item of one rule:
//
//	digest-able and rule has digest ──► digest row of the window
//	not urgent and in quiet hours   ──► quiet row of the job, due at the end
//	else                            ──► ready row, due now
//
// Caller holds e.mu.
func (e *Engine) deliver(set model.Set, sub subject, rule model.Rule, it item, now time.Time) error {
	route := routeKey(rule)

	if rule.Digest != "" && digestable(it.Event) {
		if due, ok := nextDigest(rule.Digest, now, zoneOf(set)); ok {
			key := rule.Digest + sepKey + route
			return e.hold(key, "", due, rule, it, now, true)
		}
	}

	if !rule.Urgent {
		if end, in := quietEnd(quietOf(set, sub.job), now, zoneOf(set)); in {
			key := holdQuiet + sepKey + sub.id + sepKey + route
			return e.hold(key, sub.id, end, rule, it, now, false)
		}
	}

	return e.push("", sub.id, now, rule, it, now, false)
}

// digestable: M7, only these may wait for a digest.
func digestable(ev model.Event) bool {
	switch ev {
	case model.EvSuccess, model.EvRecover, model.EvChange, model.EvReload:
		return true
	}
	return false
}

// routeKey identifies where a rule sends to, so messages of the same route
// can share a row.
func routeKey(r model.Rule) string {
	k := strings.Join(r.Channels, ",") + sepKey + strings.Join(r.To, ",")
	if r.Attach {
		k += sepKey + "attach"
	}
	return k
}

// hold adds the item to the waiting row with this key, or starts one.
func (e *Engine) hold(key, job string, due time.Time, rule model.Rule, it item, now time.Time, digest bool) error {
	rows, err := e.cfg.Store.OutboxList()
	if err != nil {
		return err
	}

	for _, q := range rows {
		if q.Digest != key {
			continue
		}

		var p payload
		if err := json.Unmarshal([]byte(q.Payload), &p); err != nil {
			return err
		}
		p.Items = append(p.Items, it)
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		q.Payload, q.Event = string(raw), it.Event
		return e.cfg.Store.OutboxSave(q)
	}

	return e.push(key, job, due, rule, it, now, digest)
}

// push inserts a new row; digestKey "" = ready.
func (e *Engine) push(key, job string, due time.Time, rule model.Rule, it item, now time.Time, digest bool) error {
	p := payload{Items: []item{it}, To: rule.To, Attach: rule.Attach, Digest: digest}
	seen := map[string]bool{}
	var names []string
	for _, c := range rule.Channels {
		if seen[c] {
			continue
		}
		seen[c] = true
		names = append(names, c)
		p.Targets = append(p.Targets, target{Name: c})
	}
	if len(p.Targets) == 0 {
		return nil
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	q := Queued{
		Created: now, Due: due, Job: job, Event: it.Event,
		Channel: strings.Join(names, ","), Digest: key, Payload: string(raw),
	}
	if it.RunID != 0 {
		id := it.RunID
		q.RunID = &id
	}
	_, err = e.cfg.Store.OutboxAdd(q)
	return err
}
