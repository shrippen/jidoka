// Package beat sends Jidoka's own heartbeat to a push monitor (B7), so a
// dead Jidoka or a dead ploetze shows up outside: Uptime Kuma on eredin
// expects a GET every Settings.Heartbeat and alarms when it stops.
//
//	every Settings.Heartbeat ──GET──► secret HEARTBEAT_URL (Uptime Kuma push)
package beat

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// SecretURL names the secret holding the push URL (Z5).
const SecretURL = "HEARTBEAT_URL"

const sendTimeout = 10 * time.Second

// Sender sends the heartbeat when it is due.
type Sender struct {
	// URL returns the push URL; "" means not configured.
	URL    func() string
	client *http.Client
	last   time.Time
}

// New returns a sender reading its URL through url.
func New(url func() string) *Sender {
	return &Sender{URL: url, client: &http.Client{Timeout: sendTimeout}}
}

// Tick sends one beat when every has passed since the last one. A
// failed send is logged and retried on the next due tick; the monitor
// alarms anyway when beats stop.
func (s *Sender) Tick(ctx context.Context, now time.Time, every time.Duration) {
	if every <= 0 || now.Sub(s.last) < every {
		return
	}

	url := s.URL()
	if url == "" {
		return
	}
	s.last = now

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("heartbeat: bad URL")
		return
	}

	resp, err := s.client.Do(req)
	if err != nil {
		// The URL carries the monitor's token: never log it.
		slog.Warn("heartbeat failed")
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		slog.Warn("heartbeat refused", "status", resp.StatusCode)
	}
}
