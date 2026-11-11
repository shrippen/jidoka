// Adapted from andon internal/services/oidc/oidc_test.go (468474a).

// Package accesstest is a fake OIDC provider for tests: discovery, JWKS
// and the token endpoint, signed with a throwaway key.
package accesstest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Test values the provider expects.
const (
	ClientID = "jidoka"
	Secret   = "s3cret"
	GoodCode = "good"
)

// IdP is the fake provider. Set Claims (and Nonce, which the test reads
// from the authorize URL) before the callback.
type IdP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	Nonce  string
	Claims map[string]any
}

// New starts a provider that stops with the test.
func New(t *testing.T) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	idp := &IdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := idp.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": base + "/", "authorization_endpoint": base + "/authorize",
			"token_endpoint": base + "/token", "jwks_uri": base + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}}
		_ = json.NewEncoder(w).Encode(set)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code_verifier") == "" || r.FormValue("code") != GoodCode || r.FormValue("client_secret") != Secret {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": idp.sign(t)})
	})

	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// Issuer is the provider's issuer URL.
func (idp *IdP) Issuer() string {
	return idp.srv.URL + "/"
}

func (idp *IdP) sign(t *testing.T) string {
	t.Helper()
	claims := map[string]any{
		"iss": idp.Issuer(), "aud": ClientID, "nonce": idp.Nonce,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range idp.Claims {
		claims[k] = v
	}

	payload, _ := json.Marshal(claims)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: idp.key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}

	token, _ := obj.CompactSerialize()
	return token
}
