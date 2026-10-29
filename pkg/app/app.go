package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"warreth.dev/gphotos2immich/pkg/config"
	"warreth.dev/gphotos2immich/pkg/duration"
	"warreth.dev/gphotos2immich/pkg/googlephotos"
	"warreth.dev/gphotos2immich/pkg/immich"
	"warreth.dev/gphotos2immich/pkg/match"
	"warreth.dev/gphotos2immich/pkg/progress"
	"warreth.dev/gphotos2immich/pkg/state"
	"warreth.dev/gphotos2immich/pkg/util"
)

type App struct {
	Cfg      *config.Config
	Client   *immich.Client
	GPClient *googlephotos.Client
	Logger   *slog.Logger
	State    *state.SyncState
	syncCh   chan struct{}
}

func New(cfg *config.Config) (*App, error) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				return slog.Attr{}
			}
			if a.Key == slog.TimeKey {
				t := a.Value.Time()
				return slog.String(slog.TimeKey, t.Format("15:04:05"))
			}
			return a
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	client := immich.NewClient(cfg.ApiURL, cfg.ApiKey, workers)
	gpClient := googlephotos.NewClient(logger, workers)

	// Initialize persistent dedup state
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}
	syncState := state.New(filepath.Join(dataDir, "sync-state.json"))
	if err := syncState.Load(); err != nil {
		logger.Warn("Failed to load sync state, starting fresh", "error", err)
	}
	logger.Info("Loaded persistent dedup state", "entries", syncState.Count())

	return &App{
		Cfg:      cfg,
		Client:   client,
		GPClient: gpClient,
		Logger:   logger,
		State:    syncState,
		syncCh:   make(chan struct{}, 1),
	}, nil
}

// TriggerSync requests an immediate sync of all albums outside the schedule
func (a *App) TriggerSync() {
	select {
	case a.syncCh <- struct{}{}:
	default:
	}
}

func (a *App) Run(ctx context.Context) error {
	a.Logger.Info("Starting Immich Sync")

	id, name, err := a.Client.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to Immich: %w", err)
	}
	a.Logger.Info("Connected to Immich", "user_id", id, "name", name)

	if len(a.Cfg.GooglePhotos) == 0 {
		a.Logger.Warn("No albums configured")
		return nil
	}

	// Initialize schedule - all albums due immediately
	nextRun := make(map[string]time.Time)
	nowStart := time.Now()
	for _, ac := range a.Cfg.GooglePhotos {
		nextRun[ac.URL] = nowStart
	}
	progress.SetScheduleTimes(time.Time{}, nowStart)

	albumWorkers := a.Cfg.AlbumWorkers
	if albumWorkers < 1 {
		albumWorkers = 1
	}

	for {
		// Check for shutdown between cycles
		select {
		case <-ctx.Done():
			a.Logger.Info("Shutdown requested, stopping sync loop")
			return nil
		default:
		}

		// Collect albums due for sync and find earliest next run
		var due []config.GooglePhotosConfig
		earliest := time.Now().Add(24 * time.Hour)

		for _, ac := range a.Cfg.GooglePhotos {
			if !time.Now().Before(nextRun[ac.URL]) {
				due = append(due, ac)
			} else if nextRun[ac.URL].Before(earliest) {
				earliest = nextRun[ac.URL]
			}
		}

		if len(due) > 0 {
			// Fetch album list and global assets from Immich once per sync cycle
			albumCache, err := a.Client.GetAlbums(ctx)
			if err != nil {
				a.Logger.Warn("Failed to fetch Immich album list", "error", err)
			}

			globalAssets, err := a.Client.SearchAssetsByDevice(ctx, "gphotos2immich-go")
			if err != nil {
				a.Logger.Warn("Failed to fetch global assets, will fall back to re-upload for duplicates", "error", err)
				globalAssets = make(map[string]string)
			}
			a.Logger.Debug("Pre-fetched global assets from Immich", "count", len(globalAssets))

			a.Logger.Info("Processing due albums", "count", len(due), "album_workers", albumWorkers)

			// Process due albums concurrently with bounded concurrency
			sem := make(chan struct{}, albumWorkers)
			var wg sync.WaitGroup
			for _, ac := range due {
				wg.Add(1)
				go func(ac config.GooglePhotosConfig) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					a.processAlbum(ctx, ac, albumCache, globalAssets)
				}(ac)
			}
			wg.Wait()

			// Schedule next runs and record completion
			nowCompleted := time.Now()
			nextEarliest := nowCompleted.Add(24 * time.Hour)
			for _, ac := range due {
				interval, err := duration.Parse(ac.SyncInterval)
				if err != nil || interval == 0 {
					interval = 24 * time.Hour
				}
				nextRun[ac.URL] = nowCompleted.Add(interval)
				a.Logger.Info("Scheduled next sync", "album", ac.URL, "next_run", nextRun[ac.URL].Format("15:04:05"))
			}
			for _, nr := range nextRun {
				if nr.Before(nextEarliest) {
					nextEarliest = nr
				}
			}
			progress.SetScheduleTimes(nowCompleted, nextEarliest)
			continue
		}

		// Wait until the next album is due or context cancellation
		waitDuration := time.Until(earliest)
		if waitDuration < time.Second {
			waitDuration = time.Second
		}
		a.Logger.Debug("Waiting for next sync cycle", "wait", waitDuration.Round(time.Second))

		select {
		case <-ctx.Done():
			a.Logger.Info("Shutdown requested, stopping sync loop")
			return nil
		case <-a.syncCh:
			a.Logger.Info("Sync triggered manually, running all albums immediately")
			now := time.Now()
			for _, ac := range a.Cfg.GooglePhotos {
				nextRun[ac.URL] = now
			}
		case <-time.After(waitDuration):
		}
	}
}

// ItemAction describes what happened to a Google Photos item.
type ItemAction int

const (
	// ItemMatched means an existing Immich asset was found; nothing was uploaded.
	ItemMatched ItemAction = iota
	// ItemUploaded means the item was uploaded as a new asset.
	ItemUploaded
	// ItemWouldUpload is a dry-run outcome: the item would have been uploaded.
	ItemWouldUpload
	// ItemSkipped means the item was already handled or deliberately skipped.
	ItemSkipped
	// ItemFailed means processing the item returned an error.
	ItemFailed
)

func (a ItemAction) String() string {
	switch a {
	case ItemMatched:
		return "matched"
	case ItemUploaded:
		return "uploaded"
	case ItemWouldUpload:
		return "would_upload"
	case ItemSkipped:
		return "skipped"
	case ItemFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type processResult struct {
	ID              string
	Action          ItemAction
	Error           error
	BytesDownloaded int64
	BytesUploaded   int64
	Distance        int
	Ambiguous       bool
	Candidates      []match.Scored
}

// resolveAlbumID finds or creates the Immich album for a given config entry.
func (a *App) resolveAlbumID(ctx context.Context, ac config.GooglePhotosConfig, albumTitle string, albumCache []immich.Album, logger *slog.Logger) string {
	if ac.ImmichAlbumID != "" {
		return ac.ImmichAlbumID
	}
	for _, album := range albumCache {
		if album.AlbumName == albumTitle {
			return album.Id
		}
	}
	logger.Info("Creating Immich album", "title", albumTitle)
	newAlbum, err := a.Client.CreateAlbum(ctx, albumTitle)
	if err != nil {
		logger.Error("Error creating album", "error", err)
		return ""
	}
	return newAlbum.Id
}

// dedupKey normalises a filename the same way uploads are named, so an asset
// can be recognised across runs.
func dedupKey(name string) string {
	if util.IsVideoFilename(name) {
		return name
	}
	return util.StripExtension(name)
}

// prefetchAlbumAssets returns the album's existing assets keyed by dedup key.
func (a *App) prefetchAlbumAssets(ctx context.Context, albumId string, logger *slog.Logger) map[string]string {
	existingFiles := make(map[string]string)
	if albumId == "" {
		return existingFiles
	}
	assets, err := a.Client.AlbumAssets(ctx, albumId)
	if err != nil {
		logger.Warn("Failed to fetch album assets", "error", err)
		return existingFiles
	}
	for _, asset := range assets {
		existingFiles[dedupKey(asset.OriginalFileName)] = asset.ID
	}
	logger.Debug("Pre-fetched album assets", "count", len(existingFiles))
	return existingFiles
}

func (a *App) processAlbum(ctx context.Context, ac config.GooglePhotosConfig, albumCache []immich.Album, globalAssets map[string]string) {
	logger := a.Logger.With("album_url", ac.URL)
	logger.Info("Syncing Google Photos Album")

	album, err := googlephotos.ScrapeAlbum(ctx, a.GPClient, ac.URL)
	if err != nil {
		logger.Error("Error scraping album", "error", err)
		return
	}

	albumTitle := album.Title
	if ac.AlbumName != "" {
		albumTitle = ac.AlbumName
	}
	logger.Info("Found photos in album", "count", len(album.Photos), "title", albumTitle)

	if len(album.Photos) == 0 {
		logger.Info("No photos found, skipping")
		return
	}

	albumId := a.resolveAlbumID(ctx, ac, albumTitle, albumCache, logger)
	existingFiles := a.prefetchAlbumAssets(ctx, albumId, logger)

	logger.Debug("Dedup cache loaded", "album_assets", len(existingFiles), "global_assets", len(globalAssets))

	var newAssetIds []string
	var matched, uploaded, wouldUpload, skipped, failed, ambiguous int

	total := len(album.Photos)
	processed := 0

	numWorkers := a.Cfg.Workers
	if numWorkers < 1 {
		numWorkers = 1
	}
	if numWorkers > total {
		numWorkers = total
	}

	logger.Debug("Processing items", "total_items", total, "workers", numWorkers)

	// Create and start progress tracker
	tracker := progress.New(albumTitle, total, a.Cfg.Debug)
	tracker.Start()

	jobs := make(chan googlephotos.Photo, numWorkers*2)
	results := make(chan processResult, numWorkers*2)
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				results <- a.processItem(ctx, p, albumTitle, ac.URL, existingFiles, globalAssets)
			}
		}()
	}

	// Feed jobs with context cancellation support
	go func() {
		defer close(jobs)
		for _, p := range album.Photos {
			select {
			case <-ctx.Done():
				return
			case jobs <- p:
			}
		}
	}()

	// Close results after all workers finish
	go func() {
		wg.Wait()
		close(results)
	}()

	flushInterval := total / 10
	if flushInterval < 1 {
		flushInterval = 1
	}
	lastFlushCount := 0
	canWrite := albumId != "" && !a.Cfg.Dedup.DryRun

	for res := range results {
		processed++

		switch res.Action {
		case ItemMatched:
			matched++
		case ItemUploaded:
			uploaded++
		case ItemWouldUpload:
			wouldUpload++
		case ItemSkipped:
			skipped++
		case ItemFailed:
			failed++
		}
		if res.Ambiguous {
			ambiguous++
		}
		if res.Error != nil {
			logger.Error("Failed to process item", "error", res.Error)
		}

		if (res.Action == ItemUploaded || res.Action == ItemMatched) && res.ID != "" {
			newAssetIds = append(newAssetIds, res.ID)
		}

		tracker.RecordItem(
			res.BytesDownloaded,
			res.BytesUploaded,
			res.Action == ItemUploaded,
			res.Action == ItemMatched || res.Action == ItemSkipped || res.Action == ItemWouldUpload,
			res.Action == ItemFailed || res.Error != nil,
		)

		// Flush new assets to album periodically for incremental progress.
		if canWrite && len(newAssetIds) > lastFlushCount && processed%flushInterval == 0 {
			batch := newAssetIds[lastFlushCount:]
			lastFlushCount = len(newAssetIds)
			logger.Debug("Adding assets to album (incremental)", "count", len(batch), "progress", fmt.Sprintf("%d/%d", processed, total))
			if err := a.Client.AddAssetsToAlbum(ctx, albumId, batch); err != nil {
				logger.Error("Error adding assets to album", "error", err)
			}
		}

		if a.Cfg.Debug && processed%100 == 0 {
			logger.Debug("Progress", "processed", processed, "total", total, "matched", matched, "uploaded", uploaded, "skipped", skipped, "failed", failed)
		}
	}

	tracker.Stop()

	// Final flush: add remaining unflushed assets to album.
	if canWrite && len(newAssetIds) > lastFlushCount {
		remaining := newAssetIds[lastFlushCount:]
		logger.Debug("Finalizing album assets", "count", len(remaining), "album", albumTitle)
		if err := a.Client.AddAssetsToAlbum(ctx, albumId, remaining); err != nil {
			logger.Error("Error adding assets to album", "error", err)
		}
	}

	// Persist dedup state to disk after each album.
	if !a.Cfg.Dedup.DryRun {
		if err := a.State.Save(); err != nil {
			logger.Warn("Failed to save dedup state", "error", err)
		}
	}

	logger.Info("Album sync finished",
		"total", processed,
		"matched", matched,
		"uploaded", uploaded,
		"would_upload", wouldUpload,
		"skipped", skipped,
		"failed", failed,
		"ambiguous", ambiguous,
		"dry_run", a.Cfg.Dedup.DryRun,
	)
}

// matchOptions maps the dedup configuration onto matcher options.
func (a *App) matchOptions() match.Options {
	return match.Options{
		MaxDistance: a.Cfg.Dedup.SimilarityThreshold,
		MinMargin:   a.Cfg.Dedup.AmbiguityMargin,
	}
}

// tryMatch looks for the same media item already present in Immich.
//
// For images, a small rendition of the Google item is downloaded (a few KB)
// and hashed; for videos, only dimensions are used. In both cases the search is
// restricted to a tight timestamp window first, then widened if nothing is
// found.
func (a *App) tryMatch(ctx context.Context, p googlephotos.Photo) match.Result {
	if p.TakenAt.IsZero() {
		return match.Result{Verdict: match.VerdictNoMatch}
	}

	var fingerprint match.Fingerprint
	if p.IsVideo {
		fingerprint = match.FingerprintForVideo(p.Width, p.Height, p.TakenAt)
	} else {
		thumb, err := googlephotos.DownloadThumbnail(ctx, a.GPClient, p.URL, a.Cfg.Dedup.ThumbnailWidth)
		if err != nil {
			a.Logger.Debug("Thumbnail unavailable, skipping smart match", "google_id", p.ID, "error", err)
			return match.Result{Verdict: match.VerdictNoMatch}
		}
		fingerprint, err = match.FingerprintFromBytes(thumb, p.TakenAt)
		if err != nil {
			a.Logger.Debug("Cannot fingerprint item, skipping smart match", "google_id", p.ID, "error", err)
			return match.Result{Verdict: match.VerdictNoMatch}
		}
	}

	candidates := a.findCandidates(ctx, fingerprint)
	if len(candidates) == 0 {
		return match.Result{Verdict: match.VerdictNoMatch}
	}

	return match.Decide(ctx, fingerprint, candidates, a.Client, a.matchOptions())
}

// maxWideCandidates bounds the fallback search: on a busy day the wide window
// can return hundreds of assets, and comparing all of them is not worth it.
const maxWideCandidates = 25

// findCandidates returns plausible Immich assets, nearest in time first.
func (a *App) findCandidates(ctx context.Context, fingerprint match.Fingerprint) []match.Candidate {
	narrow := time.Duration(a.Cfg.Dedup.WindowSeconds) * time.Second
	candidates := a.searchCandidates(ctx, fingerprint.TakenAt.Add(-narrow), fingerprint.TakenAt.Add(narrow), 50)
	if len(candidates) > 0 {
		return candidates
	}
	if a.Cfg.Dedup.WideWindowHours <= 0 {
		return nil
	}

	wide := time.Duration(a.Cfg.Dedup.WideWindowHours) * time.Hour
	candidates = a.searchCandidates(ctx, fingerprint.TakenAt.Add(-wide), fingerprint.TakenAt.Add(wide), 100)
	if len(candidates) > maxWideCandidates {
		a.Logger.Debug("Too many candidates in wide window, skipping match", "count", len(candidates))
		return nil
	}
	return candidates
}

// searchCandidates queries Immich for assets taken in the given window,
// excluding trashed assets and the tool's own uploads.
func (a *App) searchCandidates(ctx context.Context, from, to time.Time, limit int) []match.Candidate {
	assets, err := a.Client.SearchMetadata(ctx, immich.SearchFilter{
		TakenAfter:  from,
		TakenBefore: to,
		Size:        limit,
	})
	if err != nil {
		a.Logger.Debug("Candidate search failed", "error", err)
		return nil
	}

	candidates := make([]match.Candidate, 0, len(assets))
	for _, asset := range assets {
		if asset.IsTrashed || isOwnUpload(asset.OriginalFileName) {
			continue
		}
		candidates = append(candidates, match.Candidate{
			ID:       asset.ID,
			FileName: asset.OriginalFileName,
			Width:    asset.Width,
			Height:   asset.Height,
			IsVideo:  asset.IsVideo(),
			TakenAt:  asset.FileCreatedAt,
		})
	}
	return candidates
}

// isOwnUpload reports whether the filename was generated by this tool. Those
// copies must never win a match against a real user asset.
func isOwnUpload(name string) bool {
	return strings.HasPrefix(name, "gp_")
}

func (a *App) processItem(ctx context.Context, p googlephotos.Photo, albumTitle, albumURL string, existingFiles map[string]string, globalAssets map[string]string) processResult {
	safeId := strings.ReplaceAll(p.ID, "/", "_")
	safeId = strings.ReplaceAll(safeId, ":", "_")
	baseName := fmt.Sprintf("gp_%s", safeId)

	// Dedup checks against Immich (album-level, global device search, and persistent state)
	if assetId, exists := existingFiles[baseName]; exists {
		a.Logger.Debug("Asset already in album (Immich album lookup)", "id", assetId, "filename", baseName)
		return processResult{Action: ItemSkipped}
	}

	if assetId, exists := globalAssets[baseName]; exists {
		a.Logger.Debug("Asset exists in Immich (device search), adding to album", "id", assetId, "filename", baseName)
		return processResult{ID: assetId, Action: ItemMatched}
	}

	// Persistent local state catches assets uploaded under a different deviceId/filename
	if assetId, exists := a.State.Get(baseName); exists {
		a.Logger.Debug("Asset found in persistent dedup cache", "id", assetId, "filename", baseName)
		return processResult{ID: assetId, Action: ItemMatched}
	}

	if a.Cfg.StrictMetadata && p.TakenAt.IsZero() {
		a.Logger.Warn("Skipping item with missing metadata date",
			"id", p.ID, "url", p.URL)
		return processResult{Action: ItemSkipped}
	}

	if p.IsVideo && a.Cfg.SkipVideos {
		a.Logger.Debug("Skipping video item", "id", p.ID)
		return processResult{Action: ItemSkipped}
	}

	// Smart dedup: recognise an asset that is already in Immich before
	// downloading or uploading anything.
	var matchRes match.Result
	if a.Cfg.Dedup.Mode == "smart" {
		matchRes = a.tryMatch(ctx, p)

		if matchRes.Verdict == match.VerdictMatch {
			if !a.Cfg.Dedup.DryRun {
				a.State.Set(baseName, matchRes.AssetID)
			}
			a.Logger.Info("Matched an existing Immich asset, no upload",
				"google_id", p.ID,
				"asset_id", matchRes.AssetID,
				"distance", matchRes.Distance,
			)
			return processResult{
				ID:         matchRes.AssetID,
				Action:     ItemMatched,
				Distance:   matchRes.Distance,
				Candidates: matchRes.Scored,
			}
		}

		if matchRes.Verdict == match.VerdictAmbiguous {
			// Conservative choice: keep a duplicate and let the reconciliation
			// pass resolve it later. Never guess an album assignment.
			a.Logger.Warn("Ambiguous match: keeping a duplicate for reconciliation",
				"google_id", p.ID,
				"best_distance", matchRes.Distance,
				"second_distance", matchRes.SecondDistance,
				"candidates", len(matchRes.Scored),
			)
		}
	}

	if a.Cfg.Dedup.DryRun {
		a.Logger.Info("Dry run: item would be uploaded",
			"google_id", p.ID,
			"filename", baseName,
			"verdict", string(matchRes.Verdict),
		)
		return processResult{
			Action:     ItemWouldUpload,
			Distance:   matchRes.Distance,
			Ambiguous:  matchRes.Verdict == match.VerdictAmbiguous,
			Candidates: matchRes.Scored,
		}
	}

	a.Logger.Debug("Asset not found in dedup, will download",
		"baseName", baseName,
		"gp_item_id", p.ID,
	)

	a.Logger.Debug("Downloading item", "id", safeId)
	media, err := googlephotos.DownloadItemMedia(ctx, a.GPClient, p)
	if err != nil {
		return processResult{Action: ItemFailed, Error: fmt.Errorf("error downloading item: %w", err)}
	}
	data, ext, isVideo := media.Data, media.Ext, media.Kind == "video"

	bytesDownloaded := int64(len(data))
	if len(media.VideoData) > 0 {
		bytesDownloaded += int64(len(media.VideoData))
	}

	// For videos, also check both existingFiles and globalAssets by full filename to catch video dedup
	if isVideo {
		filename := baseName + ext
		if assetId, exists := existingFiles[filename]; exists {
			a.Logger.Debug("Video already in album (album-level dedup by filename)", "id", assetId, "filename", filename)
			return processResult{Action: ItemSkipped, BytesDownloaded: bytesDownloaded}
		}
		if assetId, exists := globalAssets[filename]; exists {
			a.Logger.Debug("Video exists in Immich (device search by filename), adding to album", "id", assetId, "filename", filename)
			return processResult{ID: assetId, Action: ItemMatched, BytesDownloaded: bytesDownloaded}
		}
	}

	filename := baseName + ext
	if isVideo {
		a.Logger.Debug("Classified item as video",
			"id", safeId,
			"filename", filename,
			"ext", ext,
		)
	}

	description := p.Description
	sep := "\n"
	if description != "" {
		sep = "\n\n"
	}
	description += fmt.Sprintf("%sSource Album: %s (%s)", sep, albumTitle, albumURL)

	if p.TakenAt.IsZero() {
		a.Logger.Warn("Uploading item with missing metadata date (using current time)",
			"id", safeId, "url", p.URL, "is_video", isVideo)
	}

	// Handle motion photos for images
	if !isVideo {
		imageData, videoData, isMotion, hadMotionXMP := googlephotos.ExtractMotionPhoto(data, a.Logger)
		motionVideoExt := ".mp4"
		// Some Motion Photos contain identifying XMP metadata while Google
		// serves the video component separately through the =dv endpoint.
		// Preserve upstream's sidecar behavior for those files.
		if !isMotion && hadMotionXMP {
			sidecarData, sidecarExt, sidecarErr := googlephotos.DownloadMotionVideoSidecar(ctx, a.GPClient, p.URL)
			if sidecarErr == nil {
				videoData = sidecarData
				isMotion = true
				if sidecarExt != "" {
					motionVideoExt = sidecarExt
				}
				a.Logger.Debug("Motion photo sidecar downloaded",
					"id", safeId,
					"video_size", len(videoData),
				)
			} else {
				a.Logger.Debug("Motion XMP found but sidecar unavailable, stripping XMP flags",
					"id", safeId,
					"error", sidecarErr,
				)
				googlephotos.StripMotionPhotoXMP(imageData)
			}
		}

		// Live photos detected via Apple QuickTime sidecar metadata: the
		// video component was already fetched by DownloadItemMedia.
		if !isMotion && !hadMotionXMP && media.Kind == "live" {
			videoData = media.VideoData
			isMotion = true
			if media.VideoExt != "" {
				motionVideoExt = media.VideoExt
			}
			a.Logger.Debug("Live photo sidecar downloaded",
				"id", safeId,
				"video_size", len(videoData),
			)
		}

		if isMotion {
			a.Logger.Debug("Detected motion photo",
				"id", safeId,
				"image_size", len(imageData),
				"video_size", len(videoData),
			)

			// Upload the video part first
			videoFilename := baseName + motionVideoExt
			videoId, videoDup, videoErr := a.Client.UploadAssetWithVisibility(ctx,
				videoData, videoFilename, p.TakenAt, "", "hidden")
			if videoErr != nil {
				a.Logger.Warn("Failed to upload motion video, uploading image as static photo", "error", videoErr)
			} else if videoDup {
				a.Logger.Debug("Motion video deduplicated by Immich", "filename", videoFilename, "id", videoId)
			}

			// Upload the image linked to the video
			var uploadedId string
			var isDup bool
			if videoId != "" {
				uploadedId, isDup, err = a.Client.UploadAssetWithLive(ctx,
					imageData, filename, p.TakenAt, description, videoId)
			} else {
				uploadedId, isDup, err = a.Client.UploadAsset(ctx,
					imageData, filename, p.TakenAt, description)
			}
			if err != nil {
				return processResult{Action: ItemFailed, Error: fmt.Errorf("error uploading %s: %w", filename, err), BytesDownloaded: bytesDownloaded}
			}
			if uploadedId == "" {
				return processResult{Action: ItemFailed, Error: fmt.Errorf("upload returned empty ID for %s", filename), BytesDownloaded: bytesDownloaded}
			}
			// Only apply the live photo link for deduplicated assets, since UploadAssetWithLive
			// already applied it during the initial upload
			if videoId != "" && isDup {
				if linkErr := a.Client.LinkLivePhotoToAsset(ctx, uploadedId, videoId); linkErr != nil {
					a.Logger.Warn("Failed to link motion video to deduplicated photo", "photo_id", uploadedId, "video_id", videoId, "error", linkErr)
				}
			}

			bytesUploaded := int64(len(imageData))
			if videoId != "" {
				bytesUploaded += int64(len(videoData))
			}
			a.State.Set(baseName, uploadedId)
			if isDup {
				a.Logger.Debug("Motion photo deduplicated by Immich", "filename", filename, "id", uploadedId)
				return processResult{ID: uploadedId, Action: ItemMatched, BytesDownloaded: bytesDownloaded, BytesUploaded: bytesUploaded}
			}
			a.Logger.Debug("Uploaded motion photo", "filename", filename, "id", uploadedId)
			return processResult{ID: uploadedId, Action: ItemUploaded, BytesDownloaded: bytesDownloaded, BytesUploaded: bytesUploaded}
		}

		// Not a motion photo
		data = imageData
	}

	uploadedId, isDup, err := a.Client.UploadAsset(ctx, data, filename, p.TakenAt, description)
	if err != nil {
		return processResult{Action: ItemFailed, Error: fmt.Errorf("error uploading %s: %w", filename, err), BytesDownloaded: bytesDownloaded}
	}
	if uploadedId == "" {
		return processResult{Action: ItemFailed, Error: fmt.Errorf("upload returned empty ID for %s", filename), BytesDownloaded: bytesDownloaded}
	}

	bytesUploaded := int64(len(data))
	a.State.Set(baseName, uploadedId)

	if isDup {
		a.Logger.Debug("Asset deduplicated by Immich", "filename", filename, "id", uploadedId)
		return processResult{ID: uploadedId, Action: ItemMatched, BytesDownloaded: bytesDownloaded, BytesUploaded: bytesUploaded}
	}

	a.Logger.Debug("Uploaded item", "filename", filename, "id", uploadedId)
	return processResult{ID: uploadedId, Action: ItemUploaded, BytesDownloaded: bytesDownloaded, BytesUploaded: bytesUploaded}
}
