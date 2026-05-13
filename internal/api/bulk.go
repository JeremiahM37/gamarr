package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// bulkJobRequest accepts an explicit job-id list. When the list is empty the
// handler treats it as "all jobs matching the natural filter for this op"
// (failed for retry, active/downloading for cancel).
type bulkJobRequest struct {
	JobIDs []string `json:"job_ids"`
}

// jobFailedStatuses are job.status values treated as "failed" for the retry
// bulk op. Mirrors the per-job retry handler's behavior.
var jobFailedStatuses = map[string]bool{
	"failed":      true,
	"error":       true,
	"dead_letter": true,
}

// jobActiveStatuses are job.status values treated as "active/cancellable" for
// the cancel bulk op.
var jobActiveStatuses = map[string]bool{
	"downloading": true,
	"stalled":     true,
	"queued":      true,
	"searching":   true,
	"pending":     true,
}

// handleBulkRetry retries multiple jobs in a single call.
//
//	POST /api/admin/bulk/retry
//	  body: {"job_ids": ["abc", "def"]}     -> retry just those
//	  body: {} or empty body                 -> retry ALL failed jobs
//
// Returns per-id outcomes so AI/UI clients can show partial successes.
func (s *Server) handleBulkRetry(w http.ResponseWriter, r *http.Request) {
	req, err := readBulkJobRequest(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	targets := req.JobIDs
	if len(targets) == 0 {
		// Fan out to every failed job.
		for _, item := range s.mgr.Jobs().Items() {
			status, _ := item.Data["status"].(string)
			if jobFailedStatuses[strings.ToLower(status)] {
				targets = append(targets, item.ID)
			}
		}
	}

	results := make([]map[string]interface{}, 0, len(targets))
	succeeded := 0
	for _, id := range targets {
		ok, msg := s.mgr.RetryJob(id)
		if ok {
			succeeded++
		}
		results = append(results, map[string]interface{}{
			"job_id":  id,
			"success": ok,
			"message": msg,
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"success":   true,
		"requested": len(targets),
		"succeeded": succeeded,
		"results":   results,
	})
}

// handleBulkCancel cancels multiple jobs in a single call.
//
//	POST /api/admin/bulk/cancel
//	  body: {"job_ids": ["abc", "def"]}     -> cancel just those
//	  body: {} or empty body                 -> cancel ALL active jobs
//
// Returns per-id outcomes.
func (s *Server) handleBulkCancel(w http.ResponseWriter, r *http.Request) {
	req, err := readBulkJobRequest(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}

	targets := req.JobIDs
	if len(targets) == 0 {
		// Fan out to every active job.
		for _, item := range s.mgr.Jobs().Items() {
			status, _ := item.Data["status"].(string)
			if jobActiveStatuses[strings.ToLower(status)] {
				targets = append(targets, item.ID)
			}
		}
	}

	results := make([]map[string]interface{}, 0, len(targets))
	succeeded := 0
	for _, id := range targets {
		_, exists := s.mgr.Jobs().Get(id)
		if !exists {
			results = append(results, map[string]interface{}{
				"job_id":  id,
				"success": false,
				"message": "not found",
			})
			continue
		}
		s.mgr.Jobs().Delete(id)
		succeeded++
		results = append(results, map[string]interface{}{
			"job_id":  id,
			"success": true,
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"success":   true,
		"requested": len(targets),
		"succeeded": succeeded,
		"results":   results,
	})
}

// bulkWishlistRequest deletes many wishlist items at once.
type bulkWishlistRequest struct {
	IDs []int64 `json:"ids"`
}

// handleBulkDeleteWishlist removes multiple wishlist entries in one call.
//
//	POST /api/wishlist/bulk-delete
//	  body: {"ids": [1, 2, 3]}
//
// Per-id results allow AI/UI clients to show which deletions succeeded.
func (s *Server) handleBulkDeleteWishlist(w http.ResponseWriter, r *http.Request) {
	if !requireContentTypeJSON(w, r) {
		return
	}
	var req bulkWishlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "Invalid request body")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, 400, "ids is required and must be non-empty")
		return
	}
	if len(req.IDs) > 500 {
		writeError(w, 400, "Too many ids (limit 500 per request)")
		return
	}

	results := make([]map[string]interface{}, 0, len(req.IDs))
	succeeded := 0
	for _, id := range req.IDs {
		if id <= 0 {
			results = append(results, map[string]interface{}{
				"id":      id,
				"success": false,
				"message": "invalid id",
			})
			continue
		}
		if err := s.mgr.Jobs().DeleteWishlistItem(id); err != nil {
			results = append(results, map[string]interface{}{
				"id":      id,
				"success": false,
				"message": err.Error(),
			})
			continue
		}
		succeeded++
		results = append(results, map[string]interface{}{
			"id":      id,
			"success": true,
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"success":   true,
		"requested": len(req.IDs),
		"succeeded": succeeded,
		"results":   results,
	})
}

// readBulkJobRequest parses (and tolerates an empty/missing body — that case
// means "operate on all matching jobs") while still rejecting wrong content-
// types or malformed JSON.
func readBulkJobRequest(r *http.Request) (bulkJobRequest, error) {
	var req bulkJobRequest
	if r.ContentLength == 0 {
		return req, nil
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		base := ct
		if idx := strings.Index(base, ";"); idx >= 0 {
			base = base[:idx]
		}
		base = strings.TrimSpace(strings.ToLower(base))
		if base != "application/json" {
			return req, errInvalidContentType
		}
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, errInvalidBody
	}
	if len(req.JobIDs) > 500 {
		return req, errTooManyIDs
	}
	return req, nil
}

func requireContentTypeJSON(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		writeJSON(w, 415, map[string]interface{}{"success": false, "error": "Content-Type required (application/json)"})
		return false
	}
	base := ct
	if idx := strings.Index(base, ";"); idx >= 0 {
		base = base[:idx]
	}
	base = strings.TrimSpace(strings.ToLower(base))
	if base != "application/json" {
		writeJSON(w, 415, map[string]interface{}{"success": false, "error": "Content-Type must be application/json"})
		return false
	}
	return true
}

// Sentinel errors so the handler can render the right status code.
type bulkError struct{ msg string }

func (e bulkError) Error() string { return e.msg }

var (
	errInvalidContentType = bulkError{msg: "Content-Type must be application/json"}
	errInvalidBody        = bulkError{msg: "Invalid request body"}
	errTooManyIDs         = bulkError{msg: "Too many job_ids (limit 500 per request)"}
)
