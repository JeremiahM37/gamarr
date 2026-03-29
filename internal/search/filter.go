package search

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"gamarr/internal/models"
)

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "in": true,
	"on": true, "at": true, "to": true, "for": true, "and": true,
	"or": true, "is": true, "it": true, "by": true,
}

var suspiciousRe = regexp.MustCompile(
	`(?i)password|keygen|crack[\s._-]+only|warez|DevCourseWeb|sample\s+only|trailer\s+only` +
		`|\.exe\.torrent|password\.txt|clickbait|survey|free\s+download` +
		`|RAT\b|trojan|malware|crypto[\s._-]*miner|coin[\s._-]*miner`,
)

var cyrillicRe = regexp.MustCompile(`[\x{0400}-\x{04FF}]`)
var cjkRe = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3040}-\x{309F}\x{30A0}-\x{30FF}]`)
var nonEnglishTagsRe = regexp.MustCompile(
	`(?i)\b(RUS|RUSSIAN|RU|MULTI\d*RUS|RePack[\s._-]+by[\s._-]+R\.G|R\.G\.|` +
		`Repack[\s._-]+by[\s._-]+xatab|xatab|decepticon|механики|FitGirl[\s._-]+Repack` +
		`|KaOs|FRENCH|GERMAN|ITALIAN|SPANISH|PORTUGUESE|POLISH|CZECH|HUNGARIAN` +
		`|KOREAN|JAPANESE|CHINESE|ARABIC|TURKISH|THAI|VIETNAMESE` +
		`|JPN|KOR|CHN|FRA|DEU|ITA|ESP|POR|POL|CZE)\b`,
)
var fitgirlRe = regexp.MustCompile(`(?i)fitgirl`)
var englishTagRe = regexp.MustCompile(`(?i)\b(USA|EN|ENG|English|NTSC|PAL)\b`)

var trustedGroups = map[string]bool{
	"fitgirl": true, "dodi": true, "gog": true, "codex": true,
	"plaza": true, "skidrow": true, "cpy": true, "empress": true,
	"rune": true, "razordox": true, "tinyiso": true, "elamigos": true,
}

var riskyExtRe = regexp.MustCompile(`(?i)\.(scr|bat|cmd|vbs|vbe|js|jse|wsf|wsh|ps1|msi|hta|cpl|reg)$`)
var doubleExtRe = regexp.MustCompile(`\.(\w{2,4})\.(\w{2,4})$`)
var wordRe = regexp.MustCompile(`\w+`)

// IsNonEnglish checks if a title appears to be non-English.
func IsNonEnglish(title string) bool {
	if cyrillicRe.MatchString(title) || cjkRe.MatchString(title) {
		return true
	}
	if nonEnglishTagsRe.MatchString(title) {
		if fitgirlRe.MatchString(title) {
			return false
		}
		if englishTagRe.MatchString(title) {
			return false
		}
		return true
	}
	return false
}

// SafetyScore rates a result's safety (0-100).
func SafetyScore(r *models.SearchResult) (int, []string) {
	score := 50
	warnings := make([]string, 0)
	titleLower := strings.ToLower(r.Title)

	if r.Seeders >= 100 {
		score += 30
	} else if r.Seeders >= 20 {
		score += 15
	} else if r.Seeders >= 5 {
		score += 5
	} else if r.Seeders <= 1 {
		score -= 20
		warnings = append(warnings, "Very few seeders")
	}

	for group := range trustedGroups {
		if strings.Contains(titleLower, group) {
			score += 20
			break
		}
	}

	if r.Size > 0 && r.Size < 10_000_000 {
		score -= 30
		warnings = append(warnings, "Suspiciously small file")
	}

	if riskyExtRe.MatchString(titleLower) {
		score -= 40
		warnings = append(warnings, "Risky file extension")
	}

	if m := doubleExtRe.FindStringSubmatch(titleLower); m != nil {
		ext2 := m[2]
		if ext2 == "exe" || ext2 == "scr" || ext2 == "bat" || ext2 == "cmd" || ext2 == "msi" {
			score -= 40
			warnings = append(warnings, "Double file extension")
		}
	}

	if r.Age < 1 && r.Seeders < 5 {
		score -= 10
		warnings = append(warnings, "Brand new upload")
	}

	score = int(math.Max(0, math.Min(100, float64(score))))
	return score, warnings
}

// TitleRelevant checks if a title is relevant to a query.
func TitleRelevant(query, title string) bool {
	qWords := extractWords(query)
	tWords := extractWords(title)
	if len(qWords) == 0 || len(tWords) == 0 {
		return false
	}
	overlap := 0
	for w := range qWords {
		if tWords[w] {
			overlap++
		}
	}
	return overlap >= 1
}

func extractWords(s string) map[string]bool {
	words := make(map[string]bool)
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if !stopwords[w] {
			words[w] = true
		}
	}
	return words
}

// HumanSize formats bytes into a human-readable string.
func HumanSize(size int64) string {
	if size == 0 {
		return "?"
	}
	s := float64(size)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if math.Abs(s) < 1024 {
			return fmt.Sprintf("%.1f %s", s, unit)
		}
		s /= 1024
	}
	return fmt.Sprintf("%.1f TB", s)
}

// FilterGameResults filters and deduplicates search results.
func FilterGameResults(results []*models.SearchResult, query string) []*models.SearchResult {
	var filtered []*models.SearchResult
	seen := make(map[string]*models.SearchResult)

	for _, r := range results {
		if suspiciousRe.MatchString(r.Title) {
			continue
		}
		if IsNonEnglish(r.Title) {
			continue
		}
		if r.Seeders < 1 {
			continue
		}
		if r.Size > 0 && (r.Size < 1_000_000 || r.Size > 200_000_000_000) {
			continue
		}
		if !TitleRelevant(query, r.Title) {
			continue
		}

		score, warnings := SafetyScore(r)
		r.SafetyScore = score
		r.SafetyWarnings = warnings
		if score <= 10 {
			continue
		}

		// Dedup by normalized title
		norm := normalizeTitle(r.Title)
		if existing, ok := seen[norm]; ok {
			if r.Seeders > existing.Seeders {
				// Remove old, add new
				for i, f := range filtered {
					if f == existing {
						filtered = append(filtered[:i], filtered[i+1:]...)
						break
					}
				}
				seen[norm] = r
				filtered = append(filtered, r)
			}
			continue
		}
		seen[norm] = r
		filtered = append(filtered, r)
	}
	return filtered
}

func normalizeTitle(title string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(title) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
