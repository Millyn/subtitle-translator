package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sherpaRepo  = "k2-fsa/sherpa-onnx"
	releasesAPI = "https://api.github.com/repos/" + sherpaRepo + "/releases/tags/asr-models"
)

// RemoteMeta represents a remote ASR model from GitHub releases.
type RemoteMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TagName     string `json:"tagName"`
	DownloadURL string `json:"downloadUrl"`
	SizeBytes   int64  `json:"sizeBytes"`
	PublishedAt string `json:"publishedAt"`
}

var (
	remoteCache     []RemoteMeta
	remoteCacheTime time.Time
	remoteCacheMu   sync.Mutex
)

// FetchRemoteModels fetches the latest ASR model list from GitHub releases.
// Results are cached for 5 minutes to avoid rate limiting.
func FetchRemoteModels(ctx context.Context) ([]RemoteMeta, error) {
	remoteCacheMu.Lock()
	defer remoteCacheMu.Unlock()
	if time.Since(remoteCacheTime) < 5*time.Minute && remoteCache != nil {
		return remoteCache, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch GitHub releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}

	var release struct {
		TagName   string `json:"tag_name"`
		Published string `json:"published_at"`
		Assets    []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}

	var models []RemoteMeta
	for _, asset := range release.Assets {
		if !strings.HasSuffix(asset.Name, ".tar.bz2") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(asset.Name, "sherpa-onnx-"), ".tar.bz2")
		models = append(models, RemoteMeta{
			ID:          id,
			Name:        strings.TrimSuffix(asset.Name, ".tar.bz2"),
			TagName:     release.TagName,
			DownloadURL: asset.BrowserDownloadURL,
			SizeBytes:   asset.Size,
			PublishedAt: release.Published,
		})
	}

	remoteCache = models
	remoteCacheTime = time.Now()
	return models, nil
}
