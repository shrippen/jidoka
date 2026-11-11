package web

import (
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// hsts keeps browsers on HTTPS for a year once they saw Jidoka over TLS.
const hsts = "max-age=31536000"

// csp: everything same-origin, no inline script or style. Markup carries
// no style attributes and no inline handlers.
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; frame-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// crossOrigin refuses state-changing requests a browser marks as sent by
// another site (Sec-Fetch-Site, Origin). Requests without those headers
// (API clients) pass.
var crossOrigin = http.NewCrossOriginProtection()

// secured adds the security headers and refuses cross-site posts.
func (s *Server) secured(next http.Handler) http.Handler {
	next = crossOrigin.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.secureCookies() {
			h.Set("Strict-Transport-Security", hsts)
		}
		next.ServeHTTP(w, r)
	})
}

// secureCookies: cookies and HSTS need https, which only the dev login
// (plain http on localhost) does without.
func (s *Server) secureCookies() bool {
	return s.deps.Access == nil || !s.deps.Access.Dev()
}

// recovered turns a panic in a handler into a 500 and a log line.
func (s *Server) recovered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil || p == http.ErrAbortHandler {
				return
			}
			s.log.Error("panic", "route", routeOf(r), "panic", p, "stack", string(debug.Stack()))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// statusWriter remembers the status for the log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// unmatched is the log's route for a request no pattern took.
const unmatched = "-"

// routeOf is the pattern that served r ("POST /ping/{token}"), never the
// path or the query: a ping token or a code in the URL stays out of the log.
func routeOf(r *http.Request) string {
	if r.Pattern == "" {
		return unmatched
	}
	return r.Pattern
}

// logged writes one line per request: route, status, duration.
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		// Probes poll all day; their line would only bury the others.
		if strings.HasPrefix(r.URL.Path, healthPath) {
			return
		}
		s.log.Info("request", "method", r.Method, "route", routeOf(r), "status", sw.status,
			"ms", time.Since(start).Milliseconds())
	})
}
