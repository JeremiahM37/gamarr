package search

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gamarr/internal/models"
	"gamarr/internal/sources"
)

// VimmPlatformSlugs returns all platform slugs Vimm supports per the runtime
// sources registry.
func VimmPlatformSlugs(reg *sources.Registry) []string {
	slugs := make([]string, 0, len(reg.Vimm.PlatformSystems))
	for s := range reg.Vimm.PlatformSystems {
		slugs = append(slugs, s)
	}
	return slugs
}

// vimmGameRe matches a vault game link. Group 1 is the numeric id, group 2 is
// the attribute string, group 3 is the link text.
var vimmGameRe = regexp.MustCompile(`<a\s+href=\s*"/vault/(\d+)"([^>]*)>([^<]+)</a>`)
var vimmFlagRe = regexp.MustCompile(`class="flag" title="([^"]+)"`)
var vimmTDRe = regexp.MustCompile(`(?is)<td[^>]*>(.*?)</td>`)
var vimmTagRe = regexp.MustCompile(`<[^>]+>`)
var vimmSysTokenRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,24}$`)
var vimmDigitRe = regexp.MustCompile(`^\d+$`)
var vimmHiddenRe = regexp.MustCompile(`(?i)display:\s*none`)
var titleSysRe = regexp.MustCompile(`\(([A-Za-z0-9]+)\)\s*$`)

type vimmHit struct {
	ID, Title, System, Region string
}

func parseVimmSearchHTML(body string) []vimmHit {
	var hits []vimmHit
	seen := make(map[string]bool)
	for _, loc := range vimmGameRe.FindAllStringSubmatchIndex(body, -1) {
		id := body[loc[2]:loc[3]]
		attrs := body[loc[4]:loc[5]]
		title := strings.TrimSpace(body[loc[6]:loc[7]])
		if id == "999999" || vimmDigitRe.MatchString(title) || vimmHiddenRe.MatchString(attrs) {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		row := surroundingTR(body, loc[0])
		hits = append(hits, vimmHit{
			ID:     id,
			Title:  title,
			System: systemFromRow(row),
			Region: flagsFrom(row),
		})
	}
	return hits
}

func surroundingTR(body string, pos int) string {
	start := strings.LastIndex(body[:pos], "<tr")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[pos:], "</tr>")
	if end < 0 {
		return body[start:]
	}
	return body[start : pos+end+len("</tr>")]
}

func flagsFrom(row string) string {
	if row == "" {
		return ""
	}
	var regions []string
	seen := make(map[string]bool)
	for _, m := range vimmFlagRe.FindAllStringSubmatch(row, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		regions = append(regions, m[1])
	}
	return strings.Join(regions, ", ")
}

func systemFromRow(row string) string {
	if row == "" {
		return ""
	}
	m := vimmTDRe.FindStringSubmatch(row)
	if m == nil {
		return ""
	}
	inner := strings.TrimSpace(m[1])
	if strings.Contains(strings.ToLower(inner), "<a") {
		return ""
	}
	inner = strings.TrimSpace(vimmTagRe.ReplaceAllString(inner, ""))
	if vimmSysTokenRe.MatchString(inner) {
		return inner
	}
	return ""
}

// SearchVimm searches Vimm's Lair for ROMs.
func SearchVimm(reg *sources.Registry, query string, platformSlug string) []*models.SearchResult {
	if IsCircuitOpen("vimm") {
		slog.Warn("vimm circuit open, skipping search")
		return nil
	}
	params := url.Values{"p": {"list"}, "q": {query}}
	if platformSlug != "" {
		if sys, ok := reg.Vimm.PlatformSystems[platformSlug]; ok {
			params.Set("system", sys)
		}
	}
	systemFromFilter := reg.Vimm.PlatformSystems[platformSlug]

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	req, _ := http.NewRequest("GET", reg.Vimm.BaseURL+"?"+params.Encode(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Vimm search error", "error", err)
		RecordSearchFail("vimm", err.Error())
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		RecordSearchFail("vimm", fmt.Sprintf("HTTP %d", resp.StatusCode))
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	reverseMap := make(map[string]string)
	for slug, sys := range reg.Vimm.PlatformSystems {
		reverseMap[strings.ToLower(sys)] = slug
	}

	var results []*models.SearchResult
	for _, hit := range parseVimmSearchHTML(string(body)) {
		slug := platformSlug
		systemClean := systemFromFilter
		if hit.System != "" {
			systemClean = hit.System
			if s, ok := reverseMap[strings.ToLower(hit.System)]; ok {
				slug = s
			} else if platformSlug == "" {
				slug = ""
			}
		}
		if systemClean == "" {
			systemClean = "Unknown"
		}
		if hit.System == "" && slug == "" {
			if ts := titleSysRe.FindStringSubmatch(hit.Title); ts != nil {
				sysName := ts[1]
				if s, ok := reverseMap[strings.ToLower(sysName)]; ok {
					slug = s
					systemClean = sysName
				}
			}
		}

		displayTitle := hit.Title
		if hit.Region != "" && !strings.Contains(hit.Title, hit.Region) {
			displayTitle = fmt.Sprintf("%s (%s)", displayTitle, hit.Region)
		}
		if systemClean != "" && systemClean != "Unknown" && !strings.Contains(displayTitle, "("+systemClean+")") {
			displayTitle = fmt.Sprintf("%s (%s)", displayTitle, systemClean)
		}

		results = append(results, &models.SearchResult{
			Title:          displayTitle,
			SizeHuman:      "?",
			Indexer:        "Vimm's Lair",
			GUID:           fmt.Sprintf("%s%s", reg.Vimm.BaseURL, hit.ID),
			Platform:       systemClean,
			PlatformSlug:   slug,
			SourceType:     "ddl",
			VimmID:         hit.ID,
			SafetyScore:    90,
			SafetyWarnings: []string{},
		})
	}
	if len(results) > 20 {
		results = results[:20]
	}

	RecordSearchSuccess("vimm")
	return results
}
