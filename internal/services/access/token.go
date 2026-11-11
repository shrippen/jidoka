package access

import (
	"errors"
	"slices"
	"strings"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/repos"
)

const (
	// tokenPrefix marks a Jidoka token in a leak scan or a paste.
	tokenPrefix = "jdk_"

	// touchToken bounds the writes of last_used.
	touchToken = time.Minute
)

// Token is an API token as listed: never its value (Z2).
type Token struct {
	ID       int64
	Name     string
	Rights   []Right
	Created  time.Time
	LastUsed *time.Time
}

// CreateToken makes a token and returns its value. That is the only time
// it exists in clear; the database keeps its hash.
func (s *Service) CreateToken(name string, rights []Right) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(rights) == 0 {
		return "", errors.New("token: name and rights needed")
	}

	value := tokenPrefix + crypto.NewToken()
	list := make([]string, len(rights))
	for i, r := range rights {
		list[i] = string(r)
	}

	if _, err := repos.AddToken(s.db, name, crypto.TokenHash(value), list, s.now()); err != nil {
		return "", err
	}
	return value, nil
}

// Tokens lists all tokens, oldest first.
func (s *Service) Tokens() ([]Token, error) {
	stored, err := repos.ListTokens(s.db)
	if err != nil {
		return nil, err
	}

	out := make([]Token, 0, len(stored))
	for _, t := range stored {
		tok := Token{ID: t.ID, Name: t.Name, Created: t.Created, LastUsed: t.LastUsed}
		for _, r := range t.Rights {
			tok.Rights = append(tok.Rights, Right(r))
		}
		out = append(out, tok)
	}
	return out, nil
}

// DeleteToken revokes a token.
func (s *Service) DeleteToken(id int64) error {
	return repos.DeleteToken(s.db, id)
}

// CheckToken accepts a Bearer value that holds need. ErrUnauthorized for an
// unknown token, ErrForbidden for a known one without the right.
func (s *Service) CheckToken(bearer string, need Right) error {
	if bearer == "" {
		return ErrUnauthorized
	}

	t, err := repos.TokenByHash(s.db, crypto.TokenHash(bearer))
	if errors.Is(err, repos.ErrNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if !slices.Contains(t.Rights, string(need)) {
		return ErrForbidden
	}

	now := s.now()
	if t.LastUsed == nil || now.Sub(*t.LastUsed) >= touchToken {
		_ = repos.TouchToken(s.db, t.ID, now)
	}
	return nil
}
