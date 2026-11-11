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
	"net/netip"
	"time"

	"jidoka/internal/model"
	"jidoka/internal/services/access"
	"jidoka/internal/services/alerts"
	"jidoka/internal/services/ops"
	"jidoka/internal/services/plan"
	"jidoka/internal/services/secrets"
	"jidoka/internal/services/settings"
	"jidoka/internal/services/status"
	"jidoka/internal/services/watch"
)

// Deps are the services the routes use. Fields may stay nil in tests;
// every use is nil-safe.
type Deps struct {
	Access  *access.Service
	Status  *status.Service
	Plan    *plan.Planner
	Ops     *ops.Service
	Watch   *watch.Watch
	Alerts  *alerts.Engine
	Secrets *secrets.Service
	Config  *settings.Service

	// TrustedProxy is a comma-separated list of CIDRs or addresses
	// (env TRUSTED_PROXY). Only a request from one of them may name the
	// client in X-Forwarded-For; the ping rate limit uses that address.
	TrustedProxy string

	// Settings returns the settings in force (zone, Andon link, base URL).
	Settings func() model.Settings

	// Health is nil when the process is healthy (B5): the planner ticked
	// lately and the database takes writes.
	Health func() error

	Version string
	Log     *slog.Logger
}

// Server is the HTTP side of Jidoka.
type Server struct {
	deps    Deps
	mux     *http.ServeMux
	log     *slog.Logger
	proxies []netip.Prefix
}

// New wires the routes.
func New(d Deps) *Server {
	s := &Server{deps: d, mux: http.NewServeMux(), log: d.Log}
	if s.log == nil {
		s.log = slog.Default()
	}

	s.proxies = s.parseProxies(d.TrustedProxy)
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
	s.registerPages()
	s.registerHome()
	s.registerAccess()
	s.registerSettings()
	s.registerAPI()
	s.registerPing()
	s.registerActions()
}

// settings are the settings in force; zero values when none is wired.
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
