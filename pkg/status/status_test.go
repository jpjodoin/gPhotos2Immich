package status

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-status.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	store.Ensure("https://album/1", "Holidays", true)
	store.Ensure("https://album/2", "Family", false)

	store.MarkRunning("https://album/1", "Holidays")
	store.MarkFinished("https://album/1", "Holidays", Counts{
		Total: 10, Matched: 8, Uploaded: 1, Skipped: 1,
	}, nil, 1500*time.Millisecond)

	snapshot := store.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("snapshot size = %d, want 2", len(snapshot))
	}
	if snapshot[0].Title != "Family" || snapshot[0].Enabled {
		t.Fatalf("expected Family first and disabled, got %+v", snapshot[0])
	}
	holidays := snapshot[1]
	if holidays.Result != ResultSuccess || holidays.Counts.Matched != 8 || holidays.DurationMs != 1500 {
		t.Fatalf("unexpected holidays status: %+v", holidays)
	}

	// Persistence: reload from disk and check the same values.
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again := reloaded.Snapshot()
	if len(again) != 2 {
		t.Fatalf("reloaded snapshot size = %d, want 2", len(again))
	}
	if again[1].Counts.Matched != 8 || again[1].Title != "Holidays" {
		t.Fatalf("reloaded holidays = %+v", again[1])
	}
}

func TestMarkFinishedResults(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sync-status.json"))
	if err != nil {
		t.Fatal(err)
	}

	store.Ensure("a", "A", true)
	store.MarkFinished("a", "A", Counts{Failed: 2}, nil, time.Second)
	if got := store.Snapshot()[0].Result; got != ResultPartial {
		t.Fatalf("result = %q, want partial", got)
	}

	store.MarkFinished("a", "A", Counts{}, errors.New("boom"), time.Second)
	if got := store.Snapshot()[0]; got.Result != ResultError || got.LastError != "boom" {
		t.Fatalf("result = %+v, want error with message", got)
	}
}

func TestSetNextRun(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "sync-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.Ensure("a", "A", true)
	next := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	store.SetNextRun("a", next)

	got := store.Snapshot()[0].NextRun
	if !got.Equal(next) {
		t.Fatalf("next run = %v, want %v", got, next)
	}
}
