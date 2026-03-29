package search

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gamarr/internal/models"
)

var vimmSystemMap = map[string]string{
	"nes": "NES", "snes": "SNES", "n64": "N64", "ngc": "GameCube",
	"wii": "Wii", "gb": "GB", "gbc": "GBC", "gba": "GBA", "nds": "DS",
	"genesis": "Genesis", "saturn": "Saturn", "dc": "Dreamcast",
	"psx": "PS1", "ps2": "PS2", "ps3": "PS3", "psp": "PSP",
	"xbox": "Xbox", "xbox360": "Xbox360",
}

// VimmPlatformSlugs returns all platform slugs Vimm supports.
func VimmPlatformSlugs() []string {
	slugs := make([]string, 0, len(vimmSystemMap))
	for s := range vimmSystemMap {
		slugs = append(slugs, s)
	}
	return slugs
}

var vimmGameRe = regexp.MustCompile(`<a\s+href=\s*"/vault/(\d+)"[^>]*>([^<]+)</a>`)

// SearchVimm searches Vimm's Lair for ROMs.
func SearchVimm(query string, platformSlug string) []*models.SearchResult {
	params := url.Values{"p": {"list"}, "q": {query}}
	if platformSlug != "" {
		if sys, ok := vimmSystemMap[platformSlug]; ok {
			params.Set("system", sys)
		}
	}
	systemFromFilter := vimmSystemMap[platformSlug]

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, _ := http.NewRequest("GET", "https://vimm.net/vault/?"+params.Encode(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Vimm search error", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Build reverse map for platform detection
	reverseMap := make(map[string]string)
	for slug, sys := range vimmSystemMap {
		reverseMap[strings.ToLower(sys)] = slug
	}

	var results []*models.SearchResult
	titleSysRe := regexp.MustCompile(`\(([A-Za-z0-9]+)\)\s*$`)

	for _, m := range vimmGameRe.FindAllStringSubmatch(string(body), -1) {
		gameID := m[1]
		gameName := strings.TrimSpace(m[2])

		slug := platformSlug
		systemClean := systemFromFilter
		if systemClean == "" {
			systemClean = "Unknown"
		}

		// Detect platform from title when no filter
		if slug == "" {
			if ts := titleSysRe.FindStringSubmatch(gameName); ts != nil {
				sysName := ts[1]
				if s, ok := reverseMap[strings.ToLower(sysName)]; ok {
					slug = s
					systemClean = sysName
				}
			}
		}

		displayTitle := gameName
		if systemClean != "" && systemClean != "Unknown" && !strings.Contains(gameName, "("+systemClean+")") {
			displayTitle = fmt.Sprintf("%s (%s)", gameName, systemClean)
		}

		results = append(results, &models.SearchResult{
			Title:        displayTitle,
			SizeHuman:    "?",
			Indexer:      "Vimm's Lair",
			GUID:         fmt.Sprintf("https://vimm.net/vault/%s", gameID),
			Platform:     systemClean,
			PlatformSlug: slug,
			SourceType:   "ddl",
			VimmID:       gameID,
			SafetyScore:    90,
			SafetyWarnings: []string{},
		})
	}
	if len(results) > 20 {
		results = results[:20]
	}
	return results
}
