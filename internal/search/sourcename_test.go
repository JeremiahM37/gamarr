package search

import (
	"testing"

	"gamarr/internal/models"
)

func TestSourceNameFor(t *testing.T) {
	cases := []struct {
		name string
		r    *models.SearchResult
		want string
	}{
		{"nil", nil, ""},
		{"vimm", &models.SearchResult{SourceType: "ddl", VimmID: "1654", Indexer: "Vimm's Lair"}, "vimm"},
		{"myrient", &models.SearchResult{SourceType: "ddl", Indexer: "Myrient"}, "myrient"},
		{"torrent", &models.SearchResult{SourceType: "torrent", Indexer: "SomeTracker"}, "prowlarr"},
		{"usenet", &models.SearchResult{SourceType: "torrent", DownloadProtocol: "nzb"}, "prowlarr"},
	}
	for _, c := range cases {
		if got := SourceNameFor(c.r); got != c.want {
			t.Errorf("%s: SourceNameFor = %q, want %q", c.name, got, c.want)
		}
	}
}
