package download

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gamarr/internal/db"
	"gamarr/internal/platform"
)

// ScanLibraryDirs scans vault and ROM directories to populate the library.
// Clears previous scan entries and rescans from scratch for accuracy.
func (m *Manager) ScanLibraryDirs() {
	// Clear previous scan entries so we always reflect current disk state
	m.jobs.ClearScanEntries()
	total := 0

	// Scan PC games vault — each top-level entry is one game (no recursion)
	if m.cfg.GamesVaultPath != "" {
		n := m.scanVault(m.cfg.GamesVaultPath)
		total += n
	}

	// Scan ROM platform directories
	if m.cfg.GamesRomsPath != "" {
		entries, err := os.ReadDir(m.cfg.GamesRomsPath)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					slug := e.Name()
					platName := platformNameFromSlug(slug)
					n := m.scanDir(filepath.Join(m.cfg.GamesRomsPath, slug), platName, slug, false)
					total += n
				}
			}
		}
	}

	if total > 0 {
		slog.Info("library scan complete", "new_items", total)
	}
}

// scanVault scans the PC games vault. Each top-level entry (dir or archive) is one game.
func (m *Manager) scanVault(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Skip loose .gamarr.json sidecars
		if strings.HasSuffix(name, ".gamarr.json") {
			continue
		}
		fp := filepath.Join(dir, name)
		added += m.addLibraryEntry(fp, name, "PC", "", true)
	}
	return added
}

// gameExtensions are file extensions that represent playable games/ROMs.
// Every extension and format in the platform registry counts, plus the
// archive formats ROMs commonly ship in.
var gameExtensions = func() map[string]bool {
	m := map[string]bool{".zip": true, ".7z": true, ".rar": true}
	for _, ext := range platform.KnownExtensions() {
		m[ext] = true
	}
	return m
}()

// titleExtensions are stripped from a scanned file name to form its title:
// archives plus every registry format, longest first so ".tar.gz" wins over
// any shorter suffix.
var titleExtensions = func() []string {
	exts := []string{".zip", ".rar", ".7z", ".tar", ".tar.gz"}
	seen := map[string]bool{}
	for _, p := range platform.Registry {
		if p.IsPC {
			continue
		}
		for _, ext := range append(append([]string{}, p.Extensions...), p.Formats...) {
			if !seen[ext] {
				seen[ext] = true
				exts = append(exts, ext)
			}
		}
	}
	sort.SliceStable(exts, func(i, j int) bool { return len(exts[i]) > len(exts[j]) })
	return exts
}()

func (m *Manager) scanDir(dir, platform, platformSlug string, isPC bool) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	added := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, ".gamarr.json") || strings.HasSuffix(name, ".extracted") {
			continue
		}
		fp := filepath.Join(dir, name)

		if e.IsDir() {
			// For ROM platforms, check if this directory contains game files
			// or is just an organizational subdirectory (like "roms/")
			if !isPC && containsGameFilesFor(fp, platformSlug) {
				// This is a game folder (e.g., "TowerFall [NSP]/")
				added += m.addLibraryEntry(fp, name, platform, platformSlug, isPC)
			} else {
				// Recurse into subdirectories (handles nested "roms/" dirs)
				added += m.scanDir(fp, platform, platformSlug, isPC)
			}
		} else {
			// Single file — check if it's a game file
			ext := strings.ToLower(filepath.Ext(name))
			if isPC || isGameFile(ext, platformSlug) {
				// Skip small files (DLC, updates, sidecars)
				if info, err := e.Info(); err == nil && info.Size() < 1_000_000 && !isPC {
					continue
				}
				// Skip update files
				nameLower := strings.ToLower(name)
				if strings.HasPrefix(nameLower, "[update]") || strings.Contains(nameLower, "update v") {
					continue
				}
				// Skip DLC/costume files for Smash etc.
				if strings.Contains(nameLower, "costume") || strings.Contains(nameLower, "challenger pack") ||
					strings.Contains(nameLower, "spirit board") || strings.Contains(nameLower, "fighters pass") ||
					strings.Contains(nameLower, "[dlc]") || strings.Contains(nameLower, "vault shopper") {
					continue
				}
				added += m.addLibraryEntry(fp, name, platform, platformSlug, isPC)
			}
		}
	}
	return added
}

func (m *Manager) addLibraryEntry(fp, name, platform, platformSlug string, isPC bool) int {
	sourceID := "scan:" + fp
	if m.jobs.LibraryHasSourceID(sourceID) {
		return 0
	}

	var fileSize int64
	info, err := os.Stat(fp)
	if err == nil {
		if info.IsDir() {
			fileSize = dirSize(fp)
		} else {
			fileSize = info.Size()
		}
	}

	title := cleanTitle(name)
	id, err := m.jobs.AddLibraryItem(&db.LibraryItem{
		Title:        title,
		Platform:     platform,
		PlatformSlug: platformSlug,
		IsPC:         isPC,
		FilePath:     fp,
		FileSize:     fileSize,
		Source:       "scan",
		SourceType:   "scan",
		SourceID:     sourceID,
		Metadata:     "{}",
	})
	if err != nil {
		return 0
	}

	// A row recorded when the game was downloaded points at this same file under
	// a different source scheme, so the guard above cannot see it and the game
	// shows twice: once titled from the torrent, with no size, and once from this
	// scan. The scan is the better record - it has the real size from disk and a
	// title derived from the filename rather than the raw torrent name. Pruned
	// after the insert, so a failed insert cannot leave the file unrecorded.
	if n := m.jobs.DeleteLibraryItemsByPath(fp, id); n > 0 {
		slog.Info("library scan superseded download-time rows", "path", fp, "removed", n)
	}
	return 1
}

// isGameFile reports whether ext is a game file in the ROM folder for slug:
// anything in gameExtensions, plus the folder platform's own formats that
// double as everyday files elsewhere (.md is a Genesis ROM in roms/genesis
// and Markdown anywhere else).
func isGameFile(ext, slug string) bool {
	if gameExtensions[ext] {
		return true
	}
	p, ok := platform.Lookup(slug)
	return ok && p.Accepts(ext)
}

// containsGameFiles checks if a directory directly contains game ROM files.
func containsGameFiles(dir string) bool {
	return containsGameFilesFor(dir, "")
}

// containsGameFilesFor is containsGameFiles inside the ROM folder for slug.
func containsGameFilesFor(dir, slug string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if isGameFile(ext, slug) {
			return true
		}
	}
	return false
}

// TrackInLibrary adds a completed download to the library.
func (m *Manager) TrackInLibrary(title, platform, platformSlug string, isPC bool, filePath string, fileSize int64, source, sourceType, sourceID string) {
	if m.jobs.LibraryHasSourceID(sourceID) {
		return
	}
	id, err := m.jobs.AddLibraryItem(&db.LibraryItem{
		Title:        title,
		Platform:     platform,
		PlatformSlug: platformSlug,
		IsPC:         isPC,
		FilePath:     filePath,
		FileSize:     fileSize,
		Source:       source,
		SourceType:   sourceType,
		SourceID:     sourceID,
		Metadata:     "{}",
	})
	if err != nil {
		slog.Warn("failed to add to library", "error", err)
		return
	}
	m.jobs.LogActivity("import_completed", title, "Added to library: "+platform, "", &id)
}

func dirSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func cleanTitle(name string) string {
	// Remove the archive or ROM extension
	lower := strings.ToLower(name)
	for _, ext := range titleExtensions {
		if strings.HasSuffix(lower, ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	// URL-decode percent-encoded filenames
	name = strings.ReplaceAll(name, "%20", " ")
	name = strings.ReplaceAll(name, "%28", "(")
	name = strings.ReplaceAll(name, "%29", ")")
	name = strings.ReplaceAll(name, "%2C", ",")
	return strings.TrimSpace(name)
}

// platformNameFromSlug names a ROM library folder: the registry name, or the
// upper-cased folder name when the folder is not a registry platform.
func platformNameFromSlug(slug string) string {
	if name := platform.NameForSlug(slug); name != "" {
		return name
	}
	return strings.ToUpper(slug)
}
