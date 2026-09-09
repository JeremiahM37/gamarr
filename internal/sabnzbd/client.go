// Package sabnzbd is a minimal SABnzbd API client used for Usenet (NZB)
// downloads.
package sabnzbd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const maxNZBBytes = 32 << 20 // 32 MiB — NZBs are tiny; bound the download.

// Client is a SABnzbd API client.
type Client struct {
	baseURL string
	apiKey  string
	client  *http.Client
	// fetchClient retrieves NZB bodies. Separated so SABnzbd (often behind a
	// VPN DNS that cannot resolve Docker service names like "prowlarr") is
	// never asked to fetch grab URLs itself.
	fetchClient *http.Client
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
		fetchClient: &http.Client{
			Timeout: 60 * time.Second,
			// Follow redirects to the indexer CDN; Prowlarr grab URLs 30x.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects fetching NZB")
				}
				return nil
			},
		},
	}
}

// AddNZBByURL fetches the NZB (from Prowlarr/indexer) in-process and uploads
// it to SABnzbd via addfile. SABnzbd must not fetch the URL itself: when it
// shares a VPN container, Docker DNS names like http://prowlarr:9696 do not
// resolve and "URL Fetching failed" is the result.
func (c *Client) AddNZBByURL(nzbURL, title, category string) (string, error) {
	nzbData, err := c.fetchNZB(nzbURL)
	if err != nil {
		return "", err
	}
	filename := nzbFilename(title, nzbURL)
	return c.addNZBFile(nzbData, filename, category)
}

func (c *Client) fetchNZB(nzbURL string) ([]byte, error) {
	resp, err := c.fetchClient.Get(nzbURL)
	if err != nil {
		return nil, fmt.Errorf("fetch NZB: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch NZB: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNZBBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read NZB: %w", err)
	}
	if len(body) > maxNZBBytes {
		return nil, fmt.Errorf("NZB exceeded %d MiB", maxNZBBytes>>20)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("fetch NZB: empty body")
	}
	trimmed := bytes.TrimSpace(body)
	if !bytes.HasPrefix(trimmed, []byte("<?xml")) && !bytes.HasPrefix(trimmed, []byte("<nzb")) {
		snippet := string(trimmed)
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		return nil, fmt.Errorf("fetch NZB: response is not an NZB (%q)", snippet)
	}
	return body, nil
}

func (c *Client) addNZBFile(nzbData []byte, filename, category string) (string, error) {
	if filename == "" {
		filename = "download.nzb"
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("mode", "addfile")
	_ = w.WriteField("apikey", c.apiKey)
	_ = w.WriteField("output", "json")
	_ = w.WriteField("priority", "0")
	if category != "" {
		_ = w.WriteField("cat", category)
	}
	// nzbname is the display name in SABnzbd; the file field is the payload.
	name := strings.TrimSuffix(filename, ".nzb")
	_ = w.WriteField("nzbname", name)
	part, err := w.CreateFormFile("name", filename)
	if err != nil {
		return "", fmt.Errorf("create NZB form file: %w", err)
	}
	if _, err := part.Write(nzbData); err != nil {
		return "", fmt.Errorf("write NZB form file: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("close NZB form: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL+"/api", &buf)
	if err != nil {
		return "", fmt.Errorf("create SABnzbd request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.client.Do(req)
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

func nzbFilename(title, nzbURL string) string {
	name := strings.TrimSpace(title)
	if name == "" {
		if u, err := url.Parse(nzbURL); err == nil {
			if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
				name = base
			}
			if f := u.Query().Get("file"); f != "" {
				name = f
			}
		}
	}
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 0x20 {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		return "download.nzb"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".nzb") {
		name += ".nzb"
	}
	return name
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
