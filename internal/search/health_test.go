package search

import (
	"testing"
	"time"
)

// resetHealthStore clears the global health store between tests.
func resetHealthStore() {
	healthMu.Lock()
	defer healthMu.Unlock()
	healthStore = make(map[string]*SourceHealth)
}

func TestRecordSearchSuccess(t *testing.T) {
	resetHealthStore()

	RecordSearchSuccess("test-source")
	h := GetSourceHealth("test-source")
	if h == nil {
		t.Fatal("expected health to exist")
	}
	if h.SearchOK != 1 {
		t.Errorf("SearchOK=%d, want 1", h.SearchOK)
	}
	if h.Score < 100 {
		// Initial 100 + 5, clamped to 100
		t.Errorf("Score=%d, want 100 (clamped)", h.Score)
	}

	// Record another success to verify increment
	RecordSearchSuccess("test-source")
	h = GetSourceHealth("test-source")
	if h.SearchOK != 2 {
		t.Errorf("SearchOK=%d, want 2", h.SearchOK)
	}
}

func TestRecordSearchFail(t *testing.T) {
	resetHealthStore()

	RecordSearchFail("fail-source", "connection refused")
	h := GetSourceHealth("fail-source")
	if h == nil {
		t.Fatal("expected health to exist")
	}
	if h.SearchFail != 1 {
		t.Errorf("SearchFail=%d, want 1", h.SearchFail)
	}
	// Initial 100 - 10 = 90
	if h.Score != 90 {
		t.Errorf("Score=%d, want 90", h.Score)
	}
	if h.LastError != "connection refused" {
		t.Errorf("LastError=%q, want 'connection refused'", h.LastError)
	}
}

func TestRecordSearchFail_ScoreClampAtZero(t *testing.T) {
	resetHealthStore()

	// 11 failures: 100 - 110 = clamped to 0
	for i := 0; i < 11; i++ {
		RecordSearchFail("zero-source", "error")
	}
	h := GetSourceHealth("zero-source")
	if h.Score != 0 {
		t.Errorf("Score=%d, want 0 (clamped)", h.Score)
	}
}

func TestRecordSearchSuccess_ScoreClampAt100(t *testing.T) {
	resetHealthStore()

	// Start with a success (100 + 5 clamped to 100)
	RecordSearchSuccess("clamp-source")
	RecordSearchSuccess("clamp-source")
	RecordSearchSuccess("clamp-source")
	h := GetSourceHealth("clamp-source")
	if h.Score != 100 {
		t.Errorf("Score=%d, want 100 (clamped)", h.Score)
	}
}

func TestCircuitBreaker_Opens(t *testing.T) {
	resetHealthStore()

	// 3 consecutive failures should open circuit
	for i := 0; i < 3; i++ {
		RecordSearchFail("circuit-source", "timeout")
	}

	if !IsCircuitOpen("circuit-source") {
		t.Error("expected circuit to be open after 3 failures")
	}
}

func TestCircuitBreaker_NotOpenBeforeThreshold(t *testing.T) {
	resetHealthStore()

	RecordSearchFail("threshold-source", "err")
	RecordSearchFail("threshold-source", "err")

	if IsCircuitOpen("threshold-source") {
		t.Error("circuit should not be open after only 2 failures")
	}
}

func TestCircuitBreaker_Closes(t *testing.T) {
	resetHealthStore()

	// Open the circuit
	for i := 0; i < 3; i++ {
		RecordSearchFail("retry-source", "error")
	}
	if !IsCircuitOpen("retry-source") {
		t.Fatal("expected circuit to be open")
	}

	// Simulate circuit retry period passing by manipulating circuitOpenUntil
	healthMu.Lock()
	healthStore["retry-source"].circuitOpenUntil = float64(time.Now().Unix()) - 1
	healthMu.Unlock()

	if IsCircuitOpen("retry-source") {
		t.Error("circuit should be closed after retry period")
	}
}

func TestCircuitBreaker_ClosesOnSuccess(t *testing.T) {
	resetHealthStore()

	// Open the circuit
	for i := 0; i < 3; i++ {
		RecordSearchFail("recover-source", "error")
	}
	if !IsCircuitOpen("recover-source") {
		t.Fatal("expected circuit to be open")
	}

	// Success should close it
	RecordSearchSuccess("recover-source")
	if IsCircuitOpen("recover-source") {
		t.Error("circuit should close on success")
	}
}

func TestResetCircuit(t *testing.T) {
	resetHealthStore()

	// Open the circuit
	for i := 0; i < 3; i++ {
		RecordSearchFail("reset-source", "error")
	}
	if !IsCircuitOpen("reset-source") {
		t.Fatal("expected circuit to be open")
	}

	ok := ResetCircuit("reset-source")
	if !ok {
		t.Error("ResetCircuit should return true for existing source")
	}
	if IsCircuitOpen("reset-source") {
		t.Error("circuit should be closed after reset")
	}

	h := GetSourceHealth("reset-source")
	if h.Score != 100 {
		t.Errorf("Score=%d after reset, want 100", h.Score)
	}
}

func TestResetCircuit_NonExistent(t *testing.T) {
	resetHealthStore()

	ok := ResetCircuit("nonexistent")
	if ok {
		t.Error("ResetCircuit should return false for nonexistent source")
	}
}

func TestIsCircuitOpen_NonExistent(t *testing.T) {
	resetHealthStore()

	if IsCircuitOpen("missing") {
		t.Error("IsCircuitOpen should return false for nonexistent source")
	}
}

func TestGetAllSourceHealth(t *testing.T) {
	resetHealthStore()

	RecordSearchSuccess("source-a")
	RecordSearchFail("source-b", "error")

	all := GetAllSourceHealth()
	if len(all) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(all))
	}
	if _, ok := all["source-a"]; !ok {
		t.Error("expected source-a in health map")
	}
	if _, ok := all["source-b"]; !ok {
		t.Error("expected source-b in health map")
	}
}

func TestGetAllSourceHealth_Empty(t *testing.T) {
	resetHealthStore()

	all := GetAllSourceHealth()
	if len(all) != 0 {
		t.Errorf("expected 0 sources, got %d", len(all))
	}
}

func TestGetSourceHealth_NonExistent(t *testing.T) {
	resetHealthStore()

	h := GetSourceHealth("missing")
	if h != nil {
		t.Error("expected nil for nonexistent source")
	}
}

func TestRecordDownloadSuccess(t *testing.T) {
	resetHealthStore()

	RecordDownloadSuccess("dl-source")
	h := GetSourceHealth("dl-source")
	if h.DownloadOK != 1 {
		t.Errorf("DownloadOK=%d, want 1", h.DownloadOK)
	}
}

func TestRecordDownloadFail(t *testing.T) {
	resetHealthStore()

	RecordDownloadFail("dl-fail", "404 not found")
	h := GetSourceHealth("dl-fail")
	if h.DownloadFail != 1 {
		t.Errorf("DownloadFail=%d, want 1", h.DownloadFail)
	}
	if h.LastError != "404 not found" {
		t.Errorf("LastError=%q, want '404 not found'", h.LastError)
	}
	if h.Score != 90 {
		t.Errorf("Score=%d, want 90", h.Score)
	}
}

func TestLastErrorTruncated(t *testing.T) {
	resetHealthStore()

	longError := ""
	for i := 0; i < 500; i++ {
		longError += "x"
	}
	RecordSearchFail("truncate-source", longError)
	h := GetSourceHealth("truncate-source")
	if len(h.LastError) > 400 {
		t.Errorf("LastError length=%d, want <= 400", len(h.LastError))
	}
}

func TestSnapshotHealth_CircuitRetryInSec(t *testing.T) {
	resetHealthStore()

	// Open circuit
	for i := 0; i < 3; i++ {
		RecordSearchFail("snap-source", "err")
	}

	h := GetSourceHealth("snap-source")
	if !h.CircuitOpen {
		t.Error("expected CircuitOpen=true in snapshot")
	}
	if h.CircuitRetryInSec <= 0 {
		t.Errorf("CircuitRetryInSec=%d, want >0", h.CircuitRetryInSec)
	}
}
