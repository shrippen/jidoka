package access

import (
	"errors"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/repos"
)

const (
	// SessionTTL is how long a session lives after its last use (sliding).
	SessionTTL = 30 * 24 * time.Hour

	// touchEvery bounds the writes: a use moves the expiry at most this often.
	touchEvery = time.Minute

	csrfPurpose = "csrf:"
)

// Who is the logged-in account.
type Who struct {
	Sub  string
	Name string
}

// open stores a session and returns its cookie token; the database holds
// only the hash, so a leaked copy logs nobody in.
func (s *Service) open(sub, name string) (string, error) {
	token := crypto.NewToken()
	now := s.now()

	err := repos.AddSession(s.db, repos.Session{
		Hash: crypto.TokenHash(token), Sub: sub, Name: name,
		Created: now, LastSeen: now, Expires: now.Add(SessionTTL),
	})
	if err != nil {
		return "", err
	}

	// Old sessions go with each login, so the table never grows.
	_ = repos.DeleteExpiredSessions(s.db, now)
	return token, nil
}

// Session returns the account behind a cookie token; ErrNoSession when the
// token is unknown or the session ran out. A use slides the expiry.
func (s *Service) Session(token string) (Who, error) {
	if token == "" {
		return Who{}, ErrNoSession
	}

	hash := crypto.TokenHash(token)
	sess, err := repos.SessionByHash(s.db, hash)
	if errors.Is(err, repos.ErrNotFound) {
		return Who{}, ErrNoSession
	}
	if err != nil {
		return Who{}, err
	}

	now := s.now()
	if now.After(sess.Expires) {
		_ = repos.DeleteSession(s.db, hash)
		return Who{}, ErrNoSession
	}
	if now.Sub(sess.LastSeen) >= touchEvery {
		if err := repos.TouchSession(s.db, hash, now, now.Add(SessionTTL)); err != nil {
			return Who{}, err
		}
	}
	return Who{Sub: sess.Sub, Name: sess.Name}, nil
}

// Logout ends the session of a cookie token.
func (s *Service) Logout(token string) error {
	return repos.DeleteSession(s.db, crypto.TokenHash(token))
}

// csrfSecret is the session's CSRF token, derived from its cookie token:
// stable for the session, unknown to a page that cannot read the cookie.
func csrfSecret(sessionToken string) string {
	return crypto.TokenHash(csrfPurpose + sessionToken)
}

// CSRF returns the session's CSRF token for one page, masked afresh each
// time (BREACH, see crypto.MaskToken).
func CSRF(sessionToken string) string {
	return crypto.MaskToken(csrfSecret(sessionToken))
}

// CheckCSRF reports whether submitted is a masked CSRF token of this session.
func CheckCSRF(sessionToken, submitted string) bool {
	secret, ok := crypto.UnmaskToken(submitted)
	return ok && sessionToken != "" && crypto.Same(secret, csrfSecret(sessionToken))
}
