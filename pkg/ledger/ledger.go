// Package ledger keeps track of "debts": Google Photos items that were
// uploaded to Immich because the same photo could not be found at sync time.
//
// A debt is keyed by the Google item ID (stable across albums and runs) and is
// retried on a backoff schedule until either the original shows up in Immich
// (and the duplicate is removed) or the debt expires.
//
// The on-disk format is append-only JSONL: every mutation appends a full entry,
// and the last line wins on load. This keeps writes crash-safe and cheap while
// still allowing the file to be compacted periodically.
package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Entry states.
const (
	StatePending  = "pending"
	StateResolved = "resolved"
	StateExpired  = "expired"
)

// ArchiveAfter is how long resolved/expired entries stay in the active ledger
// before being moved to the archive file.
const ArchiveAfter = 30 * 24 * time.Hour

// Entry is one debt.
type Entry struct {
	GoogleItemID string    `json:"googleItemId"`
	StateKey     string    `json:"stateKey"` // key used in state.SyncState ("gp_<id>")
	AlbumURL     string    `json:"albumUrl"`
	AlbumIDs     []string  `json:"albumIds"`
	GpAssetID    string    `json:"gpAssetId"`
	FileName     string    `json:"fileName"`
	TakenAt      time.Time `json:"takenAt"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	IsVideo      bool      `json:"isVideo"`
	DHash        uint64    `json:"dhash"`
	HasHash      bool      `json:"hasHash"`
	State        string    `json:"state"`
	ResolvedID   string    `json:"resolvedAssetId,omitempty"`
	Checks       int       `json:"checks"`
	CreatedAt    time.Time `json:"createdAt"`
	LastCheck    time.Time `json:"lastCheck"`
	NextCheck    time.Time `json:"nextCheck"`
	Note         string    `json:"note,omitempty"`
}

// Store is a concurrency-safe ledger backed by a JSONL file.
type Store struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
}

// Open loads (or creates) a ledger at the given path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, entries: make(map[string]Entry)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open ledger: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// Skip corrupt lines rather than failing to start.
			continue
		}
		if e.GoogleItemID == "" {
			continue
		}
		s.entries[e.GoogleItemID] = e
	}
	return scanner.Err()
}

// Upsert stores the entry and appends it to the journal.
func (s *Store) Upsert(e Entry) error {
	if e.GoogleItemID == "" {
		return fmt.Errorf("ledger entry has no google item id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[e.GoogleItemID] = e
	return s.appendLocked(e)
}

func (s *Store) appendLocked(e Entry) error {
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create ledger dir: %w", err)
		}
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open ledger for append: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal ledger entry: %w", err)
	}
	line = append(line, '\n')
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("append ledger entry: %w", err)
	}
	return nil
}

// Get returns a copy of an entry.
func (s *Store) Get(googleItemID string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[googleItemID]
	return e, ok
}

// AddAlbum records that the duplicate asset also belongs to another album.
func (s *Store) AddAlbum(googleItemID, albumID string) error {
	if googleItemID == "" || albumID == "" {
		return nil
	}
	s.mu.Lock()
	e, ok := s.entries[googleItemID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	for _, id := range e.AlbumIDs {
		if id == albumID {
			s.mu.Unlock()
			return nil
		}
	}
	e.AlbumIDs = append(e.AlbumIDs, albumID)
	s.entries[googleItemID] = e
	err := s.appendLocked(e)
	s.mu.Unlock()
	return err
}

// Due returns the pending entries whose next check is due.
func (s *Store) Due(now time.Time, maxAge time.Duration, limit int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	due := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.State != StatePending {
			continue
		}
		if !e.NextCheck.IsZero() && e.NextCheck.After(now) {
			continue
		}
		if maxAge > 0 && !e.CreatedAt.IsZero() && now.Sub(e.CreatedAt) >= maxAge {
			continue // Expire handles it.
		}
		due = append(due, e)
	}

	sort.Slice(due, func(i, j int) bool {
		return due[i].NextCheck.Before(due[j].NextCheck)
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due
}

// Pending returns every pending entry (used by the UI).
func (s *Store) Pending() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.State == StatePending {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Expire marks pending entries older than maxAge as expired and returns them.
// A non-positive maxAge disables expiry.
func (s *Store) Expire(now time.Time, maxAge time.Duration) ([]Entry, error) {
	if maxAge <= 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []Entry
	for id, e := range s.entries {
		if e.State != StatePending {
			continue
		}
		if e.CreatedAt.IsZero() || now.Sub(e.CreatedAt) < maxAge {
			continue
		}
		e.State = StateExpired
		e.LastCheck = now
		e.Note = "max debt age reached"
		s.entries[id] = e
		if err := s.appendLocked(e); err != nil {
			return expired, err
		}
		expired = append(expired, e)
	}
	return expired, nil
}

// Resolve marks an entry as resolved by the given Immich asset.
func (s *Store) Resolve(googleItemID, assetID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[googleItemID]
	if !ok {
		return fmt.Errorf("ledger entry %q not found", googleItemID)
	}
	e.State = StateResolved
	e.ResolvedID = assetID
	e.LastCheck = now
	s.entries[googleItemID] = e
	return s.appendLocked(e)
}

// Reschedule bumps the retry counter and sets the next check time.
func (s *Store) Reschedule(googleItemID string, checks int, nextCheck, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[googleItemID]
	if !ok {
		return fmt.Errorf("ledger entry %q not found", googleItemID)
	}
	e.Checks = checks
	e.NextCheck = nextCheck
	e.LastCheck = now
	s.entries[googleItemID] = e
	return s.appendLocked(e)
}

// Counts returns the number of entries per state.
func (s *Store) Counts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := map[string]int{StatePending: 0, StateResolved: 0, StateExpired: 0}
	for _, e := range s.entries {
		out[e.State]++
	}
	return out
}

// Total returns the number of entries currently held in memory.
func (s *Store) Total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Compact rewrites the journal with the live entries and moves old
// resolved/expired entries to the archive file.
func (s *Store) Compact(archivePath string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var keep []Entry
	var archive []Entry
	for _, e := range s.entries {
		if e.State != StatePending && now.Sub(e.LastCheck) > ArchiveAfter {
			archive = append(archive, e)
			continue
		}
		keep = append(keep, e)
	}

	if len(archive) > 0 {
		if err := appendLines(archivePath, archive); err != nil {
			return 0, err
		}
		for _, e := range archive {
			delete(s.entries, e.GoogleItemID)
		}
	}

	if err := rewrite(s.path, keep); err != nil {
		return 0, err
	}
	return len(archive), nil
}

func appendLines(path string, entries []Entry) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func rewrite(path string, entries []Entry) error {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})

	var buf []byte
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
