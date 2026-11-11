package alerts

import (
	"time"

	"jidoka/internal/model"
)

// Disturbance is the open trouble of one job (table alerts). It exists from
// the first final failure until the next ok run. A restart reads it back, so
// nothing is sent twice (M5).
//
// The table needs more than since/streak/first_sent/last_sent: Sent is one
// time per rule (JSON column), plus LastSlot, RunID, Name and Detail.
type Disturbance struct {
	// Job is the key: the job id for run trouble, FileKey(id) for a broken
	// job file, KeyGlobal for jidoka.toml, KeyRepo for the repo fetch.
	Job      string
	Event    model.Event
	Since    time.Time
	Streak   int       // slots in a row with disturbance (J13: retries do not count)
	LastSlot time.Time // slot counted last; the same slot never counts twice
	RunID    int64     // last run that belongs to it, 0 = none
	Name     string    // shown when the job is gone from the Set
	Detail   string    // invalid: file, line, message

	// Sent holds, per rule index, when the rule last queued a message
	// (first message or reminder). A missing entry = rule has not spoken.
	Sent map[int]time.Time
}

// Queued is one row of the outbox: a message that waits (quiet hours,
// digest), is being retried, or is parked because both channels are down.
type Queued struct {
	ID        int64
	Created   time.Time
	Due       time.Time
	Job       string
	RunID     *int64
	Event     model.Event
	Channel   string // targets, comma separated, for display
	Digest    string // "" = ready to send; else the key of the batch it waits in
	Payload   string // JSON, opaque to the store
	Attempts  int
	LastError string
}

// Store is everything the engine persists. All methods must be safe for
// concurrent use. A missing record is (nil/zero, nil), not an error.
//
// Flags are read per call, never cached: the UI sets them (M10) and they must
// act at once.
type Store interface {
	// Disturbance returns the open disturbance of a job, nil if none.
	Disturbance(job string) (*Disturbance, error)
	// Disturbances lists all open ones (reminders).
	Disturbances() ([]Disturbance, error)
	// SaveDisturbance inserts or replaces by Job.
	SaveDisturbance(d Disturbance) error
	// CloseDisturbance deletes it; the job has recovered.
	CloseDisturbance(job string) error

	// Flags returns acknowledge/mute state; zero Flags if none.
	Flags(job string) (model.Flags, error)
	// ResetFlags ends what lasts "until recovery": clears AckedAt and
	// MuteRecover. MutedUntil and the pause stay.
	ResetFlags(job string) error

	// OutboxAdd inserts and returns the new id.
	OutboxAdd(q Queued) (int64, error)
	// OutboxList returns every row, any order.
	OutboxList() ([]Queued, error)
	// OutboxSave replaces the row with q.ID.
	OutboxSave(q Queued) error
	// OutboxDelete removes a row that is done.
	OutboxDelete(id int64) error

	// AddDelivery records one try on one channel (every try, O4, O8).
	AddDelivery(d model.Delivery) error
}
