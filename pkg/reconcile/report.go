package reconcile

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// ReportRow is one line of the dedup CSV report.
type ReportRow struct {
	Timestamp        time.Time
	Action           string
	AlbumURL         string
	GoogleItemID     string
	GpFileName       string
	GpAssetID        string
	ResolvedAssetID  string
	ResolvedFileName string
	Distance         int
	Candidates       int
	Note             string
}

var reportMu sync.Mutex

var reportHeader = []string{
	"timestamp", "action", "album_url", "google_item_id", "gp_file",
	"gp_asset_id", "resolved_asset_id", "resolved_file", "distance",
	"candidates", "note",
}

// AppendReport appends a row to the CSV report, creating the file (and its
// directory) with a header when needed.
func AppendReport(path string, row ReportRow) error {
	if path == "" {
		return nil
	}

	reportMu.Lock()
	defer reportMu.Unlock()

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create report dir: %w", err)
		}
	}

	isNew := false
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		isNew = true
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open report: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if isNew {
		if err := w.Write(reportHeader); err != nil {
			return err
		}
	}

	return w.Write([]string{
		row.Timestamp.UTC().Format(time.RFC3339),
		row.Action,
		row.AlbumURL,
		row.GoogleItemID,
		row.GpFileName,
		row.GpAssetID,
		row.ResolvedAssetID,
		row.ResolvedFileName,
		strconv.Itoa(row.Distance),
		strconv.Itoa(row.Candidates),
		row.Note,
	})
}
