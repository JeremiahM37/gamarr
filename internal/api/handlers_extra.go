package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
	"gamarr/internal/search"
)

// ── Library ────────────────────────────────────────────────────────────────────

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 {
		pageSize = 50
	}
	query := r.URL.Query().Get("q")
	platformSlug := r.URL.Query().Get("platform")
	tagFilter := r.URL.Query().Get("tag")

	result := s.mgr.Jobs().GetLibraryPage(page, pageSize, query, platformSlug)

	// Filter by tag if specified
	if tagFilter != "" {
		taggedIDs := s.mgr.Jobs().GetLibraryItemIDsByTag(tagFilter)
		idSet := make(map[int64]bool, len(taggedIDs))
		for _, id := range taggedIDs {
			idSet[id] = true
		}
		var filtered []db.LibraryItem
		for _, item := range result.Items {
			if idSet[item.ID] {
				filtered = append(filtered, item)
			}
		}
		if filtered == nil {
			filtered = []db.LibraryItem{}
		}
		result.Items = filtered
		result.Total = len(filtered)
		result.TotalPages = 1
	}

	writeJSON(w, 200, result)
}

func (s *Server) handleDeleteLibraryItem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.mgr.Jobs().DeleteLibraryItem(id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

// ── Wishlist ───────────────────────────────────────────────────────────────────

func (s *Server) handleWishlist(w http.ResponseWriter, r *http.Request) {
	items := s.mgr.Jobs().GetWishlist()
	if items == nil {
		items = []db.WishlistItem{}
	}
	writeJSON(w, 200, map[string]interface{}{"items": items})
}

func (s *Server) handleAddWishlist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title        string `json:"title"`
		Platform     string `json:"platform"`
		PlatformSlug string `json:"platform_slug"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Title == "" {
		writeError(w, 400, "Title required")
		return
	}
	id, err := s.mgr.Jobs().AddWishlistItem(req.Title, req.Platform, req.PlatformSlug)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"success": true, "id": id})
}

func (s *Server) handleDeleteWishlist(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	s.mgr.Jobs().DeleteWishlistItem(id)
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

// ── Activity ───────────────────────────────────────────────────────────────────

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	entries, total := s.mgr.Jobs().GetActivity(page, 50)
	if entries == nil {
		entries = []db.ActivityEntry{}
	}
	writeJSON(w, 200, map[string]interface{}{
		"entries": entries,
		"total":   total,
		"page":    page,
	})
}

// ── Retry ──────────────────────────────────────────────────────────────────────

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	ok, msg := s.mgr.RetryJob(jobID)
	writeJSON(w, 200, map[string]interface{}{"success": ok, "message": msg})
}

// ── Connection Tests ───────────────────────────────────────────────────────────

func (s *Server) handleTestProwlarr(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasProwlarr() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", s.cfg.ProwlarrURL+"/api/v1/indexer", nil)
	req.Header.Set("X-Api-Key", s.cfg.ProwlarrAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	resp.Body.Close()
	writeJSON(w, 200, map[string]interface{}{"success": resp.StatusCode == 200, "status": resp.StatusCode})
}

func (s *Server) handleTestQBittorrent(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.HasQBittorrent() {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	ok := s.mgr.QB().Login()
	writeJSON(w, 200, map[string]interface{}{"success": ok})
}

func (s *Server) handleTestSABnzbd(w http.ResponseWriter, r *http.Request) {
	if s.sab == nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Not configured"})
		return
	}
	err := s.sab.TestConnection()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

// ── Source Health ─────────────────────────────────────────────────────────────

func (s *Server) handleSourcesHealth(w http.ResponseWriter, r *http.Request) {
	healthData := search.GetAllSourceHealth()
	writeJSON(w, 200, map[string]interface{}{"sources": healthData})
}

func (s *Server) handleSourceReset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ok := search.ResetCircuit(name)
	if !ok {
		writeJSON(w, 200, map[string]interface{}{"success": false, "error": "Source not found"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

// ── Config ─────────────────────────────────────────────────────────────────────

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"prowlarr": map[string]interface{}{
			"configured": s.cfg.HasProwlarr(),
			"url":        s.cfg.ProwlarrURL,
		},
		"qbittorrent": map[string]interface{}{
			"configured": s.cfg.HasQBittorrent(),
			"url":        s.cfg.QBURL,
		},
		"sabnzbd": map[string]interface{}{
			"configured": s.cfg.HasSABnzbd(),
			"url":        s.cfg.SABnzbdURL,
		},
		"transmission": map[string]interface{}{
			"configured": s.cfg.HasTransmission(),
			"url":        s.cfg.TransmissionURL,
		},
		"deluge": map[string]interface{}{
			"configured": s.cfg.HasDeluge(),
			"url":        s.cfg.DelugeURL,
		},
		"rawg": map[string]interface{}{
			"configured": s.cfg.HasRAWG(),
		},
		"gamevault_url": s.cfg.GameVaultURL,
		"romm_url":      s.cfg.RomMURL,
		"version":       "1.0.0",
	})
}

// ── Metrics (Prometheus) ───────────────────────────────────────────────────────

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.MetricsEnabled {
		http.Error(w, "Metrics disabled", 403)
		return
	}

	// Job status counts
	statusCounts := make(map[string]int)
	for _, item := range s.mgr.Jobs().Items() {
		status, _ := item.Data["status"].(string)
		statusCounts[status]++
	}

	// Library stats
	libStats := s.mgr.Jobs().LibraryStats()
	libTotal := s.mgr.Jobs().LibraryTotal()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP gamarr_jobs_total Number of download jobs by status\n")
	fmt.Fprintf(w, "# TYPE gamarr_jobs_total gauge\n")
	for status, count := range statusCounts {
		fmt.Fprintf(w, "gamarr_jobs_total{status=%q} %d\n", status, count)
	}

	fmt.Fprintf(w, "# HELP gamarr_library_total Total items in library\n")
	fmt.Fprintf(w, "# TYPE gamarr_library_total gauge\n")
	fmt.Fprintf(w, "gamarr_library_total %d\n", libTotal)

	fmt.Fprintf(w, "# HELP gamarr_library_by_platform Library items by platform\n")
	fmt.Fprintf(w, "# TYPE gamarr_library_by_platform gauge\n")
	for plat, count := range libStats {
		fmt.Fprintf(w, "gamarr_library_by_platform{platform=%q} %d\n", plat, count)
	}

	// Activity event count
	activityCount := s.mgr.Jobs().ActivityCount()
	fmt.Fprintf(w, "# HELP gamarr_activity_events_total Total activity log events\n")
	fmt.Fprintf(w, "# TYPE gamarr_activity_events_total gauge\n")
	fmt.Fprintf(w, "gamarr_activity_events_total %d\n", activityCount)

	// Source health metrics
	healthData := search.GetAllSourceHealth()
	fmt.Fprintf(w, "# HELP gamarr_source_health_score Source health score (0-100)\n")
	fmt.Fprintf(w, "# TYPE gamarr_source_health_score gauge\n")
	for name, h := range healthData {
		fmt.Fprintf(w, "gamarr_source_health_score{source=%q} %d\n", name, h.Score)
	}
	fmt.Fprintf(w, "# HELP gamarr_source_circuit_open Whether source circuit breaker is open (1=open)\n")
	fmt.Fprintf(w, "# TYPE gamarr_source_circuit_open gauge\n")
	for name, h := range healthData {
		val := 0
		if h.CircuitOpen {
			val = 1
		}
		fmt.Fprintf(w, "gamarr_source_circuit_open{source=%q} %d\n", name, val)
	}
	fmt.Fprintf(w, "# HELP gamarr_source_search_total Source search counts\n")
	fmt.Fprintf(w, "# TYPE gamarr_source_search_total counter\n")
	for name, h := range healthData {
		fmt.Fprintf(w, "gamarr_source_search_total{source=%q,result=\"ok\"} %d\n", name, h.SearchOK)
		fmt.Fprintf(w, "gamarr_source_search_total{source=%q,result=\"fail\"} %d\n", name, h.SearchFail)
	}
	fmt.Fprintf(w, "# HELP gamarr_source_download_total Source download counts\n")
	fmt.Fprintf(w, "# TYPE gamarr_source_download_total counter\n")
	for name, h := range healthData {
		fmt.Fprintf(w, "gamarr_source_download_total{source=%q,result=\"ok\"} %d\n", name, h.DownloadOK)
		fmt.Fprintf(w, "gamarr_source_download_total{source=%q,result=\"fail\"} %d\n", name, h.DownloadFail)
	}
}
