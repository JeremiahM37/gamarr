package search

import (
	"strings"

	"gamarr/internal/models"
)

// platformSizeRange defines expected file size ranges (bytes) per platform slug.
// min = suspiciously small, max = suspiciously large.
var platformSizeRange = map[string][2]int64{
	"nes":     {10e3, 5e6},   // 10KB - 5MB
	"snes":    {100e3, 10e6}, // 100KB - 10MB
	"gb":      {10e3, 5e6},   // 10KB - 5MB
	"gbc":     {10e3, 10e6},  // 10KB - 10MB
	"gba":     {100e3, 50e6}, // 100KB - 50MB
	"n64":     {1e6, 100e6},  // 1MB - 100MB
	"nds":     {1e6, 512e6},  // 1MB - 512MB
	"3ds":     {10e6, 4e9},   // 10MB - 4GB
	"psx":     {50e6, 1e9},   // 50MB - 1GB
	"ps2":     {100e6, 8e9},  // 100MB - 8GB
	"ps3":     {500e6, 50e9}, // 500MB - 50GB
	"psp":     {50e6, 4e9},   // 50MB - 4GB
	"genesis": {10e3, 10e6},  // 10KB - 10MB
	"saturn":  {50e6, 1e9},   // 50MB - 1GB
	"dc":      {50e6, 2e9},   // 50MB - 2GB
	"ngc":     {100e6, 4e9},  // 100MB - 4GB
	"wii":     {100e6, 8e9},  // 100MB - 8GB
	"xbox":    {500e6, 8e9},  // 500MB - 8GB
	"xbox360": {500e6, 16e9}, // 500MB - 16GB
	"pc":      {50e6, 100e9}, // 50MB - 100GB
	"switch":  {50e6, 32e9},  // 50MB - 32GB
}

// ScoreResults applies scoring to all results and returns them (modifies in place).
func ScoreResults(results []*models.SearchResult, query string, platformFilter string) []*models.SearchResult {
	for _, r := range results {
		sb := scoreResult(r, query, platformFilter)
		r.Score = sb.Total
		r.ScoreBreakdown = &models.ScoreBreakdown{
			TitleMatch:    sb.TitleMatch,
			PlatformMatch: sb.PlatformMatch,
			SeederScore:   sb.SeederScore,
			SizeScore:     sb.SizeScore,
			SafetyScore:   sb.SafetyScore,
			Total:         sb.Total,
			Confidence:    sb.Confidence,
		}
	}
	return results
}

type scoreBreakdown struct {
	TitleMatch    int
	PlatformMatch int
	SeederScore   int
	SizeScore     int
	SafetyScore   int
	Total         int
	Confidence    string
}

func scoreResult(r *models.SearchResult, query, platformFilter string) scoreBreakdown {
	var sb scoreBreakdown

	sb.TitleMatch = scoreTitleMatch(r.Title, query)
	sb.PlatformMatch = scorePlatformMatch(r.PlatformSlug, platformFilter)
	sb.SeederScore = scoreSeederCount(r.Seeders, r.SourceType)
	sb.SizeScore = scoreSizeRange(r.Size, r.PlatformSlug)
	sb.SafetyScore = scoreSafety(r.SafetyScore)

	sb.Total = sb.TitleMatch + sb.PlatformMatch + sb.SeederScore + sb.SizeScore + sb.SafetyScore
	if sb.Total > 100 {
		sb.Total = 100
	}
	if sb.Total < 0 {
		sb.Total = 0
	}

	switch {
	case sb.Total >= 70:
		sb.Confidence = "high"
	case sb.Total >= 40:
		sb.Confidence = "medium"
	default:
		sb.Confidence = "low"
	}

	return sb
}

// scoreTitleMatch scores title similarity (0-40).
func scoreTitleMatch(title, query string) int {
	if query == "" {
		return 20
	}
	tLower := strings.ToLower(title)
	qLower := strings.ToLower(query)

	// Exact match
	if tLower == qLower {
		return 40
	}

	// Full query is a substring
	if strings.Contains(tLower, qLower) {
		return 35
	}

	// Word overlap scoring
	qWords := extractWords(query)
	tWords := extractWords(title)
	if len(qWords) == 0 {
		return 20
	}

	overlap := 0
	for w := range qWords {
		if tWords[w] {
			overlap++
		}
	}

	ratio := float64(overlap) / float64(len(qWords))
	return int(ratio * 40)
}

// scorePlatformMatch scores platform match (0-15).
func scorePlatformMatch(resultSlug, filterSlug string) int {
	if filterSlug == "" || filterSlug == "all" {
		return 8 // neutral when no filter
	}
	if resultSlug == filterSlug {
		return 15
	}
	// PC platform has multiple slugs
	if filterSlug == "pc" && (resultSlug == "pc" || resultSlug == "") {
		return 15
	}
	return 0
}

// scoreSeederCount scores by seeder count (0-15). DDL gets flat 10.
func scoreSeederCount(seeders int, sourceType string) int {
	if sourceType == "ddl" {
		return 10
	}
	switch {
	case seeders >= 50:
		return 15
	case seeders >= 20:
		return 12
	case seeders >= 10:
		return 10
	case seeders >= 5:
		return 7
	case seeders >= 2:
		return 4
	default:
		return 0
	}
}

// scoreSizeRange scores by whether size is reasonable for the platform (0-15).
func scoreSizeRange(size int64, platformSlug string) int {
	if size == 0 {
		return 7 // unknown, neutral
	}

	slug := strings.ToLower(platformSlug)
	rng, ok := platformSizeRange[slug]
	if !ok {
		// Default range for unknown platforms
		rng = [2]int64{1e6, 50e9} // 1MB - 50GB
	}

	minSize := rng[0]
	maxSize := rng[1]

	if size >= minSize && size <= maxSize {
		return 15 // ideal range
	}
	// Slightly outside range
	if size >= minSize/2 && size <= maxSize*2 {
		return 10
	}
	// Suspiciously tiny or huge
	return 2
}

// scoreSafety maps the existing 0-100 SafetyScore to 0-15 range.
func scoreSafety(safetyScore int) int {
	if safetyScore <= 0 {
		return 0
	}
	// Scale 0-100 to 0-15
	scaled := safetyScore * 15 / 100
	if scaled > 15 {
		scaled = 15
	}
	return scaled
}
