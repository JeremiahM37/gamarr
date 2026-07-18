// Package platform defines the game platforms Gamarr supports and maps each
// one to its display info, indexer categories, and source paths.
package platform

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PlatformInfo holds display info for a platform.
type PlatformInfo struct {
	Name string
	Slug string
	IsPC bool
}

// PlatformMap maps Prowlarr category IDs to platform info.
var PlatformMap = map[int]PlatformInfo{
	4000:   {Name: "PC", Slug: "", IsPC: true},
	100010: {Name: "PC", Slug: "", IsPC: true},
	100011: {Name: "PS2", Slug: "ps2"},
	100012: {Name: "PSP", Slug: "psp"},
	100013: {Name: "Xbox", Slug: "xbox"},
	100014: {Name: "Xbox 360", Slug: "xbox360"},
	100015: {Name: "PS1", Slug: "psx"},
	100016: {Name: "Dreamcast", Slug: "dc"},
	100017: {Name: "Other", Slug: ""},
	100043: {Name: "PS3", Slug: "ps3"},
	100044: {Name: "Wii", Slug: "wii"},
	100045: {Name: "DS", Slug: "nds"},
	100046: {Name: "GameCube", Slug: "ngc"},
	100072: {Name: "3DS", Slug: "3ds"},
	100077: {Name: "PS4", Slug: "ps4"},
	100082: {Name: "Switch", Slug: "switch"},
	4050:   {Name: "Switch", Slug: "switch"},
}

// ExtraPlatform is a platform not in Prowlarr categories, for user override.
type ExtraPlatform struct {
	Slug string
	Name string
}

var ExtraPlatforms = []ExtraPlatform{
	{"n64", "Nintendo 64"},
	{"snes", "SNES"},
	{"nes", "NES"},
	{"gb", "Game Boy"},
	{"gba", "Game Boy Advance"},
	{"genesis", "Sega Genesis"},
	{"saturn", "Sega Saturn"},
	{"wiiu", "Wii U"},
	{"psvita", "PS Vita"},
}

// AllGameCategories returns all Prowlarr category IDs.
func AllGameCategories() []int {
	cats := make([]int, 0, len(PlatformMap))
	for id := range PlatformMap {
		cats = append(cats, id)
	}
	return cats
}

// DetectPlatform detects platform from a list of Prowlarr category items.
// categories can be []int or []map[string]interface{} (with "id" key).
func DetectPlatform(categories []interface{}) PlatformInfo {
	for _, cat := range categories {
		var catID int
		switch v := cat.(type) {
		case float64:
			catID = int(v)
		case int:
			catID = v
		case map[string]interface{}:
			if id, ok := v["id"].(float64); ok {
				catID = int(id)
			}
		}
		if info, ok := PlatformMap[catID]; ok {
			return info
		}
	}
	return PlatformInfo{Name: "Unknown"}
}

// GetCategoriesForPlatform returns all Prowlarr category IDs matching a platform slug.
func GetCategoriesForPlatform(slug string) []int {
	if slug == "pc" {
		return []int{4000, 100010}
	}
	var matches []int
	for catID, info := range PlatformMap {
		if info.Slug == slug {
			matches = append(matches, catID)
		}
	}
	if len(matches) > 0 {
		return matches
	}
	return AllGameCategories()
}

// metadataPlatformMap maps metadata platform names to PlatformInfo.
var metadataPlatformMap = map[string]PlatformInfo{
	"gamecube": {Name: "GameCube", Slug: "ngc"}, "ngc": {Name: "GameCube", Slug: "ngc"},
	"wii": {Name: "Wii", Slug: "wii"}, "switch": {Name: "Switch", Slug: "switch"},
	"nintendo switch": {Name: "Switch", Slug: "switch"},
	"ps1":             {Name: "PS1", Slug: "psx"}, "psx": {Name: "PS1", Slug: "psx"},
	"playstation": {Name: "PS1", Slug: "psx"},
	"ps2":         {Name: "PS2", Slug: "ps2"}, "playstation 2": {Name: "PS2", Slug: "ps2"},
	"ps3": {Name: "PS3", Slug: "ps3"}, "playstation 3": {Name: "PS3", Slug: "ps3"},
	"ps4":      {Name: "PS4", Slug: "ps4"},
	"psp":      {Name: "PSP", Slug: "psp"},
	"xbox":     {Name: "Xbox", Slug: "xbox"},
	"xbox 360": {Name: "Xbox 360", Slug: "xbox360"}, "xbox360": {Name: "Xbox 360", Slug: "xbox360"},
	"ds": {Name: "DS", Slug: "nds"}, "nds": {Name: "DS", Slug: "nds"},
	"nintendo ds": {Name: "DS", Slug: "nds"},
	"3ds":         {Name: "3DS", Slug: "3ds"}, "nintendo 3ds": {Name: "3DS", Slug: "3ds"},
	"dreamcast": {Name: "Dreamcast", Slug: "dc"},
	"n64":       {Name: "Nintendo 64", Slug: "n64"}, "nintendo 64": {Name: "Nintendo 64", Slug: "n64"},
	"snes": {Name: "SNES", Slug: "snes"}, "super nintendo": {Name: "SNES", Slug: "snes"},
	"nes": {Name: "NES", Slug: "nes"},
	"gba": {Name: "Game Boy Advance", Slug: "gba"}, "game boy advance": {Name: "Game Boy Advance", Slug: "gba"},
	"gb": {Name: "Game Boy", Slug: "gb"}, "game boy": {Name: "Game Boy", Slug: "gb"},
	"genesis": {Name: "Sega Genesis", Slug: "genesis"}, "sega genesis": {Name: "Sega Genesis", Slug: "genesis"},
	"saturn": {Name: "Sega Saturn", Slug: "saturn"}, "sega saturn": {Name: "Sega Saturn", Slug: "saturn"},
	"pc": {Name: "PC", Slug: "", IsPC: true}, "windows": {Name: "PC", Slug: "", IsPC: true},
}

// DetectPlatformFromMetadata reads metadata.json in content dir.
func DetectPlatformFromMetadata(contentPath string) (PlatformInfo, bool) {
	fi, err := os.Stat(contentPath)
	if err != nil || !fi.IsDir() {
		return PlatformInfo{}, false
	}
	metaPath := filepath.Join(contentPath, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return PlatformInfo{}, false
	}
	var meta struct {
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return PlatformInfo{}, false
	}
	plat := strings.ToLower(strings.TrimSpace(meta.Platform))
	if plat == "" {
		return PlatformInfo{}, false
	}
	if info, ok := metadataPlatformMap[plat]; ok {
		return info, true
	}
	slog.Info("unknown metadata platform", "platform", plat)
	return PlatformInfo{}, false
}

// extPlatformMap maps file extensions to platform info.
var extPlatformMap = map[string]PlatformInfo{
	".nsp": {Name: "Switch", Slug: "switch"}, ".xci": {Name: "Switch", Slug: "switch"},
	".nsz": {Name: "Switch", Slug: "switch"},
	".3ds": {Name: "3DS", Slug: "3ds"}, ".cia": {Name: "3DS", Slug: "3ds"},
	".nds": {Name: "DS", Slug: "nds"},
	".gba": {Name: "Game Boy Advance", Slug: "gba"},
	".gbc": {Name: "Game Boy Color", Slug: "gbc"},
	".gb":  {Name: "Game Boy", Slug: "gb"},
	".nes": {Name: "NES", Slug: "nes"},
	".sfc": {Name: "SNES", Slug: "snes"}, ".smc": {Name: "SNES", Slug: "snes"},
	".n64": {Name: "Nintendo 64", Slug: "n64"}, ".z64": {Name: "Nintendo 64", Slug: "n64"},
	".v64": {Name: "Nintendo 64", Slug: "n64"},
	".gcm": {Name: "GameCube", Slug: "ngc"}, ".gcz": {Name: "GameCube", Slug: "ngc"},
	".wbfs": {Name: "Wii", Slug: "wii"}, ".wad": {Name: "Wii", Slug: "wii"},
	".pbp": {Name: "PSP", Slug: "psp"}, ".cso": {Name: "PSP", Slug: "psp"},
	".gdi": {Name: "Dreamcast", Slug: "dc"}, ".cdi": {Name: "Dreamcast", Slug: "dc"},
}

var titleHints = []struct {
	Pattern *regexp.Regexp
	Info    PlatformInfo
}{
	{regexp.MustCompile(`(?i)\[nsp\]|\bnsp\b|switch`), PlatformInfo{Name: "Switch", Slug: "switch"}},
	{regexp.MustCompile(`(?i)\[xci\]|\bxci\b`), PlatformInfo{Name: "Switch", Slug: "switch"}},
	{regexp.MustCompile(`(?i)\bwiiu\b|wii\s*u`), PlatformInfo{Name: "Wii U", Slug: "wiiu"}},
	{regexp.MustCompile(`(?i)\bwii\b`), PlatformInfo{Name: "Wii", Slug: "wii"}},
	{regexp.MustCompile(`(?i)\bgamecube\b|\bngc\b|\bgcn\b`), PlatformInfo{Name: "GameCube", Slug: "ngc"}},
	{regexp.MustCompile(`(?i)\b3ds\b`), PlatformInfo{Name: "3DS", Slug: "3ds"}},
	{regexp.MustCompile(`(?i)\bnds\b|\bnintendo\s*ds\b`), PlatformInfo{Name: "DS", Slug: "nds"}},
	{regexp.MustCompile(`(?i)\bgba\b`), PlatformInfo{Name: "Game Boy Advance", Slug: "gba"}},
	{regexp.MustCompile(`(?i)\bps3\b|playstation\s*3`), PlatformInfo{Name: "PS3", Slug: "ps3"}},
	{regexp.MustCompile(`(?i)\bps2\b|playstation\s*2`), PlatformInfo{Name: "PS2", Slug: "ps2"}},
	{regexp.MustCompile(`(?i)\bps1\b|\bpsx\b`), PlatformInfo{Name: "PS1", Slug: "psx"}},
	{regexp.MustCompile(`(?i)\bpsp\b`), PlatformInfo{Name: "PSP", Slug: "psp"}},
	{regexp.MustCompile(`(?i)\bxbox\s*360`), PlatformInfo{Name: "Xbox 360", Slug: "xbox360"}},
	{regexp.MustCompile(`(?i)\bxbox\b`), PlatformInfo{Name: "Xbox", Slug: "xbox"}},
	{regexp.MustCompile(`(?i)\bdreamcast\b`), PlatformInfo{Name: "Dreamcast", Slug: "dc"}},
	{regexp.MustCompile(`(?i)\bn64\b|nintendo\s*64`), PlatformInfo{Name: "Nintendo 64", Slug: "n64"}},
	{regexp.MustCompile(`(?i)\bsnes\b|super\s*nintendo`), PlatformInfo{Name: "SNES", Slug: "snes"}},
	{regexp.MustCompile(`(?i)\bnes\b`), PlatformInfo{Name: "NES", Slug: "nes"}},
	{regexp.MustCompile(`(?i)\bgenesis\b|mega\s*drive`), PlatformInfo{Name: "Sega Genesis", Slug: "genesis"}},
}

// DetectPlatformFromFiles detects platform from file extensions and title keywords.
func DetectPlatformFromFiles(contentPath, title string) (PlatformInfo, bool) {
	exts := collectExtensions(contentPath)
	for ext := range exts {
		if info, ok := extPlatformMap[ext]; ok {
			slog.Info("platform detected from extension", "ext", ext)
			return info, true
		}
	}
	titleLower := strings.ToLower(title)
	for _, hint := range titleHints {
		if hint.Pattern.MatchString(titleLower) {
			slog.Info("platform detected from title keyword", "pattern", hint.Pattern.String())
			return hint.Info, true
		}
	}
	return PlatformInfo{}, false
}

func collectExtensions(path string) map[string]bool {
	exts := make(map[string]bool)
	fi, err := os.Stat(path)
	if err != nil {
		return exts
	}
	if !fi.IsDir() {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != "" {
			exts[ext] = true
		}
		return exts
	}
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext != "" {
			exts[ext] = true
		}
		return nil
	})
	return exts
}
