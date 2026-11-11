package redact

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

const secret = "p@ss w0rd/ü+="

func TestPlain(t *testing.T) {
	m := New([]string{secret})
	got := m.String("login " + secret + " ok")
	if got != "login *** ok" {
		t.Fatalf("got %q", got)
	}
}

func TestEncodings(t *testing.T) {
	m := New([]string{secret})
	b := []byte(secret)
	forms := map[string]string{
		"std":     base64.StdEncoding.EncodeToString(b),
		"rawstd":  base64.RawStdEncoding.EncodeToString(b),
		"url":     base64.URLEncoding.EncodeToString(b),
		"rawurl":  base64.RawURLEncoding.EncodeToString(b),
		"query":   url.QueryEscape(secret),
		"path":    url.PathEscape(secret),
		"in text": "",
	}
	delete(forms, "in text")

	for name, f := range forms {
		got := m.String("Authorization: Basic " + f + "; end")
		if strings.Contains(got, f) || !strings.Contains(got, "***") {
			t.Errorf("%s not masked: %q", name, got)
		}
	}
}

func TestShortIgnored(t *testing.T) {
	m := New([]string{"abc", "12345", ""})
	in := "abc 12345 YWJj"
	if got := m.String(in); got != in {
		t.Fatalf("got %q", got)
	}
}

func TestMinLenMasked(t *testing.T) {
	m := New([]string{"123456"})
	if got := m.String("x123456y"); got != "x***y" {
		t.Fatalf("got %q", got)
	}
}

func TestOverlap(t *testing.T) {
	m := New([]string{"secret", "secretlonger"})
	got := m.String("a secretlonger b secret c")
	if got != "a *** b *** c" {
		t.Fatalf("got %q", got)
	}
	// Order of input must not matter.
	m = New([]string{"secretlonger", "secret"})
	if got2 := m.String("secretlonger"); got2 != "***" {
		t.Fatalf("got %q", got2)
	}
}

func TestNilAndEmpty(t *testing.T) {
	var m *Masker
	if m.String("x") != "x" {
		t.Fatal("nil masker changed text")
	}
	if got := New(nil).String("x"); got != "x" {
		t.Fatalf("got %q", got)
	}
}
