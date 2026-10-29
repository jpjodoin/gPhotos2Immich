package match

import (
	"context"
	"image"
	"math"
	"sort"
	"time"
)

// Candidate is an Immich asset that may be the same media item as the Google
// Photos item being synced.
type Candidate struct {
	ID       string
	FileName string
	Width    int
	Height   int
	IsVideo  bool
	TakenAt  time.Time
}

// ThumbnailLoader returns a small rendition of an Immich asset. The matcher
// only needs a low-resolution image (a 9x8 grid is computed from it), so
// callers should fetch Immich's "thumbnail" size, not the original.
type ThumbnailLoader interface {
	Thumbnail(ctx context.Context, assetID string) (image.Image, error)
}

// Verdict is the outcome of a matching decision.
type Verdict string

const (
	// VerdictNoMatch means no candidate is close enough; the caller should upload.
	VerdictNoMatch Verdict = "no_match"
	// VerdictMatch means a single candidate is confidently the same media item;
	// the caller must reuse it and not upload.
	VerdictMatch Verdict = "match"
	// VerdictAmbiguous means several candidates are close; the caller keeps a
	// duplicate and logs the case instead of guessing.
	VerdictAmbiguous Verdict = "ambiguous"
)

// Scored is a candidate with its Hamming distance to the fingerprint.
type Scored struct {
	Candidate
	Distance int
}

// Result is what Decide returns.
type Result struct {
	Verdict        Verdict
	AssetID        string
	Distance       int
	SecondDistance int
	Scored         []Scored
	LoadErrors     int
}

// Options tunes the decision. Zero values are replaced by sane defaults.
type Options struct {
	// MaxDistance is the maximum Hamming distance (out of 64) for a candidate
	// to be considered the same photo. Default 14.
	MaxDistance int
	// MinMargin is the minimum distance gap between the best and the second
	// best candidate. A smaller gap means two photos are too similar to choose
	// automatically. Default 4.
	MinMargin int
	// AspectTolerance is the relative difference allowed between aspect ratios
	// when exact dimensions do not match (shared albums may be downscaled).
	// Default 0.03.
	AspectTolerance float64
	// DimensionTolerance is the per-axis pixel tolerance for an exact size
	// match. Default 4.
	DimensionTolerance int
	// MaxScored caps the number of scored candidates kept in Result for logs.
	// Default 10.
	MaxScored int
}

func (o Options) withDefaults() Options {
	if o.MaxDistance <= 0 {
		o.MaxDistance = 14
	}
	if o.MinMargin <= 0 {
		o.MinMargin = 4
	}
	if o.AspectTolerance <= 0 {
		o.AspectTolerance = 0.03
	}
	if o.DimensionTolerance <= 0 {
		o.DimensionTolerance = 4
	}
	if o.MaxScored <= 0 {
		o.MaxScored = 10
	}
	return o
}

// Decide compares a Google Photos fingerprint with Immich candidates and
// returns a conservative verdict. It only returns VerdictMatch when a single
// candidate is clearly better than every other one; anything doubtful is
// Ambiguous, so the caller keeps a duplicate rather than risking a wrong album
// assignment.
//
// Videos carry no visual fingerprint (their bytes are never downloaded just to
// hash them), so they are matched on exact dimensions and a unique timestamp
// candidate instead.
func Decide(ctx context.Context, fp Fingerprint, candidates []Candidate, loader ThumbnailLoader, opt Options) Result {
	opt = opt.withDefaults()

	if !fp.HasHash {
		return decideWithoutHash(fp, candidates, opt)
	}

	scored := make([]Scored, 0, len(candidates))
	loadErrors := 0
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		if !compatible(fp, c, opt) {
			continue
		}
		img, err := loader.Thumbnail(ctx, c.ID)
		if err != nil || img == nil {
			loadErrors++
			continue
		}
		scored = append(scored, Scored{
			Candidate: c,
			Distance:  Hamming(fp.DHash, DHash(img)),
		})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Distance != scored[j].Distance {
			return scored[i].Distance < scored[j].Distance
		}
		return scored[i].ID < scored[j].ID
	})

	res := Result{Verdict: VerdictNoMatch, LoadErrors: loadErrors}
	if len(scored) == 0 {
		return res
	}

	keep := opt.MaxScored
	if len(scored) < keep {
		keep = len(scored)
	}
	res.Scored = scored[:keep]
	res.Distance = scored[0].Distance
	if len(scored) > 1 {
		res.SecondDistance = scored[1].Distance
	}

	if scored[0].Distance > opt.MaxDistance {
		return res
	}

	res.AssetID = scored[0].ID
	if len(scored) == 1 || scored[1].Distance-scored[0].Distance >= opt.MinMargin {
		res.Verdict = VerdictMatch
	} else {
		res.Verdict = VerdictAmbiguous
	}
	return res
}

// decideWithoutHash handles videos (and any item without a visual fingerprint).
// It only matches when dimensions are known on both sides and exactly one
// candidate is compatible; otherwise the caller uploads.
func decideWithoutHash(fp Fingerprint, candidates []Candidate, opt Options) Result {
	res := Result{Verdict: VerdictNoMatch}
	if fp.Width <= 0 || fp.Height <= 0 {
		return res
	}

	compatibleCandidates := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if !compatible(fp, c, opt) {
			continue
		}
		if c.Width <= 0 || c.Height <= 0 {
			continue
		}
		compatibleCandidates = append(compatibleCandidates, c)
	}

	res.Scored = make([]Scored, 0, len(compatibleCandidates))
	for _, c := range compatibleCandidates {
		res.Scored = append(res.Scored, Scored{Candidate: c})
	}
	if len(res.Scored) > opt.MaxScored {
		res.Scored = res.Scored[:opt.MaxScored]
	}

	if len(compatibleCandidates) == 1 {
		res.Verdict = VerdictMatch
		res.AssetID = compatibleCandidates[0].ID
	} else if len(compatibleCandidates) > 1 {
		res.Verdict = VerdictAmbiguous
	}
	return res
}

// compatible filters out candidates that cannot be the same media item:
// different media kind, or incompatible dimensions/aspect ratio.
func compatible(fp Fingerprint, c Candidate, opt Options) bool {
	if fp.IsVideo != c.IsVideo {
		return false
	}
	if !fp.HasHash {
		// Videos: exact dimensions are required (tolerance kept tight).
		return absInt(fp.Width-c.Width) <= 1 && absInt(fp.Height-c.Height) <= 1
	}
	if fp.Width <= 0 || fp.Height <= 0 || c.Width <= 0 || c.Height <= 0 {
		// Missing dimensions on either side: do not reject on size alone.
		return true
	}

	if absInt(fp.Width-c.Width) <= opt.DimensionTolerance &&
		absInt(fp.Height-c.Height) <= opt.DimensionTolerance {
		return true
	}

	a := float64(fp.Width) / float64(fp.Height)
	b := float64(c.Width) / float64(c.Height)
	denom := math.Max(a, b)
	if denom == 0 {
		return false
	}
	return math.Abs(a-b)/denom <= opt.AspectTolerance
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
