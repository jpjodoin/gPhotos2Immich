package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"warreth.dev/gphotos2immich/pkg/duration"
)

type GooglePhotosConfig struct {
	URL           string `json:"url"`
	ImmichAlbumID string `json:"immichAlbumId"` // Optional, if existing
	AlbumName     string `json:"albumName"`     // Optional, to create new
	SyncInterval  string `json:"syncInterval"`  // e.g., "12h", "7d"
	// Enabled defaults to true. Use a pointer so an absent field is not
	// mistaken for "disabled".
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled reports whether the album should be synced. Absent means enabled.
func (g GooglePhotosConfig) IsEnabled() bool {
	return g.Enabled == nil || *g.Enabled
}

// DedupConfig controls how already-present Immich assets are recognised so
// they can be added to an album without uploading a duplicate.
type DedupConfig struct {
	// Mode is "smart" (default) or "off".
	Mode string `json:"mode"`
	// DryRun performs the matching and logs the outcome but writes nothing to
	// Immich (no upload, no album change, no deletion).
	DryRun bool `json:"dryRun"`
	// SimilarityThreshold is the maximum Hamming distance (out of 64) between
	// two perceptual hashes for the assets to be considered the same photo.
	SimilarityThreshold int `json:"similarityThreshold"`
	// AmbiguityMargin is the minimum distance gap between the best and second
	// best candidate. Below it, the item is treated as a kept duplicate.
	AmbiguityMargin int `json:"ambiguityMargin"`
	// WindowSeconds is the tight timestamp window used to find candidates.
	WindowSeconds int `json:"windowSeconds"`
	// WideWindowHours is the fallback timestamp window (0 disables it).
	WideWindowHours int `json:"wideWindowHours"`
	// ThumbnailWidth is the width of the small Google rendition used to
	// compute the perceptual hash (kept small to save bandwidth).
	ThumbnailWidth int `json:"thumbnailWidth"`
}

// DefaultDedup returns the default dedup configuration.
func DefaultDedup() DedupConfig {
	return DedupConfig{
		Mode:                "smart",
		DryRun:              false,
		SimilarityThreshold: 14,
		AmbiguityMargin:     4,
		WindowSeconds:       3,
		WideWindowHours:     24,
		ThumbnailWidth:      300,
	}
}

type Config struct {
	ApiKey         string               `json:"apiKey"`
	ApiURL         string               `json:"apiURL"`
	Debug          bool                 `json:"debug"`          // Optional, enable verbose logging
	Workers        int                  `json:"workers"`        // Optional, default 1
	AlbumWorkers   int                  `json:"albumWorkers"`   // Optional, concurrent album processing (default 1)
	StrictMetadata bool                 `json:"strictMetadata"` // Optional, skip items with missing dates
	SkipVideos     bool                 `json:"skipVideos"`     // Optional, skip video items entirely
	DataDir        string               `json:"dataDir"`        // Optional, directory for persistent state (default "./data")
	GooglePhotos   []GooglePhotosConfig `json:"googlePhotos"`
	Dedup          DedupConfig          `json:"dedup"`
}

func ReadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try ENV vars for basic config
			apiKey := os.Getenv("IMMICH_API_KEY")
			apiURL := os.Getenv("IMMICH_API_URL")

			if apiKey == "" || apiURL == "" {
				return nil, fmt.Errorf("config file not found and ENV vars missing")
			}

			cfg := &Config{
				ApiKey: apiKey,
				ApiURL: apiURL,
			}
			cfg.applyDefaults()
			return cfg, nil
		}
		return nil, err
	}
	defer file.Close()

	bytefile, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("error reading config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(bytefile, &config); err != nil {
		return nil, err
	}

	// ENV vars take runtime precedence over the config file so rotated
	// keys (e.g. a new IMMICH_API_KEY deployed via container ENV) are picked
	// up on the next config read instead of causing auth crash loops.
	if v := os.Getenv("IMMICH_API_KEY"); v != "" {
		config.ApiKey = v
	}
	if v := os.Getenv("IMMICH_API_URL"); v != "" {
		config.ApiURL = v
	}

	config.ApiKey = strings.TrimSpace(config.ApiKey)
	config.ApiURL = strings.TrimSpace(config.ApiURL)
	for i := range config.GooglePhotos {
		config.GooglePhotos[i].URL = strings.TrimSpace(config.GooglePhotos[i].URL)
		config.GooglePhotos[i].ImmichAlbumID = strings.TrimSpace(config.GooglePhotos[i].ImmichAlbumID)
		config.GooglePhotos[i].AlbumName = strings.TrimSpace(config.GooglePhotos[i].AlbumName)
		config.GooglePhotos[i].SyncInterval = strings.TrimSpace(config.GooglePhotos[i].SyncInterval)
	}

	config.applyDefaults()
	return &config, nil
}

// applyDefaults fills in the optional dedup settings.
func (c *Config) applyDefaults() {
	defaults := DefaultDedup()

	if c.Dedup.Mode == "" {
		c.Dedup.Mode = defaults.Mode
	}
	c.Dedup.Mode = strings.ToLower(strings.TrimSpace(c.Dedup.Mode))
	if c.Dedup.SimilarityThreshold <= 0 {
		c.Dedup.SimilarityThreshold = defaults.SimilarityThreshold
	}
	if c.Dedup.AmbiguityMargin <= 0 {
		c.Dedup.AmbiguityMargin = defaults.AmbiguityMargin
	}
	if c.Dedup.WindowSeconds <= 0 {
		c.Dedup.WindowSeconds = defaults.WindowSeconds
	}
	if c.Dedup.ThumbnailWidth <= 0 {
		c.Dedup.ThumbnailWidth = defaults.ThumbnailWidth
	}
}

// Validate checks that all required config fields are present and valid
func (c *Config) Validate() error {
	if c.ApiKey == "" {
		return fmt.Errorf("apiKey is required (set in config or IMMICH_API_KEY env var)")
	}
	if c.ApiURL == "" {
		return fmt.Errorf("apiURL is required (set in config or IMMICH_API_URL env var)")
	}
	if c.Workers < 0 {
		return fmt.Errorf("workers must be non-negative")
	}
	if c.AlbumWorkers < 0 {
		return fmt.Errorf("albumWorkers must be non-negative")
	}
	if c.Dedup.Mode != "" && c.Dedup.Mode != "smart" && c.Dedup.Mode != "off" {
		return fmt.Errorf("dedup.mode %q is invalid (want \"smart\" or \"off\")", c.Dedup.Mode)
	}
	if c.Dedup.SimilarityThreshold < 0 || c.Dedup.SimilarityThreshold > 32 {
		return fmt.Errorf("dedup.similarityThreshold must be between 0 and 32")
	}
	if c.Dedup.AmbiguityMargin < 0 || c.Dedup.AmbiguityMargin > 32 {
		return fmt.Errorf("dedup.ambiguityMargin must be between 0 and 32")
	}
	for i, gp := range c.GooglePhotos {
		if gp.URL == "" {
			return fmt.Errorf("googlePhotos[%d].url is required", i)
		}
		if gp.SyncInterval != "" {
			if _, err := duration.Parse(gp.SyncInterval); err != nil {
				return fmt.Errorf("googlePhotos[%d].syncInterval %q is invalid: %w", i, gp.SyncInterval, err)
			}
		}
	}
	return nil
}
