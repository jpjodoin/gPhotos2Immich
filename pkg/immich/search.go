package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"time"

	// Thumbnails are JPEG; PNG covers the occasional asset.
	_ "image/jpeg"
	_ "image/png"
)

// Asset is the subset of Immich's asset representation used for deduplication.
type Asset struct {
	ID               string    `json:"id"`
	OriginalFileName string    `json:"originalFileName"`
	Type             string    `json:"type"` // "IMAGE" or "VIDEO"
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	Checksum         string    `json:"checksum"`
	FileCreatedAt    time.Time `json:"fileCreatedAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	IsTrashed        bool      `json:"isTrashed"`
}

// IsVideo reports whether the asset is a video.
func (a Asset) IsVideo() bool {
	return a.Type == "VIDEO"
}

// SearchFilter mirrors the parts of MetadataSearchDto we use.
type SearchFilter struct {
	AlbumIDs    []string
	TakenAfter  time.Time
	TakenBefore time.Time
	Page        int
	Size        int
}

type searchResponse struct {
	Assets struct {
		Items    []Asset `json:"items"`
		NextPage any     `json:"nextPage"`
	} `json:"assets"`
}

// SearchMetadata returns one page of assets matching the filter.
func (c *Client) SearchMetadata(ctx context.Context, filter SearchFilter) ([]Asset, error) {
	items, _, err := c.searchPage(ctx, filter)
	return items, err
}

// searchPage returns one page plus whether Immich reported another one.
func (c *Client) searchPage(ctx context.Context, filter SearchFilter) ([]Asset, bool, error) {
	payload := make(map[string]any, 8)
	if len(filter.AlbumIDs) > 0 {
		payload["albumIds"] = filter.AlbumIDs
	}
	if !filter.TakenAfter.IsZero() {
		payload["takenAfter"] = filter.TakenAfter.UTC().Format(time.RFC3339Nano)
	}
	if !filter.TakenBefore.IsZero() {
		payload["takenBefore"] = filter.TakenBefore.UTC().Format(time.RFC3339Nano)
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	size := filter.Size
	if size <= 0 {
		size = 100
	}
	payload["page"] = page
	payload["size"] = size

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("marshal search payload: %w", err)
	}

	body, err := c.request(ctx, "POST", "search/metadata", jsonPayload, "")
	if err != nil {
		return nil, false, fmt.Errorf("search metadata (page %d): %w", page, err)
	}

	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, false, fmt.Errorf("parse search response: %w", err)
	}

	hasMore := false
	switch next := resp.Assets.NextPage.(type) {
	case string:
		hasMore = next != ""
	case float64:
		hasMore = next > 0
	}
	if len(resp.Assets.Items) < size {
		hasMore = false
	}
	return resp.Assets.Items, hasMore, nil
}

// SearchMetadataAll paginates through every matching asset.
func (c *Client) SearchMetadataAll(ctx context.Context, filter SearchFilter) ([]Asset, error) {
	size := filter.Size
	if size <= 0 {
		size = 1000
	}
	filter.Size = size

	var all []Asset
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		filter.Page = page
		items, hasMore, err := c.searchPage(ctx, filter)
		if err != nil {
			return all, err
		}
		all = append(all, items...)
		if !hasMore || len(items) == 0 {
			break
		}
	}
	return all, nil
}

// AlbumAssets returns every asset contained in an album.
//
// Recent Immich versions no longer embed the asset list in the album response,
// so we query the search endpoint with an albumIds filter instead.
func (c *Client) AlbumAssets(ctx context.Context, albumID string) ([]Asset, error) {
	if albumID == "" {
		return nil, nil
	}
	return c.SearchMetadataAll(ctx, SearchFilter{AlbumIDs: []string{albumID}})
}

// GetAsset fetches a single asset by ID.
func (c *Client) GetAsset(ctx context.Context, assetID string) (*Asset, error) {
	body, err := c.request(ctx, "GET", fmt.Sprintf("assets/%s", assetID), nil, "")
	if err != nil {
		return nil, err
	}
	var asset Asset
	if err := json.Unmarshal(body, &asset); err != nil {
		return nil, fmt.Errorf("parse asset response: %w", err)
	}
	return &asset, nil
}

// Thumbnail downloads a small JPEG rendition of the asset. It satisfies
// match.ThumbnailLoader, letting the matcher compute a perceptual hash without
// downloading originals.
func (c *Client) Thumbnail(ctx context.Context, assetID string) (image.Image, error) {
	body, _, err := c.doHTTP(ctx, "GET", fmt.Sprintf("assets/%s/thumbnail?size=thumbnail", assetID), nil, "")
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("decode thumbnail for %s: %w", assetID, err)
	}
	return img, nil
}

// DeleteAssets permanently removes assets from Immich. Force bypasses the
// trash, which is what duplicate cleanup needs.
func (c *Client) DeleteAssets(ctx context.Context, assetIDs []string, force bool) error {
	const batchSize = 100
	for i := 0; i < len(assetIDs); i += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + batchSize
		if end > len(assetIDs) {
			end = len(assetIDs)
		}
		payload := map[string]any{
			"ids":   assetIDs[i:end],
			"force": force,
		}
		jsonPayload, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal delete payload: %w", err)
		}
		if _, err := c.request(ctx, "DELETE", "assets", jsonPayload, ""); err != nil {
			return fmt.Errorf("delete assets: %w", err)
		}
	}
	return nil
}
