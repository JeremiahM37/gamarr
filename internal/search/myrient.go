package search

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"gamarr/internal/models"
	"gamarr/internal/platform"
)

const (
	myrientBase    = "https://myrient.erista.me/files/"
	dirCacheTTL    = time.Hour
)

var myrientPlatforms = map[string]string{
	"gba":     "No-Intro/Nintendo - Game Boy Advance/",
	"gb":      "No-Intro/Nintendo - Game Boy/",
	"gbc":     "No-Intro/Nintendo - Game Boy Color/",
	"nes":     "No-Intro/Nintendo - Nintendo Entertainment System (Headered)/",
	"snes":    "No-Intro/Nintendo - Super Nintendo Entertainment System/",
	"n64":     "No-Intro/Nintendo - Nintendo 64 (BigEndian)/",
	"nds":     "No-Intro/Nintendo - Nintendo DS (Decrypted)/",
	"3ds":     "No-Intro/Nintendo - Nintendo 3DS (Decrypted)/",
	"psx":     "Redump/Sony - PlayStation/",
	"ps2":     "Redump/Sony - PlayStation 2/",
	"ps3":     "Redump/Sony - PlayStation 3/",
	"psp":     "Redump/Sony - PlayStation Portable/",
	"dc":      "Redump/Sega - Dreamcast/",
	"saturn":  "Redump/Sega - Saturn/",
	"genesis": "No-Intro/Sega - Mega Drive - Genesis/",
	"ngc":     "Redump/Nintendo - GameCube - NKit RVZ [zstd-19-128k]/",
	"wii":     "Redump/Nintendo - Wii - NKit RVZ [zstd-19-128k]/",
	"xbox":    "Redump/Microsoft - Xbox/",
	"xbox360": "Redump/Microsoft - Xbox 360/",
}

// MyrientPlatformSlugs returns all slugs Myrient supports.
func MyrientPlatformSlugs() []string {
	slugs := make([]string, 0, len(myrientPlatforms))
	for s := range myrientPlatforms {
		slugs = append(slugs, s)
	}
	return slugs
}

type dirEntry struct {
	Name string
	URL  string
}

var (
	dirCache     = make(map[string][]dirEntry)
	dirCacheTime = make(map[string]time.Time)
	dirCacheMu   sync.RWMutex
)

var nonEnglishRegionRe = regexp.MustCompile(`\(Japan\)|\(Korea\)|\(China\)|\(Taiwan\)|\(France\)|\(Germany\)|\(Spain\)|\(Italy\)`)
var englishRegionRe = regexp.MustCompile(`\(USA|World|Europe|En\)`)

// hrefRe extracts href from <a> tags in directory listings.
var hrefRe = regexp.MustCompile(`<a\s+href="([^"]+)"[^>]*>([^<]+)</a>`)

// ClearMyrientCache clears the directory listing cache.
func ClearMyrientCache() {
	dirCacheMu.Lock()
	defer dirCacheMu.Unlock()
	dirCache = make(map[string][]dirEntry)
	dirCacheTime = make(map[string]time.Time)
	slog.Info("Myrient directory cache cleared")
}

func getMyrientListing(slug string) []dirEntry {
	dirCacheMu.RLock()
	if entries, ok := dirCache[slug]; ok && time.Since(dirCacheTime[slug]) < dirCacheTTL {
		dirCacheMu.RUnlock()
		return entries
	}
	dirCacheMu.RUnlock()

	path, ok := myrientPlatforms[slug]
	if !ok {
		return nil
	}

	listURL := myrientBase + path
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", listURL, nil)
	req.Header.Set("User-Agent", "Gamarr/1.0")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Myrient listing error", "slug", slug, "error", err)
		RecordSearchFail("myrient", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Warn("Myrient listing failed", "slug", slug, "status", resp.StatusCode)
		RecordSearchFail("myrient", fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var entries []dirEntry
	for _, m := range hrefRe.FindAllStringSubmatch(string(body), -1) {
		href := m[1]
		name := m[2]
		if href == "../" || strings.HasPrefix(href, "?") || strings.HasPrefix(href, "/") || strings.HasSuffix(name, "/") {
			continue
		}
		decoded, _ := url.QueryUnescape(name)
		if decoded == "" {
			decoded = name
		}
		fullURL := listURL + href
		entries = append(entries, dirEntry{Name: strings.TrimSpace(decoded), URL: fullURL})
	}

	dirCacheMu.Lock()
	dirCache[slug] = entries
	dirCacheTime[slug] = time.Now()
	dirCacheMu.Unlock()

	slog.Info("Myrient cached files", "slug", slug, "count", len(entries))
	return entries
}

// SearchMyrient searches Myrient ROM archives.
func SearchMyrient(query string, platformSlug string) []*models.SearchResult {
	if IsCircuitOpen("myrient") {
		slog.Warn("myrient circuit open, skipping search")
		return nil
	}

	qWords := extractWords(query)
	if len(qWords) == 0 {
		return nil
	}

	if platformSlug != "" {
		if _, ok := myrientPlatforms[platformSlug]; !ok {
			return nil
		}
	}

	var slugs []string
	if platformSlug != "" {
		slugs = []string{platformSlug}
	} else {
		// Require a platform filter — searching all is too slow
		return nil
	}

	var results []*models.SearchResult
	for _, slug := range slugs {
		files := getMyrientListing(slug)
		for _, f := range files {
			fWords := extractWords(f.Name)
			overlap := countOverlap(qWords, fWords)
			minReq := len(qWords) - 1
			if minReq < 1 {
				minReq = 1
			}
			if overlap < minReq || overlap < 1 {
				continue
			}

			// Skip non-English regions
			if nonEnglishRegionRe.MatchString(f.Name) && !englishRegionRe.MatchString(f.Name) {
				continue
			}

			// Find platform display name
			platName := "Unknown"
			isPC := false
			for catID, info := range platform.PlatformMap {
				if info.Slug == slug {
					platName = info.Name
					isPC = info.IsPC
					_ = catID
					break
				}
			}

			results = append(results, &models.SearchResult{
				Title:        f.Name,
				SizeHuman:    "?",
				Indexer:      "Myrient",
				DownloadURL:  f.URL,
				GUID:         f.URL,
				Platform:     platName,
				PlatformSlug: slug,
				IsPC:         isPC,
				SourceType:   "ddl",
				SafetyScore:    95,
				SafetyWarnings: []string{},
			})
		}
	}

	// Sort by relevance
	if len(results) > 20 {
		results = results[:20]
	}

	if len(results) > 0 {
		RecordSearchSuccess("myrient")
	}
	// Note: empty results for Myrient are normal (no match), not a failure.
	// Failures are tracked inside getMyrientListing when HTTP errors occur.
	return results
}

func countOverlap(a, b map[string]bool) int {
	count := 0
	for w := range a {
		if b[w] {
			count++
		}
	}
	return count
}
