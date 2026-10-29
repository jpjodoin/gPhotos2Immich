package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"warreth.dev/gphotos2immich/pkg/config"
)

func TestSaveConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	s := NewServer(configPath)

	body := []byte(`{
		"apiURL": "http://localhost:2283/api",
		"apiKey": "test-key",
		"workers": 2,
		"debug": true,
		"googlePhotos": []
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleConfig(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read written config: %v", err)
	}

	if !strings.Contains(string(content), "http://localhost:2283/api") {
		t.Fatalf("saved content missing expected apiURL: %s", string(content))
	}
}

func TestSaveConfigPermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Chmod-based read-only directories do not prevent writes on Windows,
		// so this test cannot make the write fail.
		t.Skip("read-only directory semantics differ on Windows")
	}

	// Create read-only directory
	tmpDir := t.TempDir()
	roDir := filepath.Join(tmpDir, "ro")
	if err := os.Mkdir(roDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(roDir, 0755)
	})

	configPath := filepath.Join(roDir, "config.json")
	s := NewServer(configPath)

	body := []byte(`{
		"apiURL": "http://localhost:2283/api",
		"apiKey": "test-key"
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()

	s.handleConfig(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500 when file cannot be written, got %d", resp.StatusCode)
	}

	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "Failed to write config file") {
		t.Fatalf("expected error message to contain 'Failed to write config file', got %s", bodyStr)
	}
}

func TestHandleSync(t *testing.T) {
	s := NewServer("dummy.json")
	var triggered bool
	s.OnSyncTrigger = func() {
		triggered = true
	}

	// Test POST
	req := httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	w := httptest.NewRecorder()
	s.handleSync(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on POST /api/sync, got %d", resp.StatusCode)
	}

	// Wait briefly for goroutine
	for i := 0; i < 50; i++ {
		if triggered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !triggered {
		t.Fatal("expected OnSyncTrigger to be called")
	}

	// Test GET (for easy browser/Home Assistant webhook usage)
	triggered = false
	reqGet := httptest.NewRequest(http.MethodGet, "/sync_now", nil)
	wGet := httptest.NewRecorder()
	s.handleSync(wGet, reqGet)

	respGet := wGet.Result()
	if respGet.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /sync_now, got %d", respGet.StatusCode)
	}

	for i := 0; i < 50; i++ {
		if triggered {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !triggered {
		t.Fatal("expected OnSyncTrigger to be called on GET")
	}

	// Test Method Not Allowed
	reqDelete := httptest.NewRequest(http.MethodDelete, "/api/sync", nil)
	wDelete := httptest.NewRecorder()
	s.handleSync(wDelete, reqDelete)

	if wDelete.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed on DELETE, got %d", wDelete.Result().StatusCode)
	}
}
func TestHandleStatus(t *testing.T) {
	s := NewServer("dummy.json")

	req := httptest.NewRequest(http.MethodGet, "/sync/status", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on /sync/status, got %d", resp.StatusCode)
	}

	bodyStr := w.Body.String()
	if !strings.Contains(bodyStr, "immichUser") || !strings.Contains(bodyStr, "lastRun") || !strings.Contains(bodyStr, "nextRun") {
		t.Fatalf("response missing expected fields: %s", bodyStr)
	}
}

func TestSaveConfigPreservesFieldsTheUIHides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	initial := `{
		"apiURL": "http://immich/api",
		"apiKey": "key",
		"dedup": {"mode": "smart", "windowSeconds": 42, "wideWindowHours": 24},
		"reconcile": {"interval": "6h", "maxPerRun": 7},
		"googlePhotos": [
			{"url": "https://album/1", "albumName": "One", "syncInterval": "12h", "immichAlbumId": "uuid-1"}
		]
	}`
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	s := NewServer(path)

	// The web UI re-sends albums without the hidden immichAlbumId field and
	// sends the dedup/reconcile blocks merged with what it loaded.
	body := []byte(`{
		"apiURL": "http://immich/api",
		"apiKey": "key",
		"dedup": {"mode": "smart", "dryRun": true, "windowSeconds": 42, "wideWindowHours": 24},
		"reconcile": {"interval": "6h", "maxPerRun": 7, "deleteMatchedDuplicates": true},
		"googlePhotos": [
			{"url": "https://album/1", "albumName": "One", "syncInterval": "12h", "enabled": true}
		]
	}`)

	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Config
	if err := json.Unmarshal(content, &saved); err != nil {
		t.Fatal(err)
	}

	if len(saved.GooglePhotos) != 1 || saved.GooglePhotos[0].ImmichAlbumID != "uuid-1" {
		t.Fatalf("immichAlbumId was not preserved: %+v", saved.GooglePhotos)
	}
	if !saved.Dedup.DryRun || saved.Dedup.WindowSeconds != 42 {
		t.Fatalf("dedup block lost fields: %+v", saved.Dedup)
	}
	if !saved.Reconcile.DeleteMatchedDuplicates || saved.Reconcile.Interval != "6h" {
		t.Fatalf("reconcile block lost fields: %+v", saved.Reconcile)
	}
}
