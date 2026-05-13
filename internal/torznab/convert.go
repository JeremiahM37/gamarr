package torznab

import (
	"crypto/md5"
	"fmt"
	"time"

	"gamarr/internal/models"
)

// ResultToItem converts a gamarr SearchResult to a Torznab feed item.
//
// The download link priority is magnet > download URL > info_hash-derived
// magnet > result GUID (typically a detail-page URL).
func ResultToItem(r *models.SearchResult) Item {
	category := CategoryForPlatform(r.PlatformSlug)

	// Stable GUID — fall back to a content hash of (title + indexer) if the
	// result has no native identifier so the same release gets the same GUID
	// across requests.
	guid := r.GUID
	if guid == "" {
		switch {
		case r.InfoHash != "":
			guid = r.InfoHash
		case r.DownloadURL != "":
			guid = r.DownloadURL
		default:
			guid = fmt.Sprintf("%x", md5.Sum([]byte(r.Title+r.Indexer)))
		}
	}

	item := Item{
		Title:    r.Title,
		GUID:     guid,
		Size:     r.Size,
		Category: categoryName(category),
		PubDate:  time.Now().UTC().Format(time.RFC1123Z),
		Attrs: []Attr{
			{Name: "category", Value: category},
		},
	}

	if r.Size > 0 {
		item.Attrs = append(item.Attrs, Attr{Name: "size", Value: fmt.Sprintf("%d", r.Size)})
	}
	if r.Seeders > 0 {
		item.Attrs = append(item.Attrs, Attr{Name: "seeders", Value: fmt.Sprintf("%d", r.Seeders)})
	}
	if r.Leechers > 0 {
		item.Attrs = append(item.Attrs, Attr{Name: "peers", Value: fmt.Sprintf("%d", r.Leechers)})
	}
	if r.Platform != "" {
		item.Attrs = append(item.Attrs, Attr{Name: "platform", Value: r.Platform})
	}
	if r.Indexer != "" {
		item.Attrs = append(item.Attrs, Attr{Name: "indexer", Value: r.Indexer})
	}

	// Download link — prefer magnet, fall back to direct URL, then synthesize
	// from info_hash if that's all we have.
	switch {
	case r.MagnetURL != "":
		item.Link = r.MagnetURL
		item.Enclosure = &Enclosure{
			URL:    r.MagnetURL,
			Length: r.Size,
			Type:   "application/x-bittorrent",
		}
	case r.DownloadURL != "":
		item.Link = r.DownloadURL
		item.Enclosure = &Enclosure{
			URL:    r.DownloadURL,
			Length: r.Size,
			Type:   "application/x-bittorrent",
		}
	case r.InfoHash != "":
		magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", r.InfoHash, r.Title)
		item.Link = magnet
		item.Enclosure = &Enclosure{
			URL:    magnet,
			Length: r.Size,
			Type:   "application/x-bittorrent",
		}
	default:
		// DDL result with only a detail page (e.g. Vimm GUID) — just expose the URL.
		item.Link = guid
	}

	return item
}
