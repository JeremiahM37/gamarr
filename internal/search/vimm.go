package search

import (
	"context"
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
	"gamarr/internal/sources"
)

// VimmPlatformSlugs returns all platform slugs Vimm supports per the runtime
// sources registry.
func VimmPlatformSlugs(reg *sources.Registry) []string {
	slugs := make([]string, 0, len(reg.Vimm.PlatformSystems))
	for s := range reg.Vimm.PlatformSystems {
		slugs = append(slugs, s)
	}
	return slugs
}

var vimmGameRe = regexp.MustCompile(`<a\s+href=\s*"/vault/(\d+)"[^>]*>([^<]+)</a>`)

// SearchVimm searches Vimm's Lair for ROMs. ctx cancels the search request.
func SearchVimm(ctx context.Context, reg *sources.Registry, query string, platformSlug string) []*models.SearchResult {
	if IsCircuitOpen("vimm") {
		slog.Warn("vimm circuit open, skipping search")
		return nil
	}
	params := url.Values{"p": {"list"}, "q": {query}}
	if platformSlug != "" {
		if sys, ok := reg.Vimm.PlatformSystems[platformSlug]; ok {
			params.Set("system", sys)
		}
	}
	systemFromFilter := reg.Vimm.PlatformSystems[platformSlug]

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", reg.Vimm.BaseURL+"?"+params.Encode(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Vimm search error", "error", err)
		RecordSearchFail("vimm", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		RecordSearchFail("vimm", fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Build reverse map for platform detection
	reverseMap := make(map[string]string)
	for slug, sys := range reg.Vimm.PlatformSystems {
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
			Title:          displayTitle,
			SizeHuman:      "?",
			Indexer:        "Vimm's Lair",
			GUID:           fmt.Sprintf("%s%s", reg.Vimm.BaseURL, gameID),
			Platform:       systemClean,
			PlatformSlug:   slug,
			SourceType:     "ddl",
			VimmID:         gameID,
			SafetyScore:    90,
			SafetyWarnings: []string{},
		})
	}
	if len(results) > 20 {
		results = results[:20]
	}

	RecordSearchSuccess("vimm")
	return results
}
