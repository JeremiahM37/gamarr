package api

import (
	"context"
	"sync"

	"gamarr/internal/models"
	"gamarr/internal/search"
)

// searchForTorznab is the SearchFunc passed to the Torznab handler. It runs
// the same 3-source fan-out as /api/search but skips the user-facing
// post-processing (blocklist filter, library-dedup, quality-profile rank,
// release-profile scoring) that downstream *arr consumers do themselves —
// they only want raw indexer-style results.
func (s *Server) searchForTorznab(ctx context.Context, query, platformSlug string) []*models.SearchResult {
	var allResults []*models.SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	slug := platformSlug
	if slug == "all" {
		slug = ""
	}

	wg.Add(3)
	go func() {
		defer wg.Done()
		results := search.SearchProwlarr(s.cfg, query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		results := search.SearchMyrient(s.cfg.Sources, query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		results := search.SearchVimm(s.cfg.Sources, query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	wg.Wait()

	// Split + filter torrent results; pass DDL through (FilterGameResults
	// targets torrent-only release artefacts like NFO/SFV/sample dirs).
	var torrentResults, ddlResults []*models.SearchResult
	for _, r := range allResults {
		if r.SourceType == "torrent" {
			torrentResults = append(torrentResults, r)
		} else {
			ddlResults = append(ddlResults, r)
		}
	}
	merged := append(search.FilterGameResults(torrentResults, query), ddlResults...)
	if merged == nil {
		return []*models.SearchResult{}
	}
	return search.ScoreResults(merged, query, platformSlug)
}
