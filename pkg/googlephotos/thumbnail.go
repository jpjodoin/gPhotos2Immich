package googlephotos

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// DownloadThumbnail fetches a small rendition of a media item ("=w<width>").
// Shared albums serve re-encoded copies, so this is a cheap way to obtain a
// perceptual fingerprint without downloading the original. The returned bytes
// are always decoded before use, so a bad URL is detected immediately.
func DownloadThumbnail(ctx context.Context, client *Client, mediaURL string, width int) ([]byte, error) {
	if width <= 0 {
		width = 300
	}

	resp, err := client.Get(ctx, mediaBaseURL(mediaURL)+"=w"+strconv.Itoa(width))
	if err != nil {
		return nil, fmt.Errorf("thumbnail download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("thumbnail returned status %d", resp.StatusCode)
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "image/") {
		return nil, fmt.Errorf("thumbnail returned unexpected content type %q", contentType)
	}

	const maxThumbnailSize = 8 << 20 // 8 MiB, far above any real thumbnail
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailSize))
	if err != nil {
		return nil, fmt.Errorf("read thumbnail body: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty thumbnail body")
	}
	return data, nil
}

// mediaBaseURL strips an existing size suffix (e.g. "=w500", "=d") so a new
// suffix can be appended safely.
func mediaBaseURL(rawURL string) string {
	idx := strings.LastIndex(rawURL, "=")
	if idx < 0 || idx == len(rawURL)-1 {
		return rawURL
	}
	suffix := rawURL[idx+1:]
	if suffix[0] < 'a' || suffix[0] > 'z' {
		// A size suffix always starts with a letter ("d", "dv", "w500", ...).
		// Anything else is probably a query string parameter.
		return rawURL
	}
	for _, r := range suffix {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !isLower && !isDigit && r != '-' {
			return rawURL
		}
	}
	return rawURL[:idx]
}
