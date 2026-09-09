package search

import (
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamarr/internal/models"
	"gamarr/internal/platform"
	"gamarr/internal/sources"
)

const minervaCacheTTL = time.Hour

// magnetHashRe pulls the infohash out of a magnet:?xt=urn:btih:... link.
var magnetHashRe = regexp.MustCompile(`(?i)urn:btih:([a-fA-F0-9]{40}|[a-zA-Z2-7]{32})`)

var (
	minervaEntryRe  = regexp.MustCompile(`(?is)<div\s+class="entry"[^>]*\bdata-name="([^"]*)"[^>]*>(.*?)</div>`)
	minervaRomIDRe  = regexp.MustCompile(`/rom\?id=(\d+)`)
	minervaTitleRe  = regexp.MustCompile(`(?is)<a[^>]*href="/rom\?id=\d+"[^>]*>([^<]+)</a>`)
	minervaSizeRe   = regexp.MustCompile(`(?is)<span>([^<]+)</span>`)
	minervaMagnetRe = regexp.MustCompile(`downloadMagnet\('([^']+)'\)`)
	minervaSizeNum  = regexp.MustCompile(`(?i)^\s*([0-9]*\.?[0-9]+)\s*(B|KB|MB|GB|TB)\s*$`)
)

type minervaHit struct {
	ID        int64
	Title     string
	SizeHuman string
	Size      int64
	Magnet    string
	Path      string // catalog-relative file path used to build the Myrient URL
}

var (
	minervaCache     = make(map[string][]minervaHit)
	minervaCacheTime = make(map[string]time.Time)
	minervaCacheMu   sync.RWMutex
)

// MinervaPlatformSlugs returns all slugs Minerva supports per the runtime
// sources registry.
func MinervaPlatformSlugs(reg *sources.Registry) []string {
	if reg == nil {
		return nil
	}
	slugs := make([]string, 0, len(reg.Minerva.PlatformPaths))
	for s := range reg.Minerva.PlatformPaths {
		slugs = append(slugs, s)
	}
	return slugs
}

// ClearMinervaCache drops cached per-system browse listings.
func ClearMinervaCache() {
	minervaCacheMu.Lock()
	defer minervaCacheMu.Unlock()
	minervaCache = make(map[string][]minervaHit)
	minervaCacheTime = make(map[string]time.Time)
	slog.Info("Minerva directory cache cleared")
}

// SearchMinerva looks up a title in Minerva Archive's per-system browse page.
//
// The browse page for a console already lists every title. We fetch that once,
// cache it, and filter locally — the same shape as Myrient, and much faster
// than Minerva's full-catalog search API.
func SearchMinerva(reg *sources.Registry, query string, platformSlug string) []*models.SearchResult {
	if reg == nil || strings.TrimSpace(reg.Minerva.BaseURL) == "" {
		return nil
	}
	if IsCircuitOpen("minerva") {
		slog.Warn("minerva circuit open, skipping search")
		return nil
	}

	qWords := extractWords(query)
	if len(qWords) == 0 {
		return nil
	}
	if platformSlug == "" {
		return nil
	}
	if _, ok := reg.Minerva.PlatformPaths[platformSlug]; !ok {
		return nil
	}

	files := getMinervaListing(reg, platformSlug)
	if files == nil {
		return nil
	}

	platName, isPC := minervaPlatformInfo(platformSlug, "")
	var results []*models.SearchResult
	for _, f := range files {
		fWords := extractWords(f.Title)
		overlap := countOverlap(qWords, fWords)
		minReq := len(qWords) - 1
		if minReq < 1 {
			minReq = 1
		}
		if overlap < minReq || overlap < 1 {
			continue
		}
		if nonEnglishRegionRe.MatchString(f.Title) && !englishRegionRe.MatchString(f.Title) {
			continue
		}

		results = append(results, &models.SearchResult{
			Title:          f.Title,
			Size:           f.Size,
			SizeHuman:      f.SizeHuman,
			Indexer:        "Minerva",
			MagnetURL:      f.Magnet,
			InfoHash:       minervaInfoHash(f.Magnet),
			GUID:           minervaGUID(reg.Minerva.BaseURL, f.ID),
			Platform:       platName,
			PlatformSlug:   platformSlug,
			IsPC:           isPC,
			SourceType:     "torrent",
			SafetyScore:    95,
			SafetyWarnings: []string{},
		})
		if len(results) >= 20 {
			break
		}
	}

	if len(results) > 0 {
		RecordSearchSuccess("minerva")
	}
	return results
}

func getMinervaListing(reg *sources.Registry, slug string) []minervaHit {
	minervaCacheMu.RLock()
	if entries, ok := minervaCache[slug]; ok && time.Since(minervaCacheTime[slug]) < minervaCacheTTL {
		minervaCacheMu.RUnlock()
		return entries
	}
	minervaCacheMu.RUnlock()

	platformPath, ok := reg.Minerva.PlatformPaths[slug]
	if !ok {
		return nil
	}
	listURL := minervaListingURL(reg.Minerva.BaseURL, platformPath)
	if listURL == "" {
		return nil
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 30 * time.Second
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	req, err := http.NewRequest("GET", listURL, nil)
	if err != nil {
		RecordSearchFail("minerva", err.Error())
		return nil
	}
	req.Header.Set("User-Agent", "Gamarr/1.0")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Minerva listing error", "slug", slug, "error", err)
		RecordSearchFail("minerva", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Warn("Minerva listing failed", "slug", slug, "status", resp.StatusCode)
		RecordSearchFail("minerva", fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		RecordSearchFail("minerva", err.Error())
		return nil
	}

	entries := parseMinervaListing(string(body), platformPath)
	minervaCacheMu.Lock()
	minervaCache[slug] = entries
	minervaCacheTime[slug] = time.Now()
	minervaCacheMu.Unlock()
	slog.Info("Minerva cached files", "slug", slug, "count", len(entries))
	return entries
}

func parseMinervaListing(body, platformPath string) []minervaHit {
	var entries []minervaHit
	for _, m := range minervaEntryRe.FindAllStringSubmatch(body, -1) {
		inner := m[2]
		idMatch := minervaRomIDRe.FindStringSubmatch(inner)
		if idMatch == nil {
			continue
		}
		id, err := strconv.ParseInt(idMatch[1], 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		title := html.UnescapeString(strings.TrimSpace(m[1]))
		if tm := minervaTitleRe.FindStringSubmatch(inner); tm != nil {
			if t := strings.TrimSpace(html.UnescapeString(tm[1])); t != "" {
				title = t
			}
		}
		if title == "" {
			continue
		}
		sizeHuman := "?"
		var size int64
		if sm := minervaSizeRe.FindStringSubmatch(inner); sm != nil {
			sizeHuman = strings.TrimSpace(html.UnescapeString(sm[1]))
			size = parseMinervaSize(sizeHuman)
		}
		magnet := ""
		if mm := minervaMagnetRe.FindStringSubmatch(inner); mm != nil {
			magnet = html.UnescapeString(mm[1])
		}
		entries = append(entries, minervaHit{
			ID:        id,
			Title:     title,
			SizeHuman: sizeHuman,
			Size:      size,
			Magnet:    magnet,
			Path:      strings.TrimSuffix(platformPath, "/") + "/" + title,
		})
	}
	return entries
}

func minervaListingURL(base, platformPath string) string {
	p := strings.Trim(strings.ReplaceAll(platformPath, "\\", "/"), "/")
	if strings.TrimSpace(base) == "" || p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.TrimRight(base, "/") + "/browse/./" + strings.Join(parts, "/") + "/"
}

func minervaTitle(fullPath string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(fullPath), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		return ""
	}
	return name
}

func minervaGUID(base string, id int64) string {
	if id <= 0 {
		return ""
	}
	return strings.TrimRight(base, "/") + "/rom?id=" + fmt.Sprintf("%d", id)
}

func minervaInfoHash(magnet string) string {
	m := magnetHashRe.FindStringSubmatch(magnet)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// minervaFileURL maps a catalog path onto the Myrient file tree the listing
// describes. Segment-escape so spaces and brackets survive as a single URL.
func minervaFileURL(myrientBase, fullPath string) string {
	p := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(fullPath), "\\", "/"), "./")
	p = strings.TrimPrefix(p, "/")
	if myrientBase == "" || p == "" {
		return ""
	}
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.TrimRight(myrientBase, "/") + "/" + strings.Join(parts, "/")
}

func parseMinervaSize(s string) int64 {
	m := minervaSizeNum.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := 1.0
	switch strings.ToUpper(m[2]) {
	case "KB":
		mult = 1024
	case "MB":
		mult = 1024 * 1024
	case "GB":
		mult = 1024 * 1024 * 1024
	case "TB":
		mult = 1024 * 1024 * 1024 * 1024
	}
	return int64(n * mult)
}

func minervaPlatformInfo(slug, consoleFallback string) (string, bool) {
	if slug != "" {
		for _, info := range platform.PlatformMap {
			if info.Slug == slug {
				return info.Name, info.IsPC
			}
		}
		for _, ep := range platform.ExtraPlatforms {
			if ep.Slug == slug {
				return ep.Name, false
			}
		}
	}
	if consoleFallback != "" {
		return consoleFallback, false
	}
	return "Unknown", false
}
