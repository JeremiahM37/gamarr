package search

import (
	"testing"

	"gamarr/internal/models"
)

func TestSourceNameFor(t *testing.T) {
	zero, seven := 0, 7
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
		{"minerva index zero", &models.SearchResult{SourceType: "torrent", Indexer: "Minerva", TorrentFileIndex: &zero}, "minerva"},
		{"minerva index seven", &models.SearchResult{SourceType: "torrent", Indexer: "Minerva", TorrentFileIndex: &seven}, "minerva"},
		{"minerva case insensitive", &models.SearchResult{SourceType: "torrent", Indexer: "MINERVA", TorrentFileIndex: &zero}, "minerva"},
		{"indexer name alone", &models.SearchResult{SourceType: "torrent", Indexer: "Minerva"}, "prowlarr"},
		{"different selection indexer", &models.SearchResult{SourceType: "torrent", Indexer: "SomeTracker", TorrentFileIndex: &zero}, "prowlarr"},
	}
	for _, c := range cases {
		if got := SourceNameFor(c.r); got != c.want {
			t.Errorf("%s: SourceNameFor = %q, want %q", c.name, got, c.want)
		}
	}
}
