package qbit

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Torrent represents a qBittorrent torrent entry.
type Torrent struct {
	Name        string  `json:"name"`
	Hash        string  `json:"hash"`
	Progress    float64 `json:"progress"`
	State       string  `json:"state"`
	TotalSize   int64   `json:"total_size"`
	DLSpeed     int64   `json:"dlspeed"`
	ETA         int     `json:"eta"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
}

// TorrentFile represents a file within a torrent.
type TorrentFile struct {
	Name string `json:"name"`
}

// Client is a qBittorrent API client with session-based cookie auth.
type Client struct {
	mu            sync.Mutex
	client        *http.Client
	baseURL       string
	user          string
	pass          string
	authenticated bool
}

// New creates a new qBittorrent client.
func New(baseURL, user, pass string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		client: &http.Client{
			Jar:     jar,
			Timeout: 15 * time.Second,
		},
		baseURL: strings.TrimRight(baseURL, "/"),
		user:    user,
		pass:    pass,
	}
}

// Login authenticates with qBittorrent.
func (c *Client) Login() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.login()
}

func (c *Client) login() bool {
	data := url.Values{
		"username": {c.user},
		"password": {c.pass},
	}
	resp, err := c.client.PostForm(c.baseURL+"/api/v2/auth/login", data)
	if err != nil {
		slog.Error("qBittorrent login failed", "error", err)
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// qBittorrent <= 5.1 replies 200 with "Ok." on success and 200 with
	// "Fails." on bad credentials, so a bare 2xx status is not proof of
	// authentication. qBittorrent >= 5.2 replies 204 with an empty body on
	// success and 401 on bad credentials.
	c.authenticated = string(body) == "Ok." ||
		(resp.StatusCode == http.StatusNoContent && len(body) == 0)
	return c.authenticated
}

// is2xx reports whether an HTTP status code indicates success. qBittorrent
// >= 5.2 returns 204 instead of 200 for responses with no body, so exact
// comparisons against 200 break against newer versions.
func is2xx(code int) bool {
	return code >= 200 && code < 300
}

// addAccepted reports whether a torrents/add response indicates the torrent
// was accepted. qBittorrent <= 5.1 replies 200 with a plain "Ok." body
// ("Fails." on error); qBittorrent >= 5.2 replies with a JSON body like
// {"added_torrent_ids":[...],"success_count":1,"pending_count":0,
// "failure_count":0} and uses 409 for rejected requests.
func addAccepted(statusCode int, body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "Ok." {
		return true
	}
	if !is2xx(statusCode) {
		return false
	}
	var result struct {
		SuccessCount *int `json:"success_count"`
		PendingCount *int `json:"pending_count"`
	}
	if err := json.Unmarshal(body, &result); err == nil && result.SuccessCount != nil {
		accepted := *result.SuccessCount
		if result.PendingCount != nil {
			accepted += *result.PendingCount
		}
		return accepted > 0
	}
	// 2xx with an empty non-JSON body (e.g. a bare 204): nothing indicates
	// failure, so treat it as accepted.
	return trimmed == ""
}

func (c *Client) ensureAuth() {
	if !c.authenticated {
		c.login()
	}
}

// AddTorrent adds a torrent to qBittorrent.
func (c *Client) AddTorrent(torrentURL, title, savePath, category string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureAuth()

	data := url.Values{
		"urls":     {torrentURL},
		"savepath": {savePath},
		"category": {category},
	}
	resp, err := c.client.PostForm(c.baseURL+"/api/v2/torrents/add", data)
	if err != nil {
		slog.Error("qBittorrent add torrent failed", "error", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		c.login()
		resp2, err := c.client.PostForm(c.baseURL+"/api/v2/torrents/add", data)
		if err != nil {
			return false
		}
		defer resp2.Body.Close()
		body, _ := io.ReadAll(resp2.Body)
		return addAccepted(resp2.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	return addAccepted(resp.StatusCode, body)
}

// GetTorrents returns torrents, optionally filtered by category.
func (c *Client) GetTorrents(category string) []Torrent {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureAuth()

	u := c.baseURL + "/api/v2/torrents/info"
	if category != "" {
		u += "?category=" + url.QueryEscape(category)
	}
	torrents, err := c.doGetJSON(u)
	if err != nil {
		return nil
	}
	return torrents
}

// GetTorrentFiles returns the file list for a torrent.
func (c *Client) GetTorrentFiles(hash string) []TorrentFile {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureAuth()

	u := fmt.Sprintf("%s/api/v2/torrents/files?hash=%s", c.baseURL, hash)
	resp, err := c.doGet(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		c.login()
		resp2, err := c.doGet(u)
		if err != nil {
			return nil
		}
		defer resp2.Body.Close()
		if !is2xx(resp2.StatusCode) {
			return nil
		}
		var files []TorrentFile
		json.NewDecoder(resp2.Body).Decode(&files)
		return files
	}
	if !is2xx(resp.StatusCode) {
		return nil
	}
	var files []TorrentFile
	json.NewDecoder(resp.Body).Decode(&files)
	return files
}

// DeleteTorrent deletes a torrent from qBittorrent.
func (c *Client) DeleteTorrent(hash string, deleteFiles bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureAuth()

	delStr := "false"
	if deleteFiles {
		delStr = "true"
	}
	data := url.Values{
		"hashes":      {hash},
		"deleteFiles": {delStr},
	}
	resp, err := c.client.PostForm(c.baseURL+"/api/v2/torrents/delete", data)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		c.login()
		resp2, err := c.client.PostForm(c.baseURL+"/api/v2/torrents/delete", data)
		if err != nil {
			return false
		}
		defer resp2.Body.Close()
		return is2xx(resp2.StatusCode)
	}
	return is2xx(resp.StatusCode)
}

func (c *Client) doGet(u string) (*http.Response, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	return c.client.Do(req)
}

func (c *Client) doGetJSON(u string) ([]Torrent, error) {
	resp, err := c.doGet(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 {
		c.login()
		resp2, err := c.doGet(u)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if !is2xx(resp2.StatusCode) {
			return nil, fmt.Errorf("HTTP %d", resp2.StatusCode)
		}
		var torrents []Torrent
		err = json.NewDecoder(resp2.Body).Decode(&torrents)
		return torrents, err
	}
	if !is2xx(resp.StatusCode) {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var torrents []Torrent
	err = json.NewDecoder(resp.Body).Decode(&torrents)
	return torrents, err
}
