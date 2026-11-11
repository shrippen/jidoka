package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"jidoka/internal/repos"
)

// profilesForm is a form the editor would send: one profile with one rule.
func profilesForm(kv ...string) url.Values {
	v := url.Values{
		"profile": {"0"}, "p0_name": {"standard"}, "p0_rule": {"0"},
		"p0_r0_on": {"fail", "timeout"}, "p0_r0_channels": {"mail"}, "p0_r0_remind": {"24h"},
	}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}

	return v
}

func TestProfilesPage(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	w := s.get(profilesPath)
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, body)
	}
	for _, want := range []string{
		`name="csrf"`, `name="p0_name" value="standard"`, `name="p0_r0_on" value="fail" checked`,
		`name="p0_r0_on" value="success"`, `name="p0_r0_channels" value="mail" checked`,
		`name="p0_r0_remind" value="24h"`, `value="add_profile"`, `value="add_rule:0"`, `value="del_rule:0:0"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, `value="del_profile:0"`) {
		t.Error("standard offers a delete button")
	}

	// The settings page links to the editor.
	if !strings.Contains(s.get("/settings").Body.String(), `href="`+profilesPath+`"`) {
		t.Error("settings page does not link the editor")
	}
}

// A valid form is saved and the browser is sent back with a notice.
func TestProfilesSave(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	v := profilesForm("p0_r0_after", "2")
	v.Add("profile", "1")
	v.Set("p1_name", "leise")
	v.Set("p1_rule", "0")
	v.Set("p1_r0_on", "success")
	v.Set("p1_r0_channels", "mail")
	v.Set("p1_r0_digest", "daily 08:00")
	v.Set("p1_r0_urgent", "1")

	w := s.post(profilesPath, v, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != profilesPath+"?saved=1" {
		t.Fatalf("%d %s %s", w.Code, w.Header().Get("Location"), w.Body)
	}

	doc, _, _ := repos.GetSettings(b.db)
	if !strings.Contains(doc, "profiles.leise") || !strings.Contains(doc, "after = 2") || !strings.Contains(doc, "urgent = true") {
		t.Errorf("stored: %s", doc)
	}

	body := s.get(profilesPath + "?saved=1").Body.String()
	for _, want := range []string{"callout-ok", `value="leise"`, `value="daily 08:00"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestProfilesNeedCSRF(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	v := profilesForm()
	v.Set("csrf", "wrong")
	if w := s.post(profilesPath, v, nil); w.Code != http.StatusForbidden {
		t.Errorf("code %d", w.Code)
	}
	if doc, _, _ := repos.GetSettings(b.db); doc != "" {
		t.Errorf("stored: %s", doc)
	}
}

// Errors answer 400 with the form as typed, the error at the field and
// no new rows; nothing is stored.
func TestProfilesError(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	v := profilesForm("p0_r0_after", "many", "p0_r0_remind", "soon")
	w := s.post(profilesPath, v, nil)
	body := w.Body.String()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
	for _, want := range []string{`value="many"`, `value="soon"`, `aria-invalid="true"`, "is-error", `name="p0_r0_on" value="timeout" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(body, `name="p0_name"`); n != 1 {
		t.Errorf("profile rows: %d", n)
	}
	if strings.Contains(body, `name="p0_r1_on"`) || strings.Contains(body, `name="p1_name"`) {
		t.Error("the form grew a row")
	}
	if doc, _, _ := repos.GetSettings(b.db); doc != "" {
		t.Errorf("stored: %s", doc)
	}
}

// The edit buttons answer with the changed form and store nothing.
func TestProfilesEditButtons(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	tests := []struct {
		act  string
		want string
		not  string
	}{
		{"add_rule:0", `name="p0_r1_on"`, ""},
		{"add_profile", `name="p1_name"`, ""},
		{"del_rule:0:0", `value="add_rule:0"`, `name="p0_r0_on" value="fail"`},
	}
	for _, tc := range tests {
		v := profilesForm("act", tc.act)
		w := s.post(profilesPath, v, nil)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, tc.want) {
			t.Errorf("%s: %d, lacks %q", tc.act, w.Code, tc.want)
		}
		if tc.not != "" && strings.Contains(body, tc.not) {
			t.Errorf("%s: still has %q", tc.act, tc.not)
		}
		if doc, _, _ := repos.GetSettings(b.db); doc != "" {
			t.Errorf("%s: stored %s", tc.act, doc)
		}
	}

	// Deleting a profile drops its row.
	v := profilesForm("act", "del_profile:1")
	v.Add("profile", "1")
	v.Set("p1_name", "weg")
	v.Set("p1_rule", "0")
	body := s.post(profilesPath, v, nil).Body.String()
	if strings.Contains(body, `value="weg"`) || !strings.Contains(body, `value="standard"`) {
		t.Error("del_profile:1 kept the wrong row")
	}
}

// A profile a job names is refused with the job's name.
func TestProfilesRefuseUsed(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	v := profilesForm()
	v.Add("profile", "1")
	v.Set("p1_name", "leise")
	v.Set("p1_rule", "0")
	v.Set("p1_r0_on", "fail")
	v.Set("p1_r0_channels", "mail")
	if w := s.post(profilesPath, v, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	b.job("pinger", "echo hi\n", "notify_profile = \"leise\"\n")
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}

	w := s.post(profilesPath, profilesForm(), nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "pinger") {
		t.Errorf("%d: %s", w.Code, w.Body)
	}
	if doc, _, _ := repos.GetSettings(b.db); !strings.Contains(doc, "leise") {
		t.Errorf("profile deleted: %s", doc)
	}
}
