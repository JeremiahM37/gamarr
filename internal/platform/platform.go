// Package platform defines the game platforms Gamarr supports and maps each
// one to its display info, indexer categories, and source paths.
//
// Every table in this package is derived from Registry (registry.go); edit
// the registry, not the derived maps.
package platform

import (
	"archive/zip"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlatformInfo holds display info for a platform.
type PlatformInfo struct {
	Name string
	Slug string
	IsPC bool
}

// PlatformMap maps Prowlarr category IDs to platform info. Derived from
// Registry: every platform's Categories, plus the legacy tracker "Other"
// bucket. Standard Newznab console categories are included, so Usenet results
// are not reported as "Unknown".
var PlatformMap map[int]PlatformInfo

// ExtraPlatform is a platform not in Prowlarr categories, for user override.
type ExtraPlatform struct {
	Slug string
	Name string
}

// ExtraPlatforms lists the registry platforms that no category identifies.
var ExtraPlatforms []ExtraPlatform

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
	return detectFromIDs(CategoryIDs(categories))
}

// CategoryIDs extracts the numeric IDs from a Prowlarr categories array, whose
// items are numbers or objects carrying an "id".
func CategoryIDs(categories []interface{}) []int {
	var ids []int
	for _, cat := range categories {
		switch v := cat.(type) {
		case float64:
			ids = append(ids, int(v))
		case int:
			ids = append(ids, v)
		case map[string]interface{}:
			if id, ok := v["id"].(float64); ok {
				ids = append(ids, int(id))
			}
		}
	}
	return ids
}

// GetCategoriesForPlatform returns all Prowlarr category IDs matching a
// platform slug: the categories the platform owns plus the extra ones its
// search requests. A slug with no categories gets every known category.
func GetCategoriesForPlatform(slug string) []int {
	if p, ok := Lookup(slug); ok && len(p.Categories) > 0 {
		cats := make([]int, 0, len(p.Categories)+len(p.SearchCategories))
		cats = append(cats, p.Categories...)
		return append(cats, p.SearchCategories...)
	}
	return AllGameCategories()
}

// metadataPlatformMap maps metadata platform names to PlatformInfo. Derived
// from each registry entry's slug, name and Aliases.
var metadataPlatformMap map[string]PlatformInfo

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

// extPlatformMap maps unique ROM extensions to platform info. Derived from
// each registry entry's Extensions.
var extPlatformMap map[string]PlatformInfo

// pcOverrideExts are the ROM formats a PC release never legitimately ships.
// It is deliberately much narrower than extPlatformMap, because it is the only
// evidence allowed to overturn a PC classification: .wad is a Doom asset, and
// .nes/.sfc/.gb/.gba/.n64 turn up inside PC games that bundle an emulator, so
// none of those can be trusted here. .3ds is out too - it is also the 3D
// Studio model format.
var pcOverrideExts = []string{".nsp", ".xci", ".nsz", ".cia", ".nds", ".wbfs", ".gcz"}

// consoleROMExts maps pcOverrideExts to their registry platform.
var consoleROMExts map[string]PlatformInfo

// DetectConsoleROM reports a console platform when the content carries a ROM
// format no PC release ships.
//
// Newznab 4050 is PC/Games, and it is also what Prowlarr maps Nyaa's only
// games category (Software - Games) to, so Switch ROMs from Nyaa arrive
// tagged PC. Category alone cannot separate the two, but the payload can:
// a torrent holding an .nsp is a Switch release whatever it was tagged.
// Only extensions are consulted - the title hints are too loose to
// overturn an explicit PC classification ("switch" matches plenty of PC
// game titles).
func DetectConsoleROM(contentPath string) (PlatformInfo, bool) {
	exts := collectExtensions(contentPath)
	sorted := make([]string, 0, len(exts))
	for ext := range exts {
		sorted = append(sorted, ext)
	}
	sort.Strings(sorted)
	for _, ext := range sorted {
		if info, ok := consoleROMExts[ext]; ok {
			slog.Info("console ROM format found in PC-tagged content", "ext", ext, "platform", info.Name)
			return info, true
		}
	}
	return PlatformInfo{}, false
}

// DetectPlatformFromFiles detects platform from file extensions and title keywords.
func DetectPlatformFromFiles(contentPath, title string) (PlatformInfo, bool) {
	if info, ok := DetectROMPlatform(contentPath); ok {
		return info, true
	}
	if info, ok := DetectPlatformFromTitle(title); ok {
		slog.Info("platform detected from title keyword", "platform", info.Name)
		return info, true
	}
	return PlatformInfo{}, false
}

// DetectROMPlatform classifies content from its ROM files alone: loose files,
// files in subfolders, and the members of .zip archives. Each file with a
// unique ROM extension votes for its platform and the most votes win (ties go
// to registry order). When no file has a unique extension but the content is a
// single file whose format only one console uses (.vb, .md, .pkg), that
// console is reported.
func DetectROMPlatform(contentPath string) (PlatformInfo, bool) {
	scan := scanContent(contentPath)
	if info, ok := scan.vote(); ok {
		slog.Info("platform detected from ROM extensions", "platform", info.Name)
		return info, true
	}
	if scan.single != "" {
		var only []Platform
		for _, p := range PlatformsAccepting(scan.single) {
			if !p.IsPC {
				only = append(only, p)
			}
		}
		if len(only) == 1 {
			slog.Info("platform detected from single-file format", "ext", scan.single, "platform", only[0].Name)
			return only[0].Info(), true
		}
	}
	return PlatformInfo{}, false
}

// ContentConflict is the post-download sanity check on a platform the job
// already carries. It reports the platform the content positively belongs to
// when that is not slug: none of the content's files is a format slug's
// platform uses, and its unique ROM extensions point elsewhere. A job whose
// content includes any format of its own platform is never contradicted, so a
// Switch release with a stray .nes inside stays a Switch release.
func ContentConflict(slug, contentPath string) (PlatformInfo, bool) {
	p, known := Lookup(slug)
	if known && p.IsPC {
		return PlatformInfo{}, false
	}
	scan := scanContent(contentPath)
	if known {
		for ext := range scan.exts {
			if p.Accepts(ext) {
				return PlatformInfo{}, false
			}
		}
	}
	info, ok := scan.vote()
	if !ok || info.Slug == slug {
		return PlatformInfo{}, false
	}
	return info, true
}

// contentScan is what the extension checks read from downloaded content.
type contentScan struct {
	exts   map[string]bool
	counts map[string]int // files per unique-extension platform slug
	// single is the extension of the only file when the content is one file,
	// looking through a .zip that holds one file.
	single string
}

// maxZipMembers bounds how much of a zip's directory is read.
const maxZipMembers = 5000

func scanContent(path string) contentScan {
	scan := contentScan{exts: map[string]bool{}, counts: map[string]int{}}
	fi, err := os.Stat(path)
	if err != nil {
		return scan
	}
	var files []string
	if fi.IsDir() {
		_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			// Gamarr's own sidecars are not content.
			if strings.HasSuffix(info.Name(), ".gamarr.json") || info.Name() == "metadata.json" {
				return nil
			}
			files = append(files, p)
			return nil
		})
	} else {
		files = []string{path}
	}
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		scan.add(ext)
		if ext == ".zip" {
			members := zipMemberExts(f)
			for _, m := range members {
				scan.add(m)
			}
			if len(files) == 1 && len(members) == 1 {
				scan.single = members[0]
			}
		}
	}
	if len(files) == 1 && scan.single == "" {
		scan.single = strings.ToLower(filepath.Ext(files[0]))
	}
	return scan
}

func (s *contentScan) add(ext string) {
	if ext == "" {
		return
	}
	s.exts[ext] = true
	if p, ok := byExtension[ext]; ok {
		s.counts[p.Slug]++
	}
}

// vote returns the platform with the most unique-extension files.
func (s *contentScan) vote() (PlatformInfo, bool) {
	var best *Platform
	bestN := 0
	for i := range Registry {
		if n := s.counts[Registry[i].Slug]; n > bestN {
			best, bestN = &Registry[i], n
		}
	}
	if best == nil {
		return PlatformInfo{}, false
	}
	return best.Info(), true
}

// zipMemberExts lists the extensions of a zip's file members from its central
// directory, without extracting anything. Unreadable archives list nothing.
func zipMemberExts(path string) []string {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil
	}
	defer r.Close()
	var out []string
	for i, f := range r.File {
		if i >= maxZipMembers {
			break
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(f.Name)); ext != "" {
			out = append(out, ext)
		}
	}
	return out
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
