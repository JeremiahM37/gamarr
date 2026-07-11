package monitor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gamarr/internal/config"
	"gamarr/internal/models"
)

func TestErrorCapture(t *testing.T) {
	ec := NewErrorCapture(5)

	ec.Add("WARN", "test", "warning 1")
	ec.Add("ERROR", "test", "error 1")
	ec.Add("WARN", "test", "warning 2")

	entries := ec.Recent(10)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Level != "WARN" {
		t.Errorf("first entry level=%q", entries[0].Level)
	}
}

func TestErrorCapture_MaxLen(t *testing.T) {
	ec := NewErrorCapture(3)

	for i := 0; i < 5; i++ {
		ec.Add("WARN", "test", "msg")
	}

	entries := ec.Recent(10)
	if len(entries) != 3 {
		t.Errorf("expected max 3 entries, got %d", len(entries))
	}
}

func TestErrorCapture_RecentLimit(t *testing.T) {
	ec := NewErrorCapture(100)
	for i := 0; i < 10; i++ {
		ec.Add("WARN", "test", "msg")
	}

	entries := ec.Recent(3)
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
}

func TestActionQueue_AddAndList(t *testing.T) {
	q := NewActionQueue()

	id := q.Add("retry_job", map[string]interface{}{"job_id": "j1"}, "Retry job j1", "safe")
	if id == "" {
		t.Error("expected non-empty ID")
	}

	pending := q.ListPending()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].Action != "retry_job" {
		t.Errorf("action=%q", pending[0].Action)
	}
}

func TestActionQueue_Complete(t *testing.T) {
	q := NewActionQueue()
	id := q.Add("retry_job", nil, "test", "safe")

	q.Complete(id, "Job re-queued")

	if len(q.ListPending()) != 0 {
		t.Error("expected 0 pending after complete")
	}
	history := q.ListHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 in history, got %d", len(history))
	}
	if history[0].Result != "Job re-queued" {
		t.Errorf("result=%q", history[0].Result)
	}
}

func TestActionQueue_Dismiss(t *testing.T) {
	q := NewActionQueue()
	id := q.Add("restart_qbittorrent", nil, "test", "approval")

	ok := q.Dismiss(id)
	if !ok {
		t.Error("expected dismiss to succeed")
	}

	if len(q.ListPending()) != 0 {
		t.Error("expected 0 pending after dismiss")
	}
	history := q.ListHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 in history")
	}
	if history[0].Result != "dismissed" {
		t.Errorf("result=%q", history[0].Result)
	}
}

func TestActionQueue_DismissNonExistent(t *testing.T) {
	q := NewActionQueue()
	ok := q.Dismiss("nonexistent")
	if ok {
		t.Error("expected false for non-existent action")
	}
}

func TestActionQueue_HistoryLimit(t *testing.T) {
	q := NewActionQueue()
	for i := 0; i < 25; i++ {
		id := q.Add("retry_job", nil, "test", "safe")
		q.Complete(id, "done")
	}
	if len(q.ListHistory()) > 20 {
		t.Errorf("history should be capped at 20, got %d", len(q.ListHistory()))
	}
}

func TestParseJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "plain JSON",
			input: `{"diagnosis": "All nominal", "actions": []}`,
		},
		{
			name:  "markdown wrapped",
			input: "```json\n{\"diagnosis\": \"test\", \"actions\": []}\n```",
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:  "with whitespace",
			input: "  \n{\"diagnosis\": \"test\"}\n  ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseJSON(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result["diagnosis"] == nil {
				t.Error("expected diagnosis field")
			}
		})
	}
}

func TestGamarrMonitor_GetStatus(t *testing.T) {
	cfg := &config.Config{
		AIMonitorEnabled:  true,
		AIProvider:        "ollama",
		AIModel:           "test-model",
		AIAutoFix:         true,
		AIMonitorInterval: 300,
	}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return nil
		},
	}

	mon := New(cfg, cb)
	status := mon.GetStatus()

	if !status.Enabled {
		t.Error("expected enabled=true")
	}
	if status.Provider != "ollama" {
		t.Errorf("provider=%q", status.Provider)
	}
	if status.Model != "test-model" {
		t.Errorf("model=%q", status.Model)
	}
	if status.Diagnosis != "Monitor has not run yet." {
		t.Errorf("diagnosis=%q", status.Diagnosis)
	}
}

func TestGamarrMonitor_CaptureError(t *testing.T) {
	cfg := &config.Config{}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return nil
		},
	}

	mon := New(cfg, cb)
	mon.CaptureError("WARN", "test", "test warning")
	mon.CaptureError("ERROR", "test", "test error")

	status := mon.GetStatus()
	if len(status.RecentErrors) != 2 {
		t.Errorf("expected 2 recent errors, got %d", len(status.RecentErrors))
	}
}

func TestProcessResponse(t *testing.T) {
	executed := false
	cfg := &config.Config{AIAutoFix: true}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return []struct {
				ID   string
				Data map[string]interface{}
			}{
				{ID: "j1", Data: map[string]interface{}{"status": "error", "title": "Game"}},
			}
		},
		ClearMyrientCache: func() { executed = true },
	}

	mon := New(cfg, cb)
	response := map[string]interface{}{
		"diagnosis": "Myrient cache stale",
		"actions": []interface{}{
			map[string]interface{}{
				"action":      "refresh_myrient_cache",
				"params":      map[string]interface{}{},
				"description": "Refresh cache",
			},
		},
	}

	mon.processResponse(response)

	if !executed {
		t.Error("expected ClearMyrientCache to be called")
	}
	if mon.diagnosis != "Myrient cache stale" {
		t.Errorf("diagnosis=%q", mon.diagnosis)
	}
}

func TestCollectContext(t *testing.T) {
	cfg := &config.Config{
		ProwlarrURL:    "http://prowlarr",
		ProwlarrAPIKey: "key",
		QBURL:          "http://qbit",
	}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return []struct {
				ID   string
				Data map[string]interface{}
			}{
				{ID: "j1", Data: map[string]interface{}{"status": "error", "title": "Game", "error": "failed"}},
				{ID: "j2", Data: map[string]interface{}{"status": "downloading", "title": "Good"}},
			}
		},
	}

	mon := New(cfg, cb)
	ctx := mon.collectContext()

	failedJobs, ok := ctx["failed_jobs"].([]map[string]interface{})
	if !ok {
		t.Fatal("expected failed_jobs")
	}
	if len(failedJobs) != 1 {
		t.Errorf("expected 1 failed job, got %d", len(failedJobs))
	}

	totalJobs, _ := ctx["total_jobs"].(int)
	if totalJobs != 2 {
		t.Errorf("total_jobs=%d, want 2", totalJobs)
	}
}

func TestCallOpenAICompat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"content": `{"diagnosis": "All nominal", "actions": []}`,
					},
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AIProvider: "ollama",
		AIModel:    "test",
		AIAPIUrl:   srv.URL,
	}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return nil
		},
	}

	mon := New(cfg, cb)
	result, err := mon.callOpenAICompat("test message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["diagnosis"] != "All nominal" {
		t.Errorf("diagnosis=%v", result["diagnosis"])
	}
}

func TestExecuteApproved_NotFound(t *testing.T) {
	cfg := &config.Config{}
	cb := Callbacks{
		GetJobs: func() []struct {
			ID   string
			Data map[string]interface{}
		} {
			return nil
		},
	}

	mon := New(cfg, cb)
	ok, msg := mon.ExecuteApproved("nonexistent")
	if ok {
		t.Error("expected false for non-existent action")
	}
	if msg == "" {
		t.Error("expected non-empty message")
	}
}

func TestAllowedActions(t *testing.T) {
	// Verify all actions have risk and label
	for name, info := range allowedActions {
		if info.Risk == "" {
			t.Errorf("action %q has no risk", name)
		}
		if info.Label == "" {
			t.Errorf("action %q has no label", name)
		}
		if info.Risk != "safe" && info.Risk != "approval" {
			t.Errorf("action %q has unexpected risk %q", name, info.Risk)
		}
	}
}

// Ensure MonitorAction serializes correctly
func TestMonitorAction_JSON(t *testing.T) {
	a := &models.MonitorAction{
		ID:          "abc",
		Action:      "retry_job",
		Params:      map[string]interface{}{"job_id": "j1"},
		Description: "Retry job",
		Risk:        "safe",
	}
	data, _ := json.Marshal(a)
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if m["action"] != "retry_job" {
		t.Errorf("action=%v", m["action"])
	}
}

// fakeDockerSocket starts a minimal one-shot HTTP server on a unix socket
// that records the request line of the first request and replies 204.
// It returns the socket path and a func that yields the captured request line.
func fakeDockerSocket(t *testing.T) (string, func() string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gamarr-dock")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix %s: %v", sock, err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	var reqLine string
	done := make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		rd := bufio.NewReader(conn)
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		mu.Lock()
		reqLine = strings.TrimRight(line, "\r\n")
		mu.Unlock()
		conn.Write([]byte("HTTP/1.0 204 No Content\r\n\r\n"))
	}()

	return sock, func() string {
		<-done
		mu.Lock()
		defer mu.Unlock()
		return reqLine
	}
}

func TestActRestartQBittorrent_ContainerName(t *testing.T) {
	tests := []struct {
		name          string
		containerName string
		wantContainer string
	}{
		{"default when unset", "", "qbittorrent"},
		{"custom name", "qbit-main", "qbit-main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock, requestLine := fakeDockerSocket(t)
			cfg := &config.Config{
				DockerSocket:    sock,
				QBContainerName: tt.containerName,
			}
			mon := New(cfg, Callbacks{})

			result := mon.executeAction("restart_qbittorrent", nil)

			wantMsg := fmt.Sprintf("Container %q restart requested", tt.wantContainer)
			if result != wantMsg {
				t.Errorf("result=%q, want %q", result, wantMsg)
			}
			wantLine := fmt.Sprintf("POST /containers/%s/restart HTTP/1.0", tt.wantContainer)
			if got := requestLine(); got != wantLine {
				t.Errorf("request line=%q, want %q", got, wantLine)
			}
		})
	}
}

func TestActRestartQBittorrent_NoSocket(t *testing.T) {
	cfg := &config.Config{DockerSocket: ""}
	mon := New(cfg, Callbacks{})
	if got := mon.actRestartQBittorrent(); got != "Docker socket not available" {
		t.Errorf("got %q", got)
	}
}
