package andon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jidoka/internal/model"
)

const waitFor = 3 * time.Second

func TestNotifyPostsStateChange(t *testing.T) {
	got := make(chan body, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b body
		_ = json.NewDecoder(r.Body).Decode(&b)
		if r.Method != http.MethodPost || r.URL.Path != "/hooks/x/sig" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		got <- b
	}))
	defer srv.Close()

	New(func() string { return srv.URL + "/hooks/x/sig" }).Notify("backup", model.JobFail)

	select {
	case b := <-got:
		if b.Job != "backup" || b.State != model.JobFail || b.Source != "jidoka" {
			t.Errorf("body: %+v", b)
		}
	case <-time.After(waitFor):
		t.Fatal("no call")
	}
}

func TestNotifyWithoutSecretDoesNothing(t *testing.T) {
	n := New(func() string { return "" })
	n.Notify("backup", model.JobFail)

	if len(n.slots) != 0 {
		t.Fatal("a call started without the secret")
	}
}

func TestNotifyDoesNotWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	New(func() string { return srv.URL }).Notify("backup", model.JobFail)
	if time.Since(start) > time.Second {
		t.Fatal("Notify waited for Andon")
	}
}
