// Package search implements the source search drivers (Prowlarr, Myrient,
// Vimm) plus result filtering, scoring, and per-source circuit breakers.
package search

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/models"
	"gamarr/internal/platform"
)

var pcRepackRe = regexp.MustCompile(
	`(?i)\b(fitgirl|dodi|skidrow|codex|cpy|empress|plaza|rune|razordox|gog|elamigos|` +
		`tinyiso|darksiders|delusional|masquerade)\b`,
)

// SearchProwlarr searches Prowlarr indexers for games.
func SearchProwlarr(cfg *config.Config, query string, platformSlug string) []*models.SearchResult {
	if !cfg.HasProwlarr() {
		return nil
	}

	if IsCircuitOpen("prowlarr") {
		slog.Warn("prowlarr circuit open, skipping search")
		return nil
	}

	filtered := platformSlug != "" && platformSlug != "all"

	client := &http.Client{Timeout: 15 * time.Second}
	var allItems []map[string]interface{}
	hadError := false

	indexers := GameIndexers(cfg)
	if len(indexers) == 0 {
		slog.Warn("no Prowlarr game indexers to search", "url", cfg.ProwlarrURL)
		return nil
	}

	for _, indexer := range indexers {
		indexerID := indexer.ID
		reqURL := fmt.Sprintf("%s/api/v1/search?query=%s&indexerIds=%d&type=search&limit=50",
			cfg.ProwlarrURL, url.QueryEscape(query), indexerID)
		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("X-Api-Key", cfg.ProwlarrAPIKey)

		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("indexer error", "indexer", indexerID, "error", err)
			hadError = true
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			hadError = true
			continue
		}
		var items []map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		allItems = append(allItems, items...)
	}

	if len(allItems) == 0 && hadError {
		RecordSearchFail("prowlarr", "all indexers failed")
	} else {
		RecordSearchSuccess("prowlarr")
	}

	var results []*models.SearchResult
	for _, item := range allItems {
		size := jsonInt64(item, "size")
		cats := jsonArray(item, "categories")
		title := jsonStr(item, "title")

		var detected platform.PlatformInfo
		if filtered {
			// Search context: a result the categories do not place anywhere
			// else is filed under the platform that was searched for, and one
			// they place on another platform is dropped.
			info, keep, assigned := platform.SearchContext(platformSlug, extractCatIDs(cats))
			if !keep {
				continue
			}
			// A PC repack name is positive evidence the categories lacked.
			if assigned && pcRepackRe.MatchString(title) {
				continue
			}
			detected = info
		} else {
			detected = platform.DetectPlatform(cats)
		}

		// Fallback: PC repack detection from title
		if detected.Name == "Unknown" {
			if pcRepackRe.MatchString(title) {
				detected = platform.PlatformInfo{Name: "PC", IsPC: true}
			}
		}

		downloadProtocol := "torrent"
		if strings.EqualFold(jsonStr(item, "protocol"), "usenet") {
			downloadProtocol = "nzb"
		}

		results = append(results, &models.SearchResult{
			Title:            jsonStr(item, "title"),
			Size:             size,
			SizeHuman:        HumanSize(size),
			Seeders:          jsonInt(item, "seeders"),
			Leechers:         jsonInt(item, "leechers"),
			Indexer:          jsonStr(item, "indexer"),
			DownloadURL:      jsonStr(item, "downloadUrl"),
			MagnetURL:        jsonStr(item, "magnetUrl"),
			InfoHash:         jsonStr(item, "infoHash"),
			GUID:             jsonStr(item, "guid"),
			Platform:         detected.Name,
			PlatformSlug:     detected.Slug,
			IsPC:             detected.IsPC,
			Age:              jsonInt(item, "age"),
			SourceType:       "torrent",
			DownloadProtocol: downloadProtocol,
			SafetyWarnings:   []string{},
		})
	}
	return results
}

func extractCatIDs(cats []interface{}) []int {
	return platform.CategoryIDs(cats)
}

func jsonStr(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func jsonInt(m map[string]interface{}, key string) int {
	v, _ := m[key].(float64)
	return int(v)
}

func jsonInt64(m map[string]interface{}, key string) int64 {
	v, _ := m[key].(float64)
	return int64(v)
}

func jsonArray(m map[string]interface{}, key string) []interface{} {
	v, _ := m[key].([]interface{})
	return v
}
