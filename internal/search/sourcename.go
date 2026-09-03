package search

import "gamarr/internal/models"

// SourceNameFor maps a search result back to the health bucket of the source
// that produced it, so callers holding only a result can ask about the source.
// Vimm results carry a vault ID and Myrient is the only other DDL driver;
// everything else came through Prowlarr.
func SourceNameFor(r *models.SearchResult) string {
	if r == nil {
		return ""
	}
	if r.VimmID != "" {
		return "vimm"
	}
	if r.SourceType == "ddl" {
		return "myrient"
	}
	return "prowlarr"
}
