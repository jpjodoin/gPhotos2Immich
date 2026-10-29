package match

import (
	"image"
	"math/bits"
)

// dhashCols and dhashRows define the 9x8 grid used by the difference hash.
// The image is stretched to 9x8 (aspect ratio is ignored on purpose); the
// aspect ratio is checked separately against the candidate dimensions.
const (
	dhashCols = 9
	dhashRows = 8
)

// DHash computes a 64-bit difference hash of the image.
//
// The image is reduced to a 9x8 grayscale grid with an area-average filter,
// then each pixel is compared with its right neighbour. Downscaling to a fixed
// tiny grid makes the hash stable across JPEG re-encoding and moderate
// resizing, which is exactly what we need to recognise a photo that Google
// re-encoded when serving a shared album.
func DHash(img image.Image) uint64 {
	grid := grayGrid(img, dhashCols, dhashRows)

	var hash uint64
	var bit uint
	for y := 0; y < dhashRows; y++ {
		for x := 0; x < dhashCols-1; x++ {
			left := grid[y*dhashCols+x]
			right := grid[y*dhashCols+x+1]
			if left > right {
				hash |= 1 << bit
			}
			bit++
		}
	}
	return hash
}

// Hamming returns the number of differing bits between two hashes.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// grayGrid reduces img to a cols x rows luminance grid using an area-average
// (box) filter. Cells that receive no source pixel keep a zero value.
func grayGrid(img image.Image, cols, rows int) []float64 {
	grid := make([]float64, cols*rows)
	if img == nil {
		return grid
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return grid
	}

	counts := make([]float64, cols*rows)
	for y := 0; y < h; y++ {
		gy := y * rows / h
		if gy >= rows {
			gy = rows - 1
		}
		for x := 0; x < w; x++ {
			gx := x * cols / w
			if gx >= cols {
				gx = cols - 1
			}
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// RGBA returns 16-bit values; the scale does not matter for a
			// relative comparison, so we keep them as-is.
			lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
			idx := gy*cols + gx
			grid[idx] += lum
			counts[idx]++
		}
	}

	for i := range grid {
		if counts[i] > 0 {
			grid[i] /= counts[i]
		}
	}
	return grid
}
