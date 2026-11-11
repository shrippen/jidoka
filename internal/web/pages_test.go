package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// rawKey matches a catalog key that leaked into a page: "board.reload".
var rawKey = regexp.MustCompile(`>\s*(?:nav|board|job|run|plan|channels|secrets|state|event|trigger|rule|action|audit)\.[a-z_0-9-]+\s*<`)

var formTag = regexp.MustCompile(`(?s)<form\b[^>]*method="post"[^>]*>.*?</form>`)

// seeded is a backend with a good job, a failing one, a watch and a secret.
func seeded(t *testing.T) (*backend, signed) {
	t.Helper()
	b := newBackend(t, "")
	b.job("bad", "echo oops\nexit 1\n", "")
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}
	set := b.jobs.Current()
	b.plan.Apply(set)
	b.watch.Apply(set)

	s := b.signedIn()
	if w := s.post("/secrets", url.Values{"name": {"TOKEN"}, "value": {secretValue}}, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("set secret: %d %s", w.Code, w.Body)
	}

	for _, id := range []string{"hello", "bad"} {
		since := b.lastID(id)
		if err := b.plan.RunNow(id, "test"); err != nil {
			t.Fatal(err)
		}
		b.waitDone(id, since)
	}

	return b, s
}

func TestPagesRender(t *testing.T) {
	b, s := seeded(t)

	pages := map[string][]string{
		"/":              {"hello", "bad", "<svg class=\"ticks\"", "data-tip=\"#"},
		"/board/tiles":   {"id=\"board\""},
		"/plan":          {"<svg class=\"chart\"", "Uhrzeit (", "class=\"legend\"", "00:00", "24:00"},
		"/plan?day=-1":   {"<svg class=\"chart\""},
		"/jobs/hello":    {"viewBox=\"0 0", "chart-axis", "/jobs/hello/run", "/jobs/hello/mute"},
		"/jobs/bad":      {"/jobs/bad/ack"},
		"/jobs/beat":     {"/ping/", "copy-btn", "/jobs/beat/renew-token"},
		"/channels":      {"class=\"status\""},
		"/secrets":       {"type=\"password\"", "autocomplete=\"off\"", "TOKEN"},
		"/runs/" + "%ID": {"codeblock"},
	}

	id := b.lastID("hello")
	for path, want := range pages {
		path = strings.Replace(path, "%ID", itoa(id), 1)
		w := do(b.srv, "GET", path, nil, s.cookies, nil)
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d", path, w.Code)
			continue
		}
		for _, sub := range want {
			if !strings.Contains(body, sub) {
				t.Errorf("%s lacks %q", path, sub)
			}
		}
		if m := rawKey.FindString(body); m != "" {
			t.Errorf("%s shows a raw key: %q", path, m)
		}
		for _, f := range formTag.FindAllString(body, -1) {
			if !strings.Contains(f, `name="csrf"`) {
				t.Errorf("%s: form without csrf: %.80s", path, f)
			}
		}
	}
}

func TestPagesEnglish(t *testing.T) {
	b, s := seeded(t)
	w := do(b.srv, "GET", "/", nil, s.cookies, map[string]string{"Accept-Language": "en"})
	if !strings.Contains(w.Body.String(), "Reload jobs") {
		t.Fatal("board not in English")
	}
}

func TestNoSecretValueOnPages(t *testing.T) {
	b, s := seeded(t)
	for _, path := range []string{"/secrets", "/", "/jobs/hello", "/channels"} {
		if body := do(b.srv, "GET", path, nil, s.cookies, nil).Body.String(); strings.Contains(body, secretValue) {
			t.Errorf("%s shows a secret value", path)
		}
	}
}

func TestTimesCarryZoneMark(t *testing.T) {
	b, s := seeded(t)
	for _, path := range []string{"/", "/jobs/hello", "/secrets"} {
		body := do(b.srv, "GET", path, nil, s.cookies, nil).Body.String()
		if !strings.Contains(body, `class="tz-mark"`) {
			t.Errorf("%s: no zone mark", path)
		}
	}
}

func TestPlanDays(t *testing.T) {
	b, s := seeded(t)
	for _, d := range []string{"-1", "0", "1", "9", "x"} {
		if w := do(b.srv, "GET", "/plan?day="+d, nil, s.cookies, nil); w.Code != http.StatusOK {
			t.Errorf("day %s: %d", d, w.Code)
		}
	}
}

func TestNewPagesNeedLogin(t *testing.T) {
	b := newBackend(t, "")
	for _, path := range []string{"/plan", "/jobs/hello", "/runs/1", "/channels", "/secrets", "/board/tiles"} {
		if w := do(b.srv, "GET", path, nil, nil, nil); w.Code != http.StatusSeeOther {
			t.Errorf("%s without session: %d", path, w.Code)
		}
	}
}

func TestUnknownJobAndRun(t *testing.T) {
	b, s := seeded(t)
	for _, path := range []string{"/jobs/nope", "/runs/99999", "/runs/abc"} {
		if w := do(b.srv, "GET", path, nil, s.cookies, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// TestScheduleAndLoadErrorInWords: a tile says its schedule in words with
// the cron text in the title, and an invalid schedule reads in the
// language of the request (D2, D3).
func TestScheduleAndLoadErrorInWords(t *testing.T) {
	b, s := seeded(t)
	b.put("late/job.toml", "run = \"run.sh\"\nschedule = \"0 25 * * *\"\n")
	b.put("late/run.sh", "#!/bin/sh\n")
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		lang string
		want []string
	}{
		{"de", []string{"Ungültiger Zeitplan: Stunde: Wert 25 außerhalb von 0-23", `title="0 6 * * *">täglich um 06:00<`}},
		{"en", []string{"invalid schedule: hour: value 25 out of range 0-23", `title="0 6 * * *">daily at 06:00<`}},
	}

	for _, tc := range tests {
		for _, path := range []string{"/", "/jobs/hello"} {
			w := do(b.srv, "GET", path, nil, s.cookies, map[string]string{"Accept-Language": tc.lang})
			body := w.Body.String()
			for _, sub := range tc.want {
				if path == "/jobs/hello" && strings.Contains(sub, "Zeitplan:") || path == "/jobs/hello" && strings.Contains(sub, "invalid schedule") {
					continue
				}
				if !strings.Contains(body, sub) {
					t.Errorf("%s (%s) lacks %q", path, tc.lang, sub)
				}
			}
		}
	}

	// The job page of the broken job shows the error too.
	w := do(b.srv, "GET", "/jobs/late", nil, s.cookies, nil)
	if !strings.Contains(w.Body.String(), "Ungültiger Zeitplan: Stunde") {
		t.Errorf("job page lacks the German load error: %s", w.Body)
	}
}
