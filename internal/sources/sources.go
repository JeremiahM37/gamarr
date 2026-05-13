// Package sources holds the runtime registry of source-driver endpoints
// (base URLs and per-platform path/system mappings).
//
// Resolved at startup from, in order:
//
//  1. GAMARR_SOURCES_PATH — local JSON file (takes precedence)
//  2. GAMARR_SOURCES_URL  — HTTP(S) URL to a JSON file
//  3. embedded defaults    — fallback if neither is set or both fail to load
//
// Legacy per-source env vars (MYRIENT_URL, VIMM_URL) still take precedence
// over the registry value when set, so existing deployments need no
// migration.
package sources

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed defaults.json
var defaultsJSON []byte

// Registry is the in-memory representation of the source-driver registry.
type Registry struct {
	Version int         `json:"version"`
	Myrient MyrientSpec `json:"myrient"`
	Vimm    VimmSpec    `json:"vimm"`
}

// MyrientSpec carries the configurable bits of the Myrient direct-download driver.
type MyrientSpec struct {
	BaseURL       string            `json:"base_url"`
	PlatformPaths map[string]string `json:"platform_paths"`
}

// VimmSpec carries the configurable bits of the Vimm direct-download driver.
type VimmSpec struct {
	BaseURL         string            `json:"base_url"`
	PlatformSystems map[string]string `json:"platform_systems"`
}

// Default returns the embedded fallback registry.
// Used when no external source is configured or when fetching fails.
func Default() (*Registry, error) {
	var r Registry
	if err := json.Unmarshal(defaultsJSON, &r); err != nil {
		return nil, fmt.Errorf("decode embedded sources registry: %w", err)
	}
	return &r, nil
}
