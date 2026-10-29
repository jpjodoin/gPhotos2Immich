package reconcile

import (
	"context"
	"testing"
	"time"

	"warreth.dev/gphotos2immich/pkg/immich"
	"warreth.dev/gphotos2immich/pkg/match"
)

func TestIsOwnUpload(t *testing.T) {
	if !IsOwnUpload("gp_AF1Qip123.jpg") {
		t.Fatal("gp_ prefix should be recognised as an own upload")
	}
	for _, name := range []string{"IMG_0001.JPG", "PXL_20230101.jpg", "", "GP_upper.jpg"} {
		if IsOwnUpload(name) {
			t.Fatalf("%q should not be an own upload", name)
		}
	}
}

func TestGoogleItemIDFromName(t *testing.T) {
	cases := map[string]string{
		"gp_AF1Qip123":     "AF1Qip123",
		"gp_AF1Qip123.jpg": "AF1Qip123",
		"gp_AF1Qip123.mp4": "AF1Qip123",
		"IMG_0001.JPG":     "",
	}
	for in, want := range cases {
		if got := googleItemIDFromName(in); got != want {
			t.Fatalf("googleItemIDFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateKey(t *testing.T) {
	if got := stateKey("a/b:c"); got != "gp_a_b_c" {
		t.Fatalf("stateKey = %q", got)
	}
}

type fakeCandidateClient struct {
	byWindow map[string][]immich.Asset
	calls    int
}

func (f *fakeCandidateClient) SearchMetadata(_ context.Context, filter immich.SearchFilter) ([]immich.Asset, error) {
	f.calls++
	key := filter.TakenAfter.UTC().Format(time.RFC3339) + "|" + filter.TakenBefore.UTC().Format(time.RFC3339)
	return f.byWindow[key], nil
}

func TestFindCandidatesFiltersOwnUploads(t *testing.T) {
	takenAt := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	narrow := 3 * time.Second
	after := takenAt.Add(-narrow)
	before := takenAt.Add(narrow)

	key := after.UTC().Format(time.RFC3339) + "|" + before.UTC().Format(time.RFC3339)
	client := &fakeCandidateClient{byWindow: map[string][]immich.Asset{
		key: {
			{ID: "real", OriginalFileName: "IMG_1.JPG", Width: 10, Height: 10},
			{ID: "own", OriginalFileName: "gp_abc.jpg", Width: 10, Height: 10},
			{ID: "trashed", OriginalFileName: "IMG_2.JPG", IsTrashed: true},
			{ID: "hidden", OriginalFileName: "IMG_3.JPG", Visibility: "hidden"},
		},
	}}

	fp := match.Fingerprint{TakenAt: takenAt, HasHash: true}
	got := FindCandidates(context.Background(), client, fp, narrow, 24*time.Hour)
	if len(got) != 1 || got[0].ID != "real" {
		t.Fatalf("candidates = %+v, want only the real asset", got)
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d, want 1 (wide window must not run)", client.calls)
	}
}

func TestFindCandidatesWidensThenGivesUp(t *testing.T) {
	takenAt := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	wide := time.Hour

	many := make([]immich.Asset, 0, DefaultMaxWideCandidates+1)
	for i := 0; i <= DefaultMaxWideCandidates; i++ {
		many = append(many, immich.Asset{ID: string(rune('a' + i)), OriginalFileName: "IMG.JPG", Width: 10, Height: 10})
	}
	key := takenAt.Add(-wide).UTC().Format(time.RFC3339) + "|" + takenAt.Add(wide).UTC().Format(time.RFC3339)
	client := &fakeCandidateClient{byWindow: map[string][]immich.Asset{key: many}}

	fp := match.Fingerprint{TakenAt: takenAt, HasHash: true}
	if got := FindCandidates(context.Background(), client, fp, 3*time.Second, wide); len(got) != 0 {
		t.Fatalf("expected no candidates when the wide window is too crowded, got %d", len(got))
	}
}
