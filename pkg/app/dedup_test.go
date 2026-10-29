package app

import "testing"

func TestDedupKey(t *testing.T) {
	cases := map[string]string{
		"IMG_0001.JPG":   "IMG_0001",
		"gp_abc123.jpg":  "gp_abc123",
		"VID_0001.MP4":   "VID_0001.MP4",
		"gp_abc123.mp4":  "gp_abc123.mp4",
		"no-extension":   "no-extension",
		"photo.mp.jpg":   "photo.mp",
		"archive.tar.gz": "archive.tar",
	}
	for in, want := range cases {
		if got := dedupKey(in); got != want {
			t.Fatalf("dedupKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAppendUnique(t *testing.T) {
	got := appendUnique([]string{"a", "b"}, "b")
	if len(got) != 2 {
		t.Fatalf("appendUnique duplicated an existing value: %v", got)
	}
	got = appendUnique(got, "c")
	if len(got) != 3 || got[2] != "c" {
		t.Fatalf("appendUnique did not append: %v", got)
	}
}
