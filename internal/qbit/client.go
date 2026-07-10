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
	
	c.authenticated = resp.StatusCode == http.StatusOK ||
    	resp.StatusCode == http.StatusNoContent ||
    	string(body) == "Ok."
	return c.authenticated
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
		return string(body) == "Ok."
	}
	body, _ := io.ReadAll(resp.Body)
	return string(body) == "Ok."
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
		if resp2.StatusCode != 200 {
			return nil
		}
		var files []TorrentFile
		json.NewDecoder(resp2.Body).Decode(&files)
		return files
	}
	if resp.StatusCode != 200 {
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
		return resp2.StatusCode == 200
	}
	return resp.StatusCode == 200
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
		if resp2.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d", resp2.StatusCode)
		}
		var torrents []Torrent
		err = json.NewDecoder(resp2.Body).Decode(&torrents)
		return torrents, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var torrents []Torrent
	err = json.NewDecoder(resp.Body).Decode(&torrents)
	return torrents, err
}
