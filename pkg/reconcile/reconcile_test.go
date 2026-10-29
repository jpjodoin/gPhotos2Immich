package reconcile

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"warreth.dev/gphotos2immich/pkg/immich"
	"warreth.dev/gphotos2immich/pkg/ledger"
	"warreth.dev/gphotos2immich/pkg/match"
)

// testImage builds a smooth, photo-like image so the perceptual hash is stable.
func testImage(w, h, variant int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cx := float64(w/4 + variant*w/4)
	r := float64(w) / 2.5
	wavesX := 1 + 0.5*float64(variant)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			base := 128 + 55*math.Sin(2*math.Pi*float64(x)/float64(w)*wavesX)
			d := math.Hypot(float64(x)-cx, float64(y)-float64(h/2))
			base += 70 * math.Exp(-(d*d)/(2*r*r))
			v := uint8(math.Max(0, math.Min(255, base)))
			img.Set(x, y, color.RGBA{R: v, G: v / 2, B: 255 - v, A: 255})
		}
	}
	return img
}

type fakeClient struct {
	assets  []immich.Asset
	thumbs  map[string]image.Image
	added   []string
	deleted []string
	own     map[string]string
}

func (f *fakeClient) SearchMetadata(context.Context, immich.SearchFilter) ([]immich.Asset, error) {
	return f.assets, nil
}

func (f *fakeClient) GetAsset(_ context.Context, id string) (*immich.Asset, error) {
	for _, a := range f.assets {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *fakeClient) Thumbnail(_ context.Context, id string) (image.Image, error) {
	img, ok := f.thumbs[id]
	if !ok {
		return nil, errors.New("no thumbnail")
	}
	return img, nil
}

func (f *fakeClient) AlbumAssets(context.Context, string) ([]immich.Asset, error) {
	return nil, nil
}

func (f *fakeClient) AddAssetsToAlbum(_ context.Context, albumID string, ids []string) error {
	for _, id := range ids {
		f.added = append(f.added, albumID+":"+id)
	}
	return nil
}

func (f *fakeClient) DeleteAssets(_ context.Context, ids []string, _ bool) error {
	f.deleted = append(f.deleted, ids...)
	return nil
}

func (f *fakeClient) SearchAssetsByDevice(context.Context, string) (map[string]string, error) {
	return f.own, nil
}

func (f *fakeClient) GetAlbums(context.Context) ([]immich.Album, error) {
	return nil, nil
}

type fakeState struct {
	values map[string]string
	saved  int
}

func (s *fakeState) Set(key, assetID string) { s.values[key] = assetID }
func (s *fakeState) Save() error             { s.saved++; return nil }

func newTestReconciler(t *testing.T, client Client, cfg Config) (*Reconciler, *ledger.Store, *fakeState) {
	t.Helper()
	store, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	state := &fakeState{values: map[string]string{}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(cfg, client, store, state, logger), store, state
}

func TestResolveOneAddsOriginalAndClosesDebt(t *testing.T) {
	img := testImage(240, 180, 1)
	takenAt := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	fp := match.FingerprintFromImage(img, takenAt)

	client := &fakeClient{
		assets: []immich.Asset{{
			ID: "original", OriginalFileName: "IMG_0001.JPG",
			Width: 240, Height: 180, FileCreatedAt: takenAt,
		}},
		thumbs: map[string]image.Image{"original": img},
	}
	rec, store, state := newTestReconciler(t, client, Config{
		NarrowWindow: 3 * time.Second,
		WideWindow:   24 * time.Hour,
		Backoff:      []time.Duration{time.Hour},
	})

	entry := ledger.Entry{
		GoogleItemID: "g1", StateKey: "gp_g1", AlbumIDs: []string{"album-1"},
		GpAssetID: "dup", FileName: "gp_g1.jpg", TakenAt: takenAt,
		Width: 240, Height: 180, DHash: fp.DHash, HasHash: true,
		State: ledger.StatePending, CreatedAt: time.Now(), LastCheck: time.Now(), NextCheck: time.Now(),
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}

	if !rec.resolveOne(context.Background(), entry, time.Now()) {
		t.Fatal("expected the debt to be resolved")
	}

	if len(client.added) != 1 || client.added[0] != "album-1:original" {
		t.Fatalf("added = %v, want [album-1:original]", client.added)
	}
	if got := state.values["gp_g1"]; got != "original" {
		t.Fatalf("state = %q, want original", got)
	}
	updated, _ := store.Get("g1")
	if updated.State != ledger.StateResolved || updated.ResolvedID != "original" {
		t.Fatalf("ledger entry = %+v", updated)
	}
}

func TestResolveOneReschedulesWhenNoMatch(t *testing.T) {
	img := testImage(240, 180, 1)
	other := testImage(240, 180, 2)
	takenAt := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	fp := match.FingerprintFromImage(img, takenAt)

	client := &fakeClient{
		assets: []immich.Asset{{
			ID: "other", OriginalFileName: "IMG_0002.JPG",
			Width: 240, Height: 180, FileCreatedAt: takenAt,
		}},
		thumbs: map[string]image.Image{"other": other},
	}
	rec, store, _ := newTestReconciler(t, client, Config{
		NarrowWindow: 3 * time.Second,
		WideWindow:   24 * time.Hour,
		Backoff:      []time.Duration{time.Hour, 6 * time.Hour},
	})

	entry := ledger.Entry{
		GoogleItemID: "g2", StateKey: "gp_g2", AlbumIDs: []string{"album-1"},
		GpAssetID: "dup", TakenAt: takenAt, Width: 240, Height: 180,
		DHash: fp.DHash, HasHash: true,
		State: ledger.StatePending, CreatedAt: time.Now(), LastCheck: time.Now(), NextCheck: time.Now(),
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}

	if rec.resolveOne(context.Background(), entry, time.Now()) {
		t.Fatal("expected the debt to stay open")
	}
	updated, _ := store.Get("g2")
	if updated.State != ledger.StatePending || updated.Checks != 1 {
		t.Fatalf("ledger entry = %+v, want pending with 1 check", updated)
	}
	if len(client.added) != 0 {
		t.Fatalf("nothing should be added, got %v", client.added)
	}
}

func TestResolveOneDryRunKeepsDebt(t *testing.T) {
	img := testImage(240, 180, 1)
	takenAt := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	fp := match.FingerprintFromImage(img, takenAt)

	client := &fakeClient{
		assets: []immich.Asset{{
			ID: "original", OriginalFileName: "IMG_0001.JPG",
			Width: 240, Height: 180, FileCreatedAt: takenAt,
		}},
		thumbs: map[string]image.Image{"original": img},
	}
	rec, store, state := newTestReconciler(t, client, Config{
		DryRun:       true,
		NarrowWindow: 3 * time.Second,
		WideWindow:   24 * time.Hour,
		Backoff:      []time.Duration{time.Hour},
	})

	entry := ledger.Entry{
		GoogleItemID: "g3", StateKey: "gp_g3", AlbumIDs: []string{"album-1"},
		GpAssetID: "dup", TakenAt: takenAt, Width: 240, Height: 180,
		DHash: fp.DHash, HasHash: true,
		State: ledger.StatePending, CreatedAt: time.Now(), LastCheck: time.Now(), NextCheck: time.Now(),
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatal(err)
	}

	if rec.resolveOne(context.Background(), entry, time.Now()) {
		t.Fatal("dry run must not close the debt")
	}
	if len(client.added) != 0 || len(client.deleted) != 0 {
		t.Fatalf("dry run wrote to Immich: added=%v deleted=%v", client.added, client.deleted)
	}
	if len(state.values) != 0 {
		t.Fatalf("dry run updated state: %v", state.values)
	}
	if updated, _ := store.Get("g3"); updated.State != ledger.StatePending {
		t.Fatalf("ledger state = %q, want pending", updated.State)
	}
}
