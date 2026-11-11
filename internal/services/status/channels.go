package status

import (
	"database/sql"
	"sort"
	"strings"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/alerts"
)

// ChanState is what the channels page says about one channel.
type ChanState string

const (
	ChanOff       ChanState = "off"       // not set up
	ChanUnchecked ChanState = "unchecked" // set up, nothing sent or tested yet
	ChanUp        ChanState = "up"        // the latest send or test went through
	ChanDown      ChanState = "down"      // see alerts.ChannelHealth
)

// ChannelRow is one channel kind on the channels page (O8).
type ChannelRow struct {
	// Kind is alerts.ChanApprise or alerts.ChanMail.
	Kind       string
	Configured bool
	// State is derived: reachable is unknown until a send or test succeeded.
	State  ChanState
	Health alerts.ChannelHealth
	// Subs are the Apprise tags usable as "apprise:<tag>"; empty for mail.
	Subs []string
	// Last is the newest message over this kind, nil if none yet.
	Last *model.Delivery
	// OK30 and Failed30 count the tries of the last 30 days.
	OK30, Failed30 int
}

// Profile is a named rule set of jidoka.toml (M4). The index in Rules is
// the one the test-message action takes for the profile "standard".
type Profile struct {
	Name  string
	Rules []model.Rule
}

// OutboxRow is a message that waits (M6, M7, M9). The payload (the message
// text) is left out on purpose.
type OutboxRow struct {
	ID        int64
	Created   time.Time
	Due       time.Time
	Job       string
	RunID     *int64
	Event     model.Event
	Channel   string
	Digest    string
	Attempts  int
	LastError string
}

// ChannelsPage is the channels page (O8).
type ChannelsPage struct {
	Rows     []ChannelRow
	Profiles []Profile
	// Quiet are the global quiet hours; nil if none.
	Quiet *model.Window
	Zone  *time.Location
	// Recent are the newest 50 deliveries, newest first.
	Recent []model.Delivery
	Outbox []OutboxRow
	// SettingsErr: jidoka.toml did not load; the saved copy or the defaults are in use (B8).
	SettingsErr *model.LoadError
	// SettingsText is the catalog key of its banner.
	SettingsText string
}

// Channels returns the channels page.
func (s *Service) Channels() (ChannelsPage, error) {
	set := s.d.Jobs.Current()
	now := s.d.Now()

	var (
		last   map[string]model.Delivery
		counts map[string]repos.DeliveryCount
		outbox []repos.OutboxItem
		p      = ChannelsPage{Rows: []ChannelRow{}, Profiles: []Profile{}, Outbox: []OutboxRow{},
			Quiet: set.Settings.Quiet, Zone: set.Settings.Zone, SettingsErr: set.SettingsErr,
			SettingsText: settingsText(set)}
	)
	err := db.WithRead(s.d.DB, func(tx *sql.Tx) error {
		var err error
		if last, err = repos.LastDeliveries(tx); err != nil {
			return err
		}
		if counts, err = repos.DeliveryCounts(tx, now.AddDate(0, 0, -countWindowDays)); err != nil {
			return err
		}
		if p.Recent, err = repos.RecentDeliveries(tx, recentDeliveries); err != nil {
			return err
		}
		outbox, err = repos.AllOutbox(tx)
		return err
	})
	if err != nil {
		return p, err
	}
	if p.Recent == nil {
		p.Recent = []model.Delivery{}
	}

	var health []alerts.ChannelHealth
	if s.d.Alerts != nil {
		health = s.d.Alerts.Health()
	}
	for _, kind := range []string{alerts.ChanApprise, alerts.ChanMail} {
		p.Rows = append(p.Rows, channelRow(set, kind, health, last, counts))
	}

	for _, name := range sortedKeys(set.Settings.Profiles) {
		p.Profiles = append(p.Profiles, Profile{Name: name, Rules: set.Settings.Profiles[name]})
	}

	for _, o := range outbox {
		p.Outbox = append(p.Outbox, OutboxRow{
			ID: o.ID, Created: o.Created, Due: o.Due, Job: o.Job, RunID: o.RunID, Event: o.Event,
			Channel: o.Channel, Digest: o.Digest, Attempts: o.Attempts, LastError: o.LastError,
		})
	}
	return p, nil
}

// kindOf is the kind of a channel name: "apprise:handy" is apprise.
func kindOf(name string) string {
	kind, _, _ := strings.Cut(name, ":")
	return kind
}

func channelRow(set model.Set, kind string, health []alerts.ChannelHealth,
	last map[string]model.Delivery, counts map[string]repos.DeliveryCount) ChannelRow {
	row := ChannelRow{Kind: kind, Subs: []string{}}
	row.Health.Channel = kind

	for _, h := range health {
		if h.Channel == kind {
			row.Health = h
		}
	}

	if kind == alerts.ChanApprise && set.Settings.Apprise != nil {
		row.Configured = true
		for tag := range set.Settings.Apprise.Tags {
			row.Subs = append(row.Subs, tag)
		}
		sort.Strings(row.Subs)
	}
	if kind == alerts.ChanMail {
		row.Configured = set.Settings.Mail != nil
	}

	for name, d := range last {
		if kindOf(name) != kind || (row.Last != nil && !d.At.After(row.Last.At)) {
			continue
		}
		d := d
		row.Last = &d
	}
	for name, c := range counts {
		if kindOf(name) == kind {
			row.OK30 += c.OK
			row.Failed30 += c.Failed
		}
	}
	row.State = stateOf(row)
	return row
}

// stateOf: a failed try that is not yet "down" proves nothing either, so
// only a successful latest delivery (or an in-memory success) says up.
func stateOf(row ChannelRow) ChanState {
	switch {
	case !row.Configured:
		return ChanOff
	case row.Health.Down:
		return ChanDown
	case row.Last != nil && row.Last.OK, !row.Health.LastOK.IsZero():
		return ChanUp
	}
	return ChanUnchecked
}
