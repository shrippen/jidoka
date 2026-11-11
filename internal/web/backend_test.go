package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/drivers/proc"
	"jidoka/internal/model"
	"jidoka/internal/repos"
	"jidoka/internal/services/access"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/jobs"
	"jidoka/internal/services/ops"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/runs"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/status"
	"jidoka/internal/services/watch"
)

// The test binary doubles as the shim, like cmd/jidoka does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == proc.ShimArg {
		os.Exit(proc.Shim(os.Args[2:]))
	}
	os.Exit(m.Run())
}

const secretValue = "s3cr3t-value-xyz"

// backend is the env with the real services behind the routes.
type backend struct {
	*env
	dir     string
	jobs    *jobs.Service
	plan    *plan.Planner
	watch   *watch.Watch
	secrets *secrets.Service
	acc     *access.Service
}

func (b *backend) put(rel, body string) {
	b.t.Helper()

	p := filepath.Join(b.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		b.t.Fatal(err)
	}
}

func (b *backend) job(id, script, extra string) {
	b.put(id+"/job.toml", "name = \""+id+"\"\nrun = \"run.sh\"\nschedule = \"0 6 * * *\"\n"+extra)
	b.put(id+"/run.sh", "#!/bin/sh\n"+script)
}

// newBackend wires every service like cmd/jidoka does, without its hooks.
func newBackend(t *testing.T, proxies string) *backend {
	t.Helper()

	crypto.Init(crypto.Derive("test-key", []byte("test-salt-0123456789")))
	t.Cleanup(func() { crypto.Init(nil) })

	e := newEnv(t)
	b := &backend{env: e, dir: t.TempDir(), acc: access.New(e.db, e.cfg)}
	b.secrets = secrets.New(e.db)
	b.jobs = jobs.New(e.db, b.dir)
	rn := runs.New(e.db, b.secrets, runs.Config{DataDir: t.TempDir()})
	b.watch = watch.New(watch.Config{DB: e.db})
	al := alerts.New(alerts.Config{
		Store: alerts.NewDBStore(e.db), Set: b.jobs.Current,
		Secrets: func(...string) (map[string]string, error) { return nil, nil },
	})
	b.plan = plan.New(plan.Config{DB: e.db, Exec: rn})

	b.job("hello", "echo \"token=$TOKEN\"\necho '3 things' > \"$JIDOKA_SUMMARY\"\n", "secrets = [\"TOKEN\"]\n")
	b.put("beat/watch.toml", "period = \"1h\"\ngrace = \"5m\"\n")
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}
	set := b.jobs.Current()
	b.plan.Apply(set)
	b.watch.Apply(set)

	b.srv = e.server(func(d *Deps) {
		d.Status = status.New(status.Deps{DB: e.db, Jobs: b.jobs, Plan: b.plan, Watch: b.watch, Alerts: al, Secrets: b.secrets, Version: "test"})
		d.Plan, d.Watch, d.Alerts, d.Secrets = b.plan, b.watch, al, b.secrets
		d.Ops = ops.New(ops.Deps{DB: e.db, Jobs: b.jobs, Runs: rn, Watch: b.watch})
		d.TrustedProxy = proxies
	})
	return b
}

// token makes an API token with rights.
func (b *backend) token(rights ...access.Right) string {
	b.t.Helper()
	v, err := b.acc.CreateToken("test", rights)
	if err != nil {
		b.t.Fatal(err)
	}
	return v
}

func bearer(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok}
}

func (b *backend) call(method, path, tok string) *httptest.ResponseRecorder {
	return do(b.srv, method, path, nil, nil, bearer(tok))
}

// decode reads a JSON answer into a generic map.
func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON (%d): %s", w.Code, w.Body)
	}
	return out
}

// lastID is the id of the newest run of job, 0 if none.
func (b *backend) lastID(job string) int64 {
	b.t.Helper()
	all, err := repos.LatestRuns(b.db)
	if err != nil {
		b.t.Fatal(err)
	}
	return all[job].ID
}

// waitDone waits until a run of job newer than since has ended.
func (b *backend) waitDone(job string, since int64) model.Run {
	b.t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		all, err := repos.LatestRuns(b.db)
		if err != nil {
			b.t.Fatal(err)
		}
		if r, ok := all[job]; ok && r.ID > since && r.State.Done() {
			full, err := repos.GetRun(b.db, r.ID)
			if err != nil {
				b.t.Fatal(err)
			}
			return full
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.t.Fatalf("run of %s did not end", job)
	return model.Run{}
}

func TestAPIAuth(t *testing.T) {
	b := newBackend(t, "")
	read, run, reload := b.token(access.RightRead), b.token(access.RightRun), b.token(access.RightReload)

	for _, path := range []string{"/api/v1/status", "/api/v1/jobs", "/api/v1/jobs/hello/runs", "/api/v1/runs/1"} {
		if w := do(b.srv, "GET", path, nil, nil, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", path, w.Code)
		}
		if w := b.call("GET", path, "jdk_wrong"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/reload", "/api/v1/jobs/hello/run"} {
		if w := do(b.srv, "POST", path, nil, nil, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without token: %d", path, w.Code)
		}
		if w := b.call("POST", path, read); w.Code != http.StatusForbidden {
			t.Errorf("POST %s with a read token: %d", path, w.Code)
		}
	}

	// Each right opens its own door only.
	if w := b.call("POST", "/api/v1/reload", run); w.Code != http.StatusForbidden {
		t.Errorf("reload with a run token: %d", w.Code)
	}
	if w := b.call("GET", "/api/v1/status", reload); w.Code != http.StatusForbidden {
		t.Errorf("status with a reload token: %d", w.Code)
	}

	w := b.call("POST", "/api/v1/reload", reload)
	d := decode(t, w)
	if w.Code != http.StatusOK || d["added"] == nil || d["invalid"] == nil {
		t.Errorf("reload: %d %s", w.Code, w.Body)
	}
	if got := b.audit(); len(got) != 1 || got[0].Action != "reload" || got[0].Actor != "api" {
		t.Errorf("audit: %+v", got)
	}
}

func (b *backend) audit() []repos.AuditEntry {
	b.t.Helper()
	l, err := repos.RecentAudit(b.db, 50)
	if err != nil {
		b.t.Fatal(err)
	}
	return l
}

func TestAPIShapes(t *testing.T) {
	b := newBackend(t, "")
	if err := b.secrets.Set("TOKEN", secretValue, "test"); err != nil {
		t.Fatal(err)
	}
	read := b.token(access.RightRead)

	for i := 0; i < 3; i++ {
		since := b.lastID("hello")
		if err := b.plan.RunNow("hello", "test"); err != nil {
			t.Fatal(err)
		}
		b.waitDone("hello", since)
	}

	st := decode(t, b.call("GET", "/api/v1/status", read))
	if st["version"] != "test" || st["invalid"].(float64) != 0 || st["channels"] == nil {
		t.Errorf("status: %v", st)
	}

	jobsOut := decode(t, b.call("GET", "/api/v1/jobs", read))
	list := jobsOut["jobs"].([]any)
	if len(list) != 2 || jobsOut["total"].(float64) != 2 {
		t.Fatalf("jobs: %v", jobsOut)
	}

	byID := map[string]map[string]any{}
	for _, j := range list {
		m := j.(map[string]any)
		byID[m["id"].(string)] = m
	}
	hello := byID["hello"]
	last := hello["last"].(map[string]any)
	if hello["kind"] != "job" || hello["state"] != "ok" || hello["summary"] != "3 things" ||
		hello["url"] != "/jobs/hello" || !strings.Contains(hello["tail"].(string), "token=***") {
		t.Errorf("hello: %v", hello)
	}
	if _, err := time.Parse(time.RFC3339, last["start"].(string)); err != nil || !strings.HasSuffix(last["start"].(string), "Z") {
		t.Errorf("start is not RFC 3339 UTC: %v", last["start"])
	}
	if hello["next"] == nil || hello["reason"] != nil {
		t.Errorf("next/reason: %v", hello)
	}
	hist := hello["history"].([]any)
	if len(hist) == 0 || hist[len(hist)-1].(map[string]any)["id"] != last["id"] {
		t.Errorf("history must end with the last run: %v", hist)
	}
	if beat := byID["beat"]; beat["kind"] != "watch" || beat["state"] != "never" {
		t.Errorf("beat: %v", beat)
	}

	if w := b.call("GET", "/api/v1/jobs?limit=1", read); len(decode(t, w)["jobs"].([]any)) != 1 {
		t.Error("limit ignored")
	}
	if w := b.call("GET", "/api/v1/jobs?limit=abc", read); w.Code != http.StatusBadRequest {
		t.Errorf("bad limit: %d", w.Code)
	}

	page := decode(t, b.call("GET", "/api/v1/jobs/hello/runs?limit=2", read))
	runs := page["runs"].([]any)
	if len(runs) != 2 || page["next_before"] == nil {
		t.Fatalf("runs page 1: %v", page)
	}
	if _, has := runs[0].(map[string]any)["output"]; has {
		t.Error("a run list carries output")
	}
	before := fmt.Sprint(int64(page["next_before"].(float64)))
	page2 := decode(t, b.call("GET", "/api/v1/jobs/hello/runs?limit=2&before="+before, read))
	if len(page2["runs"].([]any)) != 1 || page2["next_before"] != nil {
		t.Errorf("runs page 2: %v", page2)
	}
	if w := b.call("GET", "/api/v1/jobs/nope/runs", read); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", w.Code)
	}

	id := fmt.Sprint(int64(runs[0].(map[string]any)["id"].(float64)))
	one := decode(t, b.call("GET", "/api/v1/runs/"+id, read))
	if one["output"] == nil || one["state"] != "ok" || one["duration_ms"] == nil {
		t.Errorf("run: %v", one)
	}
	if w := b.call("GET", "/api/v1/runs/99999", read); w.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", w.Code)
	}
}

// TestAPINeverSecret: a job echoes its secret; no answer shows it.
func TestAPINeverSecret(t *testing.T) {
	b := newBackend(t, "")
	if err := b.secrets.Set("TOKEN", secretValue, "test"); err != nil {
		t.Fatal(err)
	}

	tok := b.token(access.RightRead, access.RightRun, access.RightReload)
	since := b.lastID("hello")
	if w := b.call("POST", "/api/v1/jobs/hello/run", tok); w.Code != http.StatusAccepted {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	r := b.waitDone("hello", since)
	if !strings.Contains(r.Output, "token=***") {
		t.Fatalf("the run did not mask its output: %q", r.Output)
	}

	for _, path := range []string{
		"/api/v1/status", "/api/v1/jobs", "/api/v1/jobs/hello/runs", fmt.Sprintf("/api/v1/runs/%d", r.ID),
	} {
		w := b.call("GET", path, tok)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), secretValue) {
			t.Errorf("%s (%d) shows the secret or fails: %s", path, w.Code, w.Body)
		}
	}
	if w := b.call("POST", "/api/v1/reload", tok); strings.Contains(w.Body.String(), secretValue) {
		t.Error("reload shows the secret")
	}

	// A second start while it is busy or after it ended is a plain answer.
	if w := b.call("POST", "/api/v1/jobs/none/run", tok); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", w.Code)
	}
}

func TestPing(t *testing.T) {
	b := newBackend(t, "")
	token, err := b.watch.Token("beat")
	if err != nil {
		t.Fatal(err)
	}
	ping := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+testHost+path, strings.NewReader(body))
		r.RemoteAddr = "192.0.2.7:4711"
		w := httptest.NewRecorder()
		b.srv.Handler().ServeHTTP(w, r)
		return w
	}
	latest := func() model.Run {
		all, err := repos.LatestRuns(b.db)
		if err != nil {
			t.Fatal(err)
		}
		full, err := repos.GetRun(b.db, all["beat"].ID)
		if err != nil {
			t.Fatal(err)
		}
		return full
	}

	if w := ping("POST", "/ping/"+token+"/start", ""); w.Code != http.StatusOK {
		t.Fatalf("start: %d", w.Code)
	}
	if r := latest(); r.State != model.RunRunning || r.Trigger != model.TrigPing {
		t.Errorf("after start: %+v", r)
	}

	if w := ping("POST", "/ping/"+token, "backup done"); w.Code != http.StatusOK {
		t.Fatalf("ok: %d", w.Code)
	}
	if r := latest(); r.State != model.RunOK || r.Output != "backup done" {
		t.Errorf("after ok: %+v", r)
	}

	if w := ping("GET", "/ping/"+token+"/fail", ""); w.Code != http.StatusOK {
		t.Fatalf("fail by GET: %d", w.Code)
	}
	if r := latest(); r.State != model.RunFail {
		t.Errorf("after fail: %+v", r)
	}

	// The body is cut at 10 KB.
	ping("POST", "/ping/"+token, strings.Repeat("x", 20<<10))
	if r := latest(); len(r.Output) != pingBodyMax {
		t.Errorf("output %d bytes", len(r.Output))
	}

	if w := ping("POST", "/ping/not-a-token", ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown token: %d", w.Code)
	}
	if w := ping("POST", "/ping/"+token+"/nope", ""); w.Code != http.StatusNotFound && w.Code != http.StatusSeeOther {
		t.Errorf("unknown suffix: %d", w.Code)
	}

	if strings.Contains(b.logs.String(), token) || strings.Contains(b.logs.String(), "not-a-token") {
		t.Error("a ping token is in the log")
	}
}

func TestPingThrottled(t *testing.T) {
	b := newBackend(t, "")

	var throttled int
	for i := 0; i < 60; i++ {
		r := httptest.NewRequest("POST", "http://"+testHost+"/ping/guess", nil)
		r.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		b.srv.Handler().ServeHTTP(w, r)

		switch w.Code {
		case http.StatusTooManyRequests:
			throttled++
		case http.StatusNotFound:
		default:
			t.Fatalf("status %d", w.Code)
		}
	}
	if throttled == 0 {
		t.Error("no ping was throttled")
	}

	// Another address has its own budget.
	r := httptest.NewRequest("POST", "http://"+testHost+"/ping/guess", nil)
	r.RemoteAddr = "192.0.2.10:1"
	w := httptest.NewRecorder()
	b.srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("other address: %d", w.Code)
	}
}

func TestClientAddr(t *testing.T) {
	b := newBackend(t, "10.0.0.0/8, 192.168.1.1, junk")

	for _, c := range []struct {
		name, peer, xff, want string
	}{
		{"no proxy", "198.51.100.4:1", "6.6.6.6", "198.51.100.4"},
		{"proxy names the client", "10.0.0.2:1", "6.6.6.6, 203.0.113.9", "203.0.113.9"},
		{"chain of proxies", "10.0.0.2:1", "203.0.113.9, 10.1.1.1", "203.0.113.9"},
		{"single address entry", "192.168.1.1:1", "203.0.113.9", "203.0.113.9"},
		{"proxy without header", "10.0.0.2:1", "", "10.0.0.2"},
		{"garbage header", "10.0.0.2:1", "not-an-ip", "10.0.0.2"},
		{"untrusted peer, forged header", "203.0.113.50:1", "10.0.0.1", "203.0.113.50"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := b.srv.clientAddr(r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if !strings.Contains(b.logs.String(), "junk") {
		t.Error("a bad TRUSTED_PROXY entry was not reported")
	}
}

// signed is a signed-in browser: cookie and CSRF token.
type signed struct {
	b       *backend
	cookies []*http.Cookie
	csrf    string
}

func (b *backend) signedIn() signed {
	c := b.signIn(b.srv)
	cookies := []*http.Cookie{c}
	return signed{b: b, cookies: cookies, csrf: csrfOf(b.t, do(b.srv, "GET", "/", nil, cookies, nil))}
}

func (s signed) post(path string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	if form.Get("csrf") == "" {
		form.Set("csrf", s.csrf)
	}
	return do(s.b.srv, "POST", path, form, s.cookies, headers)
}

func TestActionsNeedSessionAndCSRF(t *testing.T) {
	b := newBackend(t, "")

	for _, path := range []string{
		"/reload", "/jobs/hello/run", "/jobs/hello/dry", "/jobs/hello/pause", "/jobs/hello/resume",
		"/jobs/hello/ack", "/jobs/hello/mute", "/jobs/hello/unmute", "/jobs/beat/renew-token",
		"/jobs/hello/rules/0/test", "/runs/1/cancel", "/channels/mail/test", "/secrets", "/secrets/X/delete",
	} {
		if w := do(b.srv, "POST", path, url.Values{}, nil, nil); w.Code != http.StatusSeeOther ||
			!strings.HasPrefix(w.Header().Get("Location"), "/login") {
			t.Errorf("POST %s without session: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}

	s := b.signedIn()
	for _, path := range []string{"/reload", "/jobs/hello/pause", "/jobs/hello/ack", "/secrets"} {
		w := do(b.srv, "POST", path, url.Values{"name": {"X"}, "value": {secretValue}}, s.cookies, nil)
		if w.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF: %d", path, w.Code)
		}
	}
	if w := do(b.srv, "POST", "/jobs/hello/pause", url.Values{"csrf": {"forged"}}, s.cookies, nil); w.Code != http.StatusForbidden {
		t.Errorf("forged CSRF: %d", w.Code)
	}

	// Nothing above changed anything.
	if f, _ := repos.GetFlags(b.db, "hello"); f.PausedAt != nil || f.AckedAt != nil {
		t.Errorf("a refused action changed flags: %+v", f)
	}
	if secrets, _ := b.secrets.List(); len(secrets) != 0 {
		t.Error("a refused action stored a secret")
	}
	if len(b.audit()) != 0 {
		t.Errorf("a refused action was audited: %+v", b.audit())
	}

	// GET is no action.
	if w := do(b.srv, "GET", "/jobs/hello/pause", nil, s.cookies, nil); w.Code == http.StatusSeeOther && w.Header().Get("Location") == "/jobs/hello" {
		t.Error("GET paused a job")
	}
}

func TestActions(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	expect := func(w *httptest.ResponseRecorder, want string) {
		t.Helper()
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != want {
			t.Fatalf("%d %s, want 303 %s (%s)", w.Code, w.Header().Get("Location"), want, w.Body)
		}
	}

	expect(s.post("/jobs/hello/pause", nil, nil), "/jobs/hello")
	if f, _ := repos.GetFlags(b.db, "hello"); f.PausedAt == nil || f.PausedBy != "Arian" {
		t.Errorf("pause: %+v", f)
	}
	expect(s.post("/jobs/hello/resume", nil, nil), "/jobs/hello")
	if f, _ := repos.GetFlags(b.db, "hello"); f.PausedAt != nil {
		t.Errorf("resume: %+v", f)
	}

	expect(s.post("/jobs/hello/ack", url.Values{"next": {"/"}}, nil), "/")
	expect(s.post("/jobs/hello/mute", url.Values{"span": {"1h"}}, nil), "/jobs/hello")
	if f, _ := repos.GetFlags(b.db, "hello"); f.AckedAt == nil || f.MutedUntil == nil {
		t.Errorf("ack/mute: %+v", f)
	}
	expect(s.post("/jobs/hello/mute", url.Values{"span": {"recover"}}, nil), "/jobs/hello")
	expect(s.post("/jobs/hello/unmute", nil, nil), "/jobs/hello")
	if f, _ := repos.GetFlags(b.db, "hello"); f.MutedUntil != nil || f.MuteRecover {
		t.Errorf("unmute: %+v", f)
	}

	if w := s.post("/jobs/hello/mute", url.Values{"span": {"forever"}}, nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad span: %d", w.Code)
	}
	if w := s.post("/jobs/nope/ack", nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown job: %d", w.Code)
	}

	// An open redirect target is not followed.
	expect(s.post("/jobs/hello/ack", url.Values{"next": {"//evil.test"}}, nil), "/")

	// htmx gets a full-page redirect.
	w := s.post("/jobs/hello/ack", nil, map[string]string{"HX-Request": "true"})
	if w.Code != http.StatusOK || w.Header().Get("HX-Redirect") != "/jobs/hello" {
		t.Errorf("htmx: %d %v", w.Code, w.Header())
	}

	expect(s.post("/reload", nil, nil), "/")
	expect(s.post("/jobs/beat/renew-token", nil, nil), "/jobs/beat")

	// run now: the job runs (it fails without its secret, which is fine here).
	since := b.lastID("hello")
	expect(s.post("/jobs/hello/run", nil, nil), "/jobs/hello")
	b.waitDone("hello", since)
	if w := s.post("/jobs/hello/dry", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("dry: %d", w.Code)
	}
	waitIdle(t, b.plan, "hello")

	// A run that is not running cannot be cancelled.
	if w := s.post("/runs/1/cancel", nil, nil); w.Code != http.StatusConflict {
		t.Errorf("cancel: %d", w.Code)
	}

	actions := map[string]bool{}
	for _, a := range b.audit() {
		actions[a.Action] = true
		if a.Actor != "Arian" {
			t.Errorf("audit actor: %+v", a)
		}
	}
	for _, want := range []string{"pause", "resume", "ack", "mute-1h", "mute-recover", "unmute", "reload", "renew-token", "run", "dry"} {
		if !actions[want] {
			t.Errorf("no audit entry %q", want)
		}
	}
}

func TestSecretActionsNeverEchoValue(t *testing.T) {
	b := newBackend(t, "")
	s := b.signedIn()

	w := s.post("/secrets", url.Values{"name": {"TOKEN"}, "value": {secretValue}}, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/secrets" || strings.Contains(w.Body.String(), secretValue) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}

	// Errors do not echo the value either.
	for name, form := range map[string]url.Values{
		"bad name":    {"name": {"lower"}, "value": {secretValue}},
		"empty value": {"name": {"TOKEN"}, "value": {""}},
	} {
		w := s.post("/secrets", form, nil)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), secretValue) {
			t.Errorf("%s: %d", name, w.Code)
		}
	}

	if w := s.post("/secrets/TOKEN/delete", nil, nil); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if w := s.post("/secrets/TOKEN/delete", nil, nil); w.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", w.Code)
	}

	var all strings.Builder
	for _, a := range b.audit() {
		all.WriteString(a.Actor + a.Action + a.Target)
	}
	if strings.Contains(all.String(), secretValue) || !strings.Contains(all.String(), "setTOKEN") {
		t.Errorf("audit: %s", all.String())
	}
	if strings.Contains(b.logs.String(), secretValue) {
		t.Error("the secret value is in the log")
	}
}

// waitIdle waits until the planner has no run of job in flight.
func waitIdle(t *testing.T, p *plan.Planner, job string) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for p.Busy(job) {
		if time.Now().After(deadline) {
			t.Fatal("job stays busy")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCancelRunningRun(t *testing.T) {
	b := newBackend(t, "")
	b.job("slow", "sleep 30\n", "")
	if err := b.jobs.Load(); err != nil {
		t.Fatal(err)
	}
	b.plan.Apply(b.jobs.Current())
	s := b.signedIn()

	if err := b.plan.RunNow("slow", "test"); err != nil {
		t.Fatal(err)
	}

	// The run is stored as running a moment after the start.
	var id int64
	for i := 0; i < 500 && id == 0; i++ {
		if r := b.lastID("slow"); r != 0 {
			id = r
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == 0 {
		t.Fatal("run did not start")
	}

	// The row exists a moment before the run can be cancelled.
	var w *httptest.ResponseRecorder
	for i := 0; i < 100; i++ {
		w = s.post("/runs/"+itoa(id)+"/cancel", nil, nil)
		if w.Code != http.StatusConflict {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/runs/"+itoa(id) {
		t.Fatalf("cancel: %d %s", w.Code, w.Header().Get("Location"))
	}
	if r := b.waitDone("slow", 0); r.State != model.RunAborted {
		t.Errorf("state %s", r.State)
	}

	got := b.audit()
	if len(got) == 0 || got[0].Action != "cancel" || got[0].Target != "slow" || got[0].Actor != "Arian" {
		t.Errorf("audit: %+v", got)
	}
}
