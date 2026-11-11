// Adapted from andon internal/web/routes_oidc.go (468474a).

package web

import (
	"errors"
	"net/http"
	"net/url"
	"time"

	"jidoka/internal/services/access"
)

const (
	healthPath     = "/healthz"
	stateCookieAge = int(access.StateTTL / time.Second) // as the service's state lifetime
	errKeyInternal = "error.internal"
)

func (s *Server) registerLogin() {
	s.mux.HandleFunc("GET "+loginPath, s.handleLogin)
	s.mux.HandleFunc("GET /auth/oidc/login", s.handleOIDCStart)
	s.mux.HandleFunc("GET "+access.CallbackPath, s.handleOIDCCallback)
	s.mux.HandleFunc("POST /auth/dev", s.handleDevLogin)
	s.mux.HandleFunc("POST /logout", s.authed(s.handleLogout))
	s.mux.HandleFunc("GET "+healthPath, s.handleHealth)
}

// loginPage is the data of login.html.
type loginPage struct {
	Error    string // catalog key
	OIDC     bool
	Dev      bool
	StartURL string
	Next     string
}

func (s *Server) loginView(r *http.Request, errKey string) loginPage {
	next := safeNext(r.URL.Query().Get("next"))
	p := loginPage{Error: errKey, Next: next, StartURL: "/auth/oidc/login?next=" + url.QueryEscape(next)}
	if s.deps.Access != nil {
		p.OIDC, p.Dev = s.deps.Access.Available(), s.deps.Access.Dev()
	}
	return p
}

func (s *Server) loginError(w http.ResponseWriter, r *http.Request, status int, err error) {
	s.page(w, r, nil, "login", status, s.loginView(r, err.Error()))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.sessionOf(r) != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.page(w, r, nil, "login", http.StatusOK, s.loginView(r, ""))
}

// baseURL is where the IdP sends the browser back to: base_url of
// jidoka.toml, else the address the request came in on.
func (s *Server) baseURL(r *http.Request) string {
	if base := s.settings().BaseURL; base != "" {
		return base
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.deps.Access == nil {
		s.loginError(w, r, http.StatusServiceUnavailable, access.ErrDisabled)
		return
	}

	target, state, err := s.deps.Access.AuthorizeURL(r.Context(), s.baseURL(r), safeNext(r.URL.Query().Get("next")))
	if err != nil {
		s.loginError(w, r, http.StatusServiceUnavailable, loginKeyOf(err))
		return
	}

	s.setCookie(w, stateCookie, state, access.CallbackPath, stateCookieAge)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	browserState := ""
	if c, err := r.Cookie(stateCookie); err == nil {
		browserState = c.Value
	}

	// The state is single-use, whatever comes of the callback.
	s.setCookie(w, stateCookie, "", access.CallbackPath, -1)

	if s.deps.Access == nil {
		s.loginError(w, r, http.StatusServiceUnavailable, access.ErrDisabled)
		return
	}

	login, err := s.deps.Access.Complete(r.Context(), s.baseURL(r), r.URL.Query(), browserState)
	if err != nil {
		s.loginError(w, r, loginStatus(err), loginKeyOf(err))
		return
	}

	s.setSession(w, login.Token)
	http.Redirect(w, r, safeNext(login.Next), http.StatusSeeOther)
}

// loginStatus is the HTTP status of a failed login.
func loginStatus(err error) int {
	if errors.Is(err, access.ErrRefused) {
		return http.StatusForbidden
	}
	return http.StatusUnauthorized
}

// loginKeyOf keeps the service's own errors (they are catalog keys) and
// hides anything else behind a generic one: a database error is no text
// for a login page.
func loginKeyOf(err error) error {
	for _, known := range []error{access.ErrDisabled, access.ErrUnreachable, access.ErrCancelled,
		access.ErrState, access.ErrFailed, access.ErrRefused} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New(errKeyInternal)
}

func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if s.deps.Access == nil {
		s.loginError(w, r, http.StatusServiceUnavailable, access.ErrDevOff)
		return
	}

	login, err := s.deps.Access.DevLogin()
	if err != nil {
		s.loginError(w, r, http.StatusForbidden, loginKeyOf(err))
		return
	}

	s.setSession(w, login.Token)
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	if err := s.deps.Access.Logout(sess.Token); err != nil {
		s.log.Error("logout", "err", err)
	}

	s.setCookie(w, sessionCookie, "", "/", -1)
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

// handleHealth answers 200 only when the process is healthy (B5): the
// planner ticked in the last minute and the database takes writes. The
// check is Deps.Health; without one the process counts as healthy.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.deps.Health != nil {
		if err := s.deps.Health(); err != nil {
			s.log.Warn("unhealthy", "err", err)
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
	}
	_, _ = w.Write([]byte("ok\n"))
}
