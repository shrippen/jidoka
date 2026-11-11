// Adapted from andon internal/services/oidc/oidc_test.go (468474a).

package access

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/services/access/accesstest"
)

const baseURL = "http://jidoka.test"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func setup(t *testing.T, cfg Config) (*Service, *accesstest.IdP) {
	t.Helper()
	idp := accesstest.New(t)
	cfg.Issuer, cfg.ClientID, cfg.ClientSecret = idp.Issuer(), accesstest.ClientID, accesstest.Secret
	return New(testDB(t), cfg), idp
}

// login runs the flow: authorize URL, then the callback with code.
func login(t *testing.T, s *Service, idp *accesstest.IdP, code string) (Login, error) {
	t.Helper()
	target, state, err := s.AuthorizeURL(context.Background(), baseURL, "/plan")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}

	u, _ := url.Parse(target)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != baseURL+CallbackPath || q.Get("state") != state {
		t.Fatalf("unexpected authorize query: %v", q)
	}

	idp.Nonce = q.Get("nonce")
	return s.Complete(context.Background(), baseURL, url.Values{"state": {state}, "code": {code}}, state)
}

func TestLoginOpensSession(t *testing.T) {
	s, idp := setup(t, Config{AllowSub: "u-1"})
	idp.Claims = map[string]any{"sub": "u-1", "name": "Arian"}

	got, err := login(t, s, idp, accesstest.GoodCode)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.Next != "/plan" {
		t.Fatalf("next: %q", got.Next)
	}

	who, err := s.Session(got.Token)
	if err != nil || who.Sub != "u-1" || who.Name != "Arian" {
		t.Fatalf("session: %+v %v", who, err)
	}
}

// OIDC_ISSUER may be the configuration URL Authentik shows
// (…/.well-known/openid-configuration), not only the issuer.
func TestIssuerAsConfigURL(t *testing.T) {
	idp := accesstest.New(t)
	cfg := Config{Issuer: idp.Issuer() + ".well-known/openid-configuration", ClientID: accesstest.ClientID, ClientSecret: accesstest.Secret}
	s := New(testDB(t), cfg)
	idp.Claims = map[string]any{"sub": "u-1"}

	if _, err := login(t, s, idp, accesstest.GoodCode); err != nil {
		t.Fatalf("login with the configuration URL: %v", err)
	}
}

// A configuration URL with a slash at the end works too.
func TestIssuerAsConfigURLWithSlash(t *testing.T) {
	idp := accesstest.New(t)
	cfg := Config{Issuer: idp.Issuer() + ".well-known/openid-configuration/", ClientID: accesstest.ClientID, ClientSecret: accesstest.Secret}
	s := New(testDB(t), cfg)
	idp.Claims = map[string]any{"sub": "u-1"}

	if _, err := login(t, s, idp, accesstest.GoodCode); err != nil {
		t.Fatalf("login with the configuration URL and a slash: %v", err)
	}
}

// The metadata must name the issuer Jidoka was given (OIDC Discovery 4.3);
// else tokens of another issuer would pass the iss check.
func TestForeignMetaIssuerRefused(t *testing.T) {
	s, idp := setup(t, Config{})
	idp.MetaIssuer = "https://other.example/"

	if _, _, err := s.AuthorizeURL(context.Background(), baseURL, "/"); err == nil {
		t.Fatal("authorize with a foreign issuer in the metadata: no error")
	}
}

func TestOtherAccountRefused(t *testing.T) {
	s, idp := setup(t, Config{AllowSub: "u-1"})
	idp.Claims = map[string]any{"sub": "u-2", "name": "Fremd", "groups": []string{"jidoka"}}

	if _, err := login(t, s, idp, accesstest.GoodCode); !errors.Is(err, ErrRefused) {
		t.Fatalf("expected ErrRefused, got %v", err)
	}
}

func TestGroupMemberAllowed(t *testing.T) {
	s, idp := setup(t, Config{AllowGroup: "jidoka"})

	idp.Claims = map[string]any{"sub": "u-2", "groups": []string{"other", "jidoka"}}
	if _, err := login(t, s, idp, accesstest.GoodCode); err != nil {
		t.Fatalf("group member: %v", err)
	}

	idp.Claims = map[string]any{"sub": "u-3", "groups": []string{"other"}}
	if _, err := login(t, s, idp, accesstest.GoodCode); !errors.Is(err, ErrRefused) {
		t.Fatalf("non member: %v", err)
	}
}

// Z1: authentik decides; without an allow list every signed-in account enters.
func TestEveryAccountEntersWithoutAllowList(t *testing.T) {
	s, idp := setup(t, Config{})
	if !s.Available() {
		t.Fatal("OIDC must be on with issuer and client")
	}
	idp.Claims = map[string]any{"sub": "anyone"}
	if _, err := login(t, s, idp, accesstest.GoodCode); err != nil {
		t.Fatalf("login: %v", err)
	}
}

func TestOIDCOffWithoutClient(t *testing.T) {
	s := New(testDB(t), Config{})
	if s.Available() {
		t.Fatal("OIDC must be off without issuer and client")
	}
	if _, _, err := s.AuthorizeURL(context.Background(), baseURL, "/"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestReplayWrongBrowserAndBadNonce(t *testing.T) {
	s, idp := setup(t, Config{AllowSub: "u-1"})
	idp.Claims = map[string]any{"sub": "u-1"}
	ctx := context.Background()

	// Another browser holds a different state.
	_, state, _ := s.AuthorizeURL(ctx, baseURL, "/")
	params := url.Values{"state": {state}, "code": {"good"}}
	if _, err := s.Complete(ctx, baseURL, params, "other"); !errors.Is(err, ErrState) {
		t.Fatalf("wrong browser: %v", err)
	}

	// The state worked up: no second try.
	if _, err := s.Complete(ctx, baseURL, params, state); !errors.Is(err, ErrState) {
		t.Fatalf("replay: %v", err)
	}

	// A token for another nonce.
	target, state, _ := s.AuthorizeURL(ctx, baseURL, "/")
	_ = target
	idp.Nonce = "wrong"
	params = url.Values{"state": {state}, "code": {"good"}}
	if _, err := s.Complete(ctx, baseURL, params, state); !errors.Is(err, ErrFailed) {
		t.Fatalf("bad nonce: %v", err)
	}
}

func TestStateExpires(t *testing.T) {
	s, idp := setup(t, Config{AllowSub: "u-1"})
	idp.Claims = map[string]any{"sub": "u-1"}

	_, state, _ := s.AuthorizeURL(context.Background(), baseURL, "/")
	s.now = func() time.Time { return time.Now().Add(StateTTL + time.Second) }

	params := url.Values{"state": {state}, "code": {"good"}}
	if _, err := s.Complete(context.Background(), baseURL, params, state); !errors.Is(err, ErrState) {
		t.Fatalf("expired state: %v", err)
	}
}

func TestSessionSlidesAndExpires(t *testing.T) {
	s, _ := setup(t, Config{})
	start := time.Now()
	s.now = func() time.Time { return start }

	token, err := s.open("u-1", "Arian")
	if err != nil {
		t.Fatal(err)
	}
	got := Login{Token: token}

	// Used after 20 days: lives on for 30 days from then.
	s.now = func() time.Time { return start.Add(20 * 24 * time.Hour) }
	if _, err := s.Session(got.Token); err != nil {
		t.Fatalf("day 20: %v", err)
	}
	s.now = func() time.Time { return start.Add(45 * 24 * time.Hour) }
	if _, err := s.Session(got.Token); err != nil {
		t.Fatalf("day 45 after a use on day 20: %v", err)
	}

	// Unused for more than 30 days: gone.
	s.now = func() time.Time { return start.Add(80 * 24 * time.Hour) }
	if _, err := s.Session(got.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("day 80: %v", err)
	}
}

func TestSessionSurvivesNewService(t *testing.T) {
	s, _ := setup(t, Config{})
	token, _ := s.open("u-1", "Arian")
	got := Login{Token: token}

	again := New(s.db, s.cfg)
	if _, err := again.Session(got.Token); err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if _, err := again.Session("unknown"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown token: %v", err)
	}

	if err := again.Logout(got.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := again.Session(got.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after logout: %v", err)
	}
}

func TestDevLoginOffByDefault(t *testing.T) {
	s, _ := setup(t, Config{})
	if _, err := s.DevLogin(); !errors.Is(err, ErrDevOff) {
		t.Fatalf("expected ErrDevOff, got %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{"OIDC_ISSUER": "https://a/", "OIDC_ALLOW_SUB": "x", "JIDOKA_DEV": "true"}
	cfg := ConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Issuer != "https://a/" || cfg.AllowSub != "x" || cfg.Dev != devBuild {
		t.Fatalf("config: %+v", cfg)
	}
}

func TestCSRF(t *testing.T) {
	a, b := "session-a", "session-b"
	masked := CSRF(a)
	if !CheckCSRF(a, masked) {
		t.Fatal("own token refused")
	}
	if CheckCSRF(b, masked) {
		t.Fatal("token of another session accepted")
	}
	if CheckCSRF(a, "") || CheckCSRF(a, "garbage") || CheckCSRF("", masked) {
		t.Fatal("garbage accepted")
	}
	if CSRF(a) == masked {
		t.Fatal("token not masked afresh")
	}
}

func TestTokenRights(t *testing.T) {
	s, _ := setup(t, Config{})

	value, err := s.CreateToken("andon", []Right{RightRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckToken(value, RightRead); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := s.CheckToken(value, RightReload); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read token must not reload: %v", err)
	}
	if err := s.CheckToken("jdk_unknown", RightRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown: %v", err)
	}
	if err := s.CheckToken("", RightRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty: %v", err)
	}

	list, err := s.Tokens()
	if err != nil || len(list) != 1 || list[0].Name != "andon" || list[0].LastUsed == nil {
		t.Fatalf("list: %+v %v", list, err)
	}

	// The database holds the hash, not the value.
	var stored string
	if err := s.db.QueryRow("SELECT hash FROM tokens").Scan(&stored); err != nil || stored == value {
		t.Fatalf("token stored in clear: %q %v", stored, err)
	}

	if err := s.DeleteToken(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckToken(value, RightRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deleted token still works: %v", err)
	}
}

func TestCreateTokenNeedsNameAndRights(t *testing.T) {
	s, _ := setup(t, Config{})
	if _, err := s.CreateToken(" ", []Right{RightRead}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := s.CreateToken("x", nil); err == nil {
		t.Fatal("no rights accepted")
	}
}

func TestParseRights(t *testing.T) {
	if got, ok := ParseRights([]string{"read", "run"}); !ok || len(got) != 2 {
		t.Fatalf("valid: %v %v", got, ok)
	}
	if _, ok := ParseRights([]string{"read", "root"}); ok {
		t.Fatal("unknown right accepted")
	}
}

func TestDevLogin(t *testing.T) {
	s, _ := setup(t, Config{Dev: true})
	if s.Dev() != devBuild {
		t.Fatalf("dev login must follow the build tag, got %v", s.Dev())
	}
	if !devBuild {
		return
	}

	got, err := s.DevLogin()
	if err != nil {
		t.Fatal(err)
	}
	if who, err := s.Session(got.Token); err != nil || who.Sub != devSub {
		t.Fatalf("dev session: %+v %v", who, err)
	}
}
