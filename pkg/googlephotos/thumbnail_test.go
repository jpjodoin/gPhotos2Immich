package googlephotos

import "testing"

func TestMediaBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://lh3.googleusercontent.com/pw/ABC":           "https://lh3.googleusercontent.com/pw/ABC",
		"https://lh3.googleusercontent.com/pw/ABC=d":         "https://lh3.googleusercontent.com/pw/ABC",
		"https://lh3.googleusercontent.com/pw/ABC=dv":        "https://lh3.googleusercontent.com/pw/ABC",
		"https://lh3.googleusercontent.com/pw/ABC=w500":      "https://lh3.googleusercontent.com/pw/ABC",
		"https://lh3.googleusercontent.com/pw/ABC=w500-h300": "https://lh3.googleusercontent.com/pw/ABC",
		"https://lh3.googleusercontent.com/pw/ABC=":          "https://lh3.googleusercontent.com/pw/ABC=",
		"https://lh3.googleusercontent.com/pw/ABC?width=500": "https://lh3.googleusercontent.com/pw/ABC?width=500",
		"https://lh3.googleusercontent.com/pw/ABC?x=1":       "https://lh3.googleusercontent.com/pw/ABC?x=1",
	}
	for in, want := range cases {
		if got := mediaBaseURL(in); got != want {
			t.Fatalf("mediaBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
