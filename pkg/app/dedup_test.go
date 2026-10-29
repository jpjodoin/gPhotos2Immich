package app

import "testing"

func TestIsOwnUpload(t *testing.T) {
	if !isOwnUpload("gp_AF1Qip123.jpg") {
		t.Fatal("gp_ prefix should be recognised as an own upload")
	}
	for _, name := range []string{"IMG_0001.JPG", "PXL_20230101.jpg", "", "GP_upper.jpg"} {
		if isOwnUpload(name) {
			t.Fatalf("%q should not be an own upload", name)
		}
	}
}

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
