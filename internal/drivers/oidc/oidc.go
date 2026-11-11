// Adapted from andon internal/sources/oidc.go (468474a).

// Package oidc is the OpenID Connect provider's HTTP side (authentik):
// discovery, code exchange, ID token check. No state, no policy.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const (
	discoveryPath  = ".well-known/openid-configuration"
	discoveryCache = time.Hour
	clockSkew      = time.Minute
	requestTimeout = 15 * time.Second
	maxBody        = 1 << 20
)

var idTokenAlgs = []jose.SignatureAlgorithm{jose.RS256, jose.ES256, jose.HS256}

var client = &http.Client{Timeout: requestTimeout}

// Provider is the discovered endpoint set of an OIDC issuer.
type Provider struct {
	Issuer     string `json:"issuer"`
	Authorize  string `json:"authorization_endpoint"`
	Token      string `json:"token_endpoint"`
	JWKSURI    string `json:"jwks_uri"`
	EndSession string `json:"end_session_endpoint"`
}

type cachedProvider struct {
	at time.Time
	p  Provider
}

var (
	discoveryMu sync.Mutex
	discovered  = map[string]cachedProvider{}
)

// Discover reads (and caches for an hour) the issuer's configuration.
func Discover(ctx context.Context, issuer string) (Provider, error) {
	base := issuer
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	discoveryMu.Lock()
	cached, ok := discovered[base]
	discoveryMu.Unlock()
	if ok && time.Since(cached.at) < discoveryCache {
		return cached.p, nil
	}

	var p Provider
	if err := getJSON(ctx, base+discoveryPath, &p); err != nil {
		return Provider{}, fmt.Errorf("discovery: %w", err)
	}
	if p.Issuer == "" || p.Authorize == "" || p.Token == "" || p.JWKSURI == "" {
		return Provider{}, errors.New("discovery: incomplete metadata")
	}

	discoveryMu.Lock()
	discovered[base] = cachedProvider{at: time.Now(), p: p}
	discoveryMu.Unlock()
	return p, nil
}

// tokens is the token endpoint's answer (only the ID token is used).
type tokens struct {
	IDToken string `json:"id_token"`
}

// Exchange trades an authorization code (+ PKCE verifier) for the ID token.
func Exchange(ctx context.Context, p Provider, code, redirectURI, clientID, secret, verifier string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"client_id": {clientID}, "client_secret": {secret}, "code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token: HTTP %d", resp.StatusCode)
	}

	var t tokens
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&t); err != nil {
		return "", errors.New("token: invalid JSON")
	}
	return t.IDToken, nil
}

// Claims verifies the ID token (signature, iss, aud, nonce, exp) and
// returns its claims. HS256 tokens are checked against the client secret.
func Claims(ctx context.Context, p Provider, idToken, clientID, secret, nonce string) (map[string]any, error) {
	sig, err := jose.ParseSignedCompact(idToken, idTokenAlgs)
	if err != nil || len(sig.Signatures) != 1 {
		return nil, errors.New("id_token: malformed")
	}

	payload, err := verify(ctx, p, sig, secret)
	if err != nil {
		return nil, err
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errors.New("id_token: invalid claims")
	}
	if err := checkClaims(claims, p.Issuer, clientID, nonce, time.Now()); err != nil {
		return nil, err
	}
	return claims, nil
}

func verify(ctx context.Context, p Provider, sig *jose.JSONWebSignature, secret string) ([]byte, error) {
	header := sig.Signatures[0].Header
	if header.Algorithm == string(jose.HS256) {
		payload, err := sig.Verify([]byte(secret))
		if err != nil {
			return nil, errors.New("id_token: bad signature")
		}
		return payload, nil
	}

	var keys jose.JSONWebKeySet
	if err := getJSON(ctx, p.JWKSURI, &keys); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	candidates := keys.Keys
	if header.KeyID != "" {
		candidates = keys.Key(header.KeyID)
	}
	for _, key := range candidates {
		if payload, err := sig.Verify(key.Key); err == nil {
			return payload, nil
		}
	}
	return nil, errors.New("id_token: bad signature")
}

// checkClaims validates iss, aud (string or list), nonce and exp.
func checkClaims(claims map[string]any, issuer, clientID, nonce string, now time.Time) error {
	if iss, _ := claims["iss"].(string); iss != issuer {
		return errors.New("id_token: wrong issuer")
	}
	if !audienceHas(claims["aud"], clientID) {
		return errors.New("id_token: wrong audience")
	}
	if got, _ := claims["nonce"].(string); got == "" || got != nonce {
		return errors.New("id_token: wrong nonce")
	}

	exp, ok := claims["exp"].(float64)
	if !ok || now.Add(-clockSkew).After(time.Unix(int64(exp), 0)) {
		return errors.New("id_token: expired")
	}
	return nil
}

func audienceHas(aud any, clientID string) bool {
	switch v := aud.(type) {
	case string:
		return v == clientID
	case []any:
		for _, item := range v {
			if s, _ := item.(string); s == clientID {
				return true
			}
		}
	}
	return false
}

func getJSON(ctx context.Context, rawURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(target)
}
