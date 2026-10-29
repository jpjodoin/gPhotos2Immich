// Package status persists a per-album sync status so the web UI can show
// what happened on the last run, when the next one is due, and how many items
// were matched, uploaded or kept as duplicates.
package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Results reported for an album run.
const (
	ResultSuccess = "success"
	ResultPartial = "partial"
	ResultError   = "error"
	ResultRunning = "running"
	ResultIdle    = "idle"
)

// Counts summarises one album run.
type Counts struct {
	Total       int `json:"total"`
	Matched     int `json:"matched"`
	Uploaded    int `json:"uploaded"`
	WouldUpload int `json:"wouldUpload"`
	Skipped     int `json:"skipped"`
	Failed      int `json:"failed"`
	Ambiguous   int `json:"ambiguous"`
}

// Album is the status of one configured Google Photos album.
type Album struct {
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	Enabled    bool      `json:"enabled"`
	LastRun    time.Time `json:"lastRun"`
	NextRun    time.Time `json:"nextRun"`
	DurationMs int64     `json:"durationMs"`
	Result     string    `json:"result"`
	Counts     Counts    `json:"counts"`
	LastError  string    `json:"lastError,omitempty"`
}

// Store is a concurrency-safe, file-backed status store.
type Store struct {
	mu     sync.Mutex
	path   string
	albums map[string]*Album
}

// Open loads (or creates) the status store at the given path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, albums: make(map[string]*Album)}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read status file: %w", err)
	}
	if len(data) > 0 {
		var albums map[string]*Album
		if err := json.Unmarshal(data, &albums); err != nil {
			// A corrupt status file must not prevent the app from starting.
			return s, nil
		}
		for url, album := range albums {
			if album != nil {
				s.albums[url] = album
			}
		}
	}
	return s, nil
}

// Ensure registers an album (no-op when already known) and updates its title
// and enabled flag.
func (s *Store) Ensure(url, title string, enabled bool) {
	if url == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	album, ok := s.albums[url]
	if !ok {
		album = &Album{URL: url, Result: ResultIdle}
		s.albums[url] = album
	}
	if title != "" {
		album.Title = title
	}
	album.Enabled = enabled
	_ = s.saveLocked()
}

// SetNextRun records when the album is scheduled next.
func (s *Store) SetNextRun(url string, next time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	album := s.albumLocked(url)
	album.NextRun = next
	_ = s.saveLocked()
}

// MarkRunning is called when an album sync starts.
func (s *Store) MarkRunning(url, title string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	album := s.albumLocked(url)
	if title != "" {
		album.Title = title
	}
	album.Result = ResultRunning
	album.LastError = ""
	_ = s.saveLocked()
}

// MarkFinished records the outcome of an album run.
func (s *Store) MarkFinished(url, title string, counts Counts, runErr error, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	album := s.albumLocked(url)
	if title != "" {
		album.Title = title
	}
	album.LastRun = time.Now()
	album.DurationMs = duration.Milliseconds()
	album.Counts = counts
	if runErr != nil {
		album.Result = ResultError
		album.LastError = runErr.Error()
	} else if counts.Failed > 0 {
		album.Result = ResultPartial
		album.LastError = ""
	} else {
		album.Result = ResultSuccess
		album.LastError = ""
	}
	_ = s.saveLocked()
}

// Snapshot returns a sorted copy of all album statuses.
func (s *Store) Snapshot() []Album {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Album, 0, len(s.albums))
	for _, album := range s.albums {
		out = append(out, *album)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].URL < out[j].URL
	})
	return out
}

func (s *Store) albumLocked(url string) *Album {
	album, ok := s.albums[url]
	if !ok {
		album = &Album{URL: url, Result: ResultIdle}
		s.albums[url] = album
	}
	return album
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(s.albums, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
