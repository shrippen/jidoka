package apprise

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendPostsJSON(t *testing.T) {
	var path, ctype string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ctype = r.URL.Path, r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
	}))
	defer srv.Close()

	err := Send(context.Background(), srv.URL+"/notify/", "k3y", "handy",
		Msg{Title: "T", Body: "B", Type: TypeFailure, Format: FormatMarkdown})
	if err != nil {
		t.Fatal(err)
	}

	if path != "/notify/k3y" || ctype != "application/json" {
		t.Fatalf("path=%q ctype=%q", path, ctype)
	}
	want := map[string]string{"title": "T", "body": "B", "type": "failure", "tag": "handy", "format": "markdown"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestSendDefaultsAndNoTag(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
	}))
	defer srv.Close()

	if err := Send(context.Background(), srv.URL, "k", "", Msg{Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, has := got["tag"]; has || got["type"] != "info" || got["format"] != "text" {
		t.Fatalf("got %v", got)
	}
}

func TestSendErrorHidesKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	url := srv.URL
	err := Send(context.Background(), url, "secretkey", "", Msg{})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}

	srv.Close()
	err = Send(context.Background(), url, "secretkey", "", Msg{})
	if err == nil || strings.Contains(err.Error(), "secretkey") {
		t.Fatalf("key leaked or no error: %v", err)
	}
}
