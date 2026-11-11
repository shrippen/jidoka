// Package apprise sends one message through the Apprise API (M1).
//
//	Send ──► POST {url}/{key}  {"title","body","type","tag","format"}
package apprise

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	sendTimeout = 15 * time.Second
	errBodyMax  = 256
)

// Type is the Apprise notification type; clients show it as colour or icon.
type Type string

const (
	TypeInfo    Type = "info"
	TypeSuccess Type = "success"
	TypeWarning Type = "warning"
	TypeFailure Type = "failure"
)

// Format tells Apprise how to read the body.
type Format string

const (
	FormatText     Format = "text"
	FormatMarkdown Format = "markdown"
)

// Msg is one message. Empty Type means info, empty Format means text.
type Msg struct {
	Title  string
	Body   string
	Type   Type
	Format Format
}

type request struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Type   Type   `json:"type"`
	Tag    string `json:"tag,omitempty"`
	Format Format `json:"format"`
}

// Send posts m to {baseURL}/{key}. tag selects a sub channel ("" = none).
// The key is a secret: it never appears in the returned error.
func Send(ctx context.Context, baseURL, key, tag string, m Msg) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req := request{Title: m.Title, Body: m.Body, Type: m.Type, Tag: tag, Format: m.Format}
	if req.Type == "" {
		req.Type = TypeInfo
	}
	if req.Format == "" {
		req.Format = FormatText
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := strings.TrimRight(baseURL, "/") + "/" + key
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("apprise: bad url")
	}
	hr.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		// url.Error repeats the URL, which holds the key.
		return fmt.Errorf("apprise: %s", strings.ReplaceAll(err.Error(), key, "***"))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyMax))
	return fmt.Errorf("apprise: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
