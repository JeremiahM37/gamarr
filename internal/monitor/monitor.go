// Package monitor implements the AI-powered self-healing monitor, which
// captures runtime errors and queues corrective actions such as qBittorrent
// re-auth, cache clears, and orphan recovery.
package monitor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/models"
)

// Callbacks for actions the monitor can take.
type Callbacks struct {
	GetJobs func() []struct {
		ID   string
		Data map[string]interface{}
	}
	QBReauth          func() bool
	ClearMyrientCache func()
	RunOrphanRecovery func()
}

var allowedActions = map[string]struct {
	Risk  string
	Label string
}{
	"retry_job":             {"safe", "Re-queue a failed download job"},
	"clear_interrupted":     {"safe", "Mark interrupted/stuck jobs as cleared"},
	"reauth_qbittorrent":    {"safe", "Force qBittorrent session re-authentication"},
	"refresh_myrient_cache": {"safe", "Clear cached Myrient directory listings"},
	"run_orphan_recovery":   {"safe", "Run orphan torrent recovery routine"},
	"restart_qbittorrent":   {"approval", "Restart qBittorrent Docker container"},
}

func buildSystemPrompt() string {
	var actionsDoc strings.Builder
	for name, info := range allowedActions {
		actionsDoc.WriteString(fmt.Sprintf("- %s: %s [risk: %s]\n", name, info.Label, info.Risk))
	}
	return `You are an AI monitor for Gamarr, a self-hosted game and ROM download manager.

Gamarr integrates with: qBittorrent (torrent client), Prowlarr (torrent indexer), ` +
		`Myrient (direct ROM downloads — cached directory listings), and Vimm's Lair (ROM downloads).
It downloads PC games, console ROMs (NES, SNES, N64, DS, 3DS, Switch, PS1, PS2, etc.) and ` +
		`organizes them into a vault or ROM library.

Analyze the provided system state (recent WARNING/ERROR log entries and job summary) and respond ` +
		`ONLY with valid JSON in this exact schema — no markdown, no extra text:
{
  "diagnosis": "plain-text summary of what is wrong, or 'All systems nominal' if nothing is wrong",
  "actions": [
    {"action": "action_name", "params": {}, "description": "brief human-readable explanation"}
  ]
}

Allowed actions:
` + actionsDoc.String() + `
Action params:
- retry_job: {"job_id": "<id from failed_jobs list>"}
- all others: {}

Rules:
- Only suggest actions that address observed problems.
- Only use job_ids from the provided failed_jobs list — never invent them.
- restart_qbittorrent requires user approval — only suggest for severe failures.
- If everything is fine, return an empty actions array.`
}

// ErrorCapture captures WARNING+ log entries.
type ErrorCapture struct {
	mu      sync.Mutex
	entries []models.ErrorEntry
	maxLen  int
}

func NewErrorCapture(maxLen int) *ErrorCapture {
	return &ErrorCapture{maxLen: maxLen}
}

func (ec *ErrorCapture) Add(level, logger, message string) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	now := time.Now()
	ec.entries = append(ec.entries, models.ErrorEntry{
		Timestamp: float64(now.Unix()),
		TimeStr:   now.Format("15:04:05"),
		Level:     level,
		Logger:    logger,
		Message:   message,
	})
	if len(ec.entries) > ec.maxLen {
		ec.entries = ec.entries[len(ec.entries)-ec.maxLen:]
	}
}

func (ec *ErrorCapture) Recent(n int) []models.ErrorEntry {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	start := len(ec.entries) - n
	if start < 0 {
		start = 0
	}
	result := make([]models.ErrorEntry, len(ec.entries[start:]))
	copy(result, ec.entries[start:])
	return result
}

// ActionQueue manages pending actions.
type ActionQueue struct {
	mu      sync.Mutex
	pending map[string]*models.MonitorAction
	history []*models.MonitorAction
}

func NewActionQueue() *ActionQueue {
	return &ActionQueue{
		pending: make(map[string]*models.MonitorAction),
	}
}

func (q *ActionQueue) Add(action string, params interface{}, description, risk string) string {
	id := fmt.Sprintf("%x", time.Now().UnixNano()&0xffffffff)
	entry := &models.MonitorAction{
		ID:          id,
		Action:      action,
		Params:      params,
		Description: description,
		Risk:        risk,
		CreatedAt:   float64(time.Now().Unix()),
	}
	q.mu.Lock()
	q.pending[id] = entry
	q.mu.Unlock()
	return id
}

func (q *ActionQueue) Get(id string) *models.MonitorAction {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending[id]
}

func (q *ActionQueue) Complete(id, result string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.pending[id]
	if entry != nil {
		delete(q.pending, id)
		entry.Result = result
		entry.DoneAt = float64(time.Now().Unix())
		q.history = append([]*models.MonitorAction{entry}, q.history...)
		if len(q.history) > 20 {
			q.history = q.history[:20]
		}
	}
}

func (q *ActionQueue) Dismiss(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.pending[id]
	if entry == nil {
		return false
	}
	delete(q.pending, id)
	entry.Result = "dismissed"
	entry.DoneAt = float64(time.Now().Unix())
	q.history = append([]*models.MonitorAction{entry}, q.history...)
	if len(q.history) > 20 {
		q.history = q.history[:20]
	}
	return true
}

func (q *ActionQueue) ListPending() []*models.MonitorAction {
	q.mu.Lock()
	defer q.mu.Unlock()
	list := make([]*models.MonitorAction, 0, len(q.pending))
	for _, a := range q.pending {
		list = append(list, a)
	}
	return list
}

func (q *ActionQueue) ListHistory() []*models.MonitorAction {
	q.mu.Lock()
	defer q.mu.Unlock()
	result := make([]*models.MonitorAction, len(q.history))
	copy(result, q.history)
	return result
}

// GamarrMonitor is the AI-powered self-healing monitor.
type GamarrMonitor struct {
	cfg          *config.Config
	callbacks    Callbacks
	errorCapture *ErrorCapture
	actionQueue  *ActionQueue

	diagnosis   string
	lastChecked *float64
	running     bool
	mu          sync.RWMutex
	manualCh    chan struct{}
	stopCh      chan struct{}
}

// New creates a new monitor.
func New(cfg *config.Config, cb Callbacks) *GamarrMonitor {
	return &GamarrMonitor{
		cfg:          cfg,
		callbacks:    cb,
		errorCapture: NewErrorCapture(200),
		actionQueue:  NewActionQueue(),
		diagnosis:    "Monitor has not run yet.",
		manualCh:     make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
	}
}

// CaptureError adds an error to the capture log.
func (m *GamarrMonitor) CaptureError(level, logger, message string) {
	m.errorCapture.Add(level, logger, message)
}

// ActionQueue returns the action queue.
func (m *GamarrMonitor) ActionQueue() *ActionQueue { return m.actionQueue }

// Start begins the monitor loop.
func (m *GamarrMonitor) Start() {
	if !m.cfg.AIMonitorEnabled {
		slog.Info("AI monitor disabled (set AI_MONITOR_ENABLED=true to enable)")
		return
	}
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()

	slog.Info("AI monitor started",
		"provider", m.cfg.AIProvider,
		"model", m.cfg.AIModel,
		"interval", m.cfg.AIMonitorInterval,
	)
	go m.loop()
}

func (m *GamarrMonitor) Stop() {
	close(m.stopCh)
}

func (m *GamarrMonitor) loop() {
	ticker := time.NewTicker(time.Duration(m.cfg.AIMonitorInterval) * time.Second)
	defer ticker.Stop()

	// Initial delay
	select {
	case <-m.stopCh:
		return
	case <-ticker.C:
	case <-m.manualCh:
	}

	for {
		m.runCycle()
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
		case <-m.manualCh:
		}
	}
}

// TriggerManual triggers an immediate analysis.
func (m *GamarrMonitor) TriggerManual() {
	m.mu.RLock()
	running := m.running
	m.mu.RUnlock()
	if running {
		select {
		case m.manualCh <- struct{}{}:
		default:
		}
	} else {
		m.runCycle()
	}
}

func (m *GamarrMonitor) runCycle() {
	ctx := m.collectContext()
	response := m.callAI(ctx)
	if response != nil {
		m.processResponse(response)
	}
	now := float64(time.Now().Unix())
	m.mu.Lock()
	m.lastChecked = &now
	m.mu.Unlock()
}

func (m *GamarrMonitor) collectContext() map[string]interface{} {
	jobs := m.callbacks.GetJobs()
	var failedJobs []map[string]interface{}
	for _, j := range jobs {
		status, _ := j.Data["status"].(string)
		if status == "error" || status == "interrupted" {
			failedJobs = append(failedJobs, map[string]interface{}{
				"job_id":   j.ID,
				"title":    j.Data["title"],
				"platform": j.Data["platform"],
				"status":   status,
				"error":    j.Data["error"],
			})
		}
	}
	if len(failedJobs) > 20 {
		failedJobs = failedJobs[:20]
	}
	return map[string]interface{}{
		"recent_errors": m.errorCapture.Recent(50),
		"failed_jobs":   failedJobs,
		"total_jobs":    len(jobs),
		"integrations": map[string]bool{
			"prowlarr":    m.cfg.HasProwlarr(),
			"qbittorrent": m.cfg.HasQBittorrent(),
			"clamav":      m.cfg.HasClamAV(),
		},
	}
}

func (m *GamarrMonitor) callAI(ctx map[string]interface{}) map[string]interface{} {
	userMsg, _ := json.MarshalIndent(ctx, "", "  ")
	msg := "System state:\n" + string(userMsg)

	var result map[string]interface{}
	var err error
	if m.cfg.AIProvider == "anthropic" {
		result, err = m.callAnthropic(msg)
	} else {
		result, err = m.callOpenAICompat(msg)
	}
	if err != nil {
		slog.Error("AI monitor API call failed", "error", err)
		return nil
	}
	return result
}

func (m *GamarrMonitor) callOpenAICompat(userMsg string) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"model": m.cfg.AIModel,
		"messages": []map[string]string{
			{"role": "system", "content": buildSystemPrompt()},
			{"role": "user", "content": userMsg},
		},
		"temperature": 0.1,
	}
	if m.cfg.AIProvider == "openai" {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", m.cfg.AIAPIUrl+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if m.cfg.AIAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.AIAPIKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(respBody, &result)
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	return parseJSON(result.Choices[0].Message.Content)
}

func (m *GamarrMonitor) callAnthropic(userMsg string) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"model":      m.cfg.AIModel,
		"max_tokens": 1024,
		"system":     buildSystemPrompt(),
		"messages":   []map[string]string{{"role": "user", "content": userMsg}},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", m.cfg.AIAPIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(respBody, &result)
	if len(result.Content) == 0 {
		return nil, fmt.Errorf("no content in response")
	}
	return parseJSON(result.Content[0].Text)
}

func parseJSON(content string) (map[string]interface{}, error) {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		end := len(lines)
		if strings.TrimSpace(lines[end-1]) == "```" {
			end--
		}
		text = strings.Join(lines[1:end], "\n")
	}
	var result map[string]interface{}
	err := json.Unmarshal([]byte(text), &result)
	if err != nil {
		slog.Warn("AI monitor: could not parse JSON response", "error", err)
		return nil, err
	}
	return result, nil
}

func (m *GamarrMonitor) processResponse(response map[string]interface{}) {
	diagnosis, _ := response["diagnosis"].(string)
	if diagnosis == "" {
		diagnosis = "No diagnosis provided."
	}
	m.mu.Lock()
	m.diagnosis = diagnosis
	m.mu.Unlock()

	actions, _ := response["actions"].([]interface{})
	for _, a := range actions {
		suggestion, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		action, _ := suggestion["action"].(string)
		params := suggestion["params"]
		description, _ := suggestion["description"].(string)
		if description == "" {
			description = action
		}

		info, exists := allowedActions[action]
		if !exists {
			slog.Warn("AI monitor suggested unknown action", "action", action)
			continue
		}

		if info.Risk == "safe" && m.cfg.AIAutoFix {
			result := m.executeAction(action, params)
			id := m.actionQueue.Add(action, params, description, info.Risk)
			m.actionQueue.Complete(id, result)
			slog.Info("AI monitor auto-applied action", "action", action, "result", result)
		} else {
			m.actionQueue.Add(action, params, description, info.Risk)
			slog.Info("AI monitor queued action for approval", "action", action)
		}
	}
}

// ExecuteApproved executes a user-approved action.
func (m *GamarrMonitor) ExecuteApproved(actionID string) (bool, string) {
	entry := m.actionQueue.Get(actionID)
	if entry == nil {
		return false, "Action not found or already processed"
	}
	result := m.executeAction(entry.Action, entry.Params)
	m.actionQueue.Complete(actionID, result)
	return true, result
}

func (m *GamarrMonitor) executeAction(action string, params interface{}) string {
	paramsMap, _ := params.(map[string]interface{})
	if paramsMap == nil {
		paramsMap = map[string]interface{}{}
	}

	switch action {
	case "retry_job":
		jobID, _ := paramsMap["job_id"].(string)
		return m.actRetryJob(jobID)
	case "clear_interrupted":
		return m.actClearInterrupted()
	case "reauth_qbittorrent":
		if m.callbacks.QBReauth() {
			return "qBittorrent re-authenticated"
		}
		return "qBittorrent re-auth failed"
	case "refresh_myrient_cache":
		m.callbacks.ClearMyrientCache()
		return "Myrient directory cache cleared"
	case "run_orphan_recovery":
		m.callbacks.RunOrphanRecovery()
		return "Orphan torrent recovery triggered"
	case "restart_qbittorrent":
		return m.actRestartQBittorrent()
	default:
		return fmt.Sprintf("Unknown action: %s", action)
	}
}

func (m *GamarrMonitor) actRetryJob(jobID string) string {
	if jobID == "" {
		return "No job_id provided"
	}
	jobs := m.callbacks.GetJobs()
	for _, j := range jobs {
		if j.ID == jobID {
			status, _ := j.Data["status"].(string)
			if status != "error" && status != "interrupted" {
				return fmt.Sprintf("Job %q not in a failed state (status=%q)", jobID, status)
			}
			j.Data["status"] = "queued"
			j.Data["error"] = nil
			return fmt.Sprintf("Job %q re-queued", jobID)
		}
	}
	return fmt.Sprintf("Job %q not found", jobID)
}

func (m *GamarrMonitor) actClearInterrupted() string {
	jobs := m.callbacks.GetJobs()
	cleared := 0
	for _, j := range jobs {
		status, _ := j.Data["status"].(string)
		errMsg, _ := j.Data["error"].(string)
		if status == "interrupted" || (status == "error" && strings.Contains(errMsg, "Interrupted by restart")) {
			j.Data["status"] = "cleared"
			cleared++
		}
	}
	return fmt.Sprintf("Cleared %d interrupted job(s)", cleared)
}

// qbContainerName returns the configured qBittorrent container name,
// falling back to the historical default when unset.
func (m *GamarrMonitor) qbContainerName() string {
	if m.cfg.QBContainerName != "" {
		return m.cfg.QBContainerName
	}
	return "qbittorrent"
}

func (m *GamarrMonitor) actRestartQBittorrent() string {
	if m.cfg.DockerSocket == "" {
		return "Docker socket not available"
	}
	if _, err := os.Stat(m.cfg.DockerSocket); err != nil {
		return "Docker socket not available"
	}

	conn, err := net.Dial("unix", m.cfg.DockerSocket)
	if err != nil {
		return fmt.Sprintf("Docker socket error: %v", err)
	}
	defer conn.Close()

	name := m.qbContainerName()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf("POST /containers/%s/restart HTTP/1.0\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n", url.PathEscape(name))
	conn.Write([]byte(req))
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	resp := string(buf[:n])

	if strings.Contains(resp, "204") || strings.Contains(resp, "200") {
		return fmt.Sprintf("Container %q restart requested", name)
	}
	return fmt.Sprintf("Unexpected Docker response: %s", resp[:min(100, len(resp))])
}

// GetStatus returns the monitor status.
func (m *GamarrMonitor) GetStatus() models.MonitorStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return models.MonitorStatus{
		Enabled:        m.cfg.AIMonitorEnabled,
		Provider:       m.cfg.AIProvider,
		Model:          m.cfg.AIModel,
		AutoFix:        m.cfg.AIAutoFix,
		Interval:       m.cfg.AIMonitorInterval,
		LastChecked:    m.lastChecked,
		Diagnosis:      m.diagnosis,
		RecentErrors:   m.errorCapture.Recent(50),
		PendingActions: m.actionQueue.ListPending(),
		ActionHistory:  m.actionQueue.ListHistory(),
	}
}
