package search

import "gamarr/internal/models"

// SourceNameFor maps a search result back to the health bucket of the source
// that produced it, so callers holding only a result can ask about the source.
// Vimm results carry a vault ID; Minerva sets its indexer name; remaining DDL
// is Myrient. Everything else came through Prowlarr.
func SourceNameFor(r *models.SearchResult) string {
	if r == nil {
		return ""
	}
	if r.VimmID != "" {
		return "vimm"
	}
	if r.Indexer == "Minerva" {
		return "minerva"
	}
	if r.SourceType == "ddl" {
		return "myrient"
	}
	return "prowlarr"
}
