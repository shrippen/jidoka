package cron

import (
	"strconv"
	"strings"
	"testing"

	"jidoka/internal/i18n"
)

func TestDescribe(t *testing.T) {
	tests := []struct {
		expr, de, en string
	}{
		{"* * * * *", "jede Minute", "every minute"},
		{"*/1 * * * *", "jede Minute", "every minute"},
		{"*/5 * * * *", "alle 5 Minuten", "every 5 minutes"},
		{"15 * * * *", "stündlich um :15", "hourly at :15"},
		{"0 * * * *", "stündlich um :00", "hourly at :00"},
		{"30 6 * * *", "täglich um 06:30", "daily at 06:30"},
		{"0 6,12,18 * * *", "täglich um 06:00, 12:00, 18:00", "daily at 06:00, 12:00, 18:00"},
		{"30 8 * * 1-5", "werktags um 08:30", "weekdays at 08:30"},
		{"30 8 * * MON-FRI", "werktags um 08:30", "weekdays at 08:30"},
		{"0 3 * * 0", "wöchentlich So um 03:00", "weekly on Sun at 03:00"},
		{"0 3 * * 7", "wöchentlich So um 03:00", "weekly on Sun at 03:00"},
		{"0 3 * * 1,3", "wöchentlich Mo, Mi um 03:00", "weekly on Mon, Wed at 03:00"},
		{"0 3 * * 6,0", "wöchentlich Sa, So um 03:00", "weekly on Sat, Sun at 03:00"},
		{"0 4 1 * *", "monatlich am 1. um 04:00", "monthly on day 1 at 04:00"},
	}

	for _, tc := range tests {
		for loc, want := range map[i18n.Locale]string{i18n.LocaleDE: tc.de, i18n.LocaleEN: tc.en} {
			got, ok := Describe(tc.expr, loc)
			if !ok || got != want {
				t.Errorf("Describe(%q, %s) = %q, %v; want %q", tc.expr, loc, got, ok, want)
			}
		}
	}
}

// TestDescribeFallsBack: forms outside the common ones and invalid
// expressions give ok false, so the page shows the raw cron text.
func TestDescribeFallsBack(t *testing.T) {
	for _, expr := range []string{
		"", "0 6 * *", "0 6 * * * *", "0 0 1 1 *", "*/5 6 * * *", "0-30 * * * *",
		"0,30 6 * * *", "0 6-8 * * *", "0 */2 * * *", "60 6 * * *", "0 24 * * *",
		"0 6 32 * *", "0 6 1,15 * *", "0 6 1 * 1", "0 6 * * 8", "0 6 * * FOO", "x * * * *", "*/0 * * * *",
	} {
		if got, ok := Describe(expr, i18n.LocaleDE); ok {
			t.Errorf("Describe(%q) = %q, want fallback", expr, got)
		}
	}
}

// TestParseErrorsAreMsgs: every error kind is an i18n.Msg with a key in
// both catalogs and its params filled.
func TestParseErrorsAreMsgs(t *testing.T) {
	for _, expr := range []string{"* * * *", "*/0 * * * *", "5-1 * * * *", "x * * * *", "60 * * * *", "0 0 * * FOO"} {
		_, err := Parse(expr, berlin)
		m, ok := err.(i18n.Msg)
		if !ok || !i18n.Has(m.Key) {
			t.Errorf("Parse(%q) = %#v, want an i18n.Msg with a key", expr, err)
			continue
		}
		for _, loc := range []i18n.Locale{i18n.LocaleDE, i18n.LocaleEN} {
			if text := m.In(loc); text == m.Key || len(text) == 0 || strings.Contains(text, "{") {
				t.Errorf("Parse(%q) in %s = %q", expr, loc, text)
			}
		}
	}
}

// TestCatalogKeys: keys built or stored apart from the call (field names,
// weekdays) exist, for the scan of the catalog test cannot see them.
func TestCatalogKeys(t *testing.T) {
	keys := []string{fMinute.key, fHour.key, fDom.key, fMonth.key, fDow.key}
	for d := 0; d < daysInWeek; d++ {
		keys = append(keys, dowKey+strconv.Itoa(d))
	}
	for _, k := range keys {
		if !i18n.Has(k) {
			t.Errorf("catalog lacks %q", k)
		}
	}
}
