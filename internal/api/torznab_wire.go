package api

import (
	"context"
	"sync"

	"gamarr/internal/models"
	"gamarr/internal/search"
)

// searchForTorznab is the SearchFunc passed to the Torznab handler. It runs
// the same source fan-out as /api/search but skips the user-facing
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
	if s.minerva != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results := search.SearchMinerva(s.minerva, query, slug)
			mu.Lock()
			allResults = append(allResults, results...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Split + filter generic torrent results; pass curated selections and DDL
	// through, preserving Minerva's safety score without live seeder metadata.
	var torrentResults, ddlResults []*models.SearchResult
	for _, r := range allResults {
		if r.SourceType == "torrent" && r.TorrentFileIndex == nil {
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
