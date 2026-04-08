package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gamarr/internal/db"
	"gamarr/internal/organize"
)

// scannedFile represents a file found during a manual import scan.
type scannedFile struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Extension    string `json:"extension"`
	Platform     string `json:"platform"`
	PlatformSlug string `json:"platform_slug"`
	IsPC         bool   `json:"is_pc"`
}

// gameExtensions lists recognized game file extensions.
var gameExtensions = map[string]bool{
	".nsp": true, ".xci": true, ".nsz": true,
	".3ds": true, ".cia": true,
	".nds": true,
	".gba": true, ".gb": true, ".gbc": true,
	".n64": true, ".z64": true, ".v64": true,
	".nes": true, ".sfc": true, ".smc": true,
	".gcm": true, ".gcz": true,
	".wbfs": true, ".wad": true, ".rpx": true,
	".pbp": true, ".cso": true, ".pkg": true,
	".gdi": true, ".cdi": true,
	".iso": true,
	".zip": true, ".7z": true, ".rar": true,
	".exe": true, ".bin": true,
}

// handleScanImport handles POST /api/import/scan.
func (s *Server) handleScanImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Directory string `json:"directory"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Directory == "" {
		writeError(w, http.StatusBadRequest, "Directory path is required")
		return
	}

	info, err := os.Stat(req.Directory)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "Invalid or inaccessible directory")
		return
	}

	var files []scannedFile
	filepath.Walk(req.Directory, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !gameExtensions[ext] {
			return nil
		}

		platform, platformSlug, isPC := organize.DetectPlatform(info.Name())

		files = append(files, scannedFile{
			Path:         path,
			Name:         info.Name(),
			Size:         info.Size(),
			Extension:    ext,
			Platform:     platform,
			PlatformSlug: platformSlug,
			IsPC:         isPC,
		})
		return nil
	})

	if files == nil {
		files = []scannedFile{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
		"total":   len(files),
	})
}

// handleImportFiles handles POST /api/import/files.
func (s *Server) handleImportFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Files []struct {
			Path         string `json:"path"`
			Title        string `json:"title"`
			Platform     string `json:"platform"`
			PlatformSlug string `json:"platform_slug"`
			IsPC         bool   `json:"is_pc"`
		} `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	pipeline := organize.NewPipeline(s.cfg)
	imported := 0
	skipped := 0
	var errors []string

	for _, f := range req.Files {
		if f.Path == "" {
			skipped++
			continue
		}

		info, err := os.Stat(f.Path)
		if err != nil {
			errors = append(errors, f.Path+": file not found")
			skipped++
			continue
		}

		title := f.Title
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		}

		// Check for duplicate
		if existing := s.mgr.Jobs().FindLibraryByTitle(title, f.PlatformSlug); existing != nil {
			skipped++
			continue
		}

		// Organize (move to correct directory)
		destPath, err := pipeline.OrganizeGame(f.Path, f.Platform, f.PlatformSlug, f.IsPC)
		if err != nil {
			// If it's "already exists" that's fine, use the dest path
			if !strings.Contains(err.Error(), "already exists") {
				errors = append(errors, f.Path+": "+err.Error())
				skipped++
				continue
			}
		}

		// Add to library
		item := &db.LibraryItem{
			Title:        title,
			Platform:     f.Platform,
			PlatformSlug: f.PlatformSlug,
			IsPC:         f.IsPC,
			FilePath:     destPath,
			FileSize:     info.Size(),
			Source:       "manual",
			SourceType:   "import",
		}
		if _, err := s.mgr.Jobs().AddLibraryItem(item); err != nil {
			errors = append(errors, title+": "+err.Error())
			skipped++
			continue
		}

		s.mgr.Jobs().LogActivity("manual_import", title, "Manually imported: "+filepath.Base(f.Path), "", nil)
		imported++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	})
}
