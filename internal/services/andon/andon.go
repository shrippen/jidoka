// Package andon tells Andon about a job's state change (I6), so Andon drops
// its cache and shows the hint at once. Andon does not need it, Jidoka does
// not wait for it (I9).
//
//	Notify(job, state) ──go──► POST secret ANDON_HOOK_URL {"source","job","state"}
//
// The URL is a secret (Z5): it carries Andon's signature and is never logged.
package andon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"jidoka/internal/model"
)

// SecretHookURL names the secret that holds Andon's webhook URL.
const SecretHookURL = "ANDON_HOOK_URL"

const (
	callTimeout = 5 * time.Second
	// maxInFlight bounds the calls in the air: a dead Andon must not pile
	// up goroutines.
	maxInFlight = 8

	source      = "jidoka"
	contentType = "application/json"
)

// Notifier posts to Andon.
type Notifier struct {
	// URL returns the hook URL; "" means not configured.
	url    func() string
	client *http.Client
	slots  chan struct{}
}

// New returns a notifier reading its URL through url.
func New(url func() string) *Notifier {
	return &Notifier{
		url:    url,
		client: &http.Client{Timeout: callTimeout},
		slots:  make(chan struct{}, maxInFlight),
	}
}

// body is what Andon receives.
type body struct {
	Source string         `json:"source"`
	Job    string         `json:"job"`
	State  model.JobState `json:"state"`
}

// Notify reports that job is now in state. It returns at once; the call
// runs in the background and a failure is only logged. Without the secret
// nothing happens.
func (n *Notifier) Notify(jobID string, state model.JobState) {
	url := n.url()
	if url == "" {
		return
	}

	select {
	case n.slots <- struct{}{}:
	default:
		slog.Warn("andon hook: too many calls in flight, dropped")
		return
	}

	go func() {
		defer func() { <-n.slots }()
		n.post(url, body{Source: source, Job: jobID, State: state})
	}()
}

func (n *Notifier) post(url string, b body) {
	raw, err := json.Marshal(b)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		slog.Warn("andon hook: bad URL")
		return
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := n.client.Do(req)
	if err != nil {
		// The error text holds the URL, and the URL is a secret.
		slog.Warn("andon hook failed")
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))

	if resp.StatusCode >= http.StatusBadRequest {
		slog.Warn("andon hook refused", "status", resp.StatusCode)
	}
}
