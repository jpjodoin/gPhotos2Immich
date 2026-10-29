package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"12h", 12 * time.Hour},
		{"2d", 48 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"90d", 90 * 24 * time.Hour},
		{"1d12h", 36 * time.Hour},
		{"2160h", 90 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
	}
	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Parse(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, in := range []string{"nope", "12x", "d", "1h2"} {
		if _, err := Parse(in); err == nil {
			t.Fatalf("Parse(%q) should fail", in)
		}
	}
}
