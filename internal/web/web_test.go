package web

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/model"
	"jidoka/internal/services/access"
	"jidoka/internal/services/access/accesstest"
)

const (
	testHost = "jidoka.test"
	ownerSub = "owner-1"
)

// env is a Server on a fresh database with a fake authentik.
type env struct {
	t    *testing.T
	db   *sql.DB
	idp  *accesstest.IdP
	cfg  access.Config
	srv  *Server
	logs *bytes.Buffer
}

type option func(*Deps)

func newEnv(t *testing.T, opts ...option) *env {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	idp := accesstest.New(t)
	e := &env{t: t, db: d, idp: idp, logs: &bytes.Buffer{}}
	e.cfg = access.Config{
		Issuer: idp.Issuer(), ClientID: accesstest.ClientID, ClientSecret: accesstest.Secret,
		AllowSub: ownerSub,
	}
	idp.Claims = map[string]any{"sub": ownerSub, "name": "Arian"}

	e.srv = e.server(opts...)
	return e
}

// server is a new Server on the same database, as after a restart.
func (e *env) server(opts ...option) *Server {
	deps := Deps{
		Access:   access.New(e.db, e.cfg),
		Settings: func() model.Settings { return model.Settings{Zone: time.UTC} },
		Log:      slog.New(slog.NewTextHandler(e.logs, nil)),
		Version:  "test",
	}
	for _, o := range opts {
		o(&deps)
	}
	return New(deps)
}

// do sends one request to s, as a browser with cookies.
func do(s *Server, method, target string, form url.Values, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	r := httptest.NewRequest(method, "http://"+testHost+target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signIn runs the whole OIDC flow through the routes and returns the
// session cookie.
func (e *env) signIn(s *Server) *http.Cookie {
	e.t.Helper()
	start := do(s, "GET", "/auth/oidc/login?next=/access", nil, nil, nil)
	if start.Code != http.StatusSeeOther {
		e.t.Fatalf("start: %d %s", start.Code, start.Body)
	}

	u, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	q := u.Query()
	e.idp.Nonce = q.Get("nonce")

	state := cookieNamed(start, stateCookie)
	if state == nil || state.Value != q.Get("state") {
		e.t.Fatalf("state cookie: %+v", state)
	}

	cb := do(s, "GET", access.CallbackPath+"?code="+accesstest.GoodCode+"&state="+q.Get("state"), nil, []*http.Cookie{state}, nil)
	if cb.Code != http.StatusSeeOther || cb.Header().Get("Location") != "/access" {
		e.t.Fatalf("callback: %d %s %s", cb.Code, cb.Header().Get("Location"), cb.Body)
	}

	c := cookieNamed(cb, sessionCookie)
	if c == nil || c.Value == "" {
		e.t.Fatal("no session cookie")
	}
	return c
}

var csrfMeta = regexp.MustCompile(`<meta name="csrf" content="([^"]+)">`)

// csrfOf reads the CSRF token a page carries.
func csrfOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	m := csrfMeta.FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no csrf meta in %s", w.Body)
	}
	return m[1]
}

func TestOIDCLoginFlow(t *testing.T) {
	e := newEnv(t)

	page := do(e.srv, "GET", "/login", nil, nil, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Mit authentik anmelden") {
		t.Fatalf("login page: %d %s", page.Code, page.Body)
	}

	c := e.signIn(e.srv)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags: %+v", c)
	}

	home := do(e.srv, "GET", "/", nil, []*http.Cookie{c}, nil)
	body := home.Body.String()
	if home.Code != http.StatusOK || !strings.Contains(body, "Tafel") || !strings.Contains(body, "Arian") {
		t.Fatalf("home: %d %s", home.Code, body)
	}

	en := do(e.srv, "GET", "/", nil, []*http.Cookie{c}, map[string]string{"Accept-Language": "en-US,en"})
	if !strings.Contains(en.Body.String(), "Sign out") || !strings.Contains(en.Body.String(), `lang="en"`) {
		t.Fatalf("english: %s", en.Body)
	}
}

func TestOtherAccountRefused(t *testing.T) {
	e := newEnv(t)
	e.idp.Claims = map[string]any{"sub": "someone-else", "name": "Fremd"}

	start := do(e.srv, "GET", "/auth/oidc/login", nil, nil, nil)
	u, _ := url.Parse(start.Header().Get("Location"))
	e.idp.Nonce = u.Query().Get("nonce")

	cb := do(e.srv, "GET", access.CallbackPath+"?code="+accesstest.GoodCode+"&state="+u.Query().Get("state"),
		nil, []*http.Cookie{cookieNamed(start, stateCookie)}, nil)
	if cb.Code != http.StatusForbidden || !strings.Contains(cb.Body.String(), "nicht zugelassen") {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body)
	}
	if cookieNamed(cb, sessionCookie) != nil && cookieNamed(cb, sessionCookie).Value != "" {
		t.Fatal("a refused account got a session")
	}
}

func TestCallbackNeedsTheStartingBrowser(t *testing.T) {
	e := newEnv(t)
	start := do(e.srv, "GET", "/auth/oidc/login", nil, nil, nil)
	u, _ := url.Parse(start.Header().Get("Location"))
	e.idp.Nonce = u.Query().Get("nonce")

	// No state cookie: another browser.
	cb := do(e.srv, "GET", access.CallbackPath+"?code="+accesstest.GoodCode+"&state="+u.Query().Get("state"), nil, nil, nil)
	if cb.Code != http.StatusUnauthorized {
		t.Fatalf("callback without cookie: %d", cb.Code)
	}
}

func TestPagesNeedLogin(t *testing.T) {
	e := newEnv(t)
	w := do(e.srv, "GET", "/plan", nil, nil, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2Fplan" {
		t.Fatalf("got %d %s", w.Code, w.Header().Get("Location"))
	}

	w = do(e.srv, "GET", "/access", nil, nil, map[string]string{"HX-Request": "true"})
	if w.Header().Get("HX-Redirect") == "" {
		t.Fatal("htmx request not sent to the login as a full page load")
	}
}

func TestNextStaysLocal(t *testing.T) {
	for next, want := range map[string]string{
		"/access":      "/access",
		"//evil.test":  "/",
		"/\\evil.test": "/",
		"https://evil": "/",
		"":             "/",
	} {
		if got := safeNext(next); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}
}

func TestSessionSurvivesNewServer(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)

	again := e.server()
	if w := do(again, "GET", "/", nil, []*http.Cookie{c}, nil); w.Code != http.StatusOK {
		t.Fatalf("after restart: %d", w.Code)
	}
}

func TestLogoutNeedsCSRFAndEndsSession(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	cookies := []*http.Cookie{c}

	if w := do(e.srv, "POST", "/logout", url.Values{}, cookies, nil); w.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", w.Code)
	}
	if w := do(e.srv, "GET", "/", nil, cookies, nil); w.Code != http.StatusOK {
		t.Fatal("a refused logout ended the session")
	}

	token := csrfOf(t, do(e.srv, "GET", "/", nil, cookies, nil))
	w := do(e.srv, "POST", "/logout", url.Values{"csrf": {token}}, cookies, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("logout: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(e.srv, "GET", "/", nil, cookies, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("session lives after logout: %d", w.Code)
	}
}

func TestCSRFHeaderAndForeignToken(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	cookies := []*http.Cookie{c}
	token := csrfOf(t, do(e.srv, "GET", "/access", nil, cookies, nil))

	// htmx sends it as a header.
	ok := do(e.srv, "POST", "/access/tokens/999/delete", nil, cookies, map[string]string{csrfHeader: token})
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("header token: %d", ok.Code)
	}

	// The token of another session does not pass.
	e.idp.Claims = map[string]any{"sub": ownerSub, "name": "Arian"}
	other := e.signIn(e.srv)
	foreign := csrfOf(t, do(e.srv, "GET", "/access", nil, []*http.Cookie{other}, nil))
	bad := do(e.srv, "POST", "/access/tokens/999/delete", nil, cookies, map[string]string{csrfHeader: foreign})
	if bad.Code != http.StatusForbidden {
		t.Fatalf("foreign token: %d", bad.Code)
	}
}

func TestTokenPage(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	cookies := []*http.Cookie{c}
	token := csrfOf(t, do(e.srv, "GET", "/access", nil, cookies, nil))

	// Without CSRF nothing is made.
	if w := do(e.srv, "POST", "/access/tokens", url.Values{"name": {"x"}, "right": {"read"}}, cookies, nil); w.Code != http.StatusForbidden {
		t.Fatalf("no CSRF: %d", w.Code)
	}

	// No right: refused with a message.
	w := do(e.srv, "POST", "/access/tokens", url.Values{"csrf": {token}, "name": {"andon"}}, cookies, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Mindestens ein Recht") {
		t.Fatalf("no right: %d %s", w.Code, w.Body)
	}

	w = do(e.srv, "POST", "/access/tokens", url.Values{"csrf": {token}, "name": {"andon"}, "right": {"read", "run"}}, cookies, nil)
	value := regexp.MustCompile(`jdk_[A-Za-z0-9_-]+`).FindString(w.Body.String())
	if w.Code != http.StatusOK || value == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a page with a token value is cacheable")
	}

	// The value is shown once.
	list := do(e.srv, "GET", "/access", nil, cookies, nil)
	if strings.Contains(list.Body.String(), value) || !strings.Contains(list.Body.String(), "andon") {
		t.Fatalf("list: %s", list.Body)
	}
	if err := e.srv.deps.Access.CheckToken(value, access.RightRun); err != nil {
		t.Fatalf("new token: %v", err)
	}

	// The time of creation carries its zone mark.
	if !strings.Contains(list.Body.String(), `class="tz-mark"`) {
		t.Fatal("no zone mark next to a time")
	}

	tokens, _ := e.srv.deps.Access.Tokens()
	del := do(e.srv, "POST", "/access/tokens/"+itoa(tokens[0].ID)+"/delete", url.Values{"csrf": {token}}, cookies, nil)
	if del.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", del.Code)
	}
	if err := e.srv.deps.Access.CheckToken(value, access.RightRun); !errors.Is(err, access.ErrUnauthorized) {
		t.Fatalf("deleted token: %v", err)
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestAPIRights(t *testing.T) {
	e := newEnv(t)
	ok := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }
	e.srv.mux.HandleFunc("GET /api/v1/jobs", e.srv.api(access.RightRead, ok))
	e.srv.mux.HandleFunc("POST /api/v1/reload", e.srv.api(access.RightReload, ok))

	read, _ := e.srv.deps.Access.CreateToken("andon", []access.Right{access.RightRead})
	reload, _ := e.srv.deps.Access.CreateToken("komodo", []access.Right{access.RightReload})
	bearer := func(v string) map[string]string { return map[string]string{"Authorization": "Bearer " + v} }

	if w := do(e.srv, "GET", "/api/v1/jobs", nil, nil, bearer(read)); w.Code != http.StatusOK {
		t.Fatalf("read: %d", w.Code)
	}

	// No CSRF for a Bearer request; a read token still cannot reload.
	if w := do(e.srv, "POST", "/api/v1/reload", nil, nil, bearer(read)); w.Code != http.StatusForbidden {
		t.Fatalf("read token reloads: %d", w.Code)
	}
	if w := do(e.srv, "POST", "/api/v1/reload", nil, nil, bearer(reload)); w.Code != http.StatusOK {
		t.Fatalf("reload: %d", w.Code)
	}
	if w := do(e.srv, "GET", "/api/v1/jobs", nil, nil, bearer(reload)); w.Code != http.StatusForbidden {
		t.Fatalf("reload token reads: %d", w.Code)
	}

	for name, h := range map[string]map[string]string{
		"none":    nil,
		"unknown": bearer("jdk_nope"),
		"basic":   {"Authorization": "Basic " + read},
		"raw":     {"Authorization": read},
	} {
		w := do(e.srv, "GET", "/api/v1/jobs", nil, nil, h)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestCSPAndHeaders(t *testing.T) {
	e := newEnv(t)
	for _, target := range []string{"/login", "/healthz", "/", "/static/jidoka.css", "/nope"} {
		w := do(e.srv, "GET", target, nil, nil, nil)
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP %q", target, csp)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Strict-Transport-Security") == "" {
			t.Errorf("%s: headers %v", target, w.Header())
		}
	}
}

func TestPagesHaveNoInlineScriptOrStyle(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	for _, target := range []string{"/login", "/", "/access", "/nope"} {
		w := do(e.srv, "GET", target, nil, []*http.Cookie{c}, nil)
		body := w.Body.String()
		if regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(body) ||
			regexp.MustCompile(`(?i)<style|\sstyle=|\son[a-z]+=`).MatchString(body) {
			t.Errorf("%s: inline script or style", target)
		}
	}
}

func TestNoRawCatalogKeysOnPages(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	raw := regexp.MustCompile(`>\s*(nav|access|login|error|oidc|board|action)\.[a-z_]+`)

	for _, lang := range []string{"de", "en"} {
		for _, target := range []string{"/login", "/", "/access", "/nope"} {
			w := do(e.srv, "GET", target, nil, []*http.Cookie{c}, map[string]string{"Accept-Language": lang})
			if m := raw.FindString(w.Body.String()); m != "" {
				t.Errorf("%s %s: raw key %q", lang, target, m)
			}
		}
	}
}

func TestHealthz(t *testing.T) {
	var failure error
	e := newEnv(t, func(d *Deps) { d.Health = func() error { return failure } })

	if w := do(e.srv, "GET", "/healthz", nil, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("healthy: %d", w.Code)
	}

	failure = errors.New("planner silent")
	w := do(e.srv, "GET", "/healthz", nil, nil, nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "planner silent") {
		t.Fatal("the reason leaks to an unauthenticated caller")
	}
}

func TestLogHasNoQueryNorToken(t *testing.T) {
	e := newEnv(t)
	e.srv.mux.HandleFunc("POST /ping/{token}", func(w http.ResponseWriter, r *http.Request) {})

	do(e.srv, "POST", "/ping/sekret-ping-token?msg=sekret-query", nil, nil, nil)
	do(e.srv, "GET", "/login?next=/sekret-next", nil, nil, nil)
	do(e.srv, "GET", "/no/such/sekret-path", nil, nil, nil)

	log := e.logs.String()
	if strings.Contains(log, "sekret") {
		t.Fatalf("secret in the log:\n%s", log)
	}
	if !strings.Contains(log, "POST /ping/{token}") {
		t.Fatalf("route missing from the log:\n%s", log)
	}
}

func TestPanicBecomes500(t *testing.T) {
	e := newEnv(t)
	e.srv.mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })

	if w := do(e.srv, "GET", "/boom", nil, nil, nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d", w.Code)
	}
	if !strings.Contains(e.logs.String(), "panic") {
		t.Fatal("panic not logged")
	}
	if w := do(e.srv, "GET", "/healthz", nil, nil, nil); w.Code != http.StatusOK {
		t.Fatal("server unusable after a panic")
	}
}

func TestStaticAssets(t *testing.T) {
	e := newEnv(t)
	w := do(e.srv, "GET", "/login", nil, nil, nil)
	m := regexp.MustCompile(`href="(/static/vendor/kante/tokens\.css\?v=[0-9a-f]+)"`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("no versioned token css in %s", w.Body)
	}

	css := do(e.srv, "GET", m[1], nil, nil, nil)
	if css.Code != http.StatusOK || !strings.Contains(css.Body.String(), "--bg-void") || !strings.Contains(css.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("tokens.css: %d %v", css.Code, css.Header())
	}

	for _, f := range []string{"icon.svg", "jidoka.js", "vendor/htmx/htmx.min.js", "vendor/kante/shrippen.js", "vendor/kante/components.css", "vendor/kante/base.css", "vendor/kante/fonts/Rajdhani-700.woff2"} {
		if w := do(e.srv, "GET", "/static/"+f, nil, nil, nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", f, w.Code)
		}
	}
}

func TestZoneMark(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	got := string(tzMark(tokyo))
	if !strings.Contains(got, `data-offset="540"`) || !strings.Contains(got, "JST") || !strings.Contains(got, "hidden") {
		t.Fatalf("tzMark: %s", got)
	}

	fixed := time.FixedZone("+0530", 5*3600+1800)
	if z := zoneOf(fixed); z.Name != "UTC+0530" || z.Offset != 330 {
		t.Fatalf("zone without abbreviation: %+v", z)
	}
}

func TestAndonLinkOnlyWhenSet(t *testing.T) {
	e := newEnv(t)
	c := e.signIn(e.srv)
	if body := do(e.srv, "GET", "/", nil, []*http.Cookie{c}, nil).Body.String(); strings.Contains(body, "nav-ext") {
		t.Fatal("Andon link without andon_url")
	}

	linked := e.server(func(d *Deps) {
		d.Settings = func() model.Settings { return model.Settings{AndonURL: "https://andon.test"} }
	})
	body := do(linked, "GET", "/", nil, []*http.Cookie{c}, nil).Body.String()
	if !strings.Contains(body, `href="https://andon.test"`) {
		t.Fatalf("no Andon link: %s", body)
	}
}

func TestUnknownPathIsBehindLogin(t *testing.T) {
	e := newEnv(t)
	if w := do(e.srv, "GET", "/nope", nil, nil, nil); w.Code != http.StatusSeeOther {
		t.Fatalf("anonymous: %d", w.Code)
	}

	c := e.signIn(e.srv)
	if w := do(e.srv, "GET", "/nope", nil, []*http.Cookie{c}, nil); w.Code != http.StatusNotFound {
		t.Fatalf("signed in: %d", w.Code)
	}
}

func TestDevLogin(t *testing.T) {
	e := newEnv(t)
	e.cfg.Dev = true
	srv := e.server()
	if !srv.deps.Access.Dev() {
		t.Skip("release build: no dev login")
	}

	page := do(srv, "GET", "/login", nil, nil, nil)
	if !strings.Contains(page.Body.String(), "Dev-Anmeldung") {
		t.Fatal("no dev button")
	}

	w := do(srv, "POST", "/auth/dev", url.Values{"next": {"/access"}}, nil, nil)
	c := cookieNamed(w, sessionCookie)
	if w.Code != http.StatusSeeOther || c == nil || c.Secure {
		t.Fatalf("dev login: %d %+v", w.Code, c)
	}
	if w := do(srv, "GET", "/access", nil, []*http.Cookie{c}, nil); w.Code != http.StatusOK {
		t.Fatalf("dev session: %d", w.Code)
	}
}

func TestNoDevLoginByDefault(t *testing.T) {
	e := newEnv(t)
	if w := do(e.srv, "POST", "/auth/dev", url.Values{}, nil, nil); w.Code != http.StatusForbidden {
		t.Fatalf("dev login without JIDOKA_DEV: %d", w.Code)
	}
	if strings.Contains(do(e.srv, "GET", "/login", nil, nil, nil).Body.String(), "Dev-Anmeldung") {
		t.Fatal("dev button without JIDOKA_DEV")
	}
}
