// Package cron is a tiny 5-field cron matcher + runner. It exists so panels
// can schedule power actions on servers without pulling in a heavy dependency.
// Expression: "min hour dom month dow" — supports * , ranges - and steps /.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Expr struct {
	Minute []int
	Hour   []int
	Day    []int // day of month 1-31
	Month  []int // 1-12
	Dow    []int // 0-6 (0=Sunday)
}

// Parse compiles a 5-field cron expression.
func Parse(s string) (*Expr, error) {
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron needs 5 fields, got %d", len(fields))
	}
	e := &Expr{}
	var err error
	if e.Minute, err = field(fields[0], 0, 59); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if e.Hour, err = field(fields[1], 0, 23); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if e.Day, err = field(fields[2], 1, 31); err != nil {
		return nil, fmt.Errorf("dom: %w", err)
	}
	if e.Month, err = field(fields[3], 1, 12); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if e.Dow, err = field(fields[4], 0, 7); err != nil {
		return nil, fmt.Errorf("dow: %w", err)
	}
	return e, nil
}

func field(s string, min, max int) ([]int, error) {
	out := []int{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty field part")
		}
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			v, err := strconv.Atoi(part[i+1:])
			if err != nil || v < 1 {
				return nil, fmt.Errorf("bad step in %q", part)
			}
			step = v
			part = part[:i]
		}
		lo, hi := min, max
		if part != "*" {
			if i := strings.Index(part, "-"); i >= 0 {
				a, err1 := strconv.Atoi(part[:i])
				b, err2 := strconv.Atoi(part[i+1:])
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("bad range in %q", part)
				}
				lo, hi = a, b
			} else {
				v, err := strconv.Atoi(part)
				if err != nil {
					return nil, fmt.Errorf("bad value %q", part)
				}
				lo, hi = v, v
			}
		}
		if lo > hi || lo < min || hi > max {
			return nil, fmt.Errorf("value out of range in %q (min %d max %d)", part, min, max)
		}
		for v := lo; v <= hi; v += step {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no values")
	}
	return out, nil
}

func contains(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Match reports whether t matches the expression. Dow 7 == 0 (Sunday).
func (e *Expr) Match(t time.Time) bool {
	if !contains(e.Minute, t.Minute()) {
		return false
	}
	if !contains(e.Hour, t.Hour()) {
		return false
	}
	if !contains(e.Month, int(t.Month())) {
		return false
	}
	domOK := contains(e.Day, t.Day())
	dow := int(t.Weekday())
	if dow == 7 {
		dow = 0
	}
	dowOK := contains(e.Dow, dow)
	// standard cron: if both dom and dow are restricted, match either;
	// if only one is restricted, that one must match.
	if len(e.Day) < 31 && len(e.Dow) < 7 {
		return domOK || dowOK
	}
	return domOK && dowOK
}
