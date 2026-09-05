package download

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gamarr/internal/fileops"
	"gamarr/internal/nzbget"
	"gamarr/internal/sabnzbd"
)

// DownloadNZB starts a Usenet/NZB download. SABnzbd is preferred when both
// clients are configured to preserve the existing behavior.
func (m *Manager) DownloadNZB(sab *sabnzbd.Client, nzbURL, title, platf, platSlug string, isPC bool) (string, error) {
	if sab != nil {
		return m.downloadSABnzbd(sab, nzbURL, title, platf, platSlug, isPC)
	}
	if m.nzbget != nil {
		return m.downloadNZBGet(m.nzbget, nzbURL, title, platf, platSlug, isPC)
	}
	return "", fmt.Errorf("usenet download client not configured")
}

func (m *Manager) downloadSABnzbd(sab *sabnzbd.Client, nzbURL, title, platf, platSlug string, isPC bool) (string, error) {
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         title,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Sending to SABnzbd...",
		"source_type":   "nzb",
		"source_client": "sabnzbd",
	})
	m.jobs.LogActivity("download_started", title, "NZB via SABnzbd", jobID, nil)

	nzoID, err := sab.AddNZBByURL(nzbURL, title, m.cfg.SABnzbdCategory)
	if err != nil {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("SABnzbd error: %v", err),
		})
		return jobID, nil
	}
	m.jobs.Update(jobID, "detail", "Downloading via Usenet...")

	go m.watchSABnzbdDownload(sab, jobID, nzoID, title, platf, platSlug, isPC)
	return jobID, nil
}

func (m *Manager) downloadNZBGet(client *nzbget.Client, nzbURL, title, platf, platSlug string, isPC bool) (string, error) {
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         title,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Sending to NZBGet...",
		"source_type":   "nzb",
		"source_client": "nzbget",
	})
	m.jobs.LogActivity("download_started", title, "NZB via NZBGet", jobID, nil)

	nzbID, err := client.AddNZBByURL(nzbURL, title, m.cfg.NZBGetCategory)
	if err != nil {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		})
		return jobID, nil
	}
	m.jobs.Update(jobID, "detail", "Downloading via NZBGet...")
	m.jobs.Update(jobID, "nzb_id", nzbID)

	go m.watchNZBGetDownload(client, jobID, nzbID, title, platf, platSlug, isPC)
	return jobID, nil
}

// RecoverOrphanedNZBDownloads restarts watchers for persisted NZBGet jobs after
// a Gamarr restart. NZBGet owns the transfer, so reconnecting the watcher is
// enough to resume progress tracking and final organization.
func (m *Manager) RecoverOrphanedNZBDownloads() {
	if m.nzbget == nil {
		return
	}

	for _, item := range m.jobs.Items() {
		status, _ := item.Data["status"].(string)
		client, _ := item.Data["source_client"].(string)
		if client != "nzbget" || (status != "downloading" && status != "organizing") {
			continue
		}

		nzbID := int64Value(item.Data["nzb_id"])
		if nzbID <= 0 {
			m.jobs.UpdateMulti(item.ID, map[string]interface{}{
				"status": "error",
				"error":  "Cannot recover NZBGet download: missing NZB ID",
			})
			continue
		}

		title, _ := item.Data["title"].(string)
		platf, _ := item.Data["platform"].(string)
		platSlug, _ := item.Data["platform_slug"].(string)
		isPC, _ := item.Data["is_pc"].(bool)
		m.jobs.Update(item.ID, "detail", "Recovered NZBGet download; reconnecting watcher...")
		go m.watchNZBGetDownload(m.nzbget, item.ID, nzbID, title, platf, platSlug, isPC)
	}
}

func int64Value(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	default:
		return 0
	}
}

func (m *Manager) watchSABnzbdDownload(sab *sabnzbd.Client, jobID, nzoID, title, platf, platSlug string, isPC bool) {
	slog.Info("watching SABnzbd download", "title", title, "nzo_id", nzoID)
	maxWait := 7 * 24 * time.Hour
	start := time.Now()

	for time.Since(start) < maxWait {
		// Check queue first
		queue, err := sab.GetQueue()
		if err == nil {
			for _, slot := range queue {
				if slot.NZOID == nzoID {
					if slot.MB > 0 {
						pct := ((slot.MB - slot.MBLeft) / slot.MB) * 100
						m.jobs.Update(jobID, "detail",
							fmt.Sprintf("Downloading... %.1f%%", pct))
					}
					break
				}
			}
		}

		// Check history for completion
		history, err := sab.GetHistory(50)
		if err == nil {
			for _, slot := range history {
				if slot.NZOID == nzoID {
					if slot.Status == "Completed" {
						slog.Info("SABnzbd download completed", "title", title, "path", slot.Storage)
						m.jobs.UpdateMulti(jobID, map[string]interface{}{
							"status": "organizing",
							"detail": "NZB download complete. Organizing...",
						})
						m.organizeNZBDownloadWithClient(jobID, slot.Storage, title, platf, platSlug, isPC, "sabnzbd")
						return
					} else if slot.Status == "Failed" {
						m.jobs.UpdateMulti(jobID, map[string]interface{}{
							"status": "error",
							"error":  "SABnzbd download failed",
						})
						return
					}
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "error",
		"error":  "Timed out waiting for SABnzbd download",
	})
}

func (m *Manager) watchNZBGetDownload(client *nzbget.Client, jobID string, nzbID int64, title, platf, platSlug string, isPC bool) {
	slog.Info("watching NZBGet download", "title", title, "nzb_id", nzbID)
	maxWait := 7 * 24 * time.Hour
	start := time.Now()

	for time.Since(start) < maxWait {
		queue, err := client.GetQueue()
		if err == nil {
			for _, item := range queue {
				if item.NZBID != nzbID {
					continue
				}
				if item.FileSizeMB > 0 {
					pct := (float64(item.FileSizeMB-item.RemainingSizeMB) / float64(item.FileSizeMB)) * 100
					pct = max(0, min(100, pct))
					m.jobs.Update(jobID, "detail", fmt.Sprintf("Downloading via NZBGet... %.1f%%", pct))
				} else if item.Status != "" {
					m.jobs.Update(jobID, "detail", fmt.Sprintf("NZBGet: %s", strings.ToLower(item.Status)))
				}
				break
			}
		}

		history, err := client.GetHistory()
		if err == nil {
			for _, item := range history {
				if item.NZBID != nzbID {
					continue
				}
				status := strings.ToUpper(item.Status)
				switch {
				case strings.HasPrefix(status, "SUCCESS/"):
					storagePath := item.StoragePath()
					slog.Info("NZBGet download completed", "title", title, "path", storagePath)
					m.jobs.UpdateMulti(jobID, map[string]interface{}{
						"status": "organizing",
						"detail": "NZB download complete. Organizing...",
					})
					m.organizeNZBDownloadWithClient(jobID, storagePath, title, platf, platSlug, isPC, "nzbget")
					return
				case strings.HasPrefix(status, "FAILURE/"), strings.HasPrefix(status, "DELETED/"), strings.HasPrefix(status, "WARNING/"):
					m.jobs.UpdateMulti(jobID, map[string]interface{}{
						"status": "error",
						"error":  fmt.Sprintf("NZBGet download failed (%s)", item.Status),
					})
					return
				}
			}
		}

		time.Sleep(10 * time.Second)
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "error",
		"error":  "Timed out waiting for NZBGet download",
	})
}

func (m *Manager) organizeNZBDownload(jobID, storagePath, title, platf, platSlug string, isPC bool) {
	m.organizeNZBDownloadWithClient(jobID, storagePath, title, platf, platSlug, isPC, "sabnzbd")
}

func (m *Manager) organizeNZBDownloadWithClient(jobID, storagePath, title, platf, platSlug string, isPC bool, sourceClient string) {
	if storagePath == "" {
		m.failNZBOrganize(jobID)
		return
	}

	if !pathExists(storagePath) {
		// Gamarr can restart between moveContent and the job status update, and
		// the recovered watcher then re-enters organize with the staging path
		// already gone. When the content is sitting at its destination the
		// import did succeed, so finish the job instead of reporting a
		// completed import as a failure.
		if dest, mode, ok := m.nzbImportedDest(storagePath, platSlug, isPC); ok {
			m.completeNZBOrganize(jobID, dest, title, platf, platSlug, isPC, sourceClient, mode)
			return
		}
		m.failNZBOrganize(jobID)
		return
	}

	platf, platSlug, isPC = m.resolvePlatform(jobID, storagePath, title, platf, platSlug, isPC)

	dest, ok := m.nzbDestPath(storagePath, platSlug, isPC)
	if !ok {
		slog.Warn("no platform detected, left in staging", "title", sanitizeLog(title), "path", sanitizeLog(storagePath))
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed",
			"detail": "Downloaded (unknown platform, left in staging)",
		})
		return
	}
	if isPC {
		// Same decision, same helper as the torrent path: these two drifted once
		// already, with one refusing a duplicate while the other stored it twice.
		if occ, done, occupied := acceptOccupiedVault(dest, storagePath); occupied {
			if done {
				m.completeNZBOrganize(jobID, occ, title, platf, platSlug, isPC, sourceClient, fileops.ModeCopy)
				return
			}
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error",
				"error":  fmt.Sprintf("Organize failed: %v: %s", fileops.ErrDestinationOccupied, occ),
			})
			return
		}
	} else {
		os.MkdirAll(filepath.Dir(dest), 0755)
	}

	archive := isPC && m.vaultArchiveEnabled() && fileops.Archivable(storagePath)
	// An archive leaves the download in staging, unlike the move it replaces, and
	// it is deliberately not removed: a successful write to the vault says the
	// bytes reached the mount's cache, not the remote behind it, so nothing at
	// this layer can tell whether the archive is safe yet. Releasing the download
	// copy belongs to whatever can read the remote. The mode carries that
	// difference into the job detail, so the UI does not claim a move.
	mode := fileops.ModeMove
	var err error
	if archive {
		mode = fileops.ModeCopy
		dest = fileops.ArchiveDest(dest)
		err = fileops.Archive(storagePath, dest, nil)
	} else {
		err = moveContent(storagePath, dest)
	}
	if err != nil {
		if archive {
			slog.Error("vault archive failed, staging left in place",
				"src", storagePath, "dest", dest, "error", err)
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("Organize failed: %v", err),
		})
		return
	}

	m.completeNZBOrganize(jobID, dest, title, platf, platSlug, isPC, sourceClient, mode)
}

// nzbImportedDest finds content that already reached the library when the
// staging path is gone. Both vault layouts are checked, since the archive
// option may have been toggled between the import and a restart that
// re-enters organize.
func (m *Manager) nzbImportedDest(storagePath, platSlug string, isPC bool) (string, fileops.Mode, bool) {
	dest, ok := m.nzbDestPath(storagePath, platSlug, isPC)
	if !ok {
		return "", "", false
	}
	if pathExists(dest) {
		return dest, fileops.ModeMove, true
	}
	if isPC {
		// An archive import is always a copy, so the verb has to say so even on
		// the recovery route. Staging is gone here, so nothing can check the
		// archive against it: the name is all there is to go on.
		if archived := fileops.ArchiveDest(dest); pathExists(archived) {
			return archived, fileops.ModeCopy, true
		}
	}
	return "", "", false
}

// nzbDestPath returns the library destination for a finished Usenet download.
// The second return is false when the platform is unknown, in which case the
// content stays in staging.
func (m *Manager) nzbDestPath(storagePath, platSlug string, isPC bool) (string, bool) {
	base := filepath.Base(storagePath)
	switch {
	case isPC:
		return filepath.Join(m.cfg.GamesVaultPath, base), true
	case platSlug != "":
		return filepath.Join(m.cfg.GamesRomsPath, platSlug, base), true
	default:
		return "", false
	}
}

func (m *Manager) failNZBOrganize(jobID string) {
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "error",
		"error":  "Usenet storage path not found",
	})
}

// completeNZBOrganize marks the job done and registers the content in the
// library. TrackInLibrary dedupes on source ID, so re-entering this after a
// restart is safe.
func (m *Manager) completeNZBOrganize(jobID, dest, title, platf, platSlug string, isPC bool, sourceClient string, mode fileops.Mode) {
	label := "GameVault"
	if !isPC {
		label = fmt.Sprintf("RomM (%s)", platf)
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "completed",
		"detail": importDetail(mode, label),
	})
	writeMetadataSidecar(dest, title, platf, platSlug, isPC, "nzb")
	m.TrackInLibrary(title, platf, platSlug, isPC, dest, 0, "nzb", sourceClient, "nzb:"+dest)
	m.jobs.LogActivity("download_completed", title, fmt.Sprintf("NZB to %s", label), jobID, nil)

	// Also extract archives for ROMs if enabled
	if !isPC && platSlug != "" {
		m.maybeExtractArchives(jobID, dest)
	}
}

// RetryJob re-runs a failed import on its existing job row.
//
// It reuses the row rather than minting a new one, since a second row for the
// same torrent is what the UI draws as a duplicate card and the job store has no
// dedup. A retry that cannot start says why and leaves the job in the state it
// was already in: the UI offers a button for "error" and none at all for
// "queued", so parking it there is how a click became a dead end.
func (m *Manager) RetryJob(jobID string) (bool, string) {
	job, ok := m.jobs.Get(jobID)
	if !ok {
		return false, "Job not found"
	}
	status, _ := job["status"].(string)
	if status != "error" && status != "interrupted" && status != "dead_letter" {
		return false, fmt.Sprintf("Job not in failed state (status=%s)", status)
	}
	if vimmID := strVal(job, "vimm_id"); vimmID != "" {
		return m.retryVimmJob(jobID, job, vimmID)
	}
	hash := strVal(job, "info_hash")
	if hash == "" {
		return false, "This job has no torrent recorded, so there is nothing to import again"
	}
	torrent, found, err := m.torrentByHash(hash)
	if err != nil {
		return false, fmt.Sprintf("Cannot read the download client: %v", err)
	}
	if !found {
		return false, "The download client no longer holds this torrent"
	}
	if torrent.Progress < 1.0 {
		return false, "That download has not finished yet"
	}

	// Advisory only. The import's own claim is what excludes a second one; this
	// just answers with the specific reason before touching the row, since the
	// status alone cannot say whether an import is running.
	if _, busy := m.importing.Load(torrent.Hash); busy {
		return false, "An import is already running for this download"
	}

	retryCount := jobRetryCount(job)
	isPC, _ := job["is_pc"].(bool)

	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status":      "organizing",
		"error":       nil,
		"detail":      fmt.Sprintf("Retry #%d", retryCount+1),
		"retry_count": retryCount + 1,
	})
	m.jobs.LogActivity("download_retried", strVal(job, "title"), fmt.Sprintf("Retry #%d", retryCount+1), jobID, nil)
	go m.importFinishedTorrent("retry", jobID, torrent, strVal(job, "platform"), strVal(job, "platform_slug"), isPC)
	return true, fmt.Sprintf("Retrying (#%d)", retryCount+1)
}

func (m *Manager) retryVimmJob(jobID string, job map[string]interface{}, vimmID string) (bool, string) {
	if _, busy := m.activeDDL.LoadOrStore(jobID, struct{}{}); busy {
		return false, "A direct-download retry is already running for this job"
	}

	retryCount := jobRetryCount(job)
	isPC, _ := job["is_pc"].(bool)
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status":      "downloading",
		"error":       nil,
		"detail":      fmt.Sprintf("Retry #%d", retryCount+1),
		"retry_count": retryCount + 1,
	})
	m.jobs.LogActivity("download_retried", strVal(job, "title"), fmt.Sprintf("Retry #%d", retryCount+1), jobID, nil)

	go func() {
		defer m.activeDDL.Delete(jobID)
		m.runDDLDownloadWorker(jobID, "", vimmID, strVal(job, "title"), strVal(job, "platform"), strVal(job, "platform_slug"), isPC)
	}()
	return true, fmt.Sprintf("Retrying (#%d)", retryCount+1)
}

func jobRetryCount(job map[string]interface{}) int {
	switch rc := job["retry_count"].(type) {
	case int:
		return rc
	case int64:
		return int(rc)
	case float64:
		return int(rc)
	default:
		return 0
	}
}

func strVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// AutoRetryFailed checks for failed jobs and retries those under max_retries.
func (m *Manager) AutoRetryFailed() {
	for _, item := range m.jobs.Items() {
		status, _ := item.Data["status"].(string)
		if status != "error" {
			continue
		}
		retryCount := jobRetryCount(item.Data)
		if retryCount >= m.cfg.MaxRetries {
			// Move to dead letter (status is always "error" here).
			m.jobs.UpdateMulti(item.ID, map[string]interface{}{
				"status": "dead_letter",
				"detail": fmt.Sprintf("Max retries (%d) exceeded", m.cfg.MaxRetries),
			})
			continue
		}
		// Check if enough time has passed for backoff
		// Simple: don't auto-retry, let the user or monitor do it
	}
}
