package web

import (
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/repos"
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
		lang, schedule, loadErr string
	}{
		{"de", `title="0 6 * * *">täglich um 06:00<`, "Ungültiger Zeitplan: Stunde: Wert 25 außerhalb von 0-23"},
		{"en", `title="0 6 * * *">daily at 06:00<`, "invalid schedule: hour: value 25 out of range 0-23"},
	}

	for _, tc := range tests {
		hdr := map[string]string{"Accept-Language": tc.lang}
		board := do(b.srv, "GET", "/", nil, s.cookies, hdr).Body.String()
		job := do(b.srv, "GET", "/jobs/hello", nil, s.cookies, hdr).Body.String()

		if !strings.Contains(board, tc.schedule) || !strings.Contains(board, tc.loadErr) {
			t.Errorf("board (%s) lacks %q or %q", tc.lang, tc.schedule, tc.loadErr)
		}
		if !strings.Contains(job, tc.schedule) {
			t.Errorf("job page (%s) lacks %q", tc.lang, tc.schedule)
		}
	}

	// The job page of the broken job shows the error too.
	w := do(b.srv, "GET", "/jobs/late", nil, s.cookies, nil)
	if !strings.Contains(w.Body.String(), "Ungültiger Zeitplan: Stunde") {
		t.Errorf("job page lacks the German load error: %s", w.Body)
	}
}

// A recover with a Note (file repaired, job still failing) is no green
// "recovery" in the delivery list.
func TestDeliveryNoteShownNotRecovery(t *testing.T) {
	b, s := seeded(t)
	_, err := repos.AddDelivery(b.db, model.Delivery{Job: "bad", Event: model.EvRecover,
		Channel: "mail", At: time.Now(), OK: true, Note: "alert.file_fixed"})
	if err != nil {
		t.Fatal(err)
	}

	for accept, word := range map[string]string{"de": "Datei wieder gültig", "en": "file valid again"} {
		body := do(b.srv, "GET", "/channels", nil, s.cookies, map[string]string{"Accept-Language": accept}).Body.String()
		if !strings.Contains(body, `data-state="warn">`+word+`</span>`) {
			t.Errorf("%s: note word %q not in a warn pill", accept, word)
		}
		if strings.Contains(body, `data-state="ok">Erholung`) || strings.Contains(body, `data-state="ok">recovery`) {
			t.Errorf("%s: green recovery pill", accept)
		}
	}
}

// The board shows an invalid settings file and an unreadable jobs folder
// (B8); both read the settings source from the page data.
func TestBoardShowsInvalidSettings(t *testing.T) {
	tests := []struct {
		name  string
		spoil func(b *backend)
	}{
		{"settings", func(b *backend) { b.put("jidoka.toml", "[[profiles.standard]\n") }},
		{"dir", func(b *backend) {
			if err := os.Chmod(b.dir, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(b.dir, 0o700) })
		}},
	}

	for _, tc := range tests {
		b, s := seeded(t)
		tc.spoil(b)
		_ = b.jobs.Load()

		for _, path := range []string{"/", "/board/tiles"} {
			w := do(b.srv, "GET", path, nil, s.cookies, nil)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "callout-danger") {
				t.Errorf("%s %s: %d, no error callout: %s", tc.name, path, w.Code, w.Body)
			}
		}
	}
}
