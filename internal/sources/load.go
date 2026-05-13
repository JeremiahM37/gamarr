package sources

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Load resolves the active registry from (in order): a local file path, an
// HTTP URL, or the embedded defaults. Any error fetching/parsing an external
// source is logged and the embedded defaults are returned so the binary never
// fails to start over a missing registry.
//
// path takes precedence over url. Both arguments may be empty.
func Load(path, url string) *Registry {
	if path != "" {
		if r, err := loadFile(path); err == nil {
			slog.Info("loaded sources registry from file", "path", path, "version", r.Version)
			return r
		} else {
			slog.Warn("failed to load sources registry from file, falling back to embedded defaults",
				"path", path, "error", err)
		}
	}
	if url != "" {
		if r, err := loadURL(url); err == nil {
			slog.Info("loaded sources registry from URL", "url", url, "version", r.Version)
			return r
		} else {
			slog.Warn("failed to load sources registry from URL, falling back to embedded defaults",
				"url", url, "error", err)
		}
	}
	r, err := Default()
	if err != nil {
		// Would only happen if the embedded JSON itself were malformed —
		// a build-time invariant. Return a non-nil empty registry so callers
		// can still range over maps.
		slog.Error("embedded sources registry failed to decode; returning empty registry", "error", err)
		return &Registry{}
	}
	return r
}

func loadFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(data)
}

func loadURL(url string) (*Registry, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("url must be http(s)")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return nil, err
	}
	return decode(body)
}

func decode(data []byte) (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ApplyEnvOverrides patches the registry with any legacy per-source env vars
// that are set. Returns the (mutated) input registry for chaining. Preserves
// backwards compatibility for users who configure individual endpoints via
// env vars instead of editing the registry.
func (r *Registry) ApplyEnvOverrides(getenv func(string) string) *Registry {
	if v := getenv("MYRIENT_URL"); v != "" {
		r.Myrient.BaseURL = v
	}
	if v := getenv("VIMM_URL"); v != "" {
		r.Vimm.BaseURL = v
	}
	return r
}
