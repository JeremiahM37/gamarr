package search

import (
	"strings"

	"gamarr/internal/models"
)

// SourceNameFor maps a search result back to the health bucket of the source
// that produced it, so callers holding only a result can ask about the source.
// Minerva results carry a file selection; Vimm results carry a vault ID and
// Myrient is the only other DDL driver. Other results came through Prowlarr.
func SourceNameFor(r *models.SearchResult) string {
	if r == nil {
		return ""
	}
	if r.TorrentFileIndex != nil && strings.EqualFold(r.Indexer, "Minerva") {
		return "minerva"
	}
	if r.VimmID != "" {
		return "vimm"
	}
	if r.SourceType == "ddl" {
		return "myrient"
	}
	return "prowlarr"
}
