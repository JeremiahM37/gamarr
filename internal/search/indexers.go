package search

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"gamarr/internal/config"
)

// indexerCacheTTL bounds how often Gamarr asks Prowlarr what its indexers can
// do. Indexers change when a user edits them, which is rare next to searching.
const indexerCacheTTL = 5 * time.Minute

// Indexer is the part of a Prowlarr indexer definition Gamarr needs: which one
// to query, and what to call it when reporting what was searched.
type Indexer struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// prowlarrIndexer mirrors the fields of GET /api/v1/indexer that decide whether
// an indexer is worth searching for games.
type prowlarrIndexer struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Enable       bool   `json:"enable"`
	Capabilities struct {
		Categories []prowlarrCategory `json:"categories"`
	} `json:"capabilities"`
}

type prowlarrCategory struct {
	ID            int                `json:"id"`
	SubCategories []prowlarrCategory `json:"subCategories"`
}

// hasGameCategory reports whether the indexer advertises any category in the
// standard Newznab game ranges: 1000–1999 (Console) and 4000–4999 (PC),
// including subcategories, where trackers usually put the useful ones.
func (i prowlarrIndexer) hasGameCategory() bool {
	var walk func([]prowlarrCategory) bool
	walk = func(cats []prowlarrCategory) bool {
		for _, c := range cats {
			if (c.ID >= 1000 && c.ID < 2000) || (c.ID >= 4000 && c.ID < 5000) {
				return true
			}
			if walk(c.SubCategories) {
				return true
			}
		}
		return false
	}
	return walk(i.Capabilities.Categories)
}

var indexerCache struct {
	sync.Mutex
	key     string
	list    []prowlarrIndexer
	fetched time.Time
}

// ClearIndexerCache drops the cached indexer list, so the next search re-reads
// it from Prowlarr.
func ClearIndexerCache() {
	indexerCache.Lock()
	defer indexerCache.Unlock()
	indexerCache.key, indexerCache.list, indexerCache.fetched = "", nil, time.Time{}
}

// fetchIndexers returns Prowlarr's indexer list, cached, and whether this call
// went to the network. Callers use fresh to log at most once per refresh
// instead of once per search.
func fetchIndexers(cfg *config.Config) (list []prowlarrIndexer, fresh bool, err error) {
	indexerCache.Lock()
	defer indexerCache.Unlock()

	if indexerCache.key == cfg.ProwlarrURL && time.Since(indexerCache.fetched) < indexerCacheTTL {
		return indexerCache.list, false, nil
	}

	req, _ := http.NewRequest("GET", cfg.ProwlarrURL+"/api/v1/indexer", nil)
	req.Header.Set("X-Api-Key", cfg.ProwlarrAPIKey)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, true, fmt.Errorf("prowlarr returned HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, true, err
	}

	indexerCache.key, indexerCache.list, indexerCache.fetched = cfg.ProwlarrURL, list, time.Now()
	return list, true, nil
}

// GameIndexers resolves which Prowlarr indexers a game search should query.
//
// By default they are discovered from Prowlarr's own capabilities: every
// enabled indexer advertising a game category. Prowlarr numbers indexers in the
// order each user added them, so a hardcoded list of IDs means something
// different on every install — and querying an ID that does not exist returns
// an empty result set with HTTP 200, which reads as "no releases" rather than
// "asked the wrong tracker".
//
// PROWLARR_GAME_INDEXERS stays available to narrow the set (to exclude a slow
// tracker, or one only used manually), and IDs in it that the instance does not
// have are reported rather than silently searched.
func GameIndexers(cfg *config.Config) []Indexer {
	if !cfg.HasProwlarr() {
		return nil
	}

	all, fresh, err := fetchIndexers(cfg)
	if err != nil {
		// Prowlarr is unreachable; the search itself is about to fail too. An
		// explicit list still works without the capability lookup, so honor it.
		if len(cfg.ProwlarrGameIndexers) > 0 {
			out := make([]Indexer, 0, len(cfg.ProwlarrGameIndexers))
			for _, id := range cfg.ProwlarrGameIndexers {
				out = append(out, Indexer{ID: id, Name: fmt.Sprintf("indexer %d", id)})
			}
			return out
		}
		slog.Warn("cannot read Prowlarr indexers, no game indexers to search",
			"url", cfg.ProwlarrURL, "error", err)
		return nil
	}

	if len(cfg.ProwlarrGameIndexers) > 0 {
		return configuredIndexers(all, cfg.ProwlarrGameIndexers, fresh)
	}
	return discoverGameIndexers(all, fresh)
}

// configuredIndexers resolves an explicit PROWLARR_GAME_INDEXERS list against
// the instance, reporting the IDs it does not have.
func configuredIndexers(all []prowlarrIndexer, ids []int, fresh bool) []Indexer {
	byID := make(map[int]prowlarrIndexer, len(all))
	for _, idx := range all {
		byID[idx.ID] = idx
	}

	out := make([]Indexer, 0, len(ids))
	var unknown []int
	for _, id := range ids {
		idx, ok := byID[id]
		if !ok {
			unknown = append(unknown, id)
			continue
		}
		if !idx.Enable {
			if fresh {
				slog.Warn("configured game indexer is disabled in Prowlarr", "id", id, "name", idx.Name)
			}
			continue
		}
		if fresh && !idx.hasGameCategory() {
			slog.Warn("configured game indexer advertises no game categories",
				"id", id, "name", idx.Name)
		}
		out = append(out, Indexer{ID: idx.ID, Name: idx.Name})
	}
	if fresh && len(unknown) > 0 {
		slog.Warn("PROWLARR_GAME_INDEXERS names indexers this Prowlarr does not have "+
			"(IDs are per-instance; unset it to discover them by capability)",
			"missing", unknown)
	}
	return out
}

// discoverGameIndexers picks every enabled indexer that says it carries games.
func discoverGameIndexers(all []prowlarrIndexer, fresh bool) []Indexer {
	var out, enabled []Indexer
	for _, idx := range all {
		if !idx.Enable {
			continue
		}
		enabled = append(enabled, Indexer{ID: idx.ID, Name: idx.Name})
		if idx.hasGameCategory() {
			out = append(out, Indexer{ID: idx.ID, Name: idx.Name})
		}
	}

	if len(out) == 0 && len(enabled) > 0 {
		// Nothing advertises games. Searching every enabled indexer is a worse
		// default than searching the right ones, but a much better one than
		// searching nothing and reporting "no releases".
		if fresh {
			slog.Warn("no Prowlarr indexer advertises game categories, searching all enabled indexers",
				"count", len(enabled))
		}
		out = enabled
	}
	if fresh {
		slog.Info("discovered Prowlarr game indexers", "count", len(out))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
