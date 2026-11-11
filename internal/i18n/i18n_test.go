package i18n

import (
	"testing"
	"time"
)

func TestTFallsBackToKey(t *testing.T) {
	if got := T("no.such.key", LocaleEN, nil); got != "no.such.key" {
		t.Fatalf("got %q", got)
	}
	if got := T("nav.board", LocaleEN, nil); got != "Board" {
		t.Fatalf("got %q", got)
	}
}

func TestTParamsAndSingular(t *testing.T) {
	if got := T("access.count", LocaleDE, map[string]any{"count": 2}); got != "2 Tokens" {
		t.Fatalf("got %q", got)
	}
	if got := T("access.count", LocaleDE, map[string]any{"count": 1}); got != "1 Token" {
		t.Fatalf("got %q", got)
	}
	if got := T("access.count", LocaleDE, map[string]any{}); got != "{count} Tokens" {
		t.Fatalf("unknown placeholder must stay visible, got %q", got)
	}
}

func TestPick(t *testing.T) {
	cases := map[string]Locale{
		"":                 LocaleDE,
		"en-US,en;q=0.9":   LocaleEN,
		"fr, de;q=0.5":     LocaleDE,
		"fr, en;q=0.5, de": LocaleEN,
		"EN":               LocaleEN,
		"fr-CA":            LocaleDE,
	}
	for header, want := range cases {
		if got := Pick(header); got != want {
			t.Errorf("Pick(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestDay(t *testing.T) {
	at := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if got := Day(at, LocaleDE); got != "05.03.2026" {
		t.Fatalf("got %q", got)
	}
	if got := Day(at, LocaleEN); got != "Mar 5, 2026" {
		t.Fatalf("got %q", got)
	}
}
