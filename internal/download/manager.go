// Package download orchestrates torrent, DDL, and NZB downloads across
// clients and watches for completed transfers to import into the library.
package download

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/platform"
	"gamarr/internal/qbit"
	"gamarr/internal/safety"
	"gamarr/internal/search"
)

// NotifyCallback is called when a download completes or fails.
// Parameters: userID, notifType, title, message.
type NotifyCallback func(userID, notifType, title, message string)

// Manager handles download orchestration.
type Manager struct {
	cfg          *config.Config
	jobs         *db.JobStore
	qb           *qbit.Client
	transmission *TransmissionClient
	deluge       *DelugeClient
	NotifyFunc   NotifyCallback
}

// New creates a new download Manager.
func New(cfg *config.Config, jobs *db.JobStore, qb *qbit.Client) *Manager {
	mgr := &Manager{cfg: cfg, jobs: jobs, qb: qb}

	// Initialize optional download clients.
	if cfg.HasTransmission() {
		mgr.transmission = NewTransmissionClient(cfg)
		slog.Info("Transmission client initialized", "url", cfg.TransmissionURL)
	}
	if cfg.HasDeluge() {
		mgr.deluge = NewDelugeClient(cfg)
		slog.Info("Deluge client initialized", "url", cfg.DelugeURL)
	}

	return mgr
}

// Jobs returns the job store.
func (m *Manager) Jobs() *db.JobStore { return m.jobs }

// QB returns the qBittorrent client.
func (m *Manager) QB() *qbit.Client { return m.qb }

// Transmission returns the Transmission client (may be nil).
func (m *Manager) Transmission() *TransmissionClient { return m.transmission }

// Deluge returns the Deluge client (may be nil).
func (m *Manager) Deluge() *DelugeClient { return m.deluge }

// newJobID generates an 8-char job ID.
func newJobID() string {
	b := make([]byte, 4)
	_, _ = io.ReadFull(cryptoReader(), b)
	return fmt.Sprintf("%x", b)
}

// DownloadTorrent starts a torrent download.
// Tries clients in order: qBittorrent -> Transmission -> Deluge (first available).
func (m *Manager) DownloadTorrent(url, title, platf, platSlug string, isPC bool) (string, error) {
	if url == "" {
		return "", fmt.Errorf("no download URL")
	}
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         title,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Sending to download client...",
	})

	added := false
	clientUsed := ""
	var clientErrs []string

	// Try qBittorrent first.
	if m.cfg.HasQBittorrent() {
		m.jobs.Update(jobID, "detail", "Sending to qBittorrent...")
		if err := m.qb.AddTorrent(url, title, m.cfg.QBSavePath, m.cfg.QBCategory); err == nil {
			added = true
			clientUsed = "qBittorrent"
		} else {
			slog.Warn("qBittorrent add failed, trying fallback clients", "title", title, "error", err)
			clientErrs = append(clientErrs, fmt.Sprintf("qBittorrent: %v", err))
		}
	}

	// Try Transmission.
	if !added && m.transmission != nil {
		m.jobs.Update(jobID, "detail", "Sending to Transmission...")
		_, err := m.transmission.AddTorrent(url, m.cfg.QBSavePath)
		if err == nil {
			added = true
			clientUsed = "Transmission"
		} else {
			slog.Warn("Transmission add failed", "title", title, "error", err)
			clientErrs = append(clientErrs, fmt.Sprintf("Transmission: %v", err))
		}
	}

	// Try Deluge.
	if !added && m.deluge != nil {
		m.jobs.Update(jobID, "detail", "Sending to Deluge...")
		opts := map[string]interface{}{
			"download_location": m.cfg.QBSavePath,
		}
		_, err := m.deluge.AddTorrent(url, opts)
		if err == nil {
			added = true
			clientUsed = "Deluge"
		} else {
			slog.Warn("Deluge add failed", "title", title, "error", err)
			clientErrs = append(clientErrs, fmt.Sprintf("Deluge: %v", err))
		}
	}

	if !added {
		errMsg := "Failed to add torrent to any download client"
		if len(clientErrs) > 0 {
			errMsg = fmt.Sprintf("%s (%s)", errMsg, strings.Join(clientErrs, "; "))
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  errMsg,
		})
		// Return the job ID alongside the error: the job exists (in error
		// state) so callers can surface both the failure and its record.
		return jobID, errors.New(errMsg)
	}

	m.jobs.Update(jobID, "detail", fmt.Sprintf("Downloading via %s...", clientUsed))
	slog.Info("torrent added", "client", clientUsed, "title", title)

	go m.watchGameTorrent(jobID, title, platf, platSlug, isPC)
	return jobID, nil
}

// DownloadDDL starts a direct download.
func (m *Manager) DownloadDDL(url, vimmID, title, platf, platSlug string, isPC bool) string {
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         title,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Starting direct download...",
	})
	go m.ddlDownloadWorker(jobID, url, vimmID, title, platf, platSlug, isPC)
	return jobID
}

// OrganizeTorrent manually triggers organize for a completed torrent.
func (m *Manager) OrganizeTorrent(hash, platf, platSlug string, isPC bool) (string, error) {
	torrents := m.qb.GetTorrents(m.cfg.QBCategory)
	var torrent *qbit.Torrent
	for i := range torrents {
		if torrents[i].Hash == hash {
			torrent = &torrents[i]
			break
		}
	}
	if torrent == nil {
		return "", fmt.Errorf("torrent not found")
	}
	if torrent.Progress < 1.0 {
		return "", fmt.Errorf("torrent not yet complete")
	}

	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "organizing",
		"title":         torrent.Name,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Scanning and organizing...",
	})

	go m.organizeWithScan(jobID, torrent, platf, platSlug, isPC)
	return jobID, nil
}

func (m *Manager) watchGameTorrent(jobID, title, platf, platSlug string, isPC bool) {
	slog.Info("watching game torrent", "title", title, "platform", platf)
	maxWait := 7 * 24 * time.Hour
	start := time.Now()
	fileScanDone := false

	for time.Since(start) < maxWait {
		torrents := m.qb.GetTorrents(m.cfg.QBCategory)
		for _, t := range torrents {
			tName := t.Name
			if !titlesMatch(title, tName) {
				continue
			}

			// Layer 1: scan file list once metadata is available
			if !fileScanDone && t.Progress > 0 {
				m.jobs.Update(jobID, "detail", "Scanning file list...")
				isSafe, issues := safety.ScanTorrentFileList(m.qb, t.Hash)
				fileScanDone = true
				if !isSafe {
					slog.Warn("file list scan failed", "title", title, "issues", issues)
					m.jobs.UpdateMulti(jobID, map[string]interface{}{
						"status": "error",
						"error":  fmt.Sprintf("Blocked: %s", strings.Join(issues, "; ")),
						"detail": "Dangerous files detected - download cancelled",
					})
					m.qb.DeleteTorrent(t.Hash, true)
					return
				}
				m.jobs.Update(jobID, "detail", "File list clean. Downloading...")
			}

			// Wait for completion
			if t.Progress >= 1.0 || t.State == "stoppedUP" {
				contentPath := t.ContentPath
				savePath := t.SavePath
				if savePath == "" {
					savePath = m.cfg.QBSavePath
				}
				scanPath := contentPath
				if scanPath == "" {
					scanPath = filepath.Join(savePath, tName)
				}

				// Layer 2: ClamAV scan
				m.jobs.UpdateMulti(jobID, map[string]interface{}{
					"status": "scanning",
					"detail": "Running virus scan...",
				})
				isClean, infected := safety.ScanWithClamAV(scanPath, m.cfg.ClamAVContainer, m.cfg.ClamAVSocket, m.cfg.DockerSocket)
				if !isClean {
					slog.Warn("ClamAV found infections", "title", title, "infected", infected)
					detail := infected
					if len(detail) > 3 {
						detail = detail[:3]
					}
					m.jobs.UpdateMulti(jobID, map[string]interface{}{
						"status": "error",
						"error":  fmt.Sprintf("Virus detected: %s", strings.Join(detail, "; ")),
						"detail": "Infected files found - download quarantined",
					})
					m.qb.DeleteTorrent(t.Hash, true)
					return
				}

				m.jobs.UpdateMulti(jobID, map[string]interface{}{
					"status": "organizing",
					"detail": "Scans passed. Moving to library...",
				})
				m.organizeGame(jobID, &t, platf, platSlug, isPC)
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "error",
		"error":  "Timed out waiting for download",
	})
}

func (m *Manager) organizeGame(jobID string, torrent *qbit.Torrent, platf, platSlug string, isPC bool) {
	contentPath := torrent.ContentPath
	torrentName := torrent.Name
	torrentHash := torrent.Hash

	if contentPath == "" || !pathExists(contentPath) {
		savePath := torrent.SavePath
		if savePath == "" {
			savePath = m.cfg.QBSavePath
		}
		contentPath = filepath.Join(savePath, torrentName)
	}

	if !pathExists(contentPath) {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Cannot find downloaded files at %s", contentPath),
		})
		slog.Error("content path not found", "path", contentPath)
		return
	}

	// Platform detection from metadata
	if platSlug == "" && !isPC {
		if info, ok := platform.DetectPlatformFromMetadata(contentPath); ok {
			platf, platSlug, isPC = info.Name, info.Slug, info.IsPC
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"platform": platf, "platform_slug": platSlug, "is_pc": isPC,
			})
			slog.Info("detected platform from metadata", "platform", platf)
		}
	}

	// Platform detection from files/title
	if platSlug == "" && !isPC {
		if info, ok := platform.DetectPlatformFromFiles(contentPath, torrentName); ok {
			platf, platSlug, isPC = info.Name, info.Slug, info.IsPC
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"platform": platf, "platform_slug": platSlug, "is_pc": isPC,
			})
			slog.Info("detected platform from files/title", "platform", platf)
		}
	}

	if isPC {
		dest := filepath.Join(m.cfg.GamesVaultPath, sanitizeFilename(filepath.Base(contentPath)))
		if err := moveContent(contentPath, dest); err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": "Moved to GameVault",
		})
		writeMetadataSidecar(dest, torrentName, platf, platSlug, isPC, "torrent")
		m.TrackInLibrary(torrentName, platf, platSlug, isPC, dest, 0, "torrent", "prowlarr", "torrent:"+torrentHash)
		m.jobs.LogActivity("download_completed", torrentName, "Organized to GameVault", jobID, nil)
		slog.Info("PC game organized", "name", sanitizeLog(torrentName), "dest", sanitizeLog(dest))
	} else if platSlug != "" {
		// platSlug arrives from the download request; keep it a single path
		// component so it cannot climb out of the ROM library root.
		destDir := filepath.Join(m.cfg.GamesRomsPath, sanitizeFilename(platSlug))
		os.MkdirAll(destDir, 0755)
		dest := filepath.Join(destDir, sanitizeFilename(filepath.Base(contentPath)))
		if err := moveContent(contentPath, dest); err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": fmt.Sprintf("Moved to RomM (%s)", platf),
		})
		writeMetadataSidecar(dest, torrentName, platf, platSlug, isPC, "torrent")
		m.TrackInLibrary(torrentName, platf, platSlug, isPC, dest, 0, "torrent", "prowlarr", "torrent:"+torrentHash)
		m.jobs.LogActivity("download_completed", torrentName, fmt.Sprintf("Organized to %s", platf), jobID, nil)
		slog.Info("ROM organized", "name", sanitizeLog(torrentName), "dest", sanitizeLog(dest))

		// Experimental: extract archives
		m.maybeExtractArchives(jobID, dest)
	} else {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": "Downloaded (unknown platform, left in staging)",
		})
		slog.Warn("no platform slug, left in downloads", "name", torrentName)
		return // Don't delete torrent
	}

	m.qb.DeleteTorrent(torrentHash, true)
}

func (m *Manager) organizeWithScan(jobID string, torrent *qbit.Torrent, platf, platSlug string, isPC bool) {
	contentPath := torrent.ContentPath
	savePath := torrent.SavePath
	if savePath == "" {
		savePath = m.cfg.QBSavePath
	}
	tName := torrent.Name
	scanPath := contentPath
	if scanPath == "" {
		scanPath = filepath.Join(savePath, tName)
	}

	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "scanning", "detail": "Running virus scan...",
	})
	isClean, infected := safety.ScanWithClamAV(scanPath, m.cfg.ClamAVContainer, m.cfg.ClamAVSocket, m.cfg.DockerSocket)
	if !isClean {
		slog.Warn("ClamAV found infections", "title", tName, "infected", infected)
		detail := infected
		if len(detail) > 3 {
			detail = detail[:3]
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Virus detected: %s", strings.Join(detail, "; ")),
			"detail": "Infected files found - download quarantined",
		})
		m.qb.DeleteTorrent(torrent.Hash, true)
		return
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "organizing", "detail": "Scans passed. Moving to library...",
	})
	m.organizeGame(jobID, torrent, platf, platSlug, isPC)
}

func (m *Manager) ddlDownloadWorker(jobID, dlURL, vimmID, title, platf, platSlug string, isPC bool) {
	staging := m.cfg.QBSavePath
	if err := os.MkdirAll(staging, 0755); err != nil {
		slog.Error("cannot create staging dir", "path", staging, "error", err)
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("cannot create staging dir %s: %v", staging, err),
		})
		return
	}

	var filepath_ string
	var dlErr error

	if vimmID != "" {
		filepath_ = m.downloadVimmGame(vimmID, staging, jobID)
	} else if dlURL != "" {
		filepath_, dlErr = m.downloadDDL(dlURL, staging, jobID)
	}

	if filepath_ == "" || !pathExists(filepath_) {
		job, ok := m.jobs.Get(jobID)
		if ok {
			status, _ := job["status"].(string)
			if status != "error" {
				errMsg := "Download failed"
				if dlErr != nil {
					errMsg = fmt.Sprintf("Download failed: %v", dlErr)
				}
				m.jobs.UpdateMulti(jobID, map[string]interface{}{
					"status": "error", "error": errMsg,
				})
			}
		}
		return
	}

	// ClamAV scan
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "scanning", "detail": "Running virus scan...",
	})
	isClean, infected := safety.ScanWithClamAV(filepath_, m.cfg.ClamAVContainer, m.cfg.ClamAVSocket, m.cfg.DockerSocket)
	if !isClean {
		slog.Warn("ClamAV found infections in DDL", "title", title, "infected", infected)
		detail := infected
		if len(detail) > 3 {
			detail = detail[:3]
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Virus detected: %s", strings.Join(detail, "; ")),
		})
		os.Remove(filepath_)
		return
	}

	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "organizing", "detail": "Moving to library...",
	})
	m.organizeDDLFile(jobID, filepath_, title, platf, platSlug, isPC)
}

func (m *Manager) downloadDDL(dlURL, destPath, jobID string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, _ := http.NewRequest("GET", dlURL, nil)
	req.Header.Set("User-Agent", "Gamarr/1.0")

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("DDL download failed", "url", sanitizeLog(dlURL), "error", err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		slog.Error("DDL download failed", "url", sanitizeLog(dlURL), "status", resp.StatusCode)
		return "", fmt.Errorf("HTTP %d from server", resp.StatusCode)
	}

	total := resp.ContentLength
	cd := resp.Header.Get("Content-Disposition")
	fnRe := regexp.MustCompile(`filename="?([^";\n]+)"?`)
	var filename string
	if m := fnRe.FindStringSubmatch(cd); m != nil {
		filename = strings.TrimSpace(m[1])
	} else {
		parts := strings.Split(strings.Split(dlURL, "?")[0], "/")
		filename = parts[len(parts)-1]
	}
	// The filename comes from the remote server (Content-Disposition or URL);
	// never let it name a path outside the staging dir.
	filename = sanitizeFilename(filename)

	fp, err := safeChild(destPath, filename)
	if err != nil {
		slog.Error("DDL rejected unsafe filename", "filename", sanitizeLog(filename))
		return "", err
	}
	f, err := os.Create(fp)
	if err != nil {
		slog.Error("DDL cannot create file", "path", sanitizeLog(fp), "error", err)
		return "", fmt.Errorf("cannot create file %s: %w", fp, err)
	}
	defer f.Close()

	downloaded := int64(0)
	lastUpdate := time.Now()
	buf := make([]byte, 256*1024)
	var writeErr error

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr = f.Write(buf[:n]); writeErr != nil {
				break
			}
			downloaded += int64(n)
			if time.Since(lastUpdate) > 2*time.Second && total > 0 {
				pct := float64(downloaded) / float64(total) * 100
				m.jobs.Update(jobID, "detail",
					fmt.Sprintf("Downloading... %.1f%% (%s/%s)", pct, search.HumanSize(downloaded), search.HumanSize(total)))
				lastUpdate = time.Now()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				writeErr = readErr
			}
			break
		}
	}
	// Don't report a truncated file as a finished download — a dropped
	// connection or full disk would otherwise pass a partial archive to the
	// scan/organize pipeline as if it were complete.
	if writeErr != nil {
		os.Remove(fp)
		return "", fmt.Errorf("download interrupted after %s: %w", search.HumanSize(downloaded), writeErr)
	}
	if total > 0 && downloaded != total {
		os.Remove(fp)
		return "", fmt.Errorf("incomplete download: got %s of %s", search.HumanSize(downloaded), search.HumanSize(total))
	}
	m.jobs.Update(jobID, "detail", fmt.Sprintf("Downloaded %s", search.HumanSize(downloaded)))
	return fp, nil
}

var vimmFormRe = regexp.MustCompile(`<form[^>]*id=["']dl_form["'][^>]*>`)
var vimmActionRe = regexp.MustCompile(`action="([^"]+)"`)
var vimmMediaRe = regexp.MustCompile(`name="mediaId"\s+value="(\d+)"`)
var vimmJSMediaRe = regexp.MustCompile(`"ID":(\d+)`)
var vimmDLRe = regexp.MustCompile(`(//dl\d*\.vimm\.net/[^"']*)`)
var vimmDLNumRe = regexp.MustCompile(`dl(\d+)`)

func (m *Manager) downloadVimmGame(gameID, destPath, jobID string) string {
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	gameURL := fmt.Sprintf("https://vimm.net/vault/%s", gameID)
	m.jobs.Update(jobID, "detail", "Fetching game page...")

	req, _ := http.NewRequest("GET", gameURL, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Vimm fetch failed", "error", err)
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	pageText := string(body)

	if strings.Contains(pageText, "unavailable at the request of") {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": "Game removed by DMCA takedown",
		})
		return ""
	}

	// Extract form action and mediaId
	formTag := vimmFormRe.FindString(pageText)
	var actionURL, mediaID string

	if formTag != "" {
		if m := vimmActionRe.FindStringSubmatch(formTag); m != nil {
			actionURL = m[1]
		}
	}
	if m := vimmMediaRe.FindStringSubmatch(pageText); m != nil {
		mediaID = m[1]
	}

	// Fallbacks
	if mediaID == "" {
		if m := vimmJSMediaRe.FindStringSubmatch(pageText); m != nil {
			mediaID = m[1]
			slog.Info("Vimm: found mediaId from JS", "id", mediaID)
		}
	}
	if actionURL == "" {
		if m := vimmDLRe.FindStringSubmatch(pageText); m != nil && mediaID != "" {
			actionURL = m[1]
			slog.Info("Vimm: found dl URL from page", "url", actionURL)
		}
	}

	if actionURL == "" || mediaID == "" {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": "Could not find download form on Vimm",
		})
		return ""
	}

	if strings.HasPrefix(actionURL, "//") {
		actionURL = "https:" + actionURL
	}
	slog.Info("Vimm download", "action", actionURL, "mediaId", mediaID)

	m.jobs.Update(jobID, "detail", "Starting download from Vimm...")
	time.Sleep(3 * time.Second) // Respectful delay

	// Try action URL and alternates
	dlURLs := []string{actionURL}
	if n := vimmDLNumRe.FindStringSubmatch(actionURL); n != nil {
		dlURLs = append(dlURLs, fmt.Sprintf("https://download%s.vimm.net/download/", n[1]))
	}

	var dlResp *http.Response
	for _, dlURL := range dlURLs {
		form := strings.NewReader(fmt.Sprintf("mediaId=%s", mediaID))
		req, _ := http.NewRequest("POST", dlURL, form)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", gameURL)
		req.Header.Set("Origin", "https://vimm.net")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		streamClient := &http.Client{
			Timeout: 10 * time.Minute,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		r, err := streamClient.Do(req)
		if err != nil {
			slog.Warn("Vimm download failed", "url", dlURL, "error", err)
			continue
		}
		if r.StatusCode == 200 {
			slog.Info("Vimm download started", "url", dlURL)
			dlResp = r
			break
		}
		r.Body.Close()
		slog.Warn("Vimm download rejected", "url", dlURL, "status", r.StatusCode)
	}

	if dlResp == nil {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Vimm download server rejected request (tried %d URLs)", len(dlURLs)),
		})
		return ""
	}
	defer dlResp.Body.Close()

	total := dlResp.ContentLength
	cd := dlResp.Header.Get("Content-Disposition")
	fnRe := regexp.MustCompile(`filename="?([^";\n]+)"?`)
	filename := fmt.Sprintf("%s.7z", gameID)
	if fm := fnRe.FindStringSubmatch(cd); fm != nil {
		filename = strings.TrimSpace(fm[1])
	}
	// The filename comes from the remote server (Content-Disposition); never let
	// it name a path outside the staging dir.
	filename = sanitizeFilename(filename)

	fp, err := safeChild(destPath, filename)
	if err != nil {
		slog.Error("Vimm rejected unsafe filename", "filename", sanitizeLog(filename))
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": "Vimm returned an unsafe filename",
		})
		return ""
	}
	f, err := os.Create(fp)
	if err != nil {
		return ""
	}
	defer f.Close()

	downloaded := int64(0)
	buf := make([]byte, 256*1024)
	var writeErr error
	for {
		n, readErr := dlResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr = f.Write(buf[:n]); writeErr != nil {
				break
			}
			downloaded += int64(n)
			if total > 0 {
				pct := float64(downloaded) / float64(total) * 100
				m.jobs.Update(jobID, "detail",
					fmt.Sprintf("Downloading... %.1f%% (%s/%s)", pct, search.HumanSize(downloaded), search.HumanSize(total)))
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				writeErr = readErr
			}
			break
		}
	}
	// A dropped connection or full disk must not pass a truncated .7z off as a
	// finished download — it would land in the library as a complete game.
	if writeErr != nil || (total > 0 && downloaded != total) {
		os.Remove(fp)
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Vimm download incomplete (%s of %s)", search.HumanSize(downloaded), search.HumanSize(total)),
		})
		return ""
	}
	m.jobs.Update(jobID, "detail", fmt.Sprintf("Downloaded %s", search.HumanSize(downloaded)))
	return fp
}

func (m *Manager) organizeDDLFile(jobID, fp, title, platf, platSlug string, isPC bool) {
	filename := sanitizeFilename(filepath.Base(fp))
	if isPC {
		dest := filepath.Join(m.cfg.GamesVaultPath, filename)
		if err := moveFile(fp, dest); err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": "Moved to GameVault",
		})
		writeMetadataSidecar(dest, title, platf, platSlug, isPC, "ddl")
		m.TrackInLibrary(title, platf, platSlug, isPC, dest, 0, "ddl", "ddl", "ddl:"+dest)
		m.jobs.LogActivity("download_completed", title, "DDL to GameVault", jobID, nil)
		slog.Info("DDL PC game organized", "file", sanitizeLog(filename), "dest", sanitizeLog(dest))
	} else if platSlug != "" {
		destDir := filepath.Join(m.cfg.GamesRomsPath, sanitizeFilename(platSlug))
		os.MkdirAll(destDir, 0755)
		dest := filepath.Join(destDir, filename)
		if err := moveFile(fp, dest); err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": fmt.Sprintf("Moved to RomM (%s)", platf),
		})
		writeMetadataSidecar(dest, title, platf, platSlug, isPC, "ddl")
		m.TrackInLibrary(title, platf, platSlug, isPC, dest, 0, "ddl", "ddl", "ddl:"+dest)
		m.jobs.LogActivity("download_completed", title, fmt.Sprintf("DDL to %s", platf), jobID, nil)
		slog.Info("DDL ROM organized", "file", sanitizeLog(filename), "dest", sanitizeLog(dest))
		m.maybeExtractArchives(jobID, dest)
	} else {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": "Downloaded (unknown platform, left in staging)",
		})
	}
}

// RecoverOrphanedTorrents checks for existing game torrents and re-links them.
func (m *Manager) RecoverOrphanedTorrents() {
	// Retry login for up to 60s
	for attempt := 0; attempt < 12; attempt++ {
		if m.qb.Login() {
			break
		}
		slog.Info("orphan recovery: waiting for qBit", "attempt", attempt+1)
		time.Sleep(5 * time.Second)
		if attempt == 11 {
			slog.Warn("cannot check orphaned torrents - qBit login failed after retries")
			return
		}
	}

	torrents := m.qb.GetTorrents(m.cfg.QBCategory)
	pcReleaseGroups := map[string]bool{
		"skidrow": true, "codex": true, "fitgirl": true, "dodi": true,
		"gog": true, "plaza": true, "cpy": true, "empress": true,
		"rune": true, "razordox": true, "tinyiso": true, "elamigos": true, "repack": true,
	}
	platformHints := map[string]struct {
		Name string
		Slug string
		IsPC bool
	}{
		"wii": {"Wii", "wii", false}, "gamecube": {"GameCube", "ngc", false},
		"ngc": {"GameCube", "ngc", false}, "switch": {"Switch", "switch", false},
		"nsp": {"Switch", "switch", false}, "xci": {"Switch", "switch", false},
		"ps2": {"PS2", "ps2", false}, "ps3": {"PS3", "ps3", false},
		"psp": {"PSP", "psp", false}, "nds": {"DS", "nds", false},
		"3ds": {"3DS", "3ds", false}, "dreamcast": {"Dreamcast", "dc", false},
		"gba": {"Game Boy Advance", "gba", false},
	}

	for _, t := range torrents {
		jobID := newJobID()
		platf := "Unknown"
		var platSlug string
		isPC := false

		nameLower := strings.ToLower(t.Name)
		for grp := range pcReleaseGroups {
			if strings.Contains(nameLower, grp) {
				platf, platSlug, isPC = "PC", "", true
				break
			}
		}
		if !isPC {
			for hint, info := range platformHints {
				if strings.Contains(nameLower, hint) {
					platf, platSlug, isPC = info.Name, info.Slug, info.IsPC
					break
				}
			}
		}

		if t.Progress >= 1.0 {
			m.jobs.Set(jobID, map[string]interface{}{
				"status":        "completed_unorganized",
				"title":         t.Name,
				"platform":      platf,
				"platform_slug": platSlug,
				"is_pc":         isPC,
				"error":         nil,
				"detail":        "Completed - needs organizing (use organize button)",
			})
			slog.Info("recovered completed torrent", "name", t.Name)
		} else {
			m.jobs.Set(jobID, map[string]interface{}{
				"status":        "downloading",
				"title":         t.Name,
				"platform":      platf,
				"platform_slug": platSlug,
				"is_pc":         isPC,
				"error":         nil,
				"detail":        "Recovered - watching download...",
			})
			go m.watchGameTorrent(jobID, t.Name, platf, platSlug, isPC)
			slog.Info("recovered in-progress torrent", "name", t.Name, "progress", fmt.Sprintf("%.0f%%", t.Progress*100))
		}
	}
}

func (m *Manager) maybeExtractArchives(jobID, dest string) {
	settings := m.LoadSettings()
	if !settings.ExtractArchives {
		return
	}
	target := dest
	fi, err := os.Stat(dest)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		target = filepath.Dir(dest)
	}
	extracted := extractArchives(target)
	if len(extracted) > 0 {
		job, ok := m.jobs.Get(jobID)
		if ok {
			detail, _ := job["detail"].(string)
			m.jobs.Update(jobID, "detail", fmt.Sprintf("%s (extracted %d archive(s))", detail, len(extracted)))
		}
		slog.Info("extracted archives", "count", len(extracted))
	}
}

func extractArchives(directory string) []string {
	var extracted []string
	patterns := []string{"*.rar", "*.RAR", "*.zip", "*.ZIP", "*.7z"}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(directory, pattern))
		for _, archive := range matches {
			extractDir := archive + ".extracted"
			if pathExists(extractDir) {
				continue
			}
			os.MkdirAll(extractDir, 0755)
			ext := strings.ToLower(filepath.Ext(archive))

			var cmd *exec.Cmd
			if ext == ".rar" {
				cmd = exec.Command("unrar", "x", "-o+", "-y", archive, extractDir+"/")
			} else {
				cmd = exec.Command("7z", "x", fmt.Sprintf("-o%s", extractDir), "-y", archive)
			}
			if err := cmd.Run(); err != nil {
				slog.Warn("extraction failed", "archive", sanitizeLog(filepath.Base(archive)), "error", err)
				os.RemoveAll(extractDir)
				continue
			}
			extracted = append(extracted, archive)
			slog.Info("extracted archive", "name", sanitizeLog(filepath.Base(archive)))
		}
	}

	// Recurse into subdirectories
	entries, _ := os.ReadDir(directory)
	for _, e := range entries {
		if e.IsDir() && !strings.HasSuffix(e.Name(), ".extracted") {
			sub, err := safeChild(directory, e.Name())
			if err != nil {
				continue
			}
			extracted = append(extracted, extractArchives(sub)...)
		}
	}
	return extracted
}

func writeMetadataSidecar(destPath, title, platf, platSlug string, isPC bool, sourceType string) {
	meta := map[string]interface{}{
		"title":         title,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"source":        sourceType,
		"organized_at":  time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.MarshalIndent(meta, "", "  ")
	var sidecar string
	fi, err := os.Stat(destPath)
	if err == nil && fi.IsDir() {
		sidecar = filepath.Join(destPath, ".gamarr.json")
	} else {
		sidecar = destPath + ".gamarr.json"
	}
	if err := os.WriteFile(sidecar, data, 0644); err != nil {
		slog.Warn("failed to write metadata sidecar", "error", err)
	}
}

// moveFile moves a file, falling back to copy+delete for cross-device moves.
func moveFile(src, dest string) error {
	err := os.Rename(src, dest)
	if err == nil {
		return nil
	}
	// Cross-device link — copy and delete
	if err := copyFile(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

func moveContent(src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := copyDir(src, dest); err != nil {
			return err
		}
		return os.RemoveAll(src)
	}
	return moveFile(src, dest)
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("path %q escapes source dir", path)
		}
		target, err := safeChild(dest, rel)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LoadSettings loads settings from disk.
func (m *Manager) LoadSettings() *Settings {
	settingsFile := filepath.Join(m.cfg.DataDir, "settings.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return &Settings{ExtractArchives: m.cfg.ExtractArchives}
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return &Settings{ExtractArchives: m.cfg.ExtractArchives}
	}
	return &s
}

// SaveSettings saves settings to disk.
func (m *Manager) SaveSettings(s *Settings) {
	os.MkdirAll(m.cfg.DataDir, 0755)
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(filepath.Join(m.cfg.DataDir, "settings.json"), data, 0644)
}

// Settings for download behavior.
type Settings struct {
	ExtractArchives bool `json:"extract_archives"`
}

// DDL source management

func (m *Manager) LoadDDLSources() []map[string]interface{} {
	fp := filepath.Join(m.cfg.DataDir, "ddl_sources.json")
	data, err := os.ReadFile(fp)
	if err != nil {
		return nil
	}
	var sources []map[string]interface{}
	json.Unmarshal(data, &sources)
	return sources
}

func (m *Manager) SaveDDLSources(sources []map[string]interface{}) {
	os.MkdirAll(m.cfg.DataDir, 0755)
	data, _ := json.MarshalIndent(sources, "", "  ")
	os.WriteFile(filepath.Join(m.cfg.DataDir, "ddl_sources.json"), data, 0644)
}
