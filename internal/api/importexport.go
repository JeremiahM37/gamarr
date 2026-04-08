package api

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"gamarr/internal/db"
)

const gamarrVersion = "1.0.0"

// ── Export Types ──────────────────────────────────────────────────────────────

type exportWrapper struct {
	Version    string      `json:"version"`
	ExportDate string      `json:"export_date"`
	Type       string      `json:"type"`
	Count      int         `json:"count"`
	Items      interface{} `json:"items"`
}

// handleExportLibrary handles GET /api/export/library.
func (s *Server) handleExportLibrary(w http.ResponseWriter, r *http.Request) {
	// Get all library items (large page)
	result := s.mgr.Jobs().GetLibraryPage(1, 100000, "", "")
	w.Header().Set("Content-Disposition", "attachment; filename=gamarr-library.json")
	writeJSON(w, http.StatusOK, exportWrapper{
		Version:    gamarrVersion,
		ExportDate: time.Now().UTC().Format(time.RFC3339),
		Type:       "library",
		Count:      len(result.Items),
		Items:      result.Items,
	})
}

// handleExportWishlist handles GET /api/export/wishlist.
func (s *Server) handleExportWishlist(w http.ResponseWriter, r *http.Request) {
	items := s.mgr.Jobs().GetWishlist()
	if items == nil {
		items = []db.WishlistItem{}
	}
	w.Header().Set("Content-Disposition", "attachment; filename=gamarr-wishlist.json")
	writeJSON(w, http.StatusOK, exportWrapper{
		Version:    gamarrVersion,
		ExportDate: time.Now().UTC().Format(time.RFC3339),
		Type:       "wishlist",
		Count:      len(items),
		Items:      items,
	})
}

// handleExportRequests handles GET /api/export/requests.
func (s *Server) handleExportRequests(w http.ResponseWriter, r *http.Request) {
	requests, err := s.mgr.Jobs().ListRequests("", "", 100000, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to export requests")
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=gamarr-requests.json")
	writeJSON(w, http.StatusOK, exportWrapper{
		Version:    gamarrVersion,
		ExportDate: time.Now().UTC().Format(time.RFC3339),
		Type:       "requests",
		Count:      len(requests),
		Items:      requests,
	})
}

// ── Import Types ─────────────────────────────────────────────────────────────

// handleImportLibrary handles POST /api/import/library.
func (s *Server) handleImportLibrary(w http.ResponseWriter, r *http.Request) {
	var wrapper struct {
		Items []db.LibraryItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wrapper); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	imported := 0
	skipped := 0
	for _, item := range wrapper.Items {
		if item.Title == "" {
			skipped++
			continue
		}
		// Check for duplicate by title + platform
		if isDuplicateLibrary(s, item.Title, item.PlatformSlug) {
			skipped++
			continue
		}
		_, err := s.mgr.Jobs().AddLibraryItem(&item)
		if err != nil {
			skipped++
			continue
		}
		imported++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"imported": imported,
		"skipped":  skipped,
	})
}

// handleImportWishlist handles POST /api/import/wishlist.
func (s *Server) handleImportWishlist(w http.ResponseWriter, r *http.Request) {
	var wrapper struct {
		Items []db.WishlistItem `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&wrapper); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	imported := 0
	skipped := 0
	for _, item := range wrapper.Items {
		if item.Title == "" {
			skipped++
			continue
		}
		_, err := s.mgr.Jobs().AddWishlistItem(item.Title, item.Platform, item.PlatformSlug)
		if err != nil {
			skipped++
			continue
		}
		imported++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"imported": imported,
		"skipped":  skipped,
	})
}

// handleImportCSV handles POST /api/import/csv.
// Expects a CSV with at minimum "title" and optionally "platform" columns.
func (s *Server) handleImportCSV(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read body")
		return
	}

	reader := csv.NewReader(strings.NewReader(string(body)))
	records, err := reader.ReadAll()
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid CSV")
		return
	}

	if len(records) < 2 { // header + at least 1 row
		writeError(w, http.StatusBadRequest, "CSV must have a header row and at least one data row")
		return
	}

	// Find column indices
	header := records[0]
	titleIdx := -1
	platformIdx := -1
	for i, col := range header {
		col = strings.ToLower(strings.TrimSpace(col))
		if col == "title" || col == "name" || col == "game" {
			titleIdx = i
		}
		if col == "platform" || col == "system" || col == "console" {
			platformIdx = i
		}
	}

	if titleIdx == -1 {
		writeError(w, http.StatusBadRequest, "CSV must have a 'title' column")
		return
	}

	imported := 0
	skipped := 0
	for _, row := range records[1:] {
		if titleIdx >= len(row) {
			skipped++
			continue
		}
		title := strings.TrimSpace(row[titleIdx])
		if title == "" {
			skipped++
			continue
		}

		platform := ""
		platformSlug := ""
		if platformIdx >= 0 && platformIdx < len(row) {
			platform = strings.TrimSpace(row[platformIdx])
			platformSlug = strings.ToLower(strings.ReplaceAll(platform, " ", ""))
		}

		// Check for duplicate
		if isDuplicateLibrary(s, title, platformSlug) {
			skipped++
			continue
		}

		item := &db.LibraryItem{
			Title:        title,
			Platform:     platform,
			PlatformSlug: platformSlug,
			Source:       "import",
			SourceType:   "csv",
		}
		_, err := s.mgr.Jobs().AddLibraryItem(item)
		if err != nil {
			skipped++
			continue
		}
		imported++
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"imported": imported,
		"skipped":  skipped,
	})
}

// isDuplicateLibrary checks if a game with normalized title+platform exists.
func isDuplicateLibrary(s *Server, title, platformSlug string) bool {
	existing := s.mgr.Jobs().FindLibraryByTitle(title, platformSlug)
	return existing != nil
}
