package search

import (
	"testing"

	"gamarr/internal/models"
)

func TestIsNonEnglish(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  bool
	}{
		{"English title", "Super Mario Bros [USA]", false},
		{"Cyrillic", "Игра", true},
		{"CJK characters", "ゲーム", true},
		{"Russian tag", "Game.RUSSIAN.Repack", true},
		{"FitGirl exception", "Game FitGirl Repack", false},
		{"English tag overrides", "Game MULTI2RUS [EN]", false},
		{"xatab repack", "Game Repack by xatab", true},
		{"French tag", "Game FRENCH Edition", true},
		{"no non-English markers", "Halo Infinite CODEX", false},
		{"Japanese tag", "Game JAPANESE", true},
		{"Korean tag", "Game KOR version", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNonEnglish(tt.title)
			if got != tt.want {
				t.Errorf("IsNonEnglish(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}

func TestSafetyScore(t *testing.T) {
	tests := []struct {
		name      string
		result    *models.SearchResult
		wantMin   int
		wantMax   int
		wantWarns int
	}{
		{
			name:    "high seeders trusted group",
			result:  &models.SearchResult{Title: "Game-FitGirl", Seeders: 200, Size: 5_000_000_000},
			wantMin: 80,
			wantMax: 100,
		},
		{
			name:      "zero seeders small file",
			result:    &models.SearchResult{Title: "random game", Seeders: 0, Size: 5_000_000},
			wantMin:   0,
			wantMax:   20,
			wantWarns: 2, // very few seeders + suspiciously small
		},
		{
			name:      "risky extension",
			result:    &models.SearchResult{Title: "game.scr", Seeders: 10, Size: 1_000_000_000},
			wantMin:   0,
			wantMax:   30,
			wantWarns: 1,
		},
		{
			name:      "double extension",
			result:    &models.SearchResult{Title: "game.pdf.exe", Seeders: 10, Size: 1_000_000_000},
			wantMin:   0,
			wantMax:   30,
			wantWarns: 1,
		},
		{
			name:      "brand new low seeders",
			result:    &models.SearchResult{Title: "New Game CODEX", Seeders: 2, Size: 30_000_000_000, Age: 0},
			wantMin:   40,
			wantMax:   80,
			wantWarns: 1,
		},
		{
			name:    "moderate seeders normal size",
			result:  &models.SearchResult{Title: "Normal Game", Seeders: 25, Size: 10_000_000_000},
			wantMin: 60,
			wantMax: 75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, warnings := SafetyScore(tt.result)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("score=%d, want [%d, %d]", score, tt.wantMin, tt.wantMax)
			}
			if tt.wantWarns > 0 && len(warnings) < tt.wantWarns {
				t.Errorf("got %d warnings, want at least %d", len(warnings), tt.wantWarns)
			}
		})
	}
}

func TestTitleRelevant(t *testing.T) {
	tests := []struct {
		name  string
		query string
		title string
		want  bool
	}{
		{"exact match", "super mario", "Super Mario Bros", true},
		{"partial match", "zelda", "The Legend of Zelda Ocarina", true},
		{"no match", "halo", "Call of Duty Modern Warfare", false},
		{"stopword only query", "the", "The Game", false},
		{"empty query", "", "Some Title", false},
		{"empty title", "query", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TitleRelevant(tt.query, tt.title)
			if got != tt.want {
				t.Errorf("TitleRelevant(%q, %q) = %v, want %v", tt.query, tt.title, got, tt.want)
			}
		})
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		name string
		size int64
		want string
	}{
		{"zero", 0, "?"},
		{"bytes", 500, "500.0 B"},
		{"kilobytes", 1024, "1.0 KB"},
		{"megabytes", 1048576, "1.0 MB"},
		{"gigabytes", 1073741824, "1.0 GB"},
		{"terabytes", 1099511627776, "1.0 TB"},
		{"fractional GB", 2684354560, "2.5 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HumanSize(tt.size)
			if got != tt.want {
				t.Errorf("HumanSize(%d) = %q, want %q", tt.size, got, tt.want)
			}
		})
	}
}

func TestFilterGameResults(t *testing.T) {
	t.Run("filters suspicious titles", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game password keygen", Seeders: 100, Size: 1_000_000_000},
			{Title: "Game CODEX", Seeders: 50, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
		if filtered[0].Title != "Game CODEX" {
			t.Errorf("expected 'Game CODEX', got %q", filtered[0].Title)
		}
	})

	t.Run("filters non-English", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game RUSSIAN Repack", Seeders: 50, Size: 5_000_000_000},
			{Title: "Game FitGirl Repack", Seeders: 50, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
	})

	t.Run("filters zero seeders", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game A", Seeders: 0, Size: 5_000_000_000},
			{Title: "Game B", Seeders: 10, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
	})

	t.Run("filters tiny files", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game Small", Seeders: 10, Size: 500},
			{Title: "Game Normal", Seeders: 10, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
	})

	t.Run("filters huge files over 200GB", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game Huge", Seeders: 10, Size: 250_000_000_000},
			{Title: "Game Normal", Seeders: 10, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
	})

	t.Run("deduplicates by normalized title", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Game A CODEX", Seeders: 10, Size: 5_000_000_000},
			{Title: "game a codex", Seeders: 50, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result after dedup, got %d", len(filtered))
		}
		// Should keep the one with more seeders
		if filtered[0].Seeders != 50 {
			t.Errorf("expected dedup to keep higher seeders (50), got %d", filtered[0].Seeders)
		}
	})

	t.Run("filters irrelevant titles", func(t *testing.T) {
		results := []*models.SearchResult{
			{Title: "Totally Unrelated Movie", Seeders: 100, Size: 5_000_000_000},
			{Title: "Game Title Search", Seeders: 10, Size: 5_000_000_000},
		}
		filtered := FilterGameResults(results, "game")
		if len(filtered) != 1 {
			t.Fatalf("expected 1 result, got %d", len(filtered))
		}
	})

	t.Run("empty results", func(t *testing.T) {
		filtered := FilterGameResults(nil, "query")
		if filtered != nil && len(filtered) != 0 {
			t.Errorf("expected nil/empty, got %d results", len(filtered))
		}
	})
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"lowercase and strip symbols", "Game: The Title! (2024)", "gamethetitle2024"},
		{"truncate long titles", "abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnopqrstuvwxyz1234567890", "abcdefghijklmnopqrstuvwxyz1234567890abcdefghijklmnopqrstuvwx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTitle(tt.title)
			if got != tt.want {
				t.Errorf("normalizeTitle(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestExtractWords(t *testing.T) {
	words := extractWords("The Quick Brown Fox")
	// "the" is a stopword
	if words["the"] {
		t.Error("'the' should be filtered as stopword")
	}
	if !words["quick"] {
		t.Error("'quick' should be in words")
	}
	if !words["brown"] {
		t.Error("'brown' should be in words")
	}
}

func TestSuspiciousRegex(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"Game with password", true},
		{"Game keygen included", true},
		{"Game crack only release", true},
		{"Game.RAT.trojan", true},
		{"Normal Game Release", false},
		{"Game CODEX", false},
		{"crypto miner payload", true},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := suspiciousRe.MatchString(tt.title)
			if got != tt.want {
				t.Errorf("suspiciousRe.MatchString(%q) = %v, want %v", tt.title, got, tt.want)
			}
		})
	}
}
