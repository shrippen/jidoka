package status

import (
	"testing"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/services/alerts"
)

// The newest evidence decides: a failed test after an earlier success
// is no "reachable", and a failure known only from the deliveries (after
// a restart) is no "not checked".
func TestChannelState(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	sent := func(at time.Time, ok bool) *model.Delivery { return &model.Delivery{At: at, OK: ok} }

	tests := []struct {
		name string
		row  ChannelRow
		want ChanState
	}{
		{"off", ChannelRow{}, ChanOff},
		{"never tried", ChannelRow{Configured: true}, ChanUnchecked},
		{"sent", ChannelRow{Configured: true, Last: sent(t0, true)}, ChanUp},
		{"success in memory", ChannelRow{Configured: true, Health: alerts.ChannelHealth{LastOK: t0}}, ChanUp},
		{"test failed after success", ChannelRow{Configured: true, Last: sent(t0.Add(time.Minute), false),
			Health: alerts.ChannelHealth{LastOK: t0}}, ChanFailed},
		{"failed before newer success", ChannelRow{Configured: true, Last: sent(t0, false),
			Health: alerts.ChannelHealth{LastOK: t0.Add(time.Minute)}}, ChanUp},
		{"failed after restart", ChannelRow{Configured: true, Last: sent(t0, false)}, ChanFailed},
		{"down", ChannelRow{Configured: true, Last: sent(t0, true), Health: alerts.ChannelHealth{Down: true}}, ChanDown},
	}

	for _, tc := range tests {
		if got := stateOf(tc.row); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
