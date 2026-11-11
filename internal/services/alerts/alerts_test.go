package alerts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"jidoka/internal/drivers/apprise"
	"jidoka/internal/drivers/smtp"
	"jidoka/internal/i18n"
	"jidoka/internal/model"
)

// fakeStore is the in-memory Store.
type fakeStore struct {
	mu     sync.Mutex
	dist   map[string]Disturbance
	flags  map[string]model.Flags
	outbox map[int64]Queued
	nextID int64
	deliv  []model.Delivery
}

func newStore() *fakeStore {
	return &fakeStore{
		dist: map[string]Disturbance{}, flags: map[string]model.Flags{}, outbox: map[int64]Queued{},
	}
}

func (s *fakeStore) Disturbance(job string) (*Disturbance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.dist[job]
	if !ok {
		return nil, nil
	}
	d.Sent = copySent(d.Sent)
	return &d, nil
}

func copySent(m map[int]time.Time) map[int]time.Time {
	c := map[int]time.Time{}
	for k, v := range m {
		c[k] = v
	}
	return c
}

func (s *fakeStore) Disturbances() ([]Disturbance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Disturbance
	for _, d := range s.dist {
		d.Sent = copySent(d.Sent)
		out = append(out, d)
	}
	return out, nil
}

func (s *fakeStore) SaveDisturbance(d Disturbance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d.Sent = copySent(d.Sent)
	s.dist[d.Job] = d
	return nil
}

func (s *fakeStore) CloseDisturbance(job string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.dist, job)
	return nil
}

func (s *fakeStore) Flags(job string) (model.Flags, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flags[job], nil
}

func (s *fakeStore) ResetFlags(job string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flags[job]
	f.AckedAt, f.MuteRecover = nil, false
	s.flags[job] = f
	return nil
}

func (s *fakeStore) OutboxAdd(q Queued) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	q.ID = s.nextID
	s.outbox[q.ID] = q
	return q.ID, nil
}

func (s *fakeStore) OutboxList() ([]Queued, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Queued
	for _, q := range s.outbox {
		out = append(out, q)
	}
	return out, nil
}

func (s *fakeStore) OutboxSave(q Queued) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbox[q.ID] = q
	return nil
}

func (s *fakeStore) OutboxDelete(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.outbox, id)
	return nil
}

func (s *fakeStore) AddDelivery(d model.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliv = append(s.deliv, d)
	return nil
}

type appriseCall struct {
	tag string
	msg apprise.Msg
	key string
}

// harness is an engine with fake clock, fake channels and one Set.
type harness struct {
	t     *testing.T
	store *fakeStore
	db    Store // replaces store in the engine when set
	set   model.Set
	now   time.Time
	eng   *Engine

	appriseErr error
	mailErr    error
	apprises   []appriseCall
	mails      []smtp.Mail
}

var berlin = mustZone("Europe/Berlin")

func mustZone(n string) *time.Location {
	l, err := time.LoadLocation(n)
	if err != nil {
		panic(err)
	}
	return l
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, store: newStore()}
	h.now = time.Date(2026, 11, 11, 12, 0, 0, 0, berlin)
	h.set = model.Set{
		Settings: model.Settings{
			Zone:    berlin,
			Apprise: &model.Apprise{URL: "http://apprise/notify", Tags: map[string]string{"handy": "mobile"}},
			Mail:    &model.Mail{SMTP: "smtp:25", From: "j@x.de", To: []string{"me@x.de"}},
			BaseURL: "https://jidoka.example/",
			Profiles: map[string][]model.Rule{
				"standard": {
					{On: []model.Event{model.EvFail, model.EvTimeout, model.EvMissed, model.EvInvalid},
						Channels: []string{"apprise"}, Tail: -1},
					{On: []model.Event{model.EvRecover}, Channels: []string{"apprise"}, Tail: -1},
				},
			},
		},
		Jobs: map[string]*model.Job{},
	}
	h.eng = h.newEngine()
	return h
}

func (h *harness) newEngine() *Engine {
	var st Store = h.store
	if h.db != nil {
		st = h.db
	}
	return New(Config{
		Store: st,
		Set:   func() model.Set { return h.set },
		Secrets: func(names ...string) (map[string]string, error) {
			return map[string]string{SecretAppriseKey: "key", SecretSMTPPassword: "pw"}, nil
		},
		Now: func() time.Time { return h.now },
		Apprise: func(_ context.Context, _, key, tag string, m apprise.Msg) error {
			if h.appriseErr != nil {
				return h.appriseErr
			}
			h.apprises = append(h.apprises, appriseCall{tag: tag, msg: m, key: key})
			return nil
		},
		Mail: func(_ context.Context, _ model.Mail, _ string, m smtp.Mail) error {
			if h.mailErr != nil {
				return h.mailErr
			}
			h.mails = append(h.mails, m)
			return nil
		},
	})
}

func (h *harness) job(id string, mod func(*model.Job)) *model.Job {
	j := &model.Job{ID: id, Name: "Job " + id, Zone: berlin, Enabled: true}
	if mod != nil {
		mod(j)
	}
	h.set.Jobs[id] = j
	return j
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.eng.Tick(context.Background(), h.now); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) advance(d time.Duration) {
	h.now = h.now.Add(d)
	h.tick()
}

func (h *harness) at(hour, min int) {
	h.now = time.Date(h.now.Year(), h.now.Month(), h.now.Day(), hour, min, 0, 0, berlin)
}

func (h *harness) done(j *model.Job, st model.RunState, slot time.Time, att Attempt, mod func(*model.Run)) {
	h.t.Helper()
	r := model.Run{
		ID: int64(h.now.Unix()), Job: j.ID, State: st, Slot: slot, Attempt: 1,
		Started: h.now.Add(-3 * time.Second), Ended: h.now,
	}
	if mod != nil {
		mod(&r)
	}
	if err := h.eng.RunDone(j, r, model.JobNever, att); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) wantApprise(n int) {
	h.t.Helper()
	if len(h.apprises) != n {
		h.t.Fatalf("apprise messages = %d, want %d: %+v", len(h.apprises), n, h.apprises)
	}
}

func day(n int) time.Time { return time.Date(2026, 11, n, 3, 0, 0, 0, berlin) }

func TestFailSendsOnceThenSilent(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)

	h.done(j, model.RunFail, day(1), AttemptFinal, func(r *model.Run) {
		r.Output, r.Summary = "boom", "kaputt"
	})
	h.tick()
	h.wantApprise(1)

	m := h.apprises[0]
	if m.msg.Type != apprise.TypeFailure || m.key != "key" || m.tag != "" {
		t.Errorf("msg = %+v", m)
	}
	for _, want := range []string{"Job a: fehlgeschlagen", "kaputt", "boom", "Dauer: 3s", "CET", "https://jidoka.example/runs/"} {
		if !strings.Contains(m.msg.Title+m.msg.Body, want) {
			t.Errorf("missing %q in\n%s\n%s", want, m.msg.Title, m.msg.Body)
		}
	}

	// Same disturbance again: no second message (M5).
	h.advance(time.Hour)
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.advance(time.Second)
	h.wantApprise(1)
}

func TestRestartDoesNotResend(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Remind: 24 * time.Hour, Tail: -1}}
	})

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)

	// New engine, same store: the failure repeats, nothing goes out.
	h.eng = h.newEngine()
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.advance(time.Second)
	h.advance(23 * time.Hour)
	h.wantApprise(1)

	// The reminder still comes on time after the restart.
	h.advance(2 * time.Hour)
	h.wantApprise(2)
}

func TestAfterCountsSlotsNotAttempts(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Retries = 2
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, After: 2, Tail: -1}}
	})

	// Day 1: three attempts, the first two are retries.
	h.done(j, model.RunFail, day(1), AttemptRetry, func(r *model.Run) { r.Attempt = 1 })
	h.done(j, model.RunFail, day(1), AttemptRetry, func(r *model.Run) { r.Attempt = 2 })
	h.done(j, model.RunFail, day(1), AttemptFinal, func(r *model.Run) { r.Attempt = 3 })
	h.tick()
	h.wantApprise(0)

	// Same final result reported twice counts once.
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(0)

	// Day 2: the second slot with disturbance.
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)
	if !strings.Contains(h.apprises[0].msg.Body, "2") {
		t.Errorf("body lacks streak: %s", h.apprises[0].msg.Body)
	}
}

func TestRecoverOnlyAfterAnnouncedDisturbance(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{
			{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, After: 2, Tail: -1},
			{On: []model.Event{model.EvRecover}, Channels: []string{"apprise"}, Tail: -1},
		}
	})

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.done(j, model.RunOK, day(2), AttemptFinal, nil)
	h.tick()
	h.wantApprise(0)

	// Announced: recover follows.
	h.done(j, model.RunFail, day(3), AttemptFinal, nil)
	h.done(j, model.RunFail, day(4), AttemptFinal, nil)
	h.done(j, model.RunOK, day(5), AttemptFinal, nil)
	h.tick()
	h.wantApprise(2)
	if h.apprises[1].msg.Type != apprise.TypeSuccess || !strings.Contains(h.apprises[1].msg.Title, "erholt") {
		t.Errorf("recover = %+v", h.apprises[1].msg)
	}
}

func TestReminderTiming(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Remind: 2 * time.Hour, Tail: -1}}
	})

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.advance(119 * time.Minute)
	h.wantApprise(1)

	h.advance(time.Minute)
	h.wantApprise(2)
	if !strings.Contains(h.apprises[1].msg.Title, "Erinnerung") {
		t.Errorf("title = %q", h.apprises[1].msg.Title)
	}

	h.advance(2 * time.Hour)
	h.wantApprise(3)
}

func TestRemindZeroMeansNever(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.advance(30 * 24 * time.Hour)
	h.wantApprise(1)
}

func TestAckStopsReminders(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{
			{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Remind: time.Hour, Tail: -1},
			{On: []model.Event{model.EvRecover}, Channels: []string{"apprise"}, Tail: -1},
		}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()

	ack := h.now
	h.store.flags["a"] = model.Flags{Job: "a", AckedAt: &ack}
	h.advance(5 * time.Hour)
	h.wantApprise(1)

	// Recovery is still announced and ends the acknowledgement.
	h.done(j, model.RunOK, day(2), AttemptFinal, nil)
	h.tick()
	h.wantApprise(2)
	if h.store.flags["a"].AckedAt != nil {
		t.Error("ack not reset on recovery")
	}
}

func TestMuteUntilRecovery(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{
			{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Remind: time.Hour, Tail: -1},
			{On: []model.Event{model.EvRecover}, Channels: []string{"apprise"}, Tail: -1},
		}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)

	h.store.flags["a"] = model.Flags{Job: "a", MuteRecover: true}
	h.advance(3 * time.Hour)
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.advance(time.Second)
	h.wantApprise(1)

	h.done(j, model.RunOK, day(3), AttemptFinal, nil)
	h.tick()
	h.wantApprise(2)
	if h.store.flags["a"].MuteRecover {
		t.Error("mute not reset on recovery")
	}
}

func TestMuteUntilTime(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)
	until := h.now.Add(time.Hour)
	h.store.flags["a"] = model.Flags{Job: "a", MutedUntil: &until}

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(0)

	// After the mute the next failing slot speaks: nothing was announced yet.
	h.advance(2 * time.Hour)
	h.done(j, model.RunFail, day(2), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)
}

func TestDryRunSilent(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)
	h.done(j, model.RunFail, day(1), AttemptFinal, func(r *model.Run) { r.Dry = true })
	h.tick()
	h.wantApprise(0)
	if d, _ := h.store.Disturbance("a"); d != nil {
		t.Error("dry run opened a disturbance")
	}
}

func TestJobRulesReplaceProfile(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Profiles["leise"] = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"mail"}}}
	a := h.job("a", nil)
	b := h.job("b", func(j *model.Job) { j.Profile = "leise" })
	c := h.job("c", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvSuccess}, Channels: []string{"mail"}}}
	})

	for _, j := range []*model.Job{a, b, c} {
		h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	}
	h.tick()

	// a: standard profile → apprise. b: profile leise → mail.
	// c: own rules only listen to success, so the failure is silent.
	h.wantApprise(1)
	if len(h.mails) != 1 || !strings.Contains(h.mails[0].Subject, "Job b") {
		t.Fatalf("mails = %+v", h.mails)
	}
}

func TestQuietHoursMergeToOneMessage(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Quiet = &model.Window{Start: 22 * time.Hour, End: 7 * time.Hour, Zone: berlin}
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{
			{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Tail: -1},
			{On: []model.Event{model.EvRecover}, Channels: []string{"apprise"}, Tail: -1},
		}
	})

	h.at(2, 15)
	h.done(j, model.RunFail, day(11), AttemptFinal, nil)
	h.tick()
	h.at(3, 15)
	h.done(j, model.RunOK, day(11), AttemptFinal, nil)
	h.tick()
	h.at(6, 59)
	h.tick()
	h.wantApprise(0)

	h.at(7, 0)
	h.tick()
	h.wantApprise(1)
	m := h.apprises[0].msg
	if !strings.Contains(m.Body, "gestört 02:15, erholt 03:15") {
		t.Errorf("body:\n%s", m.Body)
	}
	if m.Type != apprise.TypeSuccess {
		t.Errorf("type = %s, want final state success", m.Type)
	}
}

func TestQuietHoursJobOverridesAndUrgent(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Quiet = &model.Window{Start: 22 * time.Hour, End: 7 * time.Hour}
	// Job has its own window that does not cover 02:15.
	a := h.job("a", func(j *model.Job) {
		j.Quiet = &model.Window{Start: 12 * time.Hour, End: 13 * time.Hour, Zone: berlin}
	})
	b := h.job("b", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Urgent: true, Tail: -1}}
	})
	c := h.job("c", nil)

	h.at(2, 15)
	for _, j := range []*model.Job{a, b, c} {
		h.done(j, model.RunFail, day(11), AttemptFinal, nil)
	}
	h.tick()

	// a (own window) and b (urgent) go at once; c waits.
	h.wantApprise(2)
}

// M7, Z6: change and reload are not collected; they go out at once even
// with a digest rule.
func TestChangeAndReloadIgnoreDigest(t *testing.T) {
	h := newHarness(t)
	rules := []model.Rule{
		{On: []model.Event{model.EvChange, model.EvReload}, Channels: []string{"apprise"}, Digest: "daily 08:00", Tail: -1},
	}
	a := h.job("a", func(j *model.Job) { j.Rules = rules })
	h.set.Settings.Profiles["standard"] = rules

	h.at(9, 0)
	h.done(a, model.RunOK, day(11), AttemptFinal, func(r *model.Run) { r.Message = "neu: 2" })
	h.tick()
	h.wantApprise(1)

	if err := h.eng.Reloaded(Reload{Added: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wantApprise(2)
}

func TestDigestCollectsSuccessNotFail(t *testing.T) {
	h := newHarness(t)
	rules := []model.Rule{
		{On: []model.Event{model.EvSuccess, model.EvFail}, Channels: []string{"apprise"}, Digest: "daily 08:00", Tail: -1},
	}
	a := h.job("a", func(j *model.Job) { j.Rules = rules })
	b := h.job("b", func(j *model.Job) { j.Rules = rules })

	h.at(9, 0)
	h.done(a, model.RunOK, day(11), AttemptFinal, func(r *model.Run) { r.Summary = "alles gut" })
	h.done(b, model.RunOK, day(11), AttemptFinal, nil)
	h.done(a, model.RunFail, day(12), AttemptFinal, nil)
	h.tick()

	// Only the failure went out; both successes wait for 08:00 tomorrow.
	h.wantApprise(1)
	if !strings.Contains(h.apprises[0].msg.Title, "fehlgeschlagen") {
		t.Errorf("first = %q", h.apprises[0].msg.Title)
	}

	h.now = h.now.Add(23*time.Hour - time.Minute)
	h.tick()
	h.wantApprise(1)

	h.advance(time.Minute)
	h.wantApprise(2)
	d := h.apprises[1].msg
	if !strings.Contains(d.Title, "Sammelmeldung (2)") || !strings.Contains(d.Body, "Job a: erfolgreich - alles gut") ||
		!strings.Contains(d.Body, "Job b") {
		t.Errorf("digest:\n%s\n%s", d.Title, d.Body)
	}
}

func TestAppriseDownFallsBackToMail(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)
	h.appriseErr = errors.New("connection refused")

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()               // try 1
	h.advance(time.Minute) // try 2
	h.wantApprise(0)
	if len(h.mails) != 0 {
		t.Fatal("mail too early")
	}

	h.advance(4 * time.Minute) // try 3, then mail
	if len(h.mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(h.mails))
	}
	if !strings.Contains(h.mails[0].Subject, "fehlgeschlagen") {
		t.Errorf("subject = %q", h.mails[0].Subject)
	}

	hl := h.eng.Health()
	if !hl[0].Down || hl[0].Channel != ChanApprise || hl[1].Down {
		t.Fatalf("health = %+v", hl)
	}

	// Row done; deliveries: 3 failed apprise, 1 ok mail.
	if n := len(h.store.outbox); n != 0 {
		t.Errorf("outbox = %d", n)
	}
	var failed, ok int
	for _, d := range h.store.deliv {
		switch {
		case d.OK && d.Channel == ChanMail:
			ok++
		case !d.OK && d.Channel == ChanApprise && d.Error != "":
			failed++
		}
	}
	if failed != 3 || ok != 1 {
		t.Errorf("deliveries failed=%d ok=%d: %+v", failed, ok, h.store.deliv)
	}

	// Next message skips the down channel at once.
	h.done(h.set.Jobs["a"], model.RunOK, day(2), AttemptFinal, nil)
	h.tick()
	if len(h.mails) != 2 {
		t.Fatalf("mails = %d, want 2", len(h.mails))
	}

	// After the probe interval apprise is tried once; success brings it up.
	h.appriseErr = nil
	h.advance(probeEvery)
	h.done(h.set.Jobs["a"], model.RunFail, day(3), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)
	if h.eng.Health()[0].Down {
		t.Error("apprise still down after successful probe")
	}
}

func TestOneChannelRuleBothChannelsNoDuplicate(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise", "mail"}, Tail: -1}}
	})
	h.appriseErr = errors.New("down")

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.advance(time.Minute)
	h.advance(4 * time.Minute)

	// Mail went out once (own target); apprise's target is covered by it.
	if len(h.mails) != 1 {
		t.Fatalf("mails = %d", len(h.mails))
	}
	if len(h.store.outbox) != 0 {
		t.Fatalf("outbox not empty: %+v", h.store.outbox)
	}
}

func TestBothDownKeptAndSentLater(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", nil)
	h.appriseErr = errors.New("down")
	h.mailErr = errors.New("down")

	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.advance(time.Minute)
	h.advance(4 * time.Minute) // apprise exhausted, mail try 1
	h.advance(time.Minute)
	h.advance(4 * time.Minute) // mail exhausted, parked

	if len(h.store.outbox) != 1 {
		t.Fatalf("outbox = %d, want 1 kept", len(h.store.outbox))
	}
	hl := h.eng.Health()
	if !hl[0].Down || !hl[1].Down {
		t.Fatalf("health = %+v", hl)
	}

	// Mail comes back; the parked message goes out at the next probe.
	h.mailErr = nil
	h.advance(parkEvery)
	if len(h.mails) != 1 || len(h.store.outbox) != 0 {
		t.Fatalf("mails=%d outbox=%d", len(h.mails), len(h.store.outbox))
	}
}

func TestSuccessfulSendFlushesParked(t *testing.T) {
	h := newHarness(t)
	a := h.job("a", nil)
	b := h.job("b", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvSuccess}, Channels: []string{"mail"}, Tail: -1}}
	})
	h.appriseErr = errors.New("down")
	h.mailErr = errors.New("down")

	h.done(a, model.RunFail, day(1), AttemptFinal, nil)
	for i := 0; i < 6; i++ {
		h.advance(5 * time.Minute)
	}
	if len(h.store.outbox) != 1 {
		t.Fatalf("outbox = %d, want parked 1", len(h.store.outbox))
	}

	// A different message succeeds on mail; the parked one follows at once.
	h.mailErr = nil
	h.done(b, model.RunOK, day(1), AttemptFinal, nil)
	h.advance(time.Second)
	h.advance(time.Second)
	if len(h.mails) != 2 || len(h.store.outbox) != 0 {
		t.Fatalf("mails=%d outbox=%d", len(h.mails), len(h.store.outbox))
	}
}

func TestEventsChangeSlowStartMissedInvalidReload(t *testing.T) {
	h := newHarness(t)
	all := []model.Event{model.EvChange, model.EvSlow, model.EvStart, model.EvMissed, model.EvInvalid, model.EvReload, model.EvSuccess}
	j := h.job("a", func(j *model.Job) {
		j.Slow = time.Second
		j.Rules = []model.Rule{{On: all, Channels: []string{"apprise"}, Tail: -1}}
	})
	h.set.Settings.Profiles["standard"] = []model.Rule{{On: []model.Event{model.EvReload, model.EvInvalid}, Channels: []string{"apprise"}, Tail: -1}}

	var titles []string
	collect := func() {
		h.tick()
		titles = nil
		for _, c := range h.apprises {
			titles = append(titles, c.msg.Title+"|"+string(c.msg.Type))
		}
	}

	if err := h.eng.Started(j, model.Run{ID: 1, Started: h.now, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	collect()
	if len(titles) != 1 || !strings.Contains(titles[0], "gestartet|info") {
		t.Fatalf("start: %v", titles)
	}

	// Retry attempt does not announce a start; dry neither.
	_ = h.eng.Started(j, model.Run{ID: 2, Started: h.now, Attempt: 2})
	_ = h.eng.Started(j, model.Run{ID: 3, Started: h.now, Attempt: 1, Dry: true})
	collect()
	if len(titles) != 1 {
		t.Fatalf("start extra: %v", titles)
	}

	// ok run with message and slow: change wins over success, slow also fires? One per rule.
	h.done(j, model.RunOK, day(1), AttemptFinal, func(r *model.Run) { r.Message = "neu: 2" })
	collect()
	if len(titles) != 2 || !strings.Contains(titles[1], "Änderung|info") {
		t.Fatalf("change: %v", titles)
	}
	if !strings.Contains(h.apprises[1].msg.Body, "neu: 2") {
		t.Errorf("body lacks message")
	}

	// Slow, no message: success outranks slow for the same rule.
	h.done(j, model.RunOK, day(2), AttemptFinal, nil)
	collect()
	if len(titles) != 3 || !strings.Contains(titles[2], "erfolgreich|success") {
		t.Fatalf("success: %v", titles)
	}

	if err := h.eng.Missed(j, h.now); err != nil {
		t.Fatal(err)
	}
	_ = h.eng.Missed(j, h.now)
	collect()
	if len(titles) != 4 || !strings.Contains(titles[3], "ausgeblieben|warning") {
		t.Fatalf("missed: %v", titles)
	}

	err := h.eng.Invalid(model.LoadError{Job: "x", File: "x/job.toml", Line: 3, Msg: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	collect()
	if len(titles) != 5 || !strings.Contains(titles[4], "ungültig|failure") ||
		!strings.Contains(h.apprises[4].msg.Body, "x/job.toml:3: bad") {
		t.Fatalf("invalid: %v", titles)
	}

	if err := h.eng.Reloaded(Reload{Added: []string{"n"}, Changed: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	collect()
	if len(titles) != 6 || !strings.Contains(h.apprises[5].msg.Body, "+n ~a") {
		t.Fatalf("reload: %v", titles)
	}
}

func TestSlowFiresWhenListened(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Slow = time.Second
		j.Rules = []model.Rule{{On: []model.Event{model.EvSlow}, Channels: []string{"apprise"}, Tail: -1}}
	})
	h.done(j, model.RunOK, day(1), AttemptFinal, nil) // 3 s
	h.tick()
	h.wantApprise(1)
}

func TestInvalidFixedRecovers(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Profiles["standard"][1].On = []model.Event{model.EvRecover}
	_ = h.eng.Invalid(model.LoadError{Job: "x", File: "x/job.toml", Msg: "bad"})
	h.tick()
	h.wantApprise(1)

	_ = h.eng.Reloaded(Reload{Fixed: []string{"x"}})
	h.tick()
	h.wantApprise(2)
	if d, _ := h.store.Disturbance("x"); d != nil {
		t.Error("disturbance still open")
	}
}

// J1: a failed fetch of the jobs repo reports under its own key and the
// next good fetch closes it.
func TestRepoErrorReportsAndRecovers(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Profiles["standard"][1].On = []model.Event{model.EvRecover}
	_ = h.eng.Invalid(model.LoadError{File: model.RepoFile, Msg: "gitea down"})
	h.tick()
	h.wantApprise(1)
	if d, _ := h.store.Disturbance(KeyRepo); d == nil {
		t.Fatal("no disturbance under KeyRepo")
	}

	_ = h.eng.Reloaded(Reload{Fixed: []string{KeyRepo}})
	h.tick()
	h.wantApprise(2)
	if d, _ := h.store.Disturbance(KeyRepo); d != nil {
		t.Error("disturbance still open")
	}
}

func TestTailAndAttach(t *testing.T) {
	h := newHarness(t)
	out := ""
	for i := 1; i <= 30; i++ {
		out += "line" + string(rune('A'+i%26)) + "\n"
	}
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{
			{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, Tail: -1},
			{On: []model.Event{model.EvFail}, Channels: []string{"mail"}, Tail: 0, Attach: true, To: []string{"x@y.de"}},
		}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, func(r *model.Run) { r.Output = out })
	h.tick()

	body := h.apprises[0].msg.Body
	if !strings.Contains(body, "Letzte 20 Zeilen") || strings.Count(body, "line") != 20 {
		t.Errorf("tail:\n%s", body)
	}

	m := h.mails[0]
	if strings.Contains(m.Body, "line") || m.Attach == nil || m.Attach.Text != out || len(m.To) != 1 || m.To[0] != "x@y.de" {
		t.Errorf("mail = %+v", m)
	}
}

func TestSubChannelTagAndEnglish(t *testing.T) {
	h := newHarness(t)
	h.eng = New(Config{
		Store: h.store, Set: func() model.Set { return h.set }, Now: func() time.Time { return h.now },
		Secrets: func(...string) (map[string]string, error) { return map[string]string{SecretAppriseKey: "k"}, nil },
		Apprise: func(_ context.Context, _, _, tag string, m apprise.Msg) error {
			h.apprises = append(h.apprises, appriseCall{tag: tag, msg: m})
			return nil
		},
	})
	h.set.Settings.Locale = "en"
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise:handy"}, Tail: -1}}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)
	if h.apprises[0].tag != "mobile" || !strings.Contains(h.apprises[0].msg.Title, "failed") {
		t.Errorf("call = %+v", h.apprises[0])
	}
}

func TestMissingSecretFailsDelivery(t *testing.T) {
	h := newHarness(t)
	h.eng = New(Config{
		Store: h.store, Set: func() model.Set { return h.set }, Now: func() time.Time { return h.now },
		Secrets: func(...string) (map[string]string, error) { return map[string]string{}, nil },
	})
	j := h.job("a", nil)
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	if len(h.store.deliv) != 1 || h.store.deliv[0].OK || !strings.Contains(h.store.deliv[0].Error, SecretAppriseKey) {
		t.Fatalf("deliveries = %+v", h.store.deliv)
	}
}

func TestTestMessages(t *testing.T) {
	h := newHarness(t)
	if err := h.eng.TestChannel(context.Background(), "mail"); err != nil || len(h.mails) != 1 {
		t.Fatalf("err=%v mails=%d", err, len(h.mails))
	}

	h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise", "mail"}, Tail: -1}}
	})
	if err := h.eng.TestRule(context.Background(), "a", 0); err != nil {
		t.Fatal(err)
	}
	h.wantApprise(1)
	if len(h.mails) != 2 {
		t.Fatalf("mails = %d", len(h.mails))
	}
	if err := h.eng.TestRule(context.Background(), "a", 5); err == nil {
		t.Error("bad rule index accepted")
	}

	// A broken channel reports its error; no fallback for tests.
	h.appriseErr = errors.New("nope")
	if err := h.eng.TestChannel(context.Background(), "apprise"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
	if len(h.store.outbox) != 0 {
		t.Error("test message queued")
	}
}

func TestQuietEnd(t *testing.T) {
	w := &model.Window{Start: 22 * time.Hour, End: 7 * time.Hour, Zone: berlin}
	cases := []struct {
		h, m    int
		in      bool
		endDay  int
		endHour int
	}{
		{2, 15, true, 11, 7}, {23, 0, true, 12, 7}, {7, 0, false, 0, 0}, {21, 59, false, 0, 0}, {22, 0, true, 12, 7},
	}
	for _, c := range cases {
		now := time.Date(2026, 11, 11, c.h, c.m, 0, 0, berlin)
		end, in := quietEnd(w, now, time.UTC)
		if in != c.in || (in && (end.Day() != c.endDay || end.Hour() != c.endHour)) {
			t.Errorf("%02d:%02d in=%v end=%v", c.h, c.m, in, end)
		}
	}

	day := &model.Window{Start: 9 * time.Hour, End: 17 * time.Hour, Zone: berlin}
	if _, in := quietEnd(day, time.Date(2026, 11, 11, 8, 0, 0, 0, berlin), time.UTC); in {
		t.Error("08:00 in 09-17")
	}
	if _, in := quietEnd(day, time.Date(2026, 11, 11, 9, 0, 0, 0, berlin), time.UTC); !in {
		t.Error("09:00 not in 09-17")
	}
	if _, in := quietEnd(nil, time.Now(), time.UTC); in {
		t.Error("nil window")
	}
}

func TestNextDigest(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, berlin) // Wednesday
	cases := []struct {
		spec string
		want time.Time
		ok   bool
	}{
		{"daily 08:00", time.Date(2026, 11, 12, 8, 0, 0, 0, berlin), true},
		{"daily 09:30", time.Date(2026, 11, 11, 9, 30, 0, 0, berlin), true},
		{"weekly Mon 08:00", time.Date(2026, 11, 16, 8, 0, 0, 0, berlin), true},
		{"weekly Wed 08:00", time.Date(2026, 11, 18, 8, 0, 0, 0, berlin), true},
		{"hourly", time.Time{}, false},
		{"daily 25:00", time.Time{}, false},
		{"weekly Xyz 08:00", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := nextDigest(c.spec, now, berlin)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("%q = %v %v, want %v %v", c.spec, got, ok, c.want, c.ok)
		}
	}
}

func TestOKClosesUnannouncedDisturbance(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail}, Channels: []string{"apprise"}, After: 2, Tail: -1}}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.done(j, model.RunOK, day(2), AttemptFinal, nil)
	h.done(j, model.RunFail, day(3), AttemptFinal, nil)
	h.tick()
	h.wantApprise(0)
}

func TestMissedAfterFailReports(t *testing.T) {
	h := newHarness(t)
	j := h.job("a", func(j *model.Job) {
		j.Rules = []model.Rule{{On: []model.Event{model.EvFail, model.EvMissed}, Channels: []string{"apprise"}, After: 2, Tail: -1}}
	})
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(0)

	if err := h.eng.Missed(j, h.now); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wantApprise(1)
	if !strings.Contains(h.apprises[0].msg.Title, "ausgeblieben") {
		t.Errorf("title = %q", h.apprises[0].msg.Title)
	}

	// A repeat of the same kind stays silent.
	_ = h.eng.Missed(j, h.now)
	h.tick()
	h.wantApprise(1)
}

func TestInvalidAfterFailReports(t *testing.T) {
	h := newHarness(t)
	h.set.Settings.Profiles["standard"][0].On = []model.Event{model.EvFail, model.EvInvalid}
	j := h.job("a", nil)
	h.done(j, model.RunFail, day(1), AttemptFinal, nil)
	h.tick()
	h.wantApprise(1)

	_ = h.eng.Invalid(model.LoadError{Job: "a", File: "a/job.toml", Msg: "bad"})
	h.tick()
	h.wantApprise(2)
	if !strings.Contains(h.apprises[1].msg.Title, "ungültig") {
		t.Errorf("title = %q", h.apprises[1].msg.Title)
	}
}

func TestFixedGlobalClosesQuietly(t *testing.T) {
	h := newHarness(t)
	_ = h.eng.Invalid(model.LoadError{File: "jidoka.toml", Msg: "bad"})
	h.tick()
	h.wantApprise(1)

	if err := h.eng.Reloaded(Reload{Fixed: []string{KeyGlobal}}); err != nil {
		t.Fatal(err)
	}
	h.tick()
	h.wantApprise(2)
	m := h.apprises[1].msg
	if !strings.Contains(m.Title, "erholt") || strings.Contains(m.Title, "neu geladen") {
		t.Errorf("title = %q", m.Title)
	}
	if d, _ := h.store.Disturbance(KeyGlobal); d != nil {
		t.Error("still open")
	}
}

// TestInvalidFollowsLocale: the reason of an "invalid" message reads in
// the language of jidoka.toml (D1, D2), not in the English of Msg.
func TestInvalidFollowsLocale(t *testing.T) {
	le := model.LoadError{
		Job: "x", File: "x/job.toml", Line: 2, Msg: "invalid schedule: hour: value 25 out of range 0-23",
		Key: "conf.schedule_invalid",
		Params: map[string]any{"detail": i18n.M("cron.range",
			"field", i18n.Key("cron.field.hour"), "value", 25, "min", 0, "max", 23)},
	}

	for _, tc := range []struct{ locale, want string }{
		{"en", "invalid schedule: hour: value 25 out of range 0-23"},
		{"de", "Ungültiger Zeitplan: Stunde: Wert 25 außerhalb von 0-23"},
		{"", "Ungültiger Zeitplan: Stunde: Wert 25 außerhalb von 0-23"},
	} {
		h := newHarness(t)
		h.set.Settings.Locale = tc.locale
		h.set.Settings.Profiles["standard"] = []model.Rule{{On: []model.Event{model.EvInvalid}, Channels: []string{"apprise"}, Tail: -1}}
		if err := h.eng.Invalid(le); err != nil {
			t.Fatal(err)
		}
		h.tick()
		h.wantApprise(1)
		if body := h.apprises[0].msg.Body; !strings.Contains(body, tc.want) {
			t.Errorf("locale %q: body %q lacks %q", tc.locale, body, tc.want)
		}
	}
}
