package sabnzbd

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a SABnzbd API client.
type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NZBSlot represents a SABnzbd queue/history item.
type NZBSlot struct {
	NZOID    string  `json:"nzo_id"`
	Filename string  `json:"filename"`
	Status   string  `json:"status"`
	Storage  string  `json:"storage"` // final path (history only)
	Size     string  `json:"size"`
	MBLeft   float64 `json:"mbleft"`
	MB       float64 `json:"mb"`
}

// New creates a new SABnzbd client.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// AddNZBByURL sends an NZB URL to SABnzbd for download.
func (c *Client) AddNZBByURL(nzbURL, title, category string) (string, error) {
	params := url.Values{
		"mode":     {"addurl"},
		"name":     {nzbURL},
		"nzbname":  {title},
		"cat":      {category},
		"apikey":   {c.apiKey},
		"output":   {"json"},
		"priority": {"0"},
	}
	resp, err := c.client.Get(c.baseURL + "/api?" + params.Encode())
	if err != nil {
		return "", fmt.Errorf("SABnzbd request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Status bool     `json:"status"`
		NZOIDs []string `json:"nzo_ids"`
		Error  string   `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("SABnzbd response parse error: %w", err)
	}
	if !result.Status {
		return "", fmt.Errorf("SABnzbd error: %s", result.Error)
	}
	if len(result.NZOIDs) > 0 {
		return result.NZOIDs[0], nil
	}
	return "", nil
}

// GetQueue returns the current download queue.
func (c *Client) GetQueue() ([]NZBSlot, error) {
	params := url.Values{
		"mode":   {"queue"},
		"apikey": {c.apiKey},
		"output": {"json"},
		"limit":  {"100"},
	}
	resp, err := c.client.Get(c.baseURL + "/api?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Queue struct {
			Slots []NZBSlot `json:"slots"`
		} `json:"queue"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Queue.Slots, nil
}

// GetHistory returns completed downloads.
func (c *Client) GetHistory(limit int) ([]NZBSlot, error) {
	params := url.Values{
		"mode":   {"history"},
		"apikey": {c.apiKey},
		"output": {"json"},
		"limit":  {fmt.Sprintf("%d", limit)},
	}
	resp, err := c.client.Get(c.baseURL + "/api?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		History struct {
			Slots []NZBSlot `json:"slots"`
		} `json:"history"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.History.Slots, nil
}

// TestConnection verifies SABnzbd is reachable.
func (c *Client) TestConnection() error {
	params := url.Values{
		"mode":   {"version"},
		"apikey": {c.apiKey},
		"output": {"json"},
	}
	resp, err := c.client.Get(c.baseURL + "/api?" + params.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	slog.Info("SABnzbd connection test passed")
	return nil
}

// DeleteHistoryItem deletes an item from SABnzbd history.
func (c *Client) DeleteHistoryItem(nzoID string) error {
	params := url.Values{
		"mode":   {"history"},
		"name":   {"delete"},
		"value":  {nzoID},
		"apikey": {c.apiKey},
		"output": {"json"},
	}
	resp, err := c.client.Get(c.baseURL + "/api?" + params.Encode())
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
