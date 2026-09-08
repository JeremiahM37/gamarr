package search

import (
	"context"
	"fmt"

	"gamarr/internal/minerva"
	"gamarr/internal/models"
	"gamarr/internal/platform"
)

// SearchMinerva searches the local file index; search never refreshes metadata
// or queries live torrent health.
func SearchMinerva(svc *minerva.Service, query, platformSlug string) []*models.SearchResult {
	if svc == nil || platformSlug == "" || platformSlug == "all" || IsCircuitOpen("minerva") {
		return nil
	}
	ctx := context.Background()
	if !svc.Ready(ctx) {
		return nil
	}
	files, err := svc.Search(ctx, query, platformSlug, 100)
	if err != nil {
		RecordSearchFail("minerva", err.Error())
		return nil
	}
	RecordSearchSuccess("minerva")
	var results []*models.SearchResult
	for _, file := range files {
		name := file.PlatformSlug
		for _, info := range platform.PlatformMap {
			if info.Slug == file.PlatformSlug {
				name = info.Name
				break
			}
		}
		for _, info := range platform.ExtraPlatforms {
			if info.Slug == file.PlatformSlug {
				name = info.Name
				break
			}
		}
		index := file.FileIndex
		results = append(results, &models.SearchResult{
			Title: file.Name, Size: file.Size, SizeHuman: HumanSize(file.Size),
			Indexer: "Minerva", SourceType: "torrent", DownloadProtocol: "torrent",
			DownloadURL: file.TorrentURL, InfoHash: file.InfoHash,
			GUID:     fmt.Sprintf("minerva:%s:%d", file.InfoHash, file.FileIndex),
			Platform: name, PlatformSlug: file.PlatformSlug,
			TorrentFileIndex: &index, TorrentFilePath: file.Path, TorrentFileSize: file.Size,
			SafetyScore: 95, SafetyWarnings: []string{},
		})
	}
	return results
}
