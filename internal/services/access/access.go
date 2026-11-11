// Package access is who may use Jidoka: one authentik account on the
// pages (Z1), API tokens with rights (Z2), CSRF tokens (Z4).
//
//	browser ──► /auth/oidc/login ──► authentik ──► /auth/oidc/callback
//	                 state, nonce, verifier (memory, 10 min)   │ sub or group allowed?
//	                                                           ▼
//	                              session: cookie token, DB holds its hash
//
//	Andon, Komodo ──► Bearer token, DB holds its hash ──► right: read | run | reload
package access

import (
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Config is what the environment gives (Z5): nothing of it is stored.
type Config struct {
	Issuer       string // OIDC_ISSUER
	ClientID     string // OIDC_CLIENT_ID
	ClientSecret string // OIDC_CLIENT_SECRET
	AllowSub     string // OIDC_ALLOW_SUB: the one account by its sub
	AllowGroup   string // OIDC_ALLOW_GROUP: or by a group claim
	Dev          bool   // JIDOKA_DEV: login without OIDC, never in a release build
}

// Environment variable names of Config.
const (
	envIssuer   = "OIDC_ISSUER"
	envClientID = "OIDC_CLIENT_ID"
	envSecret   = "OIDC_CLIENT_SECRET"
	envSub      = "OIDC_ALLOW_SUB"
	envGroup    = "OIDC_ALLOW_GROUP"
	envDev      = "JIDOKA_DEV"
	envTrue     = "true"
)

// ConfigFromEnv reads Config through getenv (os.Getenv).
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		Issuer:       getenv(envIssuer),
		ClientID:     getenv(envClientID),
		ClientSecret: getenv(envSecret),
		AllowSub:     getenv(envSub),
		AllowGroup:   getenv(envGroup),
		Dev:          devBuild && getenv(envDev) == envTrue,
	}
}

// Errors carry catalog keys: the login page shows them translated.
var (
	ErrDisabled     = errors.New("oidc.disabled")
	ErrUnreachable  = errors.New("oidc.unreachable")
	ErrCancelled    = errors.New("oidc.denied")
	ErrState        = errors.New("oidc.state")
	ErrFailed       = errors.New("oidc.failed")
	ErrRefused      = errors.New("oidc.refused")
	ErrNoSession    = errors.New("session.none")
	ErrUnauthorized = errors.New("token.unknown")
	ErrForbidden    = errors.New("token.forbidden")
	ErrDevOff       = errors.New("login.dev_off")
)

// Service decides who is in.
type Service struct {
	db  *sql.DB
	cfg Config
	now func() time.Time

	mu    sync.Mutex
	flows map[string]flow // state → started login
}

// New returns the service on d.
func New(d *sql.DB, cfg Config) *Service {
	cfg.Dev = cfg.Dev && devBuild // a release build never has the dev login

	return &Service{db: d, cfg: cfg, now: time.Now, flows: map[string]flow{}}
}

// Dev reports whether the dev login is on (JIDOKA_DEV in a non-release
// build): cookies then go without Secure, for plain http.
func (s *Service) Dev() bool {
	return s.cfg.Dev
}

// Right is what an API token may do (Z2).
type Right string

const (
	RightRead   Right = "read"
	RightRun    Right = "run"
	RightReload Right = "reload"
)

// Rights lists every right, in the order the page shows them.
func Rights() []Right {
	return []Right{RightRead, RightRun, RightReload}
}

// ParseRights turns form values into rights; false for an unknown one.
func ParseRights(values []string) ([]Right, bool) {
	var out []Right
	for _, v := range values {
		r := Right(v)
		if !r.valid() {
			return nil, false
		}
		out = append(out, r)
	}
	return out, true
}

func (r Right) valid() bool {
	for _, known := range Rights() {
		if r == known {
			return true
		}
	}
	return false
}
