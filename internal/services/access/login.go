// Adapted from andon internal/services/oidc/oidc.go (468474a).

package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"strings"
	"time"

	"jidoka/internal/drivers/oidc"
)

const (
	// CallbackPath is the redirect URI's path at BaseURL.
	CallbackPath = "/auth/oidc/callback"

	// StateTTL is how long a started login may take.
	StateTTL    = 10 * time.Minute
	scopes      = "openid profile email"
	randomBytes = 24

	devSub  = "dev"
	devName = "Dev"
)

// flow is a started login: what the callback must find again.
type flow struct {
	nonce, verifier, next string
	at                    time.Time
}

// Login is a finished login.
type Login struct {
	Token string // the session cookie's value
	Next  string // where the user wanted to go
}

// Available reports whether OIDC login is set up: a provider, a client
// and at least one account that may enter. Without it nobody gets in.
func (s *Service) Available() bool {
	c := s.cfg
	return c.Issuer != "" && c.ClientID != "" && c.ClientSecret != "" && (c.AllowSub != "" || c.AllowGroup != "")
}

func (s *Service) discover(ctx context.Context) (oidc.Provider, error) {
	return oidc.Discover(ctx, s.cfg.Issuer)
}

func redirectURI(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + CallbackPath
}

func randomToken() string {
	b := make([]byte, randomBytes)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// challenge is the PKCE S256 code challenge of verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeURL starts a login. The caller keeps the returned state in the
// browser (a cookie), so only this browser can finish the flow.
func (s *Service) AuthorizeURL(ctx context.Context, baseURL, next string) (target, state string, err error) {
	if !s.Available() {
		return "", "", ErrDisabled
	}

	p, err := s.discover(ctx)
	if err != nil {
		return "", "", ErrUnreachable
	}

	state, nonce, verifier := randomToken(), randomToken(), randomToken()
	now := s.now()

	s.mu.Lock()
	for k, v := range s.flows {
		if now.Sub(v.at) > StateTTL {
			delete(s.flows, k)
		}
	}
	s.flows[state] = flow{nonce: nonce, verifier: verifier, next: next, at: now}
	s.mu.Unlock()

	query := url.Values{
		"response_type": {"code"}, "client_id": {s.cfg.ClientID}, "redirect_uri": {redirectURI(baseURL)},
		"scope": {scopes}, "state": {state}, "nonce": {nonce},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
	}
	return p.Authorize + "?" + query.Encode(), state, nil
}

// Complete finishes the callback: state check (memory and browser), code
// exchange, token check, the one allowed account, a session.
func (s *Service) Complete(ctx context.Context, baseURL string, params url.Values, browserState string) (Login, error) {
	if params.Get("error") != "" {
		return Login{}, ErrCancelled
	}

	// A state works once: replays and strangers find nothing.
	state := params.Get("state")
	s.mu.Lock()
	f, ok := s.flows[state]
	delete(s.flows, state)
	s.mu.Unlock()
	if !ok || s.now().Sub(f.at) > StateTTL {
		return Login{}, ErrState
	}

	// Only the browser that started the flow may finish it.
	if subtle.ConstantTimeCompare([]byte(state), []byte(browserState)) != 1 {
		return Login{}, ErrState
	}

	p, err := s.discover(ctx)
	if err != nil {
		return Login{}, ErrFailed
	}
	idToken, err := oidc.Exchange(ctx, p, params.Get("code"), redirectURI(baseURL), s.cfg.ClientID, s.cfg.ClientSecret, f.verifier)
	if err != nil {
		return Login{}, ErrFailed
	}
	claims, err := oidc.Claims(ctx, p, idToken, s.cfg.ClientID, s.cfg.ClientSecret, f.nonce)
	if err != nil {
		return Login{}, ErrFailed
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Login{}, ErrFailed
	}
	if !s.allowed(sub, claimGroups(claims)) {
		return Login{}, ErrRefused
	}

	token, err := s.open(sub, claimName(claims, sub))
	if err != nil {
		return Login{}, err
	}
	return Login{Token: token, Next: f.next}, nil
}

// allowed: the configured sub, or a member of the configured group (Z1).
func (s *Service) allowed(sub string, groups []string) bool {
	if s.cfg.AllowSub != "" && sub == s.cfg.AllowSub {
		return true
	}
	if s.cfg.AllowGroup == "" {
		return false
	}
	for _, g := range groups {
		if g == s.cfg.AllowGroup {
			return true
		}
	}
	return false
}

func claimGroups(claims map[string]any) []string {
	list, _ := claims["groups"].([]any)
	out := make([]string, 0, len(list))
	for _, g := range list {
		if s, ok := g.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func claimName(claims map[string]any, fallback string) string {
	for _, key := range []string{"name", "preferred_username"} {
		if s, _ := claims[key].(string); s != "" {
			return s
		}
	}
	return fallback
}

// DevLogin opens a session for the fixed dev user. Only with JIDOKA_DEV in
// a non-release build.
func (s *Service) DevLogin() (Login, error) {
	if !s.cfg.Dev {
		return Login{}, ErrDevOff
	}

	token, err := s.open(devSub, devName)
	return Login{Token: token}, err
}
