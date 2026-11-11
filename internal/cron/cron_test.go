package cron

import (
	"strings"
	"testing"
	"time"
)

var berlin = mustZone("Europe/Berlin")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}

	return loc
}

func at(loc *time.Location, y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, loc)
}

func mustParse(t *testing.T, expr string, loc *time.Location) *Schedule {
	t.Helper()

	s, err := Parse(expr, loc)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}

	return s
}

func TestNext(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{"every 5", "*/5 * * * *", at(berlin, 2026, 5, 1, 10, 3), at(berlin, 2026, 5, 1, 10, 5)},
		{"every 5 strictly after", "*/5 * * * *", at(berlin, 2026, 5, 1, 10, 5), at(berlin, 2026, 5, 1, 10, 10)},
		{"sunday 4", "0 4 * * 0", at(berlin, 2026, 11, 11, 12, 0), at(berlin, 2026, 11, 15, 4, 0)},
		{"sunday as 7", "0 4 * * 7", at(berlin, 2026, 11, 11, 12, 0), at(berlin, 2026, 11, 15, 4, 0)},
		{"daily 6", "0 6 * * *", at(berlin, 2026, 11, 11, 6, 0), at(berlin, 2026, 11, 12, 6, 0)},
		{"range and list", "0 9-11,15 * * MON-FRI", at(berlin, 2026, 11, 13, 11, 30), at(berlin, 2026, 11, 13, 15, 0)},
		{"names", "0 0 1 JAN *", at(berlin, 2026, 3, 1, 0, 0), at(berlin, 2027, 1, 1, 0, 0)},
		{"step range", "10-20/5 * * * *", at(berlin, 2026, 1, 1, 0, 0), at(berlin, 2026, 1, 1, 0, 10)},
		{"dom or dow", "0 0 13 * FRI", at(berlin, 2026, 11, 11, 1, 0), at(berlin, 2026, 11, 13, 0, 0)},
		{"dom or dow, dom side", "0 0 13 * FRI", at(berlin, 2026, 11, 14, 1, 0), at(berlin, 2026, 11, 20, 0, 0)},
		{"dow star keeps dom", "0 0 13 * *", at(berlin, 2026, 11, 14, 1, 0), at(berlin, 2026, 12, 13, 0, 0)},
		// 2026-03-29 02:00 CET -> 03:00 CEST.
		{"gap fires at 03:00", "30 2 * * *", at(berlin, 2026, 3, 28, 12, 0), at(berlin, 2026, 3, 29, 3, 0)},
		{"gap fires once", "30 2 * * *", at(berlin, 2026, 3, 29, 3, 0), at(berlin, 2026, 3, 30, 2, 30)},
		{"gap, every 15 min", "*/15 * * * *", at(berlin, 2026, 3, 29, 1, 50), at(berlin, 2026, 3, 29, 3, 0)},
		// 2026-10-25 03:00 CEST -> 02:00 CET.
		{"overlap first", "30 2 * * *", at(berlin, 2026, 10, 24, 12, 0), time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)},
		{"overlap not twice", "30 2 * * *", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), at(berlin, 2026, 10, 26, 2, 30)},
		{"overlap hourly", "0 * * * *", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), at(berlin, 2026, 10, 25, 3, 0)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mustParse(t, tc.expr, berlin).Next(tc.after)
			if !got.Equal(tc.want) {
				t.Errorf("Next = %v, want %v", got.In(berlin), tc.want.In(berlin))
			}
		})
	}
}

func TestOverlapAfterSecondHour(t *testing.T) {
	// 02:20 CET (second occurrence) -> the 02:30 slot already ran.
	secondTwo := time.Date(2026, 10, 25, 1, 20, 0, 0, time.UTC)
	got := mustParse(t, "30 2 * * *", berlin).Next(secondTwo)

	want := at(berlin, 2026, 10, 26, 2, 30)
	if !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got.In(berlin), want)
	}
}

func TestLast(t *testing.T) {
	tests := []struct {
		name   string
		expr   string
		before time.Time
		want   time.Time
	}{
		{"daily", "0 6 * * *", at(berlin, 2026, 11, 11, 12, 0), at(berlin, 2026, 11, 11, 6, 0)},
		{"inclusive", "0 6 * * *", at(berlin, 2026, 11, 11, 6, 0), at(berlin, 2026, 11, 11, 6, 0)},
		{"yesterday", "0 6 * * *", at(berlin, 2026, 11, 11, 5, 0), at(berlin, 2026, 11, 10, 6, 0)},
		{"sunday", "0 4 * * 0", at(berlin, 2026, 11, 11, 12, 0), at(berlin, 2026, 11, 8, 4, 0)},
		{"gap slot", "30 2 * * *", at(berlin, 2026, 3, 29, 3, 10), at(berlin, 2026, 3, 29, 3, 0)},
		{"before gap slot", "30 2 * * *", at(berlin, 2026, 3, 29, 1, 50), at(berlin, 2026, 3, 28, 2, 30)},
		{"overlap, second hour", "30 2 * * *", time.Date(2026, 10, 25, 1, 20, 0, 0, time.UTC), time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mustParse(t, tc.expr, berlin).Last(tc.before)
			if !got.Equal(tc.want) {
				t.Errorf("Last = %v, want %v", got.In(berlin), tc.want.In(berlin))
			}
		})
	}
}

func TestNeverFires(t *testing.T) {
	got := mustParse(t, "0 0 31 2 *", berlin).Next(at(berlin, 2026, 1, 1, 0, 0))
	if !got.IsZero() {
		t.Errorf("Next = %v, want zero", got)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"0 25 * * *", "hour"},
		{"60 * * * *", "minute"},
		{"0 0 32 * *", "day of month"},
		{"0 0 * 13 *", "month"},
		{"0 0 * * 8", "day of week"},
		{"0 0 * * FOO", "day of week"},
		{"*/0 * * * *", "minute"},
		{"5-1 * * * *", "minute"},
		{"* * * *", "5 fields"},
		{"* * * * * *", "5 fields"},
	}

	for _, tc := range tests {
		_, err := Parse(tc.expr, berlin)
		if err == nil {
			t.Errorf("Parse(%q): want error", tc.expr)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Parse(%q) = %q, want it to mention %q", tc.expr, err, tc.want)
		}
	}
}
