package download

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gamarr/internal/db"
	"gamarr/internal/sabnzbd"
)

// DownloadNZB starts a Usenet/NZB download via SABnzbd.
func (m *Manager) DownloadNZB(sab *sabnzbd.Client, nzbURL, title, platf, platSlug string, isPC bool) (string, error) {
	if sab == nil {
		return "", fmt.Errorf("SABnzbd not configured")
	}

	jobID := newJobID()
	m.jobs.Set(jobID, db.Job{
		db.JobStatus:       db.StatusDownloading,
		db.JobTitle:        title,
		db.JobPlatform:     platf,
		db.JobPlatformSlug: platSlug,
		db.JobIsPC:         isPC,
		db.JobError:        nil,
		db.JobDetail:       "Sending to SABnzbd...",
		"source_type":      "nzb",
	})
	m.jobs.LogActivity("download_started", title, "NZB via SABnzbd", jobID, nil)

	nzoID, err := sab.AddNZBByURL(nzbURL, title, m.cfg.SABnzbdCategory)
	if err != nil {
		m.jobs.UpdateMulti(jobID, db.Job{
			db.JobStatus: db.StatusError,
			db.JobError:  fmt.Sprintf("SABnzbd error: %v", err),
		})
		return jobID, nil
	}
	m.jobs.Update(jobID, db.JobDetail, "Downloading via Usenet...")

	go m.watchSABnzbdDownload(sab, jobID, nzoID, title, platf, platSlug, isPC)
	return jobID, nil
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
						m.jobs.Update(jobID, db.JobDetail,
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
						m.jobs.UpdateMulti(jobID, db.Job{
							db.JobStatus: db.StatusOrganizing,
							db.JobDetail: "NZB download complete. Organizing...",
						})
						m.organizeNZBDownload(jobID, slot.Storage, title, platf, platSlug, isPC)
						return
					} else if slot.Status == "Failed" {
						m.jobs.UpdateMulti(jobID, db.Job{
							db.JobStatus: db.StatusError,
							db.JobError:  "SABnzbd download failed",
						})
						return
					}
				}
			}
		}

		select {
		case <-m.ctx.Done():
			// Shutting down: leave the job as-is; JobStore.loadAll marks
			// in-flight jobs "interrupted" on the next start.
			return
		case <-time.After(10 * time.Second):
		}
	}
	m.jobs.UpdateMulti(jobID, db.Job{
		db.JobStatus: db.StatusError,
		db.JobError:  "Timed out waiting for SABnzbd download",
	})
}

func (m *Manager) organizeNZBDownload(jobID, storagePath, title, platf, platSlug string, isPC bool) {
	if storagePath == "" || !pathExists(storagePath) {
		m.jobs.UpdateMulti(jobID, db.Job{
			db.JobStatus: db.StatusError,
			db.JobError:  "SABnzbd storage path not found",
		})
		return
	}

	var dest string
	if isPC {
		dest = filepath.Join(m.cfg.GamesVaultPath, filepath.Base(storagePath))
	} else if platSlug != "" {
		destDir := filepath.Join(m.cfg.GamesRomsPath, platSlug)
		os.MkdirAll(destDir, 0755)
		dest = filepath.Join(destDir, filepath.Base(storagePath))
	} else {
		m.jobs.UpdateMulti(jobID, db.Job{
			db.JobStatus: db.StatusCompleted,
			db.JobDetail: "Downloaded (unknown platform, left in staging)",
		})
		return
	}

	if err := moveContent(storagePath, dest); err != nil {
		m.jobs.UpdateMulti(jobID, db.Job{
			db.JobStatus: db.StatusError,
			db.JobError:  fmt.Sprintf("Organize failed: %v", err),
		})
		return
	}

	label := "GameVault"
	if !isPC {
		label = fmt.Sprintf("RomM (%s)", platf)
	}
	m.jobs.UpdateMulti(jobID, db.Job{
		db.JobStatus: db.StatusCompleted,
		db.JobDetail: fmt.Sprintf("Moved to %s", label),
	})
	writeMetadataSidecar(dest, title, platf, platSlug, isPC, "nzb")
	m.TrackInLibrary(title, platf, platSlug, isPC, dest, 0, "nzb", "sabnzbd", "nzb:"+dest)
	m.jobs.LogActivity("download_completed", title, fmt.Sprintf("NZB to %s", label), jobID, nil)

	// Also extract archives for ROMs if enabled
	if !isPC && platSlug != "" {
		m.maybeExtractArchives(jobID, dest)
	}
}

// RetryJob retries a failed download job.
func (m *Manager) RetryJob(jobID string) (bool, string) {
	job, ok := m.jobs.Get(jobID)
	if !ok {
		return false, "Job not found"
	}
	status := job.Status()
	if status != db.StatusError && status != db.StatusInterrupted && status != db.StatusDeadLetter {
		return false, fmt.Sprintf("Job not in failed state (status=%s)", status)
	}

	retryCount := job.RetryCount()

	m.jobs.UpdateMulti(jobID, db.Job{
		db.JobStatus:     db.StatusQueued,
		db.JobError:      nil,
		db.JobDetail:     fmt.Sprintf("Retry #%d queued", retryCount+1),
		db.JobRetryCount: retryCount + 1,
	})
	m.jobs.LogActivity("download_retried", job.Title(), fmt.Sprintf("Retry #%d", retryCount+1), jobID, nil)
	return true, fmt.Sprintf("Job re-queued (retry #%d)", retryCount+1)
}

func strVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// AutoRetryFailed checks for failed jobs and retries those under max_retries.
func (m *Manager) AutoRetryFailed() {
	for _, item := range m.jobs.Items() {
		if item.Data.Status() != db.StatusError {
			continue
		}
		retryCount := item.Data.RetryCount()
		if retryCount >= m.cfg.MaxRetries {
			// Move to dead letter (status is always "error" here).
			m.jobs.UpdateMulti(item.ID, db.Job{
				db.JobStatus: db.StatusDeadLetter,
				db.JobDetail: fmt.Sprintf("Max retries (%d) exceeded", m.cfg.MaxRetries),
			})
			continue
		}
		// Check if enough time has passed for backoff
		// Simple: don't auto-retry, let the user or monitor do it
	}
}
