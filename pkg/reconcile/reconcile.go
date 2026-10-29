package reconcile

import (
	"context"
	"image"
	"log/slog"
	"strings"
	"time"

	"warreth.dev/gphotos2immich/pkg/immich"
	"warreth.dev/gphotos2immich/pkg/ledger"
	"warreth.dev/gphotos2immich/pkg/match"
	"warreth.dev/gphotos2immich/pkg/util"
)

// Client is the Immich surface the reconciler needs.
type Client interface {
	CandidateClient
	GetAsset(ctx context.Context, assetID string) (*immich.Asset, error)
	Thumbnail(ctx context.Context, assetID string) (image.Image, error)
	AlbumAssets(ctx context.Context, albumID string) ([]immich.Asset, error)
	AddAssetsToAlbum(ctx context.Context, albumID string, assetIDs []string) error
	DeleteAssets(ctx context.Context, assetIDs []string, force bool) error
	SearchAssetsByDevice(ctx context.Context, deviceID string) (map[string]string, error)
	GetAlbums(ctx context.Context) ([]immich.Album, error)
}

// StateStore is the persistent dedup mapping shared with the sync loop.
type StateStore interface {
	Set(key, assetID string)
	Save() error
}

// Config controls the reconciliation loop.
type Config struct {
	Enabled                 bool
	Interval                time.Duration
	FullSweepInterval       time.Duration
	MaxPerRun               int
	MaxDebtAge              time.Duration
	Backoff                 []time.Duration
	DeleteMatchedDuplicates bool
	DryRun                  bool
	NarrowWindow            time.Duration
	WideWindow              time.Duration
	MatchOptions            match.Options
	ArchivePath             string
	ReportPath              string
	DeviceID                string
}

// Summary is the outcome of one reconciliation pass.
type Summary struct {
	Checked  int
	Resolved int
	Expired  int
	Created  int
}

// Reconciler resolves ledger debts: it looks for the real asset that a
// previously uploaded duplicate corresponds to, adds it to the relevant
// albums, and optionally deletes the duplicate.
type Reconciler struct {
	cfg      Config
	client   Client
	ledger   *ledger.Store
	state    StateStore
	logger   *slog.Logger
	lastFull time.Time
}

// New builds a reconciler.
func New(cfg Config, client Client, store *ledger.Store, state StateStore, logger *slog.Logger) *Reconciler {
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = 200
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if len(cfg.Backoff) == 0 {
		cfg.Backoff = []time.Duration{time.Hour}
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = "gphotos2immich-go"
	}
	return &Reconciler{
		cfg:    cfg,
		client: client,
		ledger: store,
		state:  state,
		logger: logger,
	}
}

// Run drives the reconciliation loop until the context is cancelled. A
// non-blocking send on trigger forces a full pass.
func (r *Reconciler) Run(ctx context.Context, trigger <-chan struct{}) {
	if !r.cfg.Enabled {
		r.logger.Info("Reconciliation disabled")
		return
	}

	// Give the first album sync a head start, then sweep historical duplicates.
	initial := time.NewTimer(30 * time.Second)
	defer initial.Stop()
	select {
	case <-ctx.Done():
		return
	case <-trigger:
		r.RunOnce(ctx, true)
	case <-initial.C:
		r.RunOnce(ctx, false)
	}

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			r.RunOnce(ctx, true)
		case <-ticker.C:
			r.RunOnce(ctx, false)
		}
	}
}

// RunOnce performs a single reconciliation pass.
func (r *Reconciler) RunOnce(ctx context.Context, forceFull bool) Summary {
	now := time.Now()
	var sum Summary

	expired, err := r.ledger.Expire(now, r.cfg.MaxDebtAge)
	if err != nil {
		r.logger.Warn("Failed to expire old debts", "error", err)
	}
	for _, e := range expired {
		sum.Expired++
		r.report(ReportRow{
			Timestamp:    now,
			Action:       "expired",
			AlbumURL:     e.AlbumURL,
			GoogleItemID: e.GoogleItemID,
			GpFileName:   e.FileName,
			GpAssetID:    e.GpAssetID,
			Note:         "max debt age reached",
		})
	}

	if r.shouldSweep(now, forceFull) {
		sum.Created = r.fullSweep(ctx)
		r.lastFull = now
	}

	due := r.ledger.Due(now, r.cfg.MaxDebtAge, r.cfg.MaxPerRun)
	for _, entry := range due {
		if ctx.Err() != nil {
			break
		}
		sum.Checked++
		if r.resolveOne(ctx, entry, now) {
			sum.Resolved++
		}
	}

	if r.cfg.ArchivePath != "" {
		if n, err := r.ledger.Compact(r.cfg.ArchivePath, now); err != nil {
			r.logger.Warn("Failed to compact ledger", "error", err)
		} else if n > 0 {
			r.logger.Debug("Archived resolved ledger entries", "count", n)
		}
	}

	if err := r.state.Save(); err != nil {
		r.logger.Warn("Failed to persist dedup state", "error", err)
	}

	if sum.Checked > 0 || sum.Created > 0 || sum.Expired > 0 {
		r.logger.Info("Reconciliation pass finished",
			"checked", sum.Checked,
			"resolved", sum.Resolved,
			"created", sum.Created,
			"expired", sum.Expired,
			"pending", r.ledger.Counts()[ledger.StatePending],
		)
	}
	return sum
}

func (r *Reconciler) shouldSweep(now time.Time, force bool) bool {
	if force || r.lastFull.IsZero() {
		return true
	}
	if r.cfg.FullSweepInterval <= 0 {
		return false
	}
	return now.Sub(r.lastFull) >= r.cfg.FullSweepInterval
}

// resolveOne tries to match one debt. It returns true when the debt is closed.
func (r *Reconciler) resolveOne(ctx context.Context, e ledger.Entry, now time.Time) bool {
	if e.GpAssetID == "" {
		r.reschedule(e.GoogleItemID, e.Checks, now)
		return false
	}

	fp := match.Fingerprint{
		DHash:   e.DHash,
		HasHash: e.HasHash,
		Width:   e.Width,
		Height:  e.Height,
		IsVideo: e.IsVideo,
		TakenAt: e.TakenAt,
	}

	// Historical duplicates were recorded without a hash. Compute one from the
	// duplicate's own Immich thumbnail: it is a re-encoded copy, but the
	// perceptual hash survives that.
	if !fp.HasHash && !fp.IsVideo {
		img, err := r.client.Thumbnail(ctx, e.GpAssetID)
		if err != nil {
			r.logger.Debug("Cannot fingerprint duplicate yet", "google_id", e.GoogleItemID, "error", err)
			r.reschedule(e.GoogleItemID, e.Checks, now)
			return false
		}
		fp = match.FingerprintFromImage(img, e.TakenAt)
		e.DHash = fp.DHash
		e.HasHash = true
	}

	candidates := FindCandidates(ctx, r.client, fp, r.cfg.NarrowWindow, r.cfg.WideWindow)
	res := match.Decide(ctx, fp, candidates, r.client, r.cfg.MatchOptions)
	if res.Verdict != match.VerdictMatch {
		r.reschedule(e.GoogleItemID, e.Checks, now)
		return false
	}

	if !r.cfg.DryRun {
		for _, albumID := range e.AlbumIDs {
			if err := r.client.AddAssetsToAlbum(ctx, albumID, []string{res.AssetID}); err != nil {
				r.logger.Warn("Failed to add original to album", "album_id", albumID, "error", err)
			}
		}
		if r.cfg.DeleteMatchedDuplicates {
			if err := r.client.DeleteAssets(ctx, []string{e.GpAssetID}, true); err != nil {
				r.logger.Warn("Failed to delete duplicate, will retry", "asset_id", e.GpAssetID, "error", err)
				r.reschedule(e.GoogleItemID, e.Checks, now)
				return false
			}
		}
		if e.StateKey != "" {
			// Future syncs resolve the Google item straight to the original.
			r.state.Set(e.StateKey, res.AssetID)
		}
	}

	if err := r.ledger.Resolve(e.GoogleItemID, res.AssetID, now); err != nil {
		r.logger.Warn("Failed to mark debt resolved", "google_id", e.GoogleItemID, "error", err)
	}

	r.report(ReportRow{
		Timestamp:       now,
		Action:          "resolved",
		AlbumURL:        e.AlbumURL,
		GoogleItemID:    e.GoogleItemID,
		GpFileName:      e.FileName,
		GpAssetID:       e.GpAssetID,
		ResolvedAssetID: res.AssetID,
		Distance:        res.Distance,
		Candidates:      len(res.Scored),
		Note:            deleteNote(r.cfg),
	})

	r.logger.Info("Reconciled duplicate",
		"google_id", e.GoogleItemID,
		"resolved_asset_id", res.AssetID,
		"distance", res.Distance,
		"albums", len(e.AlbumIDs),
		"deleted_duplicate", r.cfg.DeleteMatchedDuplicates && !r.cfg.DryRun,
	)
	return true
}

func deleteNote(cfg Config) string {
	if cfg.DryRun {
		return "dry run: nothing written"
	}
	if cfg.DeleteMatchedDuplicates {
		return "duplicate deleted"
	}
	return "duplicate kept"
}

func (r *Reconciler) reschedule(googleItemID string, checks int, now time.Time) {
	checks++
	next := r.nextCheck(checks, now)
	if err := r.ledger.Reschedule(googleItemID, checks, next, now); err != nil {
		r.logger.Debug("Failed to reschedule debt", "google_id", googleItemID, "error", err)
	}
}

func (r *Reconciler) nextCheck(checks int, now time.Time) time.Time {
	idx := checks - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r.cfg.Backoff) {
		idx = len(r.cfg.Backoff) - 1
	}
	return now.Add(r.cfg.Backoff[idx])
}

// fullSweep imports every asset this tool ever uploaded into the ledger, so
// duplicates created before this feature existed get reconciled too.
func (r *Reconciler) fullSweep(ctx context.Context) int {
	own, err := r.client.SearchAssetsByDevice(ctx, r.cfg.DeviceID)
	if err != nil {
		r.logger.Warn("Full sweep: cannot list own uploads", "error", err)
		return 0
	}
	if len(own) == 0 {
		return 0
	}

	var albumIndex map[string][]string
	indexLoaded := false
	created := 0

	for name, assetID := range own {
		if ctx.Err() != nil {
			break
		}
		googleItemID := googleItemIDFromName(name)
		if googleItemID == "" {
			continue
		}
		if _, exists := r.ledger.Get(googleItemID); exists {
			continue
		}

		asset, err := r.client.GetAsset(ctx, assetID)
		if err != nil {
			r.logger.Debug("Full sweep: cannot load asset", "asset_id", assetID, "error", err)
			continue
		}
		if asset.IsTrashed || asset.Visibility == "hidden" {
			continue
		}

		if !indexLoaded {
			albumIndex = r.buildAlbumIndex(ctx)
			indexLoaded = true
		}

		now := time.Now()
		entry := ledger.Entry{
			GoogleItemID: googleItemID,
			StateKey:     stateKey(googleItemID),
			AlbumIDs:     albumIndex[assetID],
			GpAssetID:    assetID,
			FileName:     asset.OriginalFileName,
			TakenAt:      asset.FileCreatedAt,
			Width:        asset.Width,
			Height:       asset.Height,
			IsVideo:      asset.IsVideo(),
			State:        ledger.StatePending,
			CreatedAt:    now,
			LastCheck:    now,
			NextCheck:    now,
			Note:         "discovered by full sweep",
		}
		if err := r.ledger.Upsert(entry); err != nil {
			r.logger.Warn("Full sweep: cannot record debt", "google_id", googleItemID, "error", err)
			continue
		}
		created++
		r.report(ReportRow{
			Timestamp:    now,
			Action:       "discovered",
			GoogleItemID: googleItemID,
			GpFileName:   asset.OriginalFileName,
			GpAssetID:    assetID,
			Note:         "full sweep",
		})
	}

	if created > 0 {
		r.logger.Info("Full sweep discovered unresolved duplicates", "count", created)
	}
	return created
}

// buildAlbumIndex maps every asset ID to the albums that contain it.
func (r *Reconciler) buildAlbumIndex(ctx context.Context) map[string][]string {
	index := make(map[string][]string)
	albums, err := r.client.GetAlbums(ctx)
	if err != nil {
		r.logger.Warn("Full sweep: cannot list albums", "error", err)
		return index
	}
	for _, album := range albums {
		if ctx.Err() != nil {
			break
		}
		assets, err := r.client.AlbumAssets(ctx, album.Id)
		if err != nil {
			r.logger.Debug("Full sweep: cannot list album assets", "album_id", album.Id, "error", err)
			continue
		}
		for _, asset := range assets {
			index[asset.ID] = append(index[asset.ID], album.Id)
		}
	}
	return index
}

func (r *Reconciler) report(row ReportRow) {
	if r.cfg.ReportPath == "" {
		return
	}
	if err := AppendReport(r.cfg.ReportPath, row); err != nil {
		r.logger.Warn("Failed to write dedup report", "error", err)
	}
}

// stateKey reproduces the key used by the sync loop for a Google item.
func stateKey(googleItemID string) string {
	safe := strings.ReplaceAll(googleItemID, "/", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	return "gp_" + safe
}

// googleItemIDFromName extracts the Google item ID from a generated filename
// ("gp_<id>.jpg" or "gp_<id>.mp4").
func googleItemIDFromName(name string) string {
	if !IsOwnUpload(name) {
		return ""
	}
	return util.StripExtension(strings.TrimPrefix(name, "gp_"))
}
