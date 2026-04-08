package search

import (
	"log/slog"
	"sync"
	"time"
)

// SourceHealth tracks health metrics for a search source.
type SourceHealth struct {
	Name             string  `json:"name"`
	SearchOK         int     `json:"search_ok"`
	SearchFail       int     `json:"search_fail"`
	DownloadOK       int     `json:"download_ok"`
	DownloadFail     int     `json:"download_fail"`
	LastError        string  `json:"last_error"`
	LastSuccessAt    float64 `json:"last_success_at"`
	Score            int     `json:"score"`
	CircuitOpen      bool    `json:"circuit_open"`
	CircuitRetryInSec int    `json:"circuit_retry_in_sec"`

	// internal
	searchFailStreak int
	circuitOpenUntil float64
}

var (
	healthMu    sync.RWMutex
	healthStore = make(map[string]*SourceHealth)

	circuitThreshold  = 3  // consecutive failures to open circuit
	circuitRetrySec   = 60 // seconds before retry after circuit opens
)

func getOrCreateHealth(name string) *SourceHealth {
	h, ok := healthStore[name]
	if !ok {
		h = &SourceHealth{Name: name, Score: 100}
		healthStore[name] = h
	}
	return h
}

// RecordSearchSuccess records a successful search for a source.
func RecordSearchSuccess(name string) {
	healthMu.Lock()
	defer healthMu.Unlock()
	h := getOrCreateHealth(name)
	h.SearchOK++
	h.searchFailStreak = 0
	h.LastSuccessAt = float64(time.Now().Unix())

	// Close circuit on success
	wasOpen := float64(time.Now().Unix()) < h.circuitOpenUntil
	h.circuitOpenUntil = 0

	// Adjust score: +5 per success, clamped 0-100
	h.Score += 5
	if h.Score > 100 {
		h.Score = 100
	}

	if wasOpen {
		slog.Info("source recovered", "source", name)
	}
}

// RecordSearchFail records a failed search for a source.
func RecordSearchFail(name string, errMsg string) {
	healthMu.Lock()
	defer healthMu.Unlock()
	h := getOrCreateHealth(name)
	h.SearchFail++
	h.searchFailStreak++
	h.LastError = errMsg
	if len(h.LastError) > 400 {
		h.LastError = h.LastError[:400]
	}

	// Adjust score: -10 per failure, clamped 0-100
	h.Score -= 10
	if h.Score < 0 {
		h.Score = 0
	}

	// Open circuit after threshold consecutive failures
	if h.searchFailStreak >= circuitThreshold {
		now := float64(time.Now().Unix())
		h.circuitOpenUntil = now + float64(circuitRetrySec)
		slog.Warn("source circuit opened", "source", name, "streak", h.searchFailStreak)
	}
}

// RecordDownloadSuccess records a successful download for a source.
func RecordDownloadSuccess(name string) {
	healthMu.Lock()
	defer healthMu.Unlock()
	h := getOrCreateHealth(name)
	h.DownloadOK++
	h.LastSuccessAt = float64(time.Now().Unix())
	h.Score += 5
	if h.Score > 100 {
		h.Score = 100
	}
}

// RecordDownloadFail records a failed download for a source.
func RecordDownloadFail(name string, errMsg string) {
	healthMu.Lock()
	defer healthMu.Unlock()
	h := getOrCreateHealth(name)
	h.DownloadFail++
	h.LastError = errMsg
	if len(h.LastError) > 400 {
		h.LastError = h.LastError[:400]
	}
	h.Score -= 10
	if h.Score < 0 {
		h.Score = 0
	}
}

// IsCircuitOpen returns true if the source's circuit breaker is open.
func IsCircuitOpen(name string) bool {
	healthMu.RLock()
	defer healthMu.RUnlock()
	h, ok := healthStore[name]
	if !ok {
		return false
	}
	return float64(time.Now().Unix()) < h.circuitOpenUntil
}

// ResetCircuit resets a source's circuit breaker and failure streak.
func ResetCircuit(name string) bool {
	healthMu.Lock()
	defer healthMu.Unlock()
	h, ok := healthStore[name]
	if !ok {
		return false
	}
	h.circuitOpenUntil = 0
	h.searchFailStreak = 0
	h.Score = 100
	slog.Info("source circuit reset", "source", name)
	return true
}

// GetSourceHealth returns health data for a single source.
func GetSourceHealth(name string) *SourceHealth {
	healthMu.RLock()
	defer healthMu.RUnlock()
	h, ok := healthStore[name]
	if !ok {
		return nil
	}
	return snapshotHealth(h)
}

// GetAllSourceHealth returns health data for all tracked sources.
func GetAllSourceHealth() map[string]*SourceHealth {
	healthMu.RLock()
	defer healthMu.RUnlock()
	out := make(map[string]*SourceHealth, len(healthStore))
	for name, h := range healthStore {
		out[name] = snapshotHealth(h)
	}
	return out
}

func snapshotHealth(h *SourceHealth) *SourceHealth {
	now := float64(time.Now().Unix())
	circuitOpen := now < h.circuitOpenUntil
	retryIn := 0
	if circuitOpen {
		retryIn = int(h.circuitOpenUntil - now)
	}
	return &SourceHealth{
		Name:              h.Name,
		SearchOK:          h.SearchOK,
		SearchFail:        h.SearchFail,
		DownloadOK:        h.DownloadOK,
		DownloadFail:      h.DownloadFail,
		LastError:         h.LastError,
		LastSuccessAt:     h.LastSuccessAt,
		Score:             h.Score,
		CircuitOpen:       circuitOpen,
		CircuitRetryInSec: retryIn,
	}
}
