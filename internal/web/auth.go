package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"jidoka/internal/i18n"
	"jidoka/internal/services/access"
)

const (
	sessionCookie = "jidoka_session"
	stateCookie   = "jidoka_oidc"
	csrfField     = "csrf"
	csrfHeader    = "X-CSRF-Token"
	bearerPrefix  = "Bearer "
	maxFormBytes  = 1 << 20
	loginPath     = "/login"
	homePath      = "/"
)

// session is the logged-in request: who, the cookie's token (for CSRF)
// and the language.
type session struct {
	Who    access.Who
	Token  string
	Locale i18n.Locale
}

// csrf is the masked CSRF token for a page of this session.
func (s *session) csrf() string {
	return access.CSRF(s.Token)
}

func localeOf(r *http.Request) i18n.Locale {
	return i18n.Pick(r.Header.Get("Accept-Language"))
}

// safeNext keeps ?next= to local paths: no open redirect. "//evil" and
// "/\evil" read as another host to a browser.
func safeNext(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, "\\") {
		return homePath
	}
	return target
}

// setCookie sets a cookie every cookie of Jidoka shares: HttpOnly, Lax
// (the IdP's redirect back is a top-level GET, which Lax sends), Secure
// unless dev.
func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// setSession gives the browser its session; calling it on every request
// slides the cookie's life like the stored session's.
func (s *Server) setSession(w http.ResponseWriter, token string) {
	s.setCookie(w, sessionCookie, token, "/", int(access.SessionTTL.Seconds()))
}

// authed runs h for a logged-in browser. Without a session a page goes to
// the login; a POST, PUT, PATCH or DELETE also needs the session's CSRF
// token (Z4), from the form field or the X-CSRF-Token header (htmx).
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessionOf(r)
		if sess == nil {
			s.toLogin(w, r)
			return
		}

		if !isSafe(r.Method) {
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
			if !s.csrfOK(r, sess) {
				s.fail(w, r, sess, http.StatusForbidden, "error.csrf")
				return
			}
		}

		s.setSession(w, sess.Token)
		h(w, r, sess)
	}
}

func isSafe(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// sessionOf is the session of r's cookie, nil without a valid one.
func (s *Server) sessionOf(r *http.Request) *session {
	if s.deps.Access == nil {
		return nil
	}

	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}

	who, err := s.deps.Access.Session(c.Value)
	if err != nil {
		if !errors.Is(err, access.ErrNoSession) {
			s.log.Error("session", "err", err)
		}
		return nil
	}
	return &session{Who: who, Token: c.Value, Locale: localeOf(r)}
}

// csrfOK reads the submitted token from the header or the form.
func (s *Server) csrfOK(r *http.Request, sess *session) bool {
	submitted := r.Header.Get(csrfHeader)
	if submitted == "" {
		if err := r.ParseForm(); err != nil {
			return false
		}
		submitted = r.PostForm.Get(csrfField)
	}
	return access.CheckCSRF(sess.Token, submitted)
}

// toLogin sends a browser to the login page and back to where it was.
func (s *Server) toLogin(w http.ResponseWriter, r *http.Request) {
	target := loginPath
	if r.Method == http.MethodGet {
		target += "?next=" + url.QueryEscape(r.URL.RequestURI())
	}

	// htmx follows a redirect inside the fragment; it wants a full page load.
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", target)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// api runs h for a request with a Bearer token that holds need (Z2). No
// session, no CSRF: a browser never sends the token by itself.
func (s *Server) api(need access.Right, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		bearer := strings.TrimPrefix(auth, bearerPrefix)
		if bearer == auth {
			bearer = ""
		}

		if s.deps.Access == nil {
			s.apiError(w, http.StatusUnauthorized, access.ErrUnauthorized)
			return
		}

		err := s.deps.Access.CheckToken(bearer, need)
		switch {
		case errors.Is(err, access.ErrUnauthorized):
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.apiError(w, http.StatusUnauthorized, err)
		case errors.Is(err, access.ErrForbidden):
			s.apiError(w, http.StatusForbidden, err)
		case err != nil:
			s.log.Error("token check", "err", err)
			s.apiError(w, http.StatusInternalServerError, errors.New("error.internal"))
		default:
			h(w, r)
		}
	}
}

// apiError answers JSON: {"error":"token.forbidden"}.
func (s *Server) apiError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
