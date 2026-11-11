package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"jidoka/internal/repos"
)

// settingsForm is a form the page would send.
func settingsForm(kv ...string) url.Values {
	v := url.Values{"zone": {"Europe/Berlin"}, "locale": {"de"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}

	return v
}

func (s signed) get(path string) *httptest.ResponseRecorder {
	return do(s.b.srv, "GET", path, nil, s.cookies, nil)
}

func TestSettingsPage(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	w := s.get("/settings")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, body)
	}
	for _, want := range []string{`href="/settings"`, `name="zone"`, `name="secret_SMTP_PASSWORD"`, `type="password"`, `name="csrf"`, "standard"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// A valid form is saved, the browser is sent back with a notice, and the
// page shows the saved values.
func TestSettingsSave(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	w := s.post("/settings", settingsForm("max_parallel", "4", "tag_name", "handy", "tag_tag", "phone", "apprise_url", "http://a/notify"), nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings?saved=1" {
		t.Fatalf("%d %s %s", w.Code, w.Header().Get("Location"), w.Body)
	}

	doc, _, _ := repos.GetSettings(b.db)
	if !strings.Contains(doc, "max_parallel = 4") || !strings.Contains(doc, "handy") {
		t.Errorf("stored: %s", doc)
	}

	body := s.get("/settings?saved=1").Body.String()
	if !strings.Contains(body, `value="4"`) || !strings.Contains(body, `value="phone"`) || !strings.Contains(body, "callout-ok") {
		t.Errorf("page: %s", body)
	}
	if got := b.jobs.Current().Settings.MaxParallel; got != 4 {
		t.Errorf("in force: %d", got)
	}
}

// A bad value answers 400 with the form as typed and the error at the
// field; nothing is stored.
func TestSettingsError(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	w := s.post("/settings", settingsForm("zone", "Mars/Base", "max_parallel", "x", "smtp", "h:25"), nil)
	body := w.Body.String()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
	for _, want := range []string{`value="Mars/Base"`, `aria-invalid="true"`, "is-error", `value="h:25"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}

	if doc, ok, _ := repos.GetSettings(b.db); ok {
		t.Errorf("stored: %s", doc)
	}
}

func TestSettingsNeedsCSRF(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	form := settingsForm()
	form.Set("csrf", "forged")
	if w := do(b.srv, "POST", "/settings", form, s.cookies, nil); w.Code != http.StatusForbidden {
		t.Errorf("forged token: %d", w.Code)
	}

	if w := do(b.srv, "GET", "/settings", nil, nil, nil); w.Code == http.StatusOK {
		t.Errorf("without a session: %d", w.Code)
	}
}

// A password is stored through the secrets service and never rendered
// again; an empty field keeps it and the clear box removes it.
func TestSettingsSecrets(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	const pw = "hunter2-hunter2"
	s.post("/settings", settingsForm("secret_SMTP_PASSWORD", pw, "smtp", "h:25", "mail_from", "j@x.org"), nil)

	body := s.get("/settings").Body.String()
	if strings.Contains(body, pw) || !strings.Contains(body, "gesetzt") {
		t.Errorf("page shows the value or not the state: %.2000s", body)
	}

	s.post("/settings", settingsForm("smtp", "h:25", "mail_from", "j@x.org"), nil)
	got, _, _ := b.secrets.Resolve([]string{"SMTP_PASSWORD"})
	if got["SMTP_PASSWORD"] != pw {
		t.Errorf("empty field dropped the secret: %v", got)
	}

	s.post("/settings", settingsForm("smtp", "h:25", "mail_from", "j@x.org", "clear_SMTP_PASSWORD", "1"), nil)
	if _, missing, _ := b.secrets.Resolve([]string{"SMTP_PASSWORD"}); len(missing) != 1 {
		t.Errorf("secret not removed: %v", missing)
	}
}

// A job that names a sub channel turns invalid when a save removes it.
func TestSettingsSaveInvalidatesJob(t *testing.T) {
	b := newBackend(t, "")
	b.job("uses", "echo\n", "[[notify]]\non = [\"fail\"]\nchannels = [\"apprise:handy\"]\n")
	s := b.signedIn()

	with := settingsForm("apprise_url", "http://a/notify", "tag_name", "handy", "tag_tag", "phone")
	if w := s.post("/settings", with, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, ok := b.jobs.Job("uses"); !ok {
		t.Fatalf("invalid with the tag: %+v", b.jobs.Current().Invalid)
	}

	if w := s.post("/settings", settingsForm(), nil); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, ok := b.jobs.Job("uses"); ok {
		t.Error("job still valid without the tag")
	}
}
