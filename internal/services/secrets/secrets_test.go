package secrets

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/db"
	"jidoka/internal/repos"
)

var testMaster = crypto.Derive("test-master-key", nil)

func setup(t *testing.T) *Service {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "jidoka.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	crypto.Init(testMaster)
	t.Cleanup(func() { crypto.Init(nil) })
	return New(d)
}

func TestSetValidates(t *testing.T) {
	s := setup(t)
	for _, name := range []string{"", "lower", "1ABC", "A-B", "A B"} {
		if err := s.Set(name, "value", "x"); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: %v", name, err)
		}
	}
	if err := s.Set("OK_1", "", "x"); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty: %v", err)
	}
	if err := s.Set("OK_1", "v", "x"); err != nil {
		t.Errorf("valid: %v", err)
	}
}

func TestSetResolveListDelete(t *testing.T) {
	s := setup(t)
	if err := s.Set("DAVIS_PASSWORD", "hunter2hunter2", "arian"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("TOKEN", "abc", "arian"); err != nil {
		t.Fatal(err)
	}

	got, missing, err := s.Resolve([]string{"DAVIS_PASSWORD", "NOPE"})
	if err != nil || got["DAVIS_PASSWORD"] != "hunter2hunter2" || !slices.Equal(missing, []string{"NOPE"}) {
		t.Fatalf("resolve: %v %v %v", got, missing, err)
	}

	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Name != "DAVIS_PASSWORD" || list[0].UpdatedBy != "arian" {
		t.Fatalf("list: %+v %v", list, err)
	}

	if err := s.Delete("TOKEN", "arian"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("TOKEN", "arian"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestStoredValueIsSealed(t *testing.T) {
	s := setup(t)
	if err := s.Set("API_KEY", "plain-text-value", "a"); err != nil {
		t.Fatal(err)
	}
	enc, err := repos.GetSecretEnc(s.db, "API_KEY")
	if err != nil || strings.Contains(string(enc), "plain-text-value") {
		t.Fatalf("stored in clear: %q %v", enc, err)
	}
}

func TestResolveWithoutKey(t *testing.T) {
	s := setup(t)
	if err := s.Set("API_KEY", "some-secret-value", "a"); err != nil {
		t.Fatal(err)
	}
	crypto.Init(nil)

	if _, _, err := s.Resolve([]string{"API_KEY"}); !errors.Is(err, crypto.ErrMissingKey) {
		t.Fatalf("resolve: %v", err)
	}
	if err := s.Set("OTHER", "value", "a"); !errors.Is(err, crypto.ErrMissingKey) {
		t.Fatalf("set: %v", err)
	}
	if got, missing, err := s.Resolve(nil); err != nil || len(got) != 0 || missing != nil {
		t.Fatalf("no names need no key: %v %v %v", got, missing, err)
	}
}

func TestResolveWrongKey(t *testing.T) {
	s := setup(t)
	if err := s.Set("API_KEY", "some-secret-value", "a"); err != nil {
		t.Fatal(err)
	}
	crypto.Init(crypto.Derive("another-master-key", nil))

	_, _, err := s.Resolve([]string{"API_KEY"})
	if err == nil || !strings.Contains(err.Error(), "API_KEY") || strings.Contains(err.Error(), "some-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuditNeverHoldsValue(t *testing.T) {
	s := setup(t)
	const value = "very-secret-value"
	steps := []func() error{
		func() error { return s.Set("API_KEY", value, "arian") },
		func() error { return s.Set("API_KEY", value+"2", "ines") },
		func() error { return s.Delete("API_KEY", "arian") },
	}
	for i, step := range steps {
		s.now = func() time.Time { return time.Date(2026, 11, 11, 10, i, 0, 0, time.UTC) }
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}

	got, err := repos.RecentAudit(s.db, 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("audit: %+v %v", got, err)
	}

	// Newest first: delete, replace, set.
	want := []struct{ actor, action string }{{"arian", "delete"}, {"ines", "replace"}, {"arian", "set"}}
	for i, w := range want {
		e := got[i]
		if e.Actor != w.actor || e.Action != w.action || e.Target != "API_KEY" {
			t.Errorf("entry %d: %+v", i, e)
		}
		if strings.Contains(e.Actor+e.Action+e.Target, "very-secret") {
			t.Errorf("value in audit: %+v", e)
		}
	}
}

func TestValuesMinLength(t *testing.T) {
	s := setup(t)
	for name, v := range map[string]string{"SHORT": "12345", "EDGE": "123456", "LONG": "long-enough-value"} {
		if err := s.Set(name, v, "a"); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Values()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"123456", "long-enough-value"}) {
		t.Fatalf("values: %v", got)
	}
}

func TestRotate(t *testing.T) {
	s := setup(t)
	if err := s.Set("API_KEY", "some-secret-value", "a"); err != nil {
		t.Fatal(err)
	}
	ping, err := crypto.Encrypt("ping-token", crypto.PurposePing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.PutPing(s.db, "feed", "hash", ping, time.Now()); err != nil {
		t.Fatal(err)
	}

	next := crypto.Derive("the-new-master-key", nil)
	if err := s.Rotate(next); err != nil {
		t.Fatal(err)
	}

	// Under the old key the values no longer open; under the new one they do.
	if _, _, err := s.Resolve([]string{"API_KEY"}); err == nil {
		t.Fatal("old key still opens")
	}
	crypto.Init(next)
	got, _, err := s.Resolve([]string{"API_KEY"})
	if err != nil || got["API_KEY"] != "some-secret-value" {
		t.Fatalf("new key: %v %v", got, err)
	}
	p, _ := repos.GetPing(s.db, "feed")
	if v, err := crypto.Decrypt(p.Enc, crypto.PurposePing); err != nil || v != "ping-token" {
		t.Fatalf("ping: %q %v", v, err)
	}
	if list, _ := s.List(); list[0].UpdatedBy != "a" {
		t.Fatal("rotation changed the author")
	}
}

func TestRotateWithoutKey(t *testing.T) {
	s := setup(t)
	crypto.Init(nil)
	if err := s.Rotate(testMaster); !errors.Is(err, crypto.ErrMissingKey) {
		t.Fatalf("err = %v", err)
	}
}
