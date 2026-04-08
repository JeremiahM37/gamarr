package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/config"
	"gamarr/internal/download"
	"gamarr/internal/models"
	"gamarr/internal/monitor"
	"gamarr/internal/platform"
	"gamarr/internal/sabnzbd"
	"gamarr/internal/scheduler"
	"gamarr/internal/search"
)

//go:embed web/index.html
var webFS embed.FS

// Server holds all API dependencies.
type Server struct {
	cfg       *config.Config
	mgr       *download.Manager
	mon       *monitor.GamarrMonitor
	sab       *sabnzbd.Client
	sessions  *SessionStore
	scheduler *scheduler.Scheduler
}

// NewRouter creates a new chi router with all routes.
func NewRouter(cfg *config.Config, mgr *download.Manager, mon *monitor.GamarrMonitor, sab *sabnzbd.Client, sched *scheduler.Scheduler) http.Handler {
	sessions := NewSessionStore()
	s := &Server{cfg: cfg, mgr: mgr, mon: mon, sab: sab, sessions: sessions, scheduler: sched}

	// Rate limiter: 60-second window.
	rl := NewRateLimiter(60, map[string]int{
		"login":    5,
		"search":   30,
		"download": 10,
		"api":      60,
		"default":  120,
	})

	r := chi.NewRouter()

	// Outermost middleware (applied first).
	r.Use(logMiddleware)
	r.Use(func(next http.Handler) http.Handler { return requestSizeLimitMiddleware(next) })
	r.Use(func(next http.Handler) http.Handler { return securityHeadersMiddleware(next) })
	r.Use(corsMiddleware)
	r.Use(func(next http.Handler) http.Handler { return rateLimitMiddleware(rl, next) })
	r.Use(func(next http.Handler) http.Handler { return authMiddleware(cfg, mgr.Jobs(), sessions, next) })

	// UI
	r.Get("/", s.handleIndex)

	// Auth routes (exempt from auth middleware).
	r.Post("/api/login", handleLogin(cfg, mgr.Jobs(), sessions))
	r.Post("/api/logout", handleLogout(sessions, mgr.Jobs()))
	r.Post("/api/register", handleRegister(mgr.Jobs(), sessions))
	r.Get("/api/auth/status", handleAuthStatus(mgr.Jobs(), sessions))

	// Search & browse
	r.Get("/api/search", s.handleSearch)
	r.Get("/api/platforms", s.handlePlatforms)
	r.Get("/api/sources", s.handleSources)

	// Downloads
	r.Post("/api/download", s.handleDownload)
	r.Get("/api/downloads", s.handleDownloads)
	r.Delete("/api/downloads/torrent/{hash}", s.handleDeleteTorrent)
	r.Delete("/api/downloads/{jobID}", s.handleDeleteJob)
	r.Post("/api/downloads/clear", s.handleClearFinished)
	r.Post("/api/downloads/organize/{hash}", s.handleOrganizeTorrent)
	r.Post("/api/downloads/{jobID}/retry", s.handleRetryJob)

	// Library
	r.Get("/api/library", s.handleLibrary)
	r.Delete("/api/library/{id}", s.handleDeleteLibraryItem)

	// Wishlist
	r.Get("/api/wishlist", s.handleWishlist)
	r.Post("/api/wishlist", s.handleAddWishlist)
	r.Delete("/api/wishlist/{id}", s.handleDeleteWishlist)

	// Activity
	r.Get("/api/activity", s.handleActivity)

	// DDL sources
	r.Get("/api/ddl-sources", s.handleDDLSources)
	r.Post("/api/ddl-sources", s.handleAddDDLSource)
	r.Delete("/api/ddl-sources/{idx}", s.handleDeleteDDLSource)

	// Settings & config (admin only)
	r.Get("/api/settings", requireAdmin(s.handleGetSettings))
	r.Put("/api/settings", requireAdmin(s.handleUpdateSettings))
	r.Get("/api/config", s.handleConfig)
	r.Get("/api/stats", s.handleStats)
	r.Get("/api/health", s.handleHealth)

	// Connection tests (admin only)
	r.Post("/api/test/prowlarr", requireAdmin(s.handleTestProwlarr))
	r.Post("/api/test/qbittorrent", requireAdmin(s.handleTestQBittorrent))
	r.Post("/api/test/sabnzbd", requireAdmin(s.handleTestSABnzbd))

	// Source health
	r.Get("/api/sources/health", s.handleSourcesHealth)
	r.Post("/api/sources/{name}/reset", s.handleSourceReset)

	// Monitoring
	r.Get("/api/monitor/status", s.handleMonitorStatus)
	r.Post("/api/monitor/analyze", s.handleMonitorAnalyze)
	r.Post("/api/monitor/actions/{actionID}/approve", s.handleMonitorApprove)
	r.Post("/api/monitor/actions/{actionID}/dismiss", s.handleMonitorDismiss)

	// Admin dashboard & user management
	r.Get("/api/admin/dashboard", requireAdmin(s.handleAdminDashboard))
	r.Get("/api/users", requireAdmin(handleListUsers(mgr.Jobs())))
	r.Patch("/api/users/{id}", requireAdmin(handleUpdateUser(mgr.Jobs())))
	r.Delete("/api/users/{id}", requireAdmin(handleDeleteUser(mgr.Jobs())))

	// Requests
	r.Post("/api/requests", s.handleCreateRequest)
	r.Get("/api/requests", s.handleListRequests)
	r.Get("/api/requests/{id}", s.handleGetRequest)
	r.Patch("/api/requests/{id}", s.handleUpdateRequest)
	r.Delete("/api/requests/{id}", requireAdmin(s.handleDeleteRequest))
	r.Post("/api/requests/{id}/search", s.handleSearchRequest)
	r.Post("/api/requests/{id}/download", s.handleDownloadForRequest)

	// Notifications
	r.Get("/api/notifications", s.handleGetNotifications)
	r.Get("/api/notifications/unread", s.handleUnreadCount)
	r.Post("/api/notifications/{id}/read", s.handleMarkRead)
	r.Post("/api/notifications/read-all", s.handleMarkAllRead)

	// Webhooks
	r.Get("/api/webhooks", s.handleGetWebhooks)
	r.Post("/api/webhooks", s.handleAddWebhook)
	r.Delete("/api/webhooks/{id}", s.handleDeleteWebhook)
	r.Post("/api/webhooks/test", s.handleTestWebhook)

	// Import/Export
	r.Get("/api/export/library", s.handleExportLibrary)
	r.Get("/api/export/wishlist", s.handleExportWishlist)
	r.Get("/api/export/requests", s.handleExportRequests)
	r.Post("/api/import/library", s.handleImportLibrary)
	r.Post("/api/import/wishlist", s.handleImportWishlist)
	r.Post("/api/import/csv", s.handleImportCSV)

	// Scheduler
	r.Get("/api/scheduler/status", s.handleSchedulerStatus)
	r.Post("/api/scheduler/run", s.handleSchedulerRun)

	// Play History
	r.Post("/api/history", s.handleAddHistory)
	r.Get("/api/history", s.handleGetHistory)
	r.Get("/api/history/stats", s.handleHistoryStats)
	r.Patch("/api/history/{id}", s.handleUpdateHistory)
	r.Delete("/api/history/{id}", s.handleDeleteHistory)

	// Duplicate detection
	r.Get("/api/library/check", s.handleCheckLibrary)

	// Quality Profiles
	r.Get("/api/quality-profiles", s.handleGetQualityProfiles)
	r.Post("/api/quality-profiles", s.handleCreateQualityProfile)
	r.Put("/api/quality-profiles/{id}", s.handleUpdateQualityProfile)
	r.Delete("/api/quality-profiles/{id}", s.handleDeleteQualityProfile)

	// Blocklist
	r.Get("/api/blocklist", s.handleGetBlocklist)
	r.Post("/api/blocklist", s.handleAddBlocklistEntry)
	r.Delete("/api/blocklist/clear", s.handleClearBlocklist)
	r.Delete("/api/blocklist/{id}", s.handleDeleteBlocklistEntry)

	// Release Profiles
	r.Get("/api/release-profiles", s.handleGetReleaseProfiles)
	r.Post("/api/release-profiles", s.handleCreateReleaseProfile)
	r.Put("/api/release-profiles/{id}", s.handleUpdateReleaseProfile)
	r.Delete("/api/release-profiles/{id}", s.handleDeleteReleaseProfile)

	// Manual Import (scan + import files)
	r.Post("/api/import/scan", s.handleScanImport)
	r.Post("/api/import/files", s.handleImportFiles)

	// Tags
	r.Get("/api/tags", s.handleGetTags)
	r.Post("/api/tags", s.handleCreateTag)
	r.Delete("/api/tags/{id}", s.handleDeleteTag)
	r.Post("/api/library/{id}/tags", s.handleAddItemTag)
	r.Delete("/api/library/{id}/tags/{tagID}", s.handleRemoveItemTag)
	r.Get("/api/library/{id}/tags", s.handleGetItemTags)

	// Backup / Restore
	r.Get("/api/backup", requireAdmin(s.handleBackupDownload))
	r.Post("/api/backup/create", requireAdmin(s.handleBackupCreate))
	r.Get("/api/backup/list", requireAdmin(s.handleBackupList))
	r.Post("/api/restore", requireAdmin(s.handleRestore))

	// Release Calendar
	r.Get("/api/calendar", s.handleCalendar)
	r.Get("/api/calendar/recent", s.handleCalendarRecent)

	// Metadata & enrichment
	s.RegisterMetadataRoutes(r)

	// Metrics
	r.Get("/metrics", s.handleMetrics)

	return r
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]interface{}{"success": false, "error": msg})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if !strings.HasPrefix(r.URL.Path, "/api/downloads") && !strings.HasPrefix(r.URL.Path, "/api/monitor") {
			slog.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start).String())
		}
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index.html not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	platformFilter := r.URL.Query().Get("platform")
	if query == "" {
		writeJSON(w, 200, map[string]interface{}{"results": []interface{}{}, "error": "No query"})
		return
	}

	start := time.Now()
	var allResults []*models.SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Search all sources concurrently
	wg.Add(3)
	go func() {
		defer wg.Done()
		slug := platformFilter
		if slug == "all" {
			slug = ""
		}
		results := search.SearchProwlarr(s.cfg, query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		slug := platformFilter
		if slug == "all" {
			slug = ""
		}
		results := search.SearchMyrient(query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		slug := platformFilter
		if slug == "all" {
			slug = ""
		}
		results := search.SearchVimm(query, slug)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	wg.Wait()

	// Filter torrent results, pass through DDL
	var torrentResults, ddlResults []*models.SearchResult
	for _, r := range allResults {
		if r.SourceType == "torrent" {
			torrentResults = append(torrentResults, r)
		} else {
			ddlResults = append(ddlResults, r)
		}
	}
	filtered := search.FilterGameResults(torrentResults, query)
	var results []*models.SearchResult
	results = append(results, filtered...)
	results = append(results, ddlResults...)
	if results == nil {
		results = []*models.SearchResult{}
	}

	// Filter blocklisted results
	var nonBlocked []*models.SearchResult
	for _, r := range results {
		if !s.mgr.Jobs().IsBlocklisted(r.DownloadURL, r.InfoHash) {
			nonBlocked = append(nonBlocked, r)
		}
	}
	results = nonBlocked
	if results == nil {
		results = []*models.SearchResult{}
	}

	// Apply release profile scoring and filtering
	var profileFiltered []*models.SearchResult
	for _, r := range results {
		adjustment, exclude := s.mgr.Jobs().ApplyReleaseProfiles(r.Title)
		if exclude {
			continue
		}
		r.Score += adjustment
		profileFiltered = append(profileFiltered, r)
	}
	results = profileFiltered
	if results == nil {
		results = []*models.SearchResult{}
	}

	// Apply search scoring
	results = search.ScoreResults(results, query, platformFilter)

	// Apply quality profile source ranking boost
	for _, r := range results {
		boost := s.mgr.Jobs().SourceRankScore(r.Indexer)
		r.Score += boost
	}

	// Sort by score descending
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Cross-reference with library for duplicate detection
	libraryMap := s.mgr.Jobs().GetAllLibraryTitles()
	if libraryMap != nil {
		for _, r := range results {
			key := strings.ToLower(strings.TrimSpace(r.Title)) + "|" + r.PlatformSlug
			if _, found := libraryMap[key]; found {
				r.InLibrary = true
			}
		}
	}

	elapsed := int(time.Since(start).Milliseconds())

	// Source metadata
	sourceMeta := []map[string]interface{}{
		{"name": "prowlarr", "label": "Prowlarr", "color": "#f97316", "source_type": "torrent", "enabled": s.cfg.HasProwlarr()},
		{"name": "myrient", "label": "Myrient", "color": "#10b981", "source_type": "ddl", "enabled": true},
		{"name": "vimm", "label": "Vimm's Lair", "color": "#6366f1", "source_type": "ddl", "enabled": true},
	}

	writeJSON(w, 200, map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        sourceMeta,
	})
}

func (s *Server) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	platforms := []map[string]string{
		{"id": "all", "name": "All Platforms"},
		{"id": "pc", "name": "PC"},
	}
	seen := map[string]bool{"PC": true}

	type sortEntry struct {
		CatID int
		Info  platform.PlatformInfo
	}
	var entries []sortEntry
	for catID, info := range platform.PlatformMap {
		entries = append(entries, sortEntry{catID, info})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CatID < entries[j].CatID })

	for _, e := range entries {
		if e.Info.IsPC || seen[e.Info.Name] || e.Info.Name == "Other" || e.Info.Slug == "" {
			continue
		}
		seen[e.Info.Name] = true
		platforms = append(platforms, map[string]string{"id": e.Info.Slug, "name": e.Info.Name})
	}
	for _, ep := range platform.ExtraPlatforms {
		if !seen[ep.Name] {
			platforms = append(platforms, map[string]string{"id": ep.Slug, "name": ep.Name})
		}
	}
	writeJSON(w, 200, map[string]interface{}{"platforms": platforms})
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	healthData := search.GetAllSourceHealth()
	sourceMeta := []map[string]interface{}{
		{"name": "prowlarr", "label": "Prowlarr", "color": "#f97316", "source_type": "torrent", "enabled": s.cfg.HasProwlarr()},
		{"name": "myrient", "label": "Myrient", "color": "#10b981", "source_type": "ddl", "enabled": true},
		{"name": "vimm", "label": "Vimm's Lair", "color": "#6366f1", "source_type": "ddl", "enabled": true},
	}
	// Attach health data to each source
	for _, src := range sourceMeta {
		name, _ := src["name"].(string)
		if h, ok := healthData[name]; ok {
			src["health"] = h
		}
	}
	writeJSON(w, 200, map[string]interface{}{"sources": sourceMeta})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request body")
		return
	}

	// Check for duplicate in library (warn but don't block)
	var duplicateWarning string
	if existing := s.mgr.Jobs().FindLibraryByTitle(req.Title, req.PlatformSlug); existing != nil {
		duplicateWarning = fmt.Sprintf("Game already exists in library: %s (%s)", existing.Title, existing.Platform)
	}

	if req.SourceType == "ddl" {
		if req.DownloadURL == "" && req.VimmID == "" {
			writeError(w, 400, "No download URL")
			return
		}
		jobID := s.mgr.DownloadDDL(req.DownloadURL, req.VimmID, req.Title, req.Platform, req.PlatformSlug, req.IsPC)
		resp := map[string]interface{}{"success": true, "job_id": jobID}
		if duplicateWarning != "" {
			resp["warning"] = duplicateWarning
		}
		writeJSON(w, 200, resp)
		return
	}

	// NZB / Usenet route
	if req.DownloadProtocol == "nzb" && s.sab != nil {
		nzbURL := req.DownloadURL
		if nzbURL == "" {
			writeError(w, 400, "No NZB URL")
			return
		}
		jobID, err := s.mgr.DownloadNZB(s.sab, nzbURL, req.Title, req.Platform, req.PlatformSlug, req.IsPC)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		resp := map[string]interface{}{"success": true, "job_id": jobID}
		if duplicateWarning != "" {
			resp["warning"] = duplicateWarning
		}
		writeJSON(w, 200, resp)
		return
	}

	// Torrent download
	url := req.DownloadURL
	if url == "" {
		url = req.MagnetURL
	}
	if url == "" && req.InfoHash != "" {
		url = fmt.Sprintf("magnet:?xt=urn:btih:%s", req.InfoHash)
	}

	jobID, err := s.mgr.DownloadTorrent(url, req.Title, req.Platform, req.PlatformSlug, req.IsPC)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	resp := map[string]interface{}{"success": true, "job_id": jobID}
	if duplicateWarning != "" {
		resp["warning"] = duplicateWarning
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	downloads := make([]models.DownloadEntry, 0)

	statusMap := map[string]string{
		"downloading": "downloading", "stalledDL": "stalled",
		"metaDL": "metadata", "forcedDL": "downloading",
		"pausedDL": "paused", "queuedDL": "queued",
		"uploading": "seeding", "stalledUP": "seeding",
		"forcedUP": "seeding", "stoppedUP": "completed",
		"pausedUP": "completed", "queuedUP": "completed",
		"checkingDL": "checking", "checkingUP": "checking",
		"stoppedDL": "paused",
		"error": "error", "missingFiles": "error",
	}

	matchedJobIDs := make(map[string]bool)

	// Active torrents from qBit
	torrents := s.mgr.QB().GetTorrents(s.cfg.QBCategory)
	jobs := s.mgr.Jobs()

	for _, t := range torrents {
		progress := float64(int(t.Progress*1000)) / 10.0
		speed := search.HumanSize(t.DLSpeed) + "/s"

		// Try to match to a job
		var matchedJob struct {
			ID   string
			Data map[string]interface{}
		}
		found := false
		for _, item := range jobs.Items() {
			jTitle, _ := item.Data["title"].(string)
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(jTitle)) ||
				strings.Contains(strings.ToLower(jTitle), strings.ToLower(t.Name)) {
				matchedJob = item
				found = true
				break
			}
		}

		if found {
			matchedJobIDs[matchedJob.ID] = true
			jStatus, _ := matchedJob.Data["status"].(string)
			displayStatus := jStatus
			if jStatus == "downloading" {
				if mapped, ok := statusMap[t.State]; ok {
					displayStatus = mapped
				}
			}
			platf, _ := matchedJob.Data["platform"].(string)
			errMsg, _ := matchedJob.Data["error"].(string)
			detail, _ := matchedJob.Data["detail"].(string)

			downloads = append(downloads, models.DownloadEntry{
				Type:     "job",
				Title:    jTitle(matchedJob.Data),
				Platform: platf,
				Status:   displayStatus,
				JobID:    matchedJob.ID,
				Error:    errMsg,
				Detail:   detail,
				Progress: progress,
				Size:     search.HumanSize(t.TotalSize),
				Speed:    speed,
				ETA:      t.ETA,
				Hash:     t.Hash,
			})
		} else {
			status := t.State
			if mapped, ok := statusMap[t.State]; ok {
				status = mapped
			}
			downloads = append(downloads, models.DownloadEntry{
				Type:     "torrent",
				Title:    t.Name,
				Progress: progress,
				Status:   status,
				Size:     search.HumanSize(t.TotalSize),
				Speed:    speed,
				ETA:      t.ETA,
				Hash:     t.Hash,
			})
		}
	}

	// Unmatched jobs
	for _, item := range jobs.Items() {
		if matchedJobIDs[item.ID] {
			continue
		}
		platf, _ := item.Data["platform"].(string)
		status, _ := item.Data["status"].(string)
		errMsg, _ := item.Data["error"].(string)
		detail, _ := item.Data["detail"].(string)

		downloads = append(downloads, models.DownloadEntry{
			Type:     "job",
			Title:    jTitle(item.Data),
			Platform: platf,
			Status:   status,
			JobID:    item.ID,
			Error:    errMsg,
			Detail:   detail,
		})
	}

	writeJSON(w, 200, map[string]interface{}{"downloads": downloads})
}

func jTitle(data map[string]interface{}) string {
	t, _ := data["title"].(string)
	return t
}

func (s *Server) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	ok := s.mgr.QB().DeleteTorrent(hash, true)
	writeJSON(w, 200, map[string]interface{}{"success": ok})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	if s.mgr.Jobs().Contains(jobID) {
		s.mgr.Jobs().Delete(jobID)
		writeJSON(w, 200, map[string]interface{}{"success": true})
		return
	}
	writeError(w, 404, "Not found")
}

func (s *Server) handleClearFinished(w http.ResponseWriter, r *http.Request) {
	cleared := 0
	for _, item := range s.mgr.Jobs().Items() {
		status, _ := item.Data["status"].(string)
		if status == "completed" || status == "error" {
			s.mgr.Jobs().Delete(item.ID)
			cleared++
		}
	}
	writeJSON(w, 200, map[string]interface{}{"success": true, "cleared": cleared})
}

func (s *Server) handleOrganizeTorrent(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	var req struct {
		PlatformSlug string `json:"platform_slug"`
		IsPC         bool   `json:"is_pc"`
		Platform     string `json:"platform"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	jobID, err := s.mgr.OrganizeTorrent(hash, req.Platform, req.PlatformSlug, req.IsPC)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"success": true, "job_id": jobID})
}

func (s *Server) handleDDLSources(w http.ResponseWriter, r *http.Request) {
	builtIn := []map[string]interface{}{
		{"name": "Myrient", "url": "https://myrient.erista.me/files/", "type": "myrient", "builtin": true,
			"platforms": search.MyrientPlatformSlugs()},
		{"name": "Vimm's Lair", "url": "https://vimm.net/vault/", "type": "vimm", "builtin": true,
			"platforms": search.VimmPlatformSlugs()},
	}
	custom := s.mgr.LoadDDLSources()
	all := builtIn
	for _, c := range custom {
		all = append(all, c)
	}
	writeJSON(w, 200, map[string]interface{}{"sources": all})
}

func (s *Server) handleAddDDLSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Name == "" || req.URL == "" {
		writeError(w, 400, "Name and URL required")
		return
	}
	sources := s.mgr.LoadDDLSources()
	sources = append(sources, map[string]interface{}{"name": req.Name, "url": req.URL, "type": "custom"})
	s.mgr.SaveDDLSources(sources)
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

func (s *Server) handleDeleteDDLSource(w http.ResponseWriter, r *http.Request) {
	idx, _ := strconv.Atoi(chi.URLParam(r, "idx"))
	sources := s.mgr.LoadDDLSources()
	if idx < 0 || idx >= len(sources) {
		writeError(w, 404, "Not found")
		return
	}
	sources = append(sources[:idx], sources[idx+1:]...)
	s.mgr.SaveDDLSources(sources)
	writeJSON(w, 200, map[string]interface{}{"success": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.mgr.LoadSettings()
	writeJSON(w, 200, settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]interface{}
	json.NewDecoder(r.Body).Decode(&req)
	settings := s.mgr.LoadSettings()
	if v, ok := req["extract_archives"].(bool); ok {
		settings.ExtractArchives = v
	}
	s.mgr.SaveSettings(settings)
	writeJSON(w, 200, settings)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Job status counts
	items := s.mgr.Jobs().Items()
	byStatus := make(map[string]int)
	for _, item := range items {
		status, _ := item.Data["status"].(string)
		byStatus[status]++
	}

	// Library stats (from library_items table)
	libStats := s.mgr.Jobs().LibraryStats()
	libTotal := s.mgr.Jobs().LibraryTotal()
	recentItems := s.mgr.Jobs().RecentLibraryItems(10)

	var recent []map[string]interface{}
	for _, item := range recentItems {
		recent = append(recent, map[string]interface{}{
			"title":         item.Title,
			"platform":      item.Platform,
			"platform_slug": item.PlatformSlug,
			"added_at":      item.AddedAt,
			"file_size":     item.FileSize,
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"platforms":     libStats,
		"by_status":     byStatus,
		"library_total": libTotal,
		"total_jobs":    len(items),
		"recent":        recent,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "version": "2.0.0"})
}

func (s *Server) handleMonitorStatus(w http.ResponseWriter, r *http.Request) {
	if s.mon == nil {
		writeJSON(w, 200, map[string]interface{}{"enabled": false, "error": "Monitor not initialized"})
		return
	}
	writeJSON(w, 200, s.mon.GetStatus())
}

func (s *Server) handleMonitorAnalyze(w http.ResponseWriter, r *http.Request) {
	if s.mon == nil {
		writeError(w, 500, "Monitor not initialized")
		return
	}
	s.mon.TriggerManual()
	time.Sleep(100 * time.Millisecond)
	status := s.mon.GetStatus()
	writeJSON(w, 200, map[string]interface{}{"success": true, "enabled": status.Enabled, "diagnosis": status.Diagnosis,
		"provider": status.Provider, "model": status.Model, "auto_fix": status.AutoFix,
		"interval": status.Interval, "last_checked": status.LastChecked,
		"recent_errors": status.RecentErrors, "pending_actions": status.PendingActions,
		"action_history": status.ActionHistory,
	})
}

func (s *Server) handleMonitorApprove(w http.ResponseWriter, r *http.Request) {
	if s.mon == nil {
		writeError(w, 500, "Monitor not initialized")
		return
	}
	actionID := chi.URLParam(r, "actionID")
	ok, msg := s.mon.ExecuteApproved(actionID)
	writeJSON(w, 200, map[string]interface{}{"success": ok, "message": msg})
}

func (s *Server) handleMonitorDismiss(w http.ResponseWriter, r *http.Request) {
	if s.mon == nil {
		writeError(w, 500, "Monitor not initialized")
		return
	}
	actionID := chi.URLParam(r, "actionID")
	dismissed := s.mon.ActionQueue().Dismiss(actionID)
	writeJSON(w, 200, map[string]interface{}{"success": dismissed})
}
