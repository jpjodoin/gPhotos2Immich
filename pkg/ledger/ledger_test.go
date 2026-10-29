package ledger

import (
	"path/filepath"
	"testing"
	"time"
)

func newEntry(id string, now time.Time) Entry {
	return Entry{
		GoogleItemID: id,
		StateKey:     "gp_" + id,
		AlbumIDs:     []string{"album-1"},
		GpAssetID:    "asset-" + id,
		FileName:     "gp_" + id + ".jpg",
		TakenAt:      now.Add(-time.Hour),
		State:        StatePending,
		CreatedAt:    now,
		LastCheck:    now,
		NextCheck:    now,
	}
}

func TestUpsertGetAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	now := time.Now()

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(newEntry("a", now)); err != nil {
		t.Fatal(err)
	}

	got, ok := store.Get("a")
	if !ok || got.GpAssetID != "asset-a" {
		t.Fatalf("Get = %+v, %v", got, ok)
	}

	// Mutations are appended; the last line must win on reload.
	got.State = StateResolved
	got.ResolvedID = "original"
	if err := store.Upsert(got); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, ok := reloaded.Get("a")
	if !ok || again.State != StateResolved || again.ResolvedID != "original" {
		t.Fatalf("reloaded = %+v", again)
	}
}

func TestDueRespectsNextCheckAndMaxAge(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	future := newEntry("future", now)
	future.NextCheck = now.Add(time.Hour)
	ready := newEntry("ready", now)
	ready.NextCheck = now.Add(-time.Minute)
	old := newEntry("old", now)
	old.CreatedAt = now.Add(-100 * 24 * time.Hour)
	old.NextCheck = now.Add(-time.Minute)

	for _, e := range []Entry{future, ready, old} {
		if err := store.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}

	due := store.Due(now, 90*24*time.Hour, 100)
	if len(due) != 1 || due[0].GoogleItemID != "ready" {
		t.Fatalf("due = %+v, want only ready", due)
	}

	// A zero max age disables expiry: the old entry becomes due again.
	due = store.Due(now, 0, 100)
	if len(due) != 2 {
		t.Fatalf("due with no max age = %d, want 2", len(due))
	}
}

func TestExpireAndResolve(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	old := newEntry("old", now)
	old.CreatedAt = now.Add(-100 * 24 * time.Hour)
	fresh := newEntry("fresh", now)
	for _, e := range []Entry{old, fresh} {
		if err := store.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}

	expired, err := store.Expire(now, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].GoogleItemID != "old" {
		t.Fatalf("expired = %+v", expired)
	}
	if e, _ := store.Get("old"); e.State != StateExpired {
		t.Fatalf("old state = %q, want expired", e.State)
	}

	if err := store.Resolve("fresh", "original", now); err != nil {
		t.Fatal(err)
	}
	if e, _ := store.Get("fresh"); e.State != StateResolved || e.ResolvedID != "original" {
		t.Fatalf("fresh = %+v", e)
	}

	counts := store.Counts()
	if counts[StateExpired] != 1 || counts[StateResolved] != 1 || counts[StatePending] != 0 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestAddAlbumIsIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.Upsert(newEntry("a", now)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if err := store.AddAlbum("a", "album-2"); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := store.Get("a")
	if len(e.AlbumIDs) != 2 {
		t.Fatalf("album ids = %v, want 2", e.AlbumIDs)
	}
}

func TestCompactArchivesOldEntries(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	resolved := newEntry("resolved", now)
	resolved.State = StateResolved
	resolved.LastCheck = now.Add(-60 * 24 * time.Hour)
	pending := newEntry("pending", now)

	for _, e := range []Entry{resolved, pending} {
		if err := store.Upsert(e); err != nil {
			t.Fatal(err)
		}
	}

	archived, err := store.Compact(filepath.Join(dir, "archive.jsonl"), now)
	if err != nil {
		t.Fatal(err)
	}
	if archived != 1 {
		t.Fatalf("archived = %d, want 1", archived)
	}
	if _, ok := store.Get("resolved"); ok {
		t.Fatal("resolved entry should have been archived")
	}
	if _, ok := store.Get("pending"); !ok {
		t.Fatal("pending entry must be kept")
	}

	reloaded, err := Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Total() != 1 {
		t.Fatalf("reloaded total = %d, want 1", reloaded.Total())
	}
}
