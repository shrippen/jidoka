// Package web is the browser and API side: routes, templates, login,
// CSRF. Pages and API handlers plug into the frame built here.
//
//	request ─► log ─► recover ─► security headers ─► mux
//	                                                  ├─ authed(page)   session cookie, CSRF on POST
//	                                                  ├─ api(right)     Bearer token, no CSRF
//	                                                  └─ public         /login, /healthz, /static
package web

import (
	"log/slog"
	"net/http"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/services/access"
)

// Deps are the services the routes use. Fields may stay nil in tests;
// every use is nil-safe.
type Deps struct {
	Access *access.Service

	// Settings returns the current jidoka.toml (zone, Andon link, base URL).
	Settings func() model.Settings

	// Health is nil when the process is healthy (B5): the planner ticked
	// lately and the database takes writes.
	Health func() error

	Version string
	Log     *slog.Logger
}

// Server is the HTTP side of Jidoka.
type Server struct {
	deps Deps
	mux  *http.ServeMux
	log  *slog.Logger
}

// New wires the routes.
func New(d Deps) *Server {
	s := &Server{deps: d, mux: http.NewServeMux(), log: d.Log}
	if s.log == nil {
		s.log = slog.Default()
	}

	s.routes()
	return s
}

// Handler is the whole site, with its middleware.
func (s *Server) Handler() http.Handler {
	return s.logged(s.recovered(s.secured(s.mux)))
}

// routes registers every route. A page or API worker adds one line with
// its own register function from its routes_*.go.
func (s *Server) routes() {
	s.registerStatic()
	s.registerLogin()
	s.registerHome()
	s.registerAccess()
}

// settings is the current jidoka.toml; zero values when none is wired.
func (s *Server) settings() model.Settings {
	var set model.Settings
	if s.deps.Settings != nil {
		set = s.deps.Settings()
	}
	if set.Zone == nil {
		set.Zone = time.UTC
	}
	return set
}
