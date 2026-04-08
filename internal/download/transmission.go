package download

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"gamarr/internal/config"
)

// TransmissionClient wraps the Transmission RPC API.
type TransmissionClient struct {
	cfg       *config.Config
	client    *http.Client
	mu        sync.Mutex
	sessionID string
}

// NewTransmissionClient creates a new Transmission RPC client.
func NewTransmissionClient(cfg *config.Config) *TransmissionClient {
	return &TransmissionClient{
		cfg: cfg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// transmissionRequest is the RPC request format.
type transmissionRequest struct {
	Method    string                 `json:"method"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// transmissionResponse is the RPC response format.
type transmissionResponse struct {
	Result    string                 `json:"result"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// AddTorrent adds a torrent by URL or magnet link.
func (t *TransmissionClient) AddTorrent(url, downloadDir string) (map[string]interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	args := map[string]interface{}{
		"filename": url,
	}
	if downloadDir != "" {
		args["download-dir"] = downloadDir
	}

	resp, err := t.call("torrent-add", args)
	if err != nil {
		return nil, err
	}
	if resp.Result != "success" {
		return nil, fmt.Errorf("transmission: %s", resp.Result)
	}

	// The response may have "torrent-added" or "torrent-duplicate".
	if added, ok := resp.Arguments["torrent-added"]; ok {
		if m, ok := added.(map[string]interface{}); ok {
			return m, nil
		}
	}
	if dup, ok := resp.Arguments["torrent-duplicate"]; ok {
		if m, ok := dup.(map[string]interface{}); ok {
			return m, nil
		}
	}

	return resp.Arguments, nil
}

// GetTorrents returns status info for all torrents.
// If fields is nil, returns a default set of fields.
func (t *TransmissionClient) GetTorrents(fields []string) ([]map[string]interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if fields == nil {
		fields = []string{"id", "name", "status", "percentDone", "totalSize", "rateDownload", "hashString", "downloadDir"}
	}

	args := map[string]interface{}{
		"fields": fields,
	}

	resp, err := t.call("torrent-get", args)
	if err != nil {
		return nil, err
	}
	if resp.Result != "success" {
		return nil, fmt.Errorf("transmission: %s", resp.Result)
	}

	torrentsRaw, ok := resp.Arguments["torrents"]
	if !ok {
		return nil, nil
	}

	// Convert to []map[string]interface{}.
	torrentsJSON, err := json.Marshal(torrentsRaw)
	if err != nil {
		return nil, err
	}
	var torrents []map[string]interface{}
	if err := json.Unmarshal(torrentsJSON, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

// RemoveTorrent removes torrents by ID.
func (t *TransmissionClient) RemoveTorrent(ids []int, deleteData bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	resp, err := t.call("torrent-remove", map[string]interface{}{
		"ids":               ids,
		"delete-local-data": deleteData,
	})
	if err != nil {
		return err
	}
	if resp.Result != "success" {
		return fmt.Errorf("transmission: %s", resp.Result)
	}
	return nil
}

// Diagnose tests the Transmission connection.
func (t *TransmissionClient) Diagnose() map[string]interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()

	resp, err := t.call("session-get", nil)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}
	if resp.Result != "success" {
		return map[string]interface{}{
			"success": false,
			"error":   resp.Result,
		}
	}
	return map[string]interface{}{
		"success": true,
	}
}

func (t *TransmissionClient) call(method string, args map[string]interface{}) (*transmissionResponse, error) {
	reqBody := transmissionRequest{
		Method:    method,
		Arguments: args,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/transmission/rpc", t.cfg.TransmissionURL)
	resp, err := t.doRequest(url, body)
	if err != nil {
		return nil, err
	}

	// Handle 409 Conflict — Transmission requires a session-id header.
	if resp.StatusCode == http.StatusConflict {
		t.sessionID = resp.Header.Get("X-Transmission-Session-Id")
		resp.Body.Close()
		if t.sessionID == "" {
			return nil, fmt.Errorf("transmission: 409 but no session ID header")
		}
		resp, err = t.doRequest(url, body)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("transmission: authentication failed")
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var trResp transmissionResponse
	if err := json.Unmarshal(respBody, &trResp); err != nil {
		return nil, fmt.Errorf("transmission: invalid JSON response")
	}

	return &trResp, nil
}

func (t *TransmissionClient) doRequest(url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if t.sessionID != "" {
		req.Header.Set("X-Transmission-Session-Id", t.sessionID)
	}
	if t.cfg.TransmissionUser != "" {
		req.SetBasicAuth(t.cfg.TransmissionUser, t.cfg.TransmissionPass)
	}
	return t.client.Do(req)
}
