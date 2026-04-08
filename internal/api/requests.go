package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"gamarr/internal/db"
	"gamarr/internal/models"
	"gamarr/internal/search"
)

// handleCreateRequest handles POST /api/requests.
func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title        string `json:"title"`
		Platform     string `json:"platform"`
		PlatformSlug string `json:"platform_slug"`
		Notes        string `json:"notes"`
		CoverURL     string `json:"cover_url"`
		Year         string `json:"year"`
		Genre        string `json:"genre"`
		UserID       string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "Title is required")
		return
	}

	userID := req.UserID
	if userID == "" {
		userID = "default"
	}

	now := time.Now()
	gr := &models.GameRequest{
		ID:           db.GenerateRequestID(),
		UserID:       userID,
		Title:        req.Title,
		Platform:     req.Platform,
		PlatformSlug: req.PlatformSlug,
		Status:       models.RequestStatusPending,
		Notes:        req.Notes,
		CoverURL:     req.CoverURL,
		Year:         req.Year,
		Genre:        req.Genre,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.mgr.Jobs().CreateRequest(gr); err != nil {
		slog.Error("failed to create request", "error", err)
		writeError(w, http.StatusInternalServerError, "Failed to create request")
		return
	}

	s.mgr.Jobs().LogActivity("request_created", gr.Title,
		fmt.Sprintf("Request for %s (%s)", gr.Title, gr.Platform), "", nil)
	slog.Info("request created", "id", gr.ID, "title", gr.Title, "platform", gr.Platform)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"request": gr,
	})
}

// handleListRequests handles GET /api/requests.
func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	userID := r.URL.Query().Get("user_id")

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 200 {
			limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	requests, err := s.mgr.Jobs().ListRequests(userID, status, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to list requests")
		return
	}
	if requests == nil {
		requests = []models.GameRequest{}
	}

	total, _ := s.mgr.Jobs().CountRequests(userID, status)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"requests": requests,
		"total":    total,
	})
}

// handleGetRequest handles GET /api/requests/{id}.
func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	req, err := s.mgr.Jobs().GetRequest(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Request not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"request": req,
	})
}

// handleUpdateRequest handles PATCH /api/requests/{id}.
func (s *Server) handleUpdateRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	req, err := s.mgr.Jobs().GetRequest(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Request not found")
		return
	}

	var body struct {
		Status     *string `json:"status"`
		AdminNotes *string `json:"admin_notes"`
		Notes      *string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if body.Status != nil {
		newStatus := *body.Status
		switch newStatus {
		case models.RequestStatusApproved:
			if req.Status != models.RequestStatusPending && req.Status != models.RequestStatusFailed {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("Cannot approve request in status '%s'", req.Status))
				return
			}
			req.Status = models.RequestStatusApproved

			// Notify the requester.
			s.createNotification(req.UserID, models.NotifTypeRequestApproved, req.Title,
				fmt.Sprintf("Your request for \"%s\" has been approved.", req.Title))

		case models.RequestStatusCancelled:
			if req.Status == models.RequestStatusCompleted || req.Status == models.RequestStatusCancelled {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("Cannot cancel request in status '%s'", req.Status))
				return
			}
			req.Status = models.RequestStatusCancelled

		case models.RequestStatusFailed, models.RequestStatusPending:
			req.Status = newStatus

		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Invalid status: %s", newStatus))
			return
		}
	}

	if body.AdminNotes != nil {
		req.AdminNotes = *body.AdminNotes
	}
	if body.Notes != nil {
		req.Notes = *body.Notes
	}

	req.UpdatedAt = time.Now()
	if err := s.mgr.Jobs().UpdateRequest(req); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to update request")
		return
	}

	slog.Info("request updated", "id", req.ID, "status", req.Status)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"request": req,
	})
}

// handleDeleteRequest handles DELETE /api/requests/{id}.
func (s *Server) handleDeleteRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.Jobs().DeleteRequest(id); err != nil {
		writeError(w, http.StatusNotFound, "Request not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleSearchRequest handles POST /api/requests/{id}/search.
// Triggers a search for the request's game across all sources.
func (s *Server) handleSearchRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	req, err := s.mgr.Jobs().GetRequest(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Request not found")
		return
	}

	query := req.Title
	platformFilter := req.PlatformSlug

	var allResults []*models.SearchResult
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		results := search.SearchProwlarr(s.cfg, query, platformFilter)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		results := search.SearchMyrient(query, platformFilter)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		results := search.SearchVimm(query, platformFilter)
		mu.Lock()
		allResults = append(allResults, results...)
		mu.Unlock()
	}()
	wg.Wait()

	// Filter and sort
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

	// Update request status to searching
	req.Status = models.RequestStatusSearching
	req.UpdatedAt = time.Now()
	_ = s.mgr.Jobs().UpdateRequest(req)

	slog.Info("search for request", "id", req.ID, "query", query, "results", len(results))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"results": results,
		"request": req,
	})
}

// handleDownloadForRequest handles POST /api/requests/{id}/download.
// Downloads a specific search result for this request.
func (s *Server) handleDownloadForRequest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	req, err := s.mgr.Jobs().GetRequest(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Request not found")
		return
	}

	var body models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Use the request's platform info if not specified in the download body.
	if body.Platform == "" {
		body.Platform = req.Platform
	}
	if body.PlatformSlug == "" {
		body.PlatformSlug = req.PlatformSlug
	}
	if body.Title == "" {
		body.Title = req.Title
	}

	// Update request status.
	req.Status = models.RequestStatusDownloading
	req.UpdatedAt = time.Now()
	_ = s.mgr.Jobs().UpdateRequest(req)

	var jobID string

	if body.SourceType == "ddl" {
		if body.DownloadURL == "" && body.VimmID == "" {
			writeError(w, http.StatusBadRequest, "No download URL")
			return
		}
		jobID = s.mgr.DownloadDDL(body.DownloadURL, body.VimmID, body.Title,
			body.Platform, body.PlatformSlug, body.IsPC)
	} else if body.DownloadProtocol == "nzb" && s.sab != nil {
		if body.DownloadURL == "" {
			writeError(w, http.StatusBadRequest, "No NZB URL")
			return
		}
		var dlErr error
		jobID, dlErr = s.mgr.DownloadNZB(s.sab, body.DownloadURL, body.Title,
			body.Platform, body.PlatformSlug, body.IsPC)
		if dlErr != nil {
			writeError(w, http.StatusBadRequest, dlErr.Error())
			return
		}
	} else {
		// Torrent download
		url := body.DownloadURL
		if url == "" {
			url = body.MagnetURL
		}
		if url == "" && body.InfoHash != "" {
			url = fmt.Sprintf("magnet:?xt=urn:btih:%s", body.InfoHash)
		}
		var dlErr error
		jobID, dlErr = s.mgr.DownloadTorrent(url, body.Title,
			body.Platform, body.PlatformSlug, body.IsPC)
		if dlErr != nil {
			writeError(w, http.StatusBadRequest, dlErr.Error())
			return
		}
	}

	// Watch this job in the background to update request status and notify on completion.
	go s.watchRequestJob(req, jobID)

	slog.Info("download started for request", "request_id", req.ID, "job_id", jobID, "title", body.Title)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":    true,
		"job_id":     jobID,
		"request_id": req.ID,
	})
}

// watchRequestJob polls a job until completion, then updates the request and sends notifications.
func (s *Server) watchRequestJob(req *models.GameRequest, jobID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	timeout := time.After(7 * 24 * time.Hour)

	for {
		select {
		case <-ticker.C:
			job, ok := s.mgr.Jobs().Get(jobID)
			if !ok {
				continue
			}
			status, _ := job["status"].(string)
			switch status {
			case "completed":
				s.completeRequest(req)
				return
			case "error":
				errMsg, _ := job["error"].(string)
				s.failRequest(req, errMsg)
				return
			}
		case <-timeout:
			s.failRequest(req, "Download timed out")
			return
		}
	}
}

// completeRequest marks a request as completed and notifies the user.
func (s *Server) completeRequest(req *models.GameRequest) {
	req.Status = models.RequestStatusCompleted
	req.UpdatedAt = time.Now()
	_ = s.mgr.Jobs().UpdateRequest(req)

	s.createNotification(req.UserID, models.NotifTypeRequestCompleted, req.Title,
		fmt.Sprintf("Your request for \"%s\" has been downloaded and is ready!", req.Title))

	s.mgr.Jobs().LogActivity("request_completed", req.Title,
		fmt.Sprintf("Request completed for %s", req.Title), "", nil)
	slog.Info("request completed", "id", req.ID, "title", req.Title)
}

// failRequest marks a request as failed and notifies the user.
func (s *Server) failRequest(req *models.GameRequest, reason string) {
	if reason == "" {
		reason = "Unknown error"
	}
	req.Status = models.RequestStatusFailed
	req.AdminNotes = reason
	req.UpdatedAt = time.Now()
	_ = s.mgr.Jobs().UpdateRequest(req)

	s.createNotification(req.UserID, models.NotifTypeRequestFailed, req.Title,
		fmt.Sprintf("Your request for \"%s\" failed: %s", req.Title, reason))

	slog.Warn("request failed", "id", req.ID, "title", req.Title, "reason", reason)
}

// createNotification is a helper to create a notification and log errors.
func (s *Server) createNotification(userID, notifType, title, message string) {
	n := &models.Notification{
		UserID:    userID,
		Type:      notifType,
		Title:     title,
		Message:   message,
		CreatedAt: time.Now(),
	}
	if _, err := s.mgr.Jobs().CreateNotification(n); err != nil {
		slog.Error("failed to create notification", "error", err, "type", notifType, "user_id", userID)
	}

	// Also call the external notify callback if set.
	if s.mgr.NotifyFunc != nil {
		s.mgr.NotifyFunc(userID, notifType, title, message)
	}
}

// Ensure search imports are used.
var _ = strings.ToLower
