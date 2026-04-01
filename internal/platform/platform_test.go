package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllGameCategories(t *testing.T) {
	cats := AllGameCategories()
	if len(cats) == 0 {
		t.Fatal("expected non-empty category list")
	}
	if len(cats) != len(PlatformMap) {
		t.Errorf("expected %d categories, got %d", len(PlatformMap), len(cats))
	}
	// Each category must exist in PlatformMap
	for _, c := range cats {
		if _, ok := PlatformMap[c]; !ok {
			t.Errorf("category %d not in PlatformMap", c)
		}
	}
}

func TestDetectPlatform(t *testing.T) {
	tests := []struct {
		name       string
		categories []interface{}
		wantName   string
	}{
		{
			name:       "Switch from 100082",
			categories: []interface{}{float64(100082)},
			wantName:   "Switch",
		},
		{
			name:       "Switch from 4050 (Nyaa)",
			categories: []interface{}{float64(4050)},
			wantName:   "Switch",
		},
		{
			name:       "PC from 4000",
			categories: []interface{}{float64(4000)},
			wantName:   "PC",
		},
		{
			name:       "PS2 from map with id",
			categories: []interface{}{map[string]interface{}{"id": float64(100011)}},
			wantName:   "PS2",
		},
		{
			name:       "int value",
			categories: []interface{}{100045},
			wantName:   "DS",
		},
		{
			name:       "unknown category",
			categories: []interface{}{float64(99999)},
			wantName:   "Unknown",
		},
		{
			name:       "empty categories",
			categories: []interface{}{},
			wantName:   "Unknown",
		},
		{
			name:       "nil categories",
			categories: nil,
			wantName:   "Unknown",
		},
		{
			name:       "first match wins",
			categories: []interface{}{float64(100082), float64(4000)},
			wantName:   "Switch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectPlatform(tt.categories)
			if result.Name != tt.wantName {
				t.Errorf("got Name=%q, want %q", result.Name, tt.wantName)
			}
		})
	}
}

func TestGetCategoriesForPlatform(t *testing.T) {
	tests := []struct {
		name     string
		slug     string
		wantLen  int
		contains []int
	}{
		{
			name:     "PC returns two categories",
			slug:     "pc",
			wantLen:  2,
			contains: []int{4000, 100010},
		},
		{
			name:     "Switch returns 100082 and 4050",
			slug:     "switch",
			wantLen:  2,
			contains: []int{100082, 4050},
		},
		{
			name: "PS2 returns at least one",
			slug: "ps2",
		},
		{
			name: "unknown slug returns all categories",
			slug: "atari2600",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cats := GetCategoriesForPlatform(tt.slug)
			if tt.wantLen > 0 && len(cats) != tt.wantLen {
				t.Errorf("got %d categories, want %d", len(cats), tt.wantLen)
			}
			for _, want := range tt.contains {
				found := false
				for _, c := range cats {
					if c == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected category %d in result", want)
				}
			}
			if tt.slug == "atari2600" {
				// Unknown slug should return all categories
				if len(cats) != len(PlatformMap) {
					t.Errorf("unknown slug should return all %d categories, got %d", len(PlatformMap), len(cats))
				}
			}
		})
	}
}

func TestDetectPlatformFromMetadata(t *testing.T) {
	t.Run("valid metadata", func(t *testing.T) {
		dir := t.TempDir()
		meta := map[string]string{"platform": "Switch"}
		data, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644)

		info, ok := DetectPlatformFromMetadata(dir)
		if !ok {
			t.Fatal("expected platform detected")
		}
		if info.Slug != "switch" {
			t.Errorf("got slug=%q, want 'switch'", info.Slug)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		dir := t.TempDir()
		meta := map[string]string{"platform": "GAMECUBE"}
		data, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644)

		info, ok := DetectPlatformFromMetadata(dir)
		if !ok {
			t.Fatal("expected platform detected")
		}
		if info.Slug != "ngc" {
			t.Errorf("got slug=%q, want 'ngc'", info.Slug)
		}
	})

	t.Run("no metadata file", func(t *testing.T) {
		dir := t.TempDir()
		_, ok := DetectPlatformFromMetadata(dir)
		if ok {
			t.Error("expected false when no metadata.json")
		}
	})

	t.Run("empty platform field", func(t *testing.T) {
		dir := t.TempDir()
		meta := map[string]string{"platform": ""}
		data, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644)

		_, ok := DetectPlatformFromMetadata(dir)
		if ok {
			t.Error("expected false for empty platform")
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "notadir.txt")
		os.WriteFile(f, []byte("hello"), 0644)
		_, ok := DetectPlatformFromMetadata(f)
		if ok {
			t.Error("expected false for non-directory path")
		}
	})

	t.Run("unknown platform value", func(t *testing.T) {
		dir := t.TempDir()
		meta := map[string]string{"platform": "ZXSpectrum"}
		data, _ := json.Marshal(meta)
		os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644)

		_, ok := DetectPlatformFromMetadata(dir)
		if ok {
			t.Error("expected false for unknown platform")
		}
	})
}

func TestDetectPlatformFromFiles(t *testing.T) {
	tests := []struct {
		name     string
		files    []string // files to create in temp dir
		title    string
		wantSlug string
		wantOK   bool
	}{
		{
			name:     "Switch NSP extension",
			files:    []string{"game.nsp"},
			title:    "Some Game",
			wantSlug: "switch",
			wantOK:   true,
		},
		{
			name:     "GBA extension",
			files:    []string{"pokemon.gba"},
			title:    "Pokemon",
			wantSlug: "gba",
			wantOK:   true,
		},
		{
			name:     "title keyword Switch",
			files:    []string{"data.bin"},
			title:    "Zelda [NSP] Switch",
			wantSlug: "switch",
			wantOK:   true,
		},
		{
			name:     "title keyword PS3",
			files:    []string{"data.bin"},
			title:    "Gran Turismo PS3",
			wantSlug: "ps3",
			wantOK:   true,
		},
		{
			name:     "no match",
			files:    []string{"readme.txt"},
			title:    "just some text file",
			wantSlug: "",
			wantOK:   false,
		},
		{
			name:     "single file path (not dir)",
			files:    nil, // we test with a .nds file directly
			title:    "",
			wantSlug: "nds",
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.name == "single file path (not dir)" {
				f := filepath.Join(t.TempDir(), "game.nds")
				os.WriteFile(f, []byte("rom"), 0644)
				path = f
			} else {
				dir := t.TempDir()
				for _, fname := range tt.files {
					os.WriteFile(filepath.Join(dir, fname), []byte("data"), 0644)
				}
				path = dir
			}

			info, ok := DetectPlatformFromFiles(path, tt.title)
			if ok != tt.wantOK {
				t.Fatalf("got ok=%v, want %v", ok, tt.wantOK)
			}
			if ok && info.Slug != tt.wantSlug {
				t.Errorf("got slug=%q, want %q", info.Slug, tt.wantSlug)
			}
		})
	}
}

func TestExtraPlatformsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, ep := range ExtraPlatforms {
		if seen[ep.Slug] {
			t.Errorf("duplicate extra platform slug: %s", ep.Slug)
		}
		seen[ep.Slug] = true
	}
}

func TestPlatformMapHasSwitchBoth(t *testing.T) {
	// Both 100082 and 4050 should map to Switch
	if info, ok := PlatformMap[100082]; !ok || info.Slug != "switch" {
		t.Error("100082 should map to switch")
	}
	if info, ok := PlatformMap[4050]; !ok || info.Slug != "switch" {
		t.Error("4050 should map to switch")
	}
}
