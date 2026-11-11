// Package model holds Jidoka's domain types. No I/O, no logic beyond
// small helpers; every layer speaks these types.
//
//	/jobs ──conf──► Set{Settings, Jobs, Invalid}
//	                      │
//	          plan ──► Run ──► runs (exec) ──► Run{State, Output}
//	                      │
//	          watch ──► JobState ──► alerts ──► Delivery
package model

import "time"

// Kind tells a scripted job from an external watch (watch.toml).
type Kind string

const (
	KindJob   Kind = "job"
	KindWatch Kind = "watch"
)

// RunState is the outcome of one run (or ping).
type RunState string

const (
	RunRunning RunState = "running"
	RunOK      RunState = "ok"
	RunFail    RunState = "fail"
	RunTimeout RunState = "timeout"
	// RunAborted: container stop or crash; never reported as a failure (J23).
	RunAborted RunState = "aborted"
	// RunSkipped: the slot fell into a running run or waited too long (J5, B4).
	RunSkipped RunState = "skipped"
)

// Done reports whether the run has ended.
func (s RunState) Done() bool { return s != RunRunning }

// Broken reports whether the run counts as a disturbance.
func (s RunState) Broken() bool { return s == RunFail || s == RunTimeout }

// JobState is what the board shows for a job.
type JobState string

const (
	JobNever    JobState = "never"
	JobRunning  JobState = "running"
	JobOK       JobState = "ok"
	JobFail     JobState = "fail"
	JobTimeout  JobState = "timeout"
	JobMissed   JobState = "missed"
	JobPaused   JobState = "paused"
	JobDisabled JobState = "disabled"
	JobInvalid  JobState = "invalid"
)

// Broken reports whether the state needs the owner.
func (s JobState) Broken() bool {
	return s == JobFail || s == JobTimeout || s == JobMissed || s == JobInvalid
}

// Trigger says why a run started; it reaches the script as JIDOKA_TRIGGER.
type Trigger string

const (
	TrigSchedule Trigger = "schedule"
	TrigManual   Trigger = "manual"
	TrigCatchup  Trigger = "catchup"
	TrigRetry    Trigger = "retry"
	TrigDry      Trigger = "dry"
	TrigPing     Trigger = "ping"
)

// Event triggers a notification rule (M2).
type Event string

const (
	EvFail    Event = "fail"
	EvTimeout Event = "timeout"
	EvMissed  Event = "missed"
	EvRecover Event = "recover"
	EvSuccess Event = "success"
	EvInvalid Event = "invalid"
	EvReload  Event = "reload"
	EvChange  Event = "change"
	EvStart   Event = "start"
	EvSlow    Event = "slow"
	// EvTest: a test message from the UI (M11).
	EvTest Event = "test"
)

// Disturbing reports whether the event opens or continues a disturbance.
// Disturbances never go into a digest (M7).
func (e Event) Disturbing() bool {
	return e == EvFail || e == EvTimeout || e == EvMissed || e == EvInvalid
}

// Window is a daily time span in a zone, e.g. quiet hours 22:00-07:00.
// Start > End wraps past midnight.
type Window struct {
	Start, End time.Duration // since midnight
	Zone       *time.Location
}

// Rule is one [[notify]] entry (M3).
type Rule struct {
	On       []Event
	Channels []string // "apprise", "apprise:handy", "mail"
	To       []string // mail recipients, overrides the channel default
	After    int      // from the n-th slot in a row with disturbance; 0 and 1 mean at once
	Remind   time.Duration
	Urgent   bool   // breaks quiet hours
	Tail     int    // last lines of output; -1 = default (20)
	Digest   string // "daily 08:00", "weekly Mon 08:00"; "" = at once
	Attach   bool   // attach_output: whole output as mail attachment
}

// Job is one folder under /jobs, scripted (job.toml) or watched (watch.toml).
type Job struct {
	ID          string // folder name, [a-z0-9-]+
	Kind        Kind
	Name        string
	Description string
	Link        string
	Tags        []string
	Dir         string // absolute folder
	File        string // job.toml or watch.toml, relative to /jobs
	Hash        string // content hash of the folder's files, to tell a change on reload

	Run      string
	Args     []string
	Schedule string // cron, 5 fields; for watches optional
	Zone     *time.Location
	Jitter   time.Duration
	Timeout  time.Duration
	Slow     time.Duration
	Retries  int
	RetryGap time.Duration
	Catchup  bool
	Lock     string
	Expect   time.Duration // W1; 0 = no watch
	Keep     time.Duration // J18; 0 = default
	Quiet    *Window
	Secrets  []string
	Env      map[string]string
	Enabled  bool

	Rules   []Rule // [[notify]]; nil = profile
	Profile string // notify_profile; "" = "standard"

	// Watch only (W2).
	Period time.Duration
	Grace  time.Duration
}

// Apprise is the Apprise API channel (M1).
type Apprise struct {
	URL  string            // e.g. http://apprise:8000/notify; the key is the secret APPRISE_KEY
	Tags map[string]string // sub channel → Apprise tag
}

// Mail is the SMTP channel (M1).
type Mail struct {
	SMTP string // host:port
	User string // password is the secret SMTP_PASSWORD
	From string
	To   []string
}

// Settings is /jobs/jidoka.toml.
type Settings struct {
	Zone        *time.Location
	Quiet       *Window
	Heartbeat   time.Duration // B7; URL is the secret HEARTBEAT_URL
	MaxParallel int           // B4, default 2
	Keep        time.Duration // J12, default 180 days
	KeepRuns    int           // J12, default 500 per job
	Apprise     *Apprise
	Mail        *Mail
	Profiles    map[string][]Rule
	AndonURL    string // I7: link in the header
	BaseURL     string // links in messages
}

// LoadError is a file Jidoka could not load (O9).
type LoadError struct {
	Job  string // "" for jidoka.toml
	File string // relative to /jobs
	Line int    // 0 = unknown
	Msg  string
}

// Set is everything loaded from /jobs at one moment.
type Set struct {
	Settings Settings
	// SettingsErr: jidoka.toml did not load; Settings is the last valid copy (B8).
	SettingsErr *LoadError
	Jobs        map[string]*Job
	Invalid     []LoadError
	Commit      string // from /jobs/.git/HEAD
	LoadedAt    time.Time
}

// Run is one execution of a job, or one ping of a watch.
type Run struct {
	ID       int64
	Job      string
	Trigger  Trigger
	State    RunState
	Slot     time.Time // the scheduled time this run belongs to; retries share it (J13)
	Attempt  int       // 1 = first try
	Started  time.Time
	Ended    time.Time // zero while running
	Exit     *int
	Summary  string // JIDOKA_SUMMARY
	Message  string // JIDOKA_NOTIFY
	Output   string // redacted, capped (J6)
	Error    string // Jidoka's own reason, e.g. missing secret
	Commit   string // jobs commit the run used
	Dry      bool
}

// Duration is the run's length; for a running run up to now.
func (r Run) Duration(now time.Time) time.Duration {
	if r.Ended.IsZero() {
		return now.Sub(r.Started)
	}
	return r.Ended.Sub(r.Started)
}

// Flags are what the UI sets on a job; they survive restarts and reloads (J21).
type Flags struct {
	Job         string
	PausedAt    *time.Time
	PausedBy    string
	AckedAt     *time.Time // M10: no reminders until recovery
	MutedUntil  *time.Time
	MuteRecover bool // muted until the job recovers
}

// Delivery is one message sent (or tried) over one channel.
type Delivery struct {
	ID      int64
	Job     string
	RunID   *int64
	Event   Event
	Channel string
	At      time.Time
	OK      bool
	Error   string
}
