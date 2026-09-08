package torznab

import (
	"encoding/xml"
	"strings"
	"testing"

	"gamarr/internal/models"
)

func TestResultToItemSelectionIsDiscoveryOnly(t *testing.T) {
	const collectionURL = "https://minerva.invalid/collection.torrent"
	const hash = "0123456789012345678901234567890123456789"
	const magnet = "magnet:?xt=urn:btih:" + hash
	for _, tc := range []struct{ name, url, magnet, hash, guid string }{
		{"magnet priority", collectionURL, magnet, hash, "minerva:" + hash + ":0"},
		{"collection URL", collectionURL, "", hash, ""},
		{"hash fallback", "", "", hash, ""},
		{"URL GUID fallback", "", "", "", collectionURL},
		{"magnet GUID fallback", "", "", "", magnet},
		{"URL without hash", collectionURL, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := 0
			result := &models.SearchResult{
				Title: "Pokemon - HeartGold (USA).nds", Platform: "DS", PlatformSlug: "nds",
				Indexer: "Minerva", SourceType: "torrent", Size: 134217728,
				DownloadURL: tc.url, MagnetURL: tc.magnet, InfoHash: tc.hash, GUID: tc.guid,
				TorrentFileIndex: &index, TorrentFilePath: "Nintendo - DS/Pokemon - HeartGold (USA).nds", TorrentFileSize: 134217728,
			}
			item := ResultToItem(result)
			if item.Link != "" || item.Enclosure != nil {
				t.Errorf("selection exposes unrestricted download: link=%q enclosure=%+v", item.Link, item.Enclosure)
			}
			if item.Title != "Pokemon - HeartGold (USA).nds" || item.Size != 134217728 || item.GUID == "" {
				t.Errorf("discovery metadata lost: %+v", item)
			}
			attrs := map[string]string{}
			for _, attr := range item.Attrs {
				attrs[attr.Name] = attr.Value
			}
			for name, want := range map[string]string{"torrent_file_index": "0", "torrent_file_path": "Nintendo - DS/Pokemon - HeartGold (USA).nds", "torrent_file_size": "134217728", "indexer": "Minerva"} {
				if attrs[name] != want {
					t.Errorf("attr[%s]=%q want %q", name, attrs[name], want)
				}
			}
			data, err := xml.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{collectionURL, "magnet:", "<link", "<enclosure"} {
				if strings.Contains(string(data), forbidden) {
					t.Errorf("serialized selection exposes %q: %s", forbidden, data)
				}
			}
		})
	}
}
