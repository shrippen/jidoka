// Package ops holds the changes the owner makes on the pages that no other
// service owns: acknowledge, mute, reload, cancel, renew a ping token (J9,
// J20, M10, W3). Each one writes an audit entry with the actor (O11).
//
//	web action ──► ops ──► repos (flags, audit)  /  jobs.Reload  /  runs.Cancel  /  watch.Renew
//
// Pause, resume and start live in plan, secrets in secrets; they audit
// themselves.
package ops

import (
	"database/sql"
	"errors"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/repos"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/watch"
)

// Audit actions (O11).
const (
	actionAck    = "ack"
	actionUnmute = "unmute"
	actionMute   = "mute-" // + span
	actionReload = "reload"
	actionCancel = "cancel"
	actionRenew  = "renew-token"

	reloadTarget = "jobs"

	muteHourLen = time.Hour
	muteDayLen  = 24 * time.Hour
)

var (
	// ErrUnknownJob means the id is neither a loaded nor an invalid job.
	ErrUnknownJob = errors.New("ops: unknown job")
	// ErrNotRunning means the run is not running here (anymore).
	ErrNotRunning = errors.New("ops: run is not running")
	// ErrBadSpan means an unknown mute span.
	ErrBadSpan = errors.New("ops: unknown mute span")
)

// MuteSpan says how long a mute lasts (M10).
type MuteSpan string

const (
	MuteHour    MuteSpan = "1h"
	MuteDay     MuteSpan = "1d"
	MuteRecover MuteSpan = "recover" // until the job recovers
)

// ParseSpan reads a form value; false for an unknown one.
func ParseSpan(v string) (MuteSpan, bool) {
	switch span := MuteSpan(v); span {
	case MuteHour, MuteDay, MuteRecover:
		return span, true
	}
	return "", false
}

// Deps are the services ops works through; all are required.
type Deps struct {
	DB    *sql.DB
	Jobs  *jobs.Service
	Runs  *runs.Service
	Watch *watch.Watch
	// Now defaults to time.Now.
	Now func() time.Time
}

// Service is the ops service.
type Service struct {
	d Deps
}

// New returns a Service.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}
}

// known: flags may hang on a loaded job or on one that failed to load
// (an invalid job is acknowledged like any other disturbance).
func (s *Service) known(id string) bool {
	set := s.d.Jobs.Current()
	if _, ok := set.Jobs[id]; ok {
		return true
	}

	for _, e := range set.Invalid {
		if e.Job == id {
			return true
		}
	}
	return false
}

// Ack acknowledges the disturbance of job: no reminders until it
// recovers (M10).
func (s *Service) Ack(id, actor string) error {
	if !s.known(id) {
		return ErrUnknownJob
	}

	now := s.d.Now()
	return db.WithTx(s.d.DB, func(tx *sql.Tx) error {
		if err := repos.SetAcked(tx, id, now); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionAck, id, now)
	})
}

// Mute silences the messages of job for span (M10).
func (s *Service) Mute(id string, span MuteSpan, actor string) error {
	if !s.known(id) {
		return ErrUnknownJob
	}

	now := s.d.Now()
	var until *time.Time
	switch span {
	case MuteHour:
		t := now.Add(muteHourLen)
		until = &t
	case MuteDay:
		t := now.Add(muteDayLen)
		until = &t
	case MuteRecover:
	default:
		return ErrBadSpan
	}

	return db.WithTx(s.d.DB, func(tx *sql.Tx) error {
		if err := repos.SetMuted(tx, id, until, span == MuteRecover); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionMute+string(span), id, now)
	})
}

// Unmute ends a mute.
func (s *Service) Unmute(id, actor string) error {
	if !s.known(id) {
		return ErrUnknownJob
	}

	now := s.d.Now()
	return db.WithTx(s.d.DB, func(tx *sql.Tx) error {
		if err := repos.ClearMuted(tx, id); err != nil {
			return err
		}
		return repos.AddAudit(tx, actor, actionUnmute, id, now)
	})
}

// Reload reads /jobs again (J20) and audits it.
func (s *Service) Reload(actor string) (jobs.Diff, error) {
	diff, err := s.d.Jobs.Reload()
	if err != nil {
		return diff, err
	}

	err = repos.AddAudit(s.d.DB, actor, actionReload, reloadTarget, s.d.Now())
	return diff, err
}

// Cancel aborts a running run (J9). ErrNotRunning when it is not running.
func (s *Service) Cancel(runID int64, actor string) error {
	run, err := repos.GetRun(s.d.DB, runID)
	if errors.Is(err, repos.ErrNotFound) {
		return ErrNotRunning
	}
	if err != nil {
		return err
	}

	if !s.d.Runs.Cancel(runID) {
		return ErrNotRunning
	}
	return repos.AddAudit(s.d.DB, actor, actionCancel, run.Job, s.d.Now())
}

// Renew replaces the ping token of a watch; the old one stops at once (W3).
func (s *Service) Renew(id, actor string) error {
	if _, err := s.d.Watch.Renew(id); err != nil {
		return err
	}
	return repos.AddAudit(s.d.DB, actor, actionRenew, id, s.d.Now())
}
