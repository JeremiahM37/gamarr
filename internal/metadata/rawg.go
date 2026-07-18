// Package metadata enriches games with cover art, genres, ratings, and other
// details fetched from the RAWG API.
package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// GameMetadata holds enriched game information from RAWG.
type GameMetadata struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	Slug            string   `json:"slug"`
	Description     string   `json:"description"`
	Released        string   `json:"released"`
	Rating          float64  `json:"rating"`
	Metacritic      int      `json:"metacritic"`
	BackgroundImage string   `json:"background_image"`
	Genres          []string `json:"genres"`
	Platforms       []string `json:"platforms"`
	Developers      []string `json:"developers"`
	Publishers      []string `json:"publishers"`
	ESRB            string   `json:"esrb_rating"`
}

const (
	rawgBaseURL = "https://api.rawg.io/api"
	cacheTTL    = 30 * time.Minute
)

type cacheEntry struct {
	data      *GameMetadata
	fetchedAt time.Time
}

// Client fetches metadata from RAWG.io with in-memory caching and rate limiting.
type Client struct {
	apiKey     string
	httpClient *http.Client
	mu         sync.RWMutex
	cache      map[string]cacheEntry
	rateMu     sync.Mutex
	lastReq    time.Time
}

// NewClient creates a RAWG metadata client. If apiKey is empty, all methods return nil.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		cache: make(map[string]cacheEntry),
	}
}

// Enabled returns true if the RAWG API key is configured.
func (c *Client) Enabled() bool {
	return c.apiKey != ""
}

// SearchGame searches RAWG for a game by query and optional platform slug.
// Returns the best match or nil if nothing found.
func (c *Client) SearchGame(query, platformSlug string) (*GameMetadata, error) {
	if !c.Enabled() {
		return nil, nil
	}

	cacheKey := strings.ToLower(query + "|" + platformSlug)

	// Check cache.
	c.mu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Since(entry.fetchedAt) < cacheTTL {
		c.mu.RUnlock()
		return entry.data, nil
	}
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.rateLimit()

	reqURL := fmt.Sprintf("%s/games", rawgBaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("key", c.apiKey)
	q.Set("search", query)
	q.Set("page_size", "5")
	q.Set("search_precise", "true")

	// Map our platform slug to RAWG platform ID if possible.
	if rawgPlatformID := mapPlatformSlugToRAWG(platformSlug); rawgPlatformID != "" {
		q.Set("platforms", rawgPlatformID)
	}

	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "Gamarr/2.0 (game download manager)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("RAWG API HTTP %d", resp.StatusCode)
	}

	var data rawgSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if len(data.Results) == 0 {
		return nil, nil
	}

	// Use the first result.
	result := &data.Results[0]
	meta := rawgGameToMetadata(result)

	// Cache the result.
	c.mu.Lock()
	c.cache[cacheKey] = cacheEntry{data: meta, fetchedAt: time.Now()}
	c.mu.Unlock()

	return meta, nil
}

// GetGame fetches full game details by RAWG ID.
func (c *Client) GetGame(id int) (*GameMetadata, error) {
	if !c.Enabled() {
		return nil, nil
	}

	cacheKey := fmt.Sprintf("id:%d", id)

	// Check cache.
	c.mu.RLock()
	if entry, ok := c.cache[cacheKey]; ok && time.Since(entry.fetchedAt) < cacheTTL {
		c.mu.RUnlock()
		return entry.data, nil
	}
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.rateLimit()

	reqURL := fmt.Sprintf("%s/games/%d", rawgBaseURL, id)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("key", c.apiKey)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", "Gamarr/2.0 (game download manager)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("game not found (RAWG ID %d)", id)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("RAWG API HTTP %d", resp.StatusCode)
	}

	var game rawgGameDetail
	if err := json.NewDecoder(resp.Body).Decode(&game); err != nil {
		return nil, err
	}

	meta := rawgDetailToMetadata(&game)

	// Cache the result.
	c.mu.Lock()
	c.cache[cacheKey] = cacheEntry{data: meta, fetchedAt: time.Now()}
	c.mu.Unlock()

	return meta, nil
}

// rateLimit enforces max 5 requests per second (200ms between requests).
func (c *Client) rateLimit() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()

	minInterval := 200 * time.Millisecond
	elapsed := time.Since(c.lastReq)
	if elapsed < minInterval {
		time.Sleep(minInterval - elapsed)
	}
	c.lastReq = time.Now()
}

// ── RAWG API response types ──────────────────────────────────────────────────

type rawgSearchResponse struct {
	Count   int             `json:"count"`
	Results []rawgGameEntry `json:"results"`
}

type rawgGameEntry struct {
	ID              int             `json:"id"`
	Name            string          `json:"name"`
	Slug            string          `json:"slug"`
	Released        string          `json:"released"`
	Rating          float64         `json:"rating"`
	Metacritic      int             `json:"metacritic"`
	BackgroundImage string          `json:"background_image"`
	Genres          []rawgNameEntry `json:"genres"`
	Platforms       []rawgPlatEntry `json:"platforms"`
	ESRBRating      *rawgNameEntry  `json:"esrb_rating"`
}

type rawgGameDetail struct {
	ID              int             `json:"id"`
	Name            string          `json:"name"`
	Slug            string          `json:"slug"`
	Description     string          `json:"description_raw"`
	Released        string          `json:"released"`
	Rating          float64         `json:"rating"`
	Metacritic      int             `json:"metacritic"`
	BackgroundImage string          `json:"background_image"`
	Genres          []rawgNameEntry `json:"genres"`
	Platforms       []rawgPlatEntry `json:"platforms"`
	Developers      []rawgNameEntry `json:"developers"`
	Publishers      []rawgNameEntry `json:"publishers"`
	ESRBRating      *rawgNameEntry  `json:"esrb_rating"`
}

type rawgNameEntry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type rawgPlatEntry struct {
	Platform rawgNameEntry `json:"platform"`
}

func rawgGameToMetadata(g *rawgGameEntry) *GameMetadata {
	meta := &GameMetadata{
		ID:              g.ID,
		Name:            g.Name,
		Slug:            g.Slug,
		Released:        g.Released,
		Rating:          g.Rating,
		Metacritic:      g.Metacritic,
		BackgroundImage: g.BackgroundImage,
	}
	for _, genre := range g.Genres {
		meta.Genres = append(meta.Genres, genre.Name)
	}
	for _, p := range g.Platforms {
		meta.Platforms = append(meta.Platforms, p.Platform.Name)
	}
	if g.ESRBRating != nil {
		meta.ESRB = g.ESRBRating.Name
	}
	return meta
}

func rawgDetailToMetadata(g *rawgGameDetail) *GameMetadata {
	meta := &GameMetadata{
		ID:              g.ID,
		Name:            g.Name,
		Slug:            g.Slug,
		Description:     truncate(g.Description, 1000),
		Released:        g.Released,
		Rating:          g.Rating,
		Metacritic:      g.Metacritic,
		BackgroundImage: g.BackgroundImage,
	}
	for _, genre := range g.Genres {
		meta.Genres = append(meta.Genres, genre.Name)
	}
	for _, p := range g.Platforms {
		meta.Platforms = append(meta.Platforms, p.Platform.Name)
	}
	for _, d := range g.Developers {
		meta.Developers = append(meta.Developers, d.Name)
	}
	for _, p := range g.Publishers {
		meta.Publishers = append(meta.Publishers, p.Name)
	}
	if g.ESRBRating != nil {
		meta.ESRB = g.ESRBRating.Name
	}
	return meta
}

// truncate caps s at roughly maxLen bytes, backing up to the nearest rune
// boundary so a multi-byte UTF-8 sequence is never split.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// ── Platform slug mapping ────────────────────────────────────────────────────

// mapPlatformSlugToRAWG maps Gamarr platform slugs to RAWG platform IDs.
// RAWG platform IDs: https://api.rawg.io/api/platforms?key=...
func mapPlatformSlugToRAWG(slug string) string {
	m := map[string]string{
		"pc":       "4",
		"ps2":      "15",
		"ps3":      "16",
		"ps4":      "18",
		"ps5":      "187",
		"psp":      "17",
		"psx":      "27",
		"vita":     "19",
		"xbox":     "80",
		"xbox360":  "14",
		"xboxone":  "1",
		"switch":   "7",
		"wii":      "11",
		"wiiu":     "10",
		"n64":      "83",
		"ngc":      "105",
		"nes":      "49",
		"snes":     "79",
		"gba":      "24",
		"gb":       "26",
		"gbc":      "43",
		"nds":      "9",
		"3ds":      "8",
		"genesis":  "167",
		"dc":       "106",
		"saturn":   "107",
		"sms":      "74",
		"gamegear": "77",
	}
	if id, ok := m[slug]; ok {
		return id
	}
	return ""
}

// rawgPlatformNameToSlug maps lowercase RAWG platform-name substrings to
// Gamarr platform slugs.
var rawgPlatformNameToSlug = map[string]string{
	"pc":                 "pc",
	"playstation 2":      "ps2",
	"playstation 3":      "ps3",
	"playstation 4":      "ps4",
	"playstation 5":      "ps5",
	"psp":                "psp",
	"playstation":        "psx",
	"ps vita":            "vita",
	"xbox":               "xbox",
	"xbox 360":           "xbox360",
	"xbox one":           "xboxone",
	"xbox series s/x":    "xboxone",
	"nintendo switch":    "switch",
	"wii":                "wii",
	"wii u":              "wiiu",
	"nintendo 64":        "n64",
	"gamecube":           "ngc",
	"nes":                "nes",
	"snes":               "snes",
	"super nintendo":     "snes",
	"game boy advance":   "gba",
	"game boy":           "gb",
	"game boy color":     "gbc",
	"nintendo ds":        "nds",
	"nintendo 3ds":       "3ds",
	"sega genesis":       "genesis",
	"sega mega drive":    "genesis",
	"dreamcast":          "dc",
	"sega saturn":        "saturn",
	"sega master system": "sms",
	"game gear":          "gamegear",
}

// rawgPlatformNamesOrdered holds the keys of rawgPlatformNameToSlug sorted
// longest-first (ties broken lexicographically) so substring matching is
// deterministic and the most specific name wins: "xbox 360" before "xbox",
// "snes" before "nes", "sega genesis" before "nes", "wii u" before "wii",
// "playstation 2" before "playstation", "game boy advance" before "game boy".
var rawgPlatformNamesOrdered = func() []string {
	keys := make([]string, 0, len(rawgPlatformNameToSlug))
	for k := range rawgPlatformNameToSlug {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}()

// MapRAWGPlatformToSlug maps a RAWG platform name to a Gamarr platform slug.
func MapRAWGPlatformToSlug(name string) string {
	lower := strings.ToLower(name)

	// URL-decode in case of encoded names.
	decoded, err := url.QueryUnescape(lower)
	if err == nil {
		lower = decoded
	}

	for _, key := range rawgPlatformNamesOrdered {
		if strings.Contains(lower, key) {
			return rawgPlatformNameToSlug[key]
		}
	}

	slog.Debug("unmapped RAWG platform", "name", name)
	return ""
}
