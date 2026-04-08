package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/metadata"
)

// rawgClient returns a lazily-initialized RAWG client for the server.
// Safe to call even when RAWG is not configured (methods return nil).
func (s *Server) rawgClient() *metadata.Client {
	return metadata.NewClient(s.cfg.RAWGAPIKey)
}

// handleMetadataSearch handles GET /api/metadata/search?q=game_name&platform=slug
func (s *Server) handleMetadataSearch(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasRAWG() {
		writeError(w, 503, "RAWG API key not configured (set RAWG_API_KEY env var)")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, 400, "Query parameter 'q' is required")
		return
	}

	platformSlug := r.URL.Query().Get("platform")

	client := s.rawgClient()
	result, err := client.SearchGame(query, platformSlug)
	if err != nil {
		writeError(w, 502, "RAWG search failed: "+err.Error())
		return
	}
	if result == nil {
		writeJSON(w, 200, map[string]interface{}{"found": false, "result": nil})
		return
	}

	writeJSON(w, 200, map[string]interface{}{"found": true, "result": result})
}

// handleMetadataGet handles GET /api/metadata/{rawg_id}
func (s *Server) handleMetadataGet(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasRAWG() {
		writeError(w, 503, "RAWG API key not configured (set RAWG_API_KEY env var)")
		return
	}

	rawgID, err := strconv.Atoi(chi.URLParam(r, "rawg_id"))
	if err != nil || rawgID <= 0 {
		writeError(w, 400, "Invalid RAWG ID")
		return
	}

	client := s.rawgClient()
	result, err := client.GetGame(rawgID)
	if err != nil {
		writeError(w, 502, "RAWG fetch failed: "+err.Error())
		return
	}
	if result == nil {
		writeError(w, 404, "Game not found")
		return
	}

	writeJSON(w, 200, result)
}

// handleEnrichLibraryItem handles POST /api/library/{id}/enrich
// Fetches RAWG metadata for a library item and saves it.
func (s *Server) handleEnrichLibraryItem(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasRAWG() {
		writeError(w, 503, "RAWG API key not configured (set RAWG_API_KEY env var)")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "Invalid library item ID")
		return
	}

	// Get the library item.
	item, err := s.mgr.Jobs().GetLibraryItem(id)
	if err != nil {
		writeError(w, 404, "Library item not found")
		return
	}

	// Search RAWG for metadata.
	client := s.rawgClient()
	meta, err := client.SearchGame(item.Title, item.PlatformSlug)
	if err != nil {
		writeError(w, 502, "RAWG search failed: "+err.Error())
		return
	}
	if meta == nil {
		writeError(w, 404, "No metadata found on RAWG for: "+item.Title)
		return
	}

	// If we got a search result, fetch full details for description, developers, publishers.
	if meta.ID > 0 {
		full, err := client.GetGame(meta.ID)
		if err == nil && full != nil {
			meta = full
		}
	}

	// Serialize metadata to JSON and save.
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		writeError(w, 500, "Failed to serialize metadata")
		return
	}

	if err := s.mgr.Jobs().UpdateLibraryItemMetadata(id, string(metaJSON)); err != nil {
		writeError(w, 500, "Failed to save metadata: "+err.Error())
		return
	}

	s.mgr.Jobs().LogActivity("metadata_enriched", item.Title,
		"Enriched with RAWG metadata ("+meta.Name+")", "", nil)

	writeJSON(w, 200, map[string]interface{}{
		"success":  true,
		"metadata": meta,
	})
}

// ── Connection Tests for new clients ─────────────────────────────────────────

func (s *Server) handleTestTransmission(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasTransmission() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	tc := s.mgr.Transmission()
	if tc == nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Client not initialized"})
		return
	}
	result := tc.Diagnose()
	writeJSON(w, 200, result)
}

func (s *Server) handleTestDeluge(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasDeluge() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	dc := s.mgr.Deluge()
	if dc == nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Client not initialized"})
		return
	}
	result := dc.Diagnose()
	writeJSON(w, 200, result)
}

// RegisterMetadataRoutes registers metadata and new client test routes on the given chi router.
// Call this from NewRouter after the router is created.
//
// Routes to add to the router in api.go:
//   r.Get("/api/metadata/search", s.handleMetadataSearch)
//   r.Get("/api/metadata/{rawg_id}", s.handleMetadataGet)
//   r.Post("/api/library/{id}/enrich", s.handleEnrichLibraryItem)
//   r.Post("/api/test/transmission", s.handleTestTransmission)
//   r.Post("/api/test/deluge", s.handleTestDeluge)
func (s *Server) RegisterMetadataRoutes(r chi.Router) {
	r.Get("/api/metadata/search", s.handleMetadataSearch)
	r.Get("/api/metadata/{rawg_id}", s.handleMetadataGet)
	r.Post("/api/library/{id}/enrich", s.handleEnrichLibraryItem)
	r.Post("/api/test/transmission", s.handleTestTransmission)
	r.Post("/api/test/deluge", s.handleTestDeluge)
}
