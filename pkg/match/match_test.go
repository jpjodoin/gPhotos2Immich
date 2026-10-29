package match

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
	"time"
)

// photoImage builds a smooth, photo-like image. The main blob moves with the
// variant, which gives a clearly different perceptual hash while staying
// resilient to JPEG re-encoding and downscaling.
func photoImage(w, h, variant int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cx := float64(w/4 + (variant%3)*w/4)
	cy := float64(h / 2)
	radius := math.Min(float64(w), float64(h)) / 2.5
	wavesX := 1 + 0.5*float64(variant)
	wavesY := 1 + 0.35*float64(variant)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Low-frequency pattern that changes significantly with the variant
			// but stays smooth enough to survive JPEG and downscaling.
			base := 128 +
				55*math.Sin(2*math.Pi*float64(x)/float64(w)*wavesX) +
				35*math.Cos(2*math.Pi*float64(y)/float64(h)*wavesY)

			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			base += 70 * math.Exp(-(d*d)/(2*radius*radius))

			v := clampByte(base)
			img.Set(x, y, color.RGBA{R: v, G: uint8(float64(v) * 0.8), B: 255 - v, A: 255})
		}
	}
	return img
}

func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func nearestResize(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*b.Dy()/h
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*b.Dx()/w
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func jpegRoundTrip(t *testing.T, img image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	out, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	return out
}

type fakeLoader struct {
	imgs map[string]image.Image
	errs map[string]error
}

func (f fakeLoader) Thumbnail(_ context.Context, id string) (image.Image, error) {
	if err, ok := f.errs[id]; ok {
		return nil, err
	}
	img, ok := f.imgs[id]
	if !ok {
		return nil, fmt.Errorf("asset %s not found", id)
	}
	return img, nil
}

func TestDHashStableAcrossJPEG(t *testing.T) {
	base := photoImage(240, 180, 1)
	want := DHash(base)

	for _, q := range []int{85, 60, 40, 25} {
		got := DHash(jpegRoundTrip(t, base, q))
		if d := Hamming(want, got); d > 6 {
			t.Errorf("jpeg quality %d: hamming %d, want <= 6", q, d)
		}
	}
}

func TestDHashStableAcrossResize(t *testing.T) {
	base := photoImage(240, 180, 1)
	want := DHash(base)

	for _, scale := range []float64{0.75, 0.5, 0.25} {
		resized := nearestResize(base, int(240*scale), int(180*scale))
		got := DHash(resized)
		if d := Hamming(want, got); d > 6 {
			t.Errorf("scale %.2f: hamming %d, want <= 6", scale, d)
		}
	}
}

func TestDHashDistinguishesDifferentImages(t *testing.T) {
	a := DHash(photoImage(240, 180, 0))
	b := DHash(photoImage(240, 180, 2))
	if d := Hamming(a, b); d <= 14 {
		t.Fatalf("hamming %d for different images, expected a clear gap", d)
	}
}

func TestDecideMatchSingleCandidate(t *testing.T) {
	img := photoImage(240, 180, 1)
	fp := FingerprintFromImage(img, time.Now())

	res := Decide(context.Background(), fp, []Candidate{{
		ID: "asset-1", FileName: "IMG_0001.JPG", Width: 240, Height: 180,
	}}, fakeLoader{imgs: map[string]image.Image{"asset-1": img}}, Options{})

	if res.Verdict != VerdictMatch {
		t.Fatalf("verdict = %q (distance %d), want match", res.Verdict, res.Distance)
	}
	if res.AssetID != "asset-1" {
		t.Fatalf("asset = %q, want asset-1", res.AssetID)
	}
}

func TestDecideMatchesReencodedCandidate(t *testing.T) {
	img := photoImage(240, 180, 1)
	fp := FingerprintFromImage(img, time.Now())
	reencoded := jpegRoundTrip(t, img, 40)

	res := Decide(context.Background(), fp, []Candidate{{
		ID: "asset-1", Width: 240, Height: 180,
	}}, fakeLoader{imgs: map[string]image.Image{"asset-1": reencoded}}, Options{})

	if res.Verdict != VerdictMatch {
		t.Fatalf("verdict = %q (distance %d), want match", res.Verdict, res.Distance)
	}
}

func TestDecideAmbiguousKeepsDuplicate(t *testing.T) {
	img := photoImage(240, 180, 1)
	fp := FingerprintFromImage(img, time.Now())

	res := Decide(context.Background(), fp, []Candidate{
		{ID: "a", Width: 240, Height: 180},
		{ID: "b", Width: 240, Height: 180},
	}, fakeLoader{imgs: map[string]image.Image{"a": img, "b": img}}, Options{})

	if res.Verdict != VerdictAmbiguous {
		t.Fatalf("verdict = %q, want ambiguous", res.Verdict)
	}
}

func TestDecideNoMatchDifferentImage(t *testing.T) {
	img := photoImage(240, 180, 1)
	other := photoImage(240, 180, 2)
	fp := FingerprintFromImage(img, time.Now())

	if d := Hamming(fp.DHash, DHash(other)); d <= 14 {
		t.Skipf("fixture images are too similar (hamming %d)", d)
	}

	res := Decide(context.Background(), fp, []Candidate{{
		ID: "other", Width: 240, Height: 180,
	}}, fakeLoader{imgs: map[string]image.Image{"other": other}}, Options{})

	if res.Verdict != VerdictNoMatch {
		t.Fatalf("verdict = %q, want no_match", res.Verdict)
	}
}

func TestDecideFiltersIncompatibleCandidates(t *testing.T) {
	img := photoImage(240, 180, 1)
	fp := FingerprintFromImage(img, time.Now())

	res := Decide(context.Background(), fp, []Candidate{
		{ID: "portrait", Width: 100, Height: 300},
		{ID: "video", Width: 240, Height: 180, IsVideo: true},
	}, fakeLoader{imgs: map[string]image.Image{"portrait": img, "video": img}}, Options{})

	if res.Verdict != VerdictNoMatch {
		t.Fatalf("verdict = %q, want no_match", res.Verdict)
	}
}

func TestDecideSkipsUnloadableCandidates(t *testing.T) {
	img := photoImage(240, 180, 1)
	fp := FingerprintFromImage(img, time.Now())

	res := Decide(context.Background(), fp, []Candidate{{ID: "broken", Width: 240, Height: 180}},
		fakeLoader{errs: map[string]error{"broken": fmt.Errorf("boom")}}, Options{})

	if res.Verdict != VerdictNoMatch {
		t.Fatalf("verdict = %q, want no_match", res.Verdict)
	}
	if res.LoadErrors != 1 {
		t.Fatalf("LoadErrors = %d, want 1", res.LoadErrors)
	}
}

func TestDecideVideosUseDimensions(t *testing.T) {
	fp := FingerprintForVideo(1920, 1080, time.Now())

	single := Decide(context.Background(), fp, []Candidate{
		{ID: "vid", Width: 1920, Height: 1080, IsVideo: true},
	}, nil, Options{})
	if single.Verdict != VerdictMatch {
		t.Fatalf("single video verdict = %q, want match", single.Verdict)
	}

	twins := Decide(context.Background(), fp, []Candidate{
		{ID: "v1", Width: 1920, Height: 1080, IsVideo: true},
		{ID: "v2", Width: 1920, Height: 1080, IsVideo: true},
	}, nil, Options{})
	if twins.Verdict != VerdictAmbiguous {
		t.Fatalf("twin videos verdict = %q, want ambiguous", twins.Verdict)
	}

	unknownDims := Decide(context.Background(), fp, []Candidate{
		{ID: "v3", IsVideo: true},
	}, nil, Options{})
	if unknownDims.Verdict != VerdictNoMatch {
		t.Fatalf("unknown dims verdict = %q, want no_match", unknownDims.Verdict)
	}
}
