package cron

import (
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		expr string
		t    time.Time
		want bool
	}{
		{"* * * * *", time.Date(2026, 10, 30, 14, 37, 0, 0, time.UTC), true},
		{"0 4 * * *", time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC), true},
		{"0 4 * * *", time.Date(2026, 10, 30, 4, 1, 0, 0, time.UTC), false},
		{"0 4 * * *", time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), false},
		{"*/5 * * * *", time.Date(2026, 10, 30, 14, 35, 0, 0, time.UTC), true},
		{"*/5 * * * *", time.Date(2026, 10, 30, 14, 37, 0, 0, time.UTC), false},
		{"30 2 * * 1-5", time.Date(2026, 10, 30, 2, 30, 0, 0, time.UTC), true}, // Friday
		{"30 2 * * 1-5", time.Date(2026, 11, 1, 2, 30, 0, 0, time.UTC), false}, // Sunday
		{"0 0 1 * *", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), true},      // 1st of month
		{"0 0 1 * *", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), false},
	}
	for _, c := range cases {
		e, err := Parse(c.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.expr, err)
			continue
		}
		if got := e.Match(c.t); got != c.want {
			t.Errorf("Match(%q @ %v) = %v, want %v", c.expr, c.t, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, expr := range []string{"", "* * *", "* * * * * *", "0 61 * * *", "a * * * *", "*/0 * * * *"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) should error", expr)
		}
	}
}
