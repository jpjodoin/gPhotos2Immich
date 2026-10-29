package match

import (
	"bytes"
	"fmt"
	"image"
	"time"

	// Register the decoders used by Google shared album downloads.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// Fingerprint is the local, downloadable representation of a Google Photos
// item. It is persisted in the reconciliation ledger so a debt can be retried
// later without downloading the item again.
type Fingerprint struct {
	DHash   uint64    `json:"dhash"`
	HasHash bool      `json:"hasHash"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
	IsVideo bool      `json:"isVideo"`
	TakenAt time.Time `json:"takenAt"`
}

// FingerprintForVideo builds a fingerprint for a video. Videos are never
// downloaded just to compute a hash, so they only carry dimensions.
func FingerprintForVideo(width, height int, takenAt time.Time) Fingerprint {
	return Fingerprint{
		Width:   width,
		Height:  height,
		IsVideo: true,
		TakenAt: takenAt,
	}
}

// FingerprintFromImage builds a fingerprint from an already decoded image.
func FingerprintFromImage(img image.Image, takenAt time.Time) Fingerprint {
	fp := Fingerprint{
		HasHash: img != nil,
		TakenAt: takenAt,
	}
	if img != nil {
		fp.DHash = DHash(img)
		b := img.Bounds()
		fp.Width = b.Dx()
		fp.Height = b.Dy()
	}
	return fp
}

// FingerprintFromBytes decodes an image payload (e.g. the small "=w300"
// rendition of a Google Photos item) and builds its fingerprint.
func FingerprintFromBytes(data []byte, takenAt time.Time) (Fingerprint, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Fingerprint{}, fmt.Errorf("decode image for fingerprint: %w", err)
	}
	return FingerprintFromImage(img, takenAt), nil
}
