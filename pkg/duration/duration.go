// Package duration parses human-friendly durations. It accepts everything
// time.ParseDuration accepts, plus "d" (day) and "w" (week) units, so album
// intervals can be written as "2d" or "1w" instead of "48h" or "168h".
package duration

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Parse parses a duration string such as "12h", "2d", "1w" or "1d12h".
func Parse(value string) (time.Duration, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	return parseUnits(s)
}

func parseUnits(s string) (time.Duration, error) {
	var total time.Duration
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && (isDigit(s[i]) || s[i] == '.' || s[i] == '-') {
			i++
		}
		if start == i {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		number, err := strconv.ParseFloat(s[start:i], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}

		unitStart := i
		for i < len(s) && !isDigit(s[i]) && s[i] != '.' && s[i] != '-' {
			i++
		}
		unit := s[unitStart:i]

		multiplier, err := unitDuration(unit)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		total += time.Duration(number * float64(multiplier))
	}
	return total, nil
}

func unitDuration(unit string) (time.Duration, error) {
	switch unit {
	case "w":
		return 7 * 24 * time.Hour, nil
	case "d":
		return 24 * time.Hour, nil
	case "h":
		return time.Hour, nil
	case "m":
		return time.Minute, nil
	case "s":
		return time.Second, nil
	case "ms":
		return time.Millisecond, nil
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
