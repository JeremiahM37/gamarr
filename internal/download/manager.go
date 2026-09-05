// Package download orchestrates torrent, DDL, and NZB downloads across
// clients and watches for completed transfers to import into the library.
package download

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/db"
	"gamarr/internal/fileops"
	"gamarr/internal/flaresolverr"
	"gamarr/internal/nzbget"
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
	nzbget       *nzbget.Client
	NotifyFunc   NotifyCallback

	// importing holds the download hashes an import is running for. Two imports
	// of one download race for its path: whichever moves it first wins, and the
	// loser stats the emptied path, reads it as missing and writes a failure
	// blaming the user's mounts. Keyed by hash rather than job id because
	// OrganizeTorrent mints a fresh job per call, so two rows can name one
	// physical download.
	importing sync.Map

	// watching holds the download hashes a watcher goroutine is polling for.
	// Keyed by hash rather than job id, and deliberately not inferred from the
	// job row: a row is persisted and outlives the process, while the goroutine
	// does not, so after a restart every row needs a watcher again and the row
	// cannot stand in for the claim.
	watching sync.Map

	// A FlareSolverr request launches a browser. Serializing those requests
	// avoids a batch of Vimm jobs exhausting a small solver container; file
	// downloads can proceed concurrently as soon as each media ID is known.
	flareSolverrMu sync.Mutex
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
	if cfg.HasNZBGet() {
		mgr.nzbget = nzbget.New(cfg.NZBGetURL, cfg.NZBGetUser, cfg.NZBGetPass)
		slog.Info("NZBGet client initialized", "url", cfg.NZBGetURL)
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

// NZBGet returns the NZBGet client (may be nil).
func (m *Manager) NZBGet() *nzbget.Client { return m.nzbget }

// newJobID generates an 8-char job ID.
func newJobID() string {
	b := make([]byte, 4)
	_, _ = io.ReadFull(cryptoReader(), b)
	return fmt.Sprintf("%x", b)
}

// DownloadTorrent starts a torrent download.
// Tries clients in order: qBittorrent -> Transmission -> Deluge (first available).
func (m *Manager) DownloadTorrent(url, infoHash, title, platf, platSlug string, isPC bool) (string, error) {
	if url == "" {
		return "", fmt.Errorf("no download URL")
	}
	jobID := newJobID()
	m.jobs.Set(jobID, map[string]interface{}{
		"status":        "downloading",
		"title":         title,
		"info_hash":     infoHash,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Sending to download client...",
	})

	added := false
	clientUsed := ""
	var knownBefore map[string]bool

	// Try qBittorrent first.
	if m.cfg.HasQBittorrent() {
		m.jobs.Update(jobID, "detail", "Sending to qBittorrent...")
		// Taken before the add, and only when it will be used: qBittorrent's add
		// returns no id, so the only thing identifying the torrent it created is
		// that it was not there a moment ago. When the indexer published a hash
		// there is nothing to resolve, and this listing sits on the request path.
		if infoHash == "" {
			knownBefore = m.hashesInCategory()
		}
		ok := m.qb.AddTorrent(url, title, m.cfg.QBSavePath, m.cfg.QBCategory)
		if ok {
			added = true
			clientUsed = "qBittorrent"
		} else {
			slog.Warn("qBittorrent add failed, trying fallback clients", "title", title)
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
		}
	}

	if !added {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error":  "Failed to add torrent to any download client",
		})
		return jobID, nil
	}

	m.jobs.Update(jobID, "detail", fmt.Sprintf("Downloading via %s...", clientUsed))
	slog.Info("torrent added", "client", clientUsed, "title", title)

	go func() {
		hash := infoHash
		// An indexer is not obliged to publish an infohash - FitGirl rows carry
		// none - which leaves the job bound to its torrent by title, and a
		// repack's release title and torrent name have nothing in common. The
		// client knows the hash, so ask it rather than giving up the binding.
		// Off the request path because it polls.
		if hash == "" && clientUsed == "qBittorrent" {
			if resolved, ok := m.resolveAddedHash(knownBefore, title); ok {
				hash = resolved
				m.jobs.Update(jobID, "info_hash", resolved)
				slog.Info("resolved infohash from client", "title", title, "hash", resolved)
			} else {
				slog.Warn("no infohash from client, matching on title", "title", title)
			}
		}
		m.watchGameTorrent(jobID, hash, title, platf, platSlug, isPC)
	}()
	return jobID, nil
}

// hashesInCategory snapshots the hashes the client already holds. A nil result
// means the listing FAILED, which is not the same as the category being empty:
// without the distinction a failed snapshot makes every existing torrent look
// new, and resolveAddedHash would return whichever one it saw first.
func (m *Manager) hashesInCategory() map[string]bool {
	torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
	if err != nil {
		slog.Warn("could not snapshot the download client before adding", "error", err)
		return nil
	}
	known := make(map[string]bool, len(torrents))
	for _, t := range torrents {
		known[strings.ToLower(t.Hash)] = true
	}
	return known
}

// resolveAddedHash identifies the torrent the client just accepted by
// eliminating the ones it already had. Polls because a magnet takes a moment to
// show up in the listing.
func (m *Manager) resolveAddedHash(knownBefore map[string]bool, title string) (string, bool) {
	if knownBefore == nil {
		return "", false
	}
	for attempt := 0; attempt < 20; attempt++ {
		torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
		if err == nil {
			var fresh []qbit.Torrent
			for _, t := range torrents {
				if !knownBefore[strings.ToLower(t.Hash)] {
					fresh = append(fresh, t)
				}
			}
			if len(fresh) == 1 {
				return fresh[0].Hash, true
			}
			// Two adds can overlap, so elimination alone is ambiguous here.
			// Prefer the title over picking one arbitrarily.
			for _, t := range fresh {
				if titlesMatch(title, t.Name) {
					return t.Hash, true
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return "", false
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
	torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
	if err != nil {
		return "", fmt.Errorf("cannot read the download client: %w", err)
	}
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
		"info_hash":     torrent.Hash,
		"platform":      platf,
		"platform_slug": platSlug,
		"is_pc":         isPC,
		"error":         nil,
		"detail":        "Scanning and organizing...",
	})

	// By value: the retry reassigns its torrent as the client republishes it, and
	// torrent points into the slice this read back from the client.
	go m.importFinishedTorrent("manual organize", jobID, *torrent, platf, platSlug, isPC)
	return jobID, nil
}

func (m *Manager) watchGameTorrent(jobID, infoHash, title, platf, platSlug string, isPC bool) {
	// One watcher per torrent, whoever asks. Orphan recovery runs at startup and
	// again on the monitor's run_orphan_recovery command, so a later pass would
	// otherwise start a rival watcher on a torrent already being watched and both
	// would race to import it. The title fallback covers indexers that report no
	// infohash, matching what JobMatchesTorrent does.
	claim := strings.ToLower(infoHash)
	if claim == "" {
		claim = "title:" + strings.ToLower(title)
	}
	if _, busy := m.watching.LoadOrStore(claim, struct{}{}); busy {
		slog.Info("a watcher is already running for this torrent", "title", title)
		return
	}
	defer m.watching.Delete(claim)

	slog.Info("watching game torrent", "title", title, "platform", platf)
	maxWait := 7 * 24 * time.Hour
	start := time.Now()
	fileScanDone := false

	for time.Since(start) < maxWait {
		torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
		if err != nil {
			slog.Warn("could not read the download client", "title", title, "error", err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, t := range torrents {
			tName := t.Name
			if !JobMatchesTorrent(infoHash, title, t.Hash, tName) {
				continue
			}

			// Layer 1: scan file list once metadata is available
			if m.cfg.FileListScanEnabled && !fileScanDone && t.Progress > 0 {
				m.jobs.Update(jobID, "detail", "Scanning file list...")
				isSafe, issues := safety.ScanTorrentFileList(m.qb, t.Hash)
				fileScanDone = true
				if !isSafe {
					slog.Warn("file list scan failed", "title", title, "issues", issues)
					// Stopping is enough to keep the files from being imported
					// or run; deleting them throws away a download the
					// operator may well consider legitimate.
					detail := "Dangerous files detected - torrent stopped for review"
					if !m.qb.StopTorrent(t.Hash) {
						slog.Error("could not stop torrent after failed file list scan", "title", title, "hash", t.Hash)
						detail = "Dangerous files detected - could not stop the torrent, review it in your client"
					}
					m.jobs.UpdateMulti(jobID, map[string]interface{}{
						"status": "error",
						"error":  fmt.Sprintf("Blocked: %s", strings.Join(issues, "; ")),
						"detail": detail,
					})
					return
				}
				m.jobs.Update(jobID, "detail", "File list clean. Downloading...")
			}

			// Wait for completion
			if t.Progress >= 1.0 || t.State == "stoppedUP" {
				m.importFinishedTorrent("job watch", jobID, t, platf, platSlug, isPC)
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

// resolvePlatform runs the detection cascade over finished content and records
// what it finds on the job row. Every import path calls it, so none can drift
// from the others on what a download turns out to be.
func (m *Manager) resolvePlatform(jobID, contentPath, title, platf, platSlug string, isPC bool) (string, string, bool) {
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
		if info, ok := platform.DetectPlatformFromFiles(contentPath, title); ok {
			platf, platSlug, isPC = info.Name, info.Slug, info.IsPC
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"platform": platf, "platform_slug": platSlug, "is_pc": isPC,
			})
			slog.Info("detected platform from files/title", "platform", platf)
		}
	}

	// A PC classification can arrive from an ambiguous category rather than a
	// real PC release: Newznab 4050 is PC/Games, but it is also where Prowlarr
	// files Nyaa's Software - Games, which is how Switch ROMs get in. The two
	// detections above are skipped once isPC is set, so without this a Nyaa
	// Switch ROM imports into GameVault instead of the Switch ROM library.
	if isPC {
		if info, ok := platform.DetectConsoleROM(contentPath); ok {
			platf, platSlug, isPC = info.Name, info.Slug, info.IsPC
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"platform": platf, "platform_slug": platSlug, "is_pc": isPC,
			})
			slog.Info("reclassified PC-tagged download from its ROM files", "platform", platf)
		}
	}

	return platf, platSlug, isPC
}

// organizeGame imports a finished torrent, reporting whether a failure is worth
// another attempt later. Only a content path that is not there yet is: the
// client may still be moving files into place when the download reads complete.
func (m *Manager) organizeGame(jobID string, torrent *qbit.Torrent, platf, platSlug string, isPC bool) (retryable bool) {
	contentPath := torrent.ContentPath
	torrentName := torrent.Name
	torrentHash := torrent.Hash

	// content_path is the client's own answer and the only authoritative one.
	// Joining the save path to the torrent's DISPLAY name is a guess, and it
	// resolves for nothing whose internal folder differs from its title, which
	// is every FitGirl release. Guess only when the client gave nothing.
	if contentPath == "" {
		savePath := torrent.SavePath
		if savePath == "" {
			savePath = m.cfg.QBSavePath
		}
		contentPath = filepath.Join(savePath, torrentName)
	}

	if _, statErr := os.Stat(contentPath); statErr != nil {
		// Only a path that is not there yet comes good on its own. A permission
		// error, or a file where a directory belongs, reads the same way on
		// every attempt, and its errno is the only thing naming the cause, so
		// neither gets retried nor discarded.
		//
		// The path comes from the download client. Gamarr has no remote path
		// mapping, so the client's paths have to resolve identically inside
		// this container — the usual cause of a path that exists for the
		// client and not for Gamarr.
		missing := errors.Is(statErr, os.ErrNotExist)
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"error": fmt.Sprintf("Cannot read the downloaded files at %s: %v — this is the path "+
				"the download client reported; Gamarr must see it at the same path, so "+
				"check the two are mounted the same way", contentPath, statErr),
		})
		slog.Error("content path not readable", "path", contentPath, "error", statErr, "retryable", missing)
		return missing
	}

	platf, platSlug, isPC = m.resolvePlatform(jobID, contentPath, torrentName, platf, platSlug, isPC)

	// DetectConsoleROM above is the first guard on this boundary and is narrow
	// by design, so everything it does not cover arrives here and this if/else
	// is the rest of the guarantee: is_pc comes in unvalidated on several
	// request bodies and is never cross-checked against platform_slug, so a
	// caller can still route a ROM to the vault. Do not restructure it into a
	// form that can reach both arms.
	var importMode fileops.Mode
	if isPC {
		dest, mode, err := m.importToVault(contentPath)
		importMode = mode
		if err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return false
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": importDetail(mode, "GameVault"),
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
		mode, err := m.importContent(contentPath, dest)
		importMode = mode
		if err != nil {
			m.jobs.UpdateMulti(jobID, map[string]interface{}{
				"status": "error", "error": fmt.Sprintf("Organize failed: %v", err),
			})
			return false
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": importDetail(mode, fmt.Sprintf("RomM (%s)", platf)),
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
		return false // Don't delete torrent
	}

	m.finishTorrent(torrentHash, torrentName, importMode)
	return false
}

// importToVault places finished PC content in the vault and reports the path it
// landed at along with the mode that got it there.
//
// Under VAULT_ARCHIVE_ENABLED a directory is written as a single tar instead of
// being imported as a folder. Writing the archive is itself always a copy, so a
// source-preserving mode reports one and the torrent is left seedable; under
// move the download is dropped, but only once the published archive is
// confirmed to stand in for it.
func (m *Manager) importToVault(src string) (string, fileops.Mode, error) {
	base := filepath.Join(m.cfg.GamesVaultPath, sanitizeFilename(filepath.Base(src)))

	// Occupancy is decided before either branch, and both take the same answer.
	// Deciding it per branch is how the archive path came to refuse a duplicate
	// while the plain path stored the same game a second time beside it.
	if occ, done, occupied := acceptOccupiedVault(base, src); occupied {
		if done {
			// Copy however the import is configured. The occupant is only known
			// to be big enough to be an archive of src, which cannot tell this
			// build from another of the same game, so honouring move here would
			// drop a newer download and keep the older build in the library.
			return occ, fileops.ModeCopy, nil
		}
		return occ, fileops.ModeCopy, fmt.Errorf("%w: %s", fileops.ErrDestinationOccupied, occ)
	}

	if m.vaultArchiveEnabled() && fileops.Archivable(src) {
		dest := fileops.ArchiveDest(base)
		if err := fileops.Archive(src, dest); err != nil {
			slog.Error("vault archive failed, download left in place",
				"src", sanitizeLog(src), "dest", sanitizeLog(dest), "error", err)
			return dest, fileops.ModeCopy, err
		}
		return dest, m.archivedImportMode(dest, src), nil
	}
	mode, err := m.importContent(src, base)
	return base, mode, err
}

// verifyArchive indirects fileops.VerifyArchive so a test can fail the check
// that authorises dropping a download. It is otherwise unreachable, since the
// only caller runs it on an archive Archive has just written successfully.
var verifyArchive = fileops.VerifyArchive

// archivedImportMode reports the mode an archive this import just wrote counts
// as. Only for an archive written from src: one that was already there cannot be
// told apart from an archive of another build.
//
// A mode that drops the source needs the published archive confirmed to stand
// in for it first. Failing that the import counts as a copy and the download
// stays, which costs disk rather than content.
func (m *Manager) archivedImportMode(dest, src string) fileops.Mode {
	mode := m.importOptions().Mode
	if mode.PreservesSource() {
		// The archive was written, not linked, so a copy is what happened. Every
		// preserving mode takes the same finishTorrent branch, so this changes
		// only the verb the UI reports, and it makes it true.
		return fileops.ModeCopy
	}
	if err := verifyArchive(dest, src); err != nil {
		slog.Error("keeping the download: the vault archive cannot be confirmed to stand in for it",
			"dest", sanitizeLog(dest), "error", err)
		return fileops.ModeCopy
	}
	return mode
}

// acceptOccupiedVault decides what an already-occupied vault destination means
// for an import of src: the occupant, whether the import counts as already done,
// and whether anything was there at all.
//
// It returns the path that EXISTS, never the one this import would have written.
// A library row aimed at a path nothing wrote reads as a stored game to whatever
// releases download copies, so the two must not be confused.
//
// An occupant only counts as this import when it could be an archive of src.
// "Something is at this name" also covers a stale archive of another build, a
// truncated leftover and a hand-placed file, and accepting those reports content
// as stored that was never stored.
func acceptOccupiedVault(base, src string) (dest string, done, occupied bool) {
	occ, exists := fileops.VaultOccupied(base)
	if !exists {
		return "", false, false
	}
	if fileops.ArchiveHolds(occ, src) {
		// Either a crash lost the job update after publishing, or a collision was
		// swallowed. This is the only trace of either.
		slog.Warn("vault already holds this game, treating the import as done",
			"dest", sanitizeLog(occ), "src", sanitizeLog(src))
		return occ, true, true
	}
	return occ, false, true
}

// finishTorrent decides what happens to the torrent once its content is in the
// library. Under a move import the data is gone from the download directory
// anyway, so the torrent goes with it. Under a source-preserving import the
// torrent is still seedable — removing it (or its files) would throw away the
// ratio the user imported this way to keep, so it is left alone unless
// REMOVE_TORRENT_AFTER_IMPORT asks otherwise, and even then the files stay.
func (m *Manager) finishTorrent(hash, name string, mode fileops.Mode) {
	if !mode.PreservesSource() {
		m.qb.DeleteTorrent(hash, true)
		return
	}
	if m.cfg.RemoveAfterImport {
		m.qb.DeleteTorrent(hash, false)
		slog.Info("removed torrent after import, files kept", "name", sanitizeLog(name), "mode", string(mode))
		return
	}
	slog.Info("torrent left seeding after import", "name", sanitizeLog(name), "mode", string(mode))
}

// importDetail describes a finished import for the job feed, so the UI does
// not claim content was "moved" when it was hardlinked in place.
func importDetail(mode fileops.Mode, target string) string {
	verb := "Moved to"
	switch mode {
	case fileops.ModeHardlink:
		verb = "Hardlinked to"
	case fileops.ModeSymlink:
		verb = "Symlinked to"
	case fileops.ModeCopy:
		verb = "Copied to"
	}
	return fmt.Sprintf("%s %s", verb, target)
}

// How many times an import is re-attempted when the content path is simply not
// there yet, and how long it waits between attempts. The watcher fires the
// moment progress reads complete, which is while the client may still be
// publishing the finished files, so the first attempt can lose that race.
// Tests shorten both.
var (
	importAttempts   = 20
	importRetryDelay = 30 * time.Second
)

// importFinishedTorrent scans and imports a finished torrent, retrying while its
// content path is simply not there yet, and reports whether the job completed.
//
// A client publishes a finished download by renaming it into place, so a path
// missing the instant progress reads complete is a race rather than a verdict.
// Every caller that imports comes through here, which is what makes the retry
// and the claim below cover all of them; the returned bool is a convenience for
// callers that act on success, since the terminal state is written to the job
// row here before returning either way.
func (m *Manager) importFinishedTorrent(via, jobID string, t qbit.Torrent, platf, platSlug string, isPC bool) bool {
	// Record the hash before anything can return. The job row's own copy comes
	// from a request parameter that is empty for any result carrying a .torrent
	// URL rather than a magnet, this is the one place holding the torrent
	// itself, and every exit below leaves a row the UI gates on it - including
	// the refusal, which would otherwise leave a dead end with no button.
	m.jobs.Update(jobID, "info_hash", t.Hash)

	// Claimed here rather than in any caller, so every path that imports is
	// excluded rather than only the one that was looked at. The hash is what two
	// rows naming one download share; the job id stands in when there is none,
	// so an empty hash cannot collapse unrelated imports onto one key.
	claim := t.Hash
	if claim == "" {
		claim = jobID
	}
	if _, busy := m.importing.LoadOrStore(claim, struct{}{}); busy {
		slog.Warn("an import is already running for this download", "via", via, "name", sanitizeLog(t.Name))
		// A refusal has to leave a row the user can act on: left at organizing
		// it would carry no button, count as active and never be pruned.
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error",
			"detail": "Another import is already running for this download.",
		})
		return false
	}
	defer m.importing.Delete(claim)

	attempt := 0
	// Empty means the import wrote its own terminal state and nothing here may
	// overwrite it: a quarantined download has already had its files deleted, and
	// telling the user to go organize it by hand would be wrong.
	var giveUp string
	for {
		attempt++
		retryable := m.organizeWithScan(jobID, &t, platf, platSlug, isPC)

		if job, ok := m.jobs.Get(jobID); ok {
			if status, _ := job["status"].(string); status == "completed" {
				slog.Info("import completed", "via", via, "name", sanitizeLog(t.Name), "attempts", attempt)
				return true
			}
		}

		if !retryable {
			break
		}
		if attempt >= importAttempts {
			giveUp = fmt.Sprintf("Gave up after %d attempts. The download is still in the client, "+
				"so use Retry once the files are in place.", attempt)
			break
		}

		// organizing is a status the job store rewrites to interrupted on startup.
		// Left at error, a restart during the wait leaves a row reading as retrying
		// with nothing retrying it.
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "organizing",
			"detail": fmt.Sprintf("Waiting for the download client to publish the finished files, attempt %d of %d", attempt, importAttempts),
			// The failed attempt left its error behind and the UI renders that
			// field whatever the status is, so a later success would show a
			// completed import under a stale failure.
			"error": nil,
		})
		time.Sleep(importRetryDelay)

		// Ask the client again rather than reusing the value that just failed:
		// content_path changes when the client publishes the finished download.
		fresh, found, err := m.torrentByHash(t.Hash)
		switch {
		case err != nil:
			slog.Warn("could not re-read the torrent, trying again",
				"via", via, "name", sanitizeLog(t.Name), "error", err)
		case !found:
			giveUp = "The download client no longer lists this torrent, so there is nothing left to import."
		default:
			t = fresh
		}
		if giveUp != "" {
			break
		}
	}

	if giveUp != "" {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{"status": "error", "detail": giveUp})
	}
	slog.Warn("import did not complete", "via", via, "name", sanitizeLog(t.Name), "attempts", attempt)
	return false
}

// torrentByHash re-reads a torrent from the client by exact hash. The bool means
// anything only when the error is nil: a read that failed is not evidence the
// client stopped holding the torrent, and acting on it as though it were turns
// one bad request into a permanent give-up.
func (m *Manager) torrentByHash(hash string) (qbit.Torrent, bool, error) {
	torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
	if err != nil {
		return qbit.Torrent{}, false, err
	}
	for _, t := range torrents {
		if strings.EqualFold(t.Hash, hash) {
			return t, true, nil
		}
	}
	return qbit.Torrent{}, false, nil
}

// organizeWithScan scans a finished torrent and imports it, reporting the same
// retryable signal organizeGame does.
func (m *Manager) organizeWithScan(jobID string, torrent *qbit.Torrent, platf, platSlug string, isPC bool) (retryable bool) {
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
		return false
	}
	m.jobs.UpdateMulti(jobID, map[string]interface{}{
		"status": "organizing", "detail": "Scans passed. Moving to library...",
	})
	return m.organizeGame(jobID, torrent, platf, platSlug, isPC)
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

	source := m.ddlSourceName(dlURL, vimmID)

	if filepath_ == "" || !pathExists(filepath_) {
		errMsg := "Download failed"
		if dlErr != nil {
			errMsg = fmt.Sprintf("Download failed: %v", dlErr)
		}
		if job, ok := m.jobs.Get(jobID); ok {
			status, _ := job["status"].(string)
			if status == "error" {
				// The downloader already wrote a specific reason; keep it.
				if e, _ := job["error"].(string); e != "" {
					errMsg = e
				}
			} else {
				m.jobs.UpdateMulti(jobID, map[string]interface{}{
					"status": "error", "error": errMsg,
				})
			}
		}
		// A source whose files never arrive is not healthy, whatever its
		// searches say. Recording it here is what lets /api/sources and the
		// scheduler see a download-side outage at all.
		if source != "" {
			search.RecordDownloadFail(source, errMsg)
		}
		return
	}
	if source != "" {
		search.RecordDownloadSuccess(source)
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

// ddlSourceName maps a DDL job back to the health bucket of the source that
// produced it: Vimm jobs carry a vault ID, Myrient jobs a URL under its base.
// An unrecognised host records nothing rather than inventing a source.
func (m *Manager) ddlSourceName(dlURL, vimmID string) string {
	if vimmID != "" {
		return "vimm"
	}
	if dlURL == "" {
		return ""
	}
	if m.cfg != nil && m.cfg.Sources != nil {
		if base := m.cfg.Sources.Myrient.BaseURL; base != "" && strings.HasPrefix(dlURL, base) {
			return "myrient"
		}
	}
	if u, err := url.Parse(dlURL); err == nil && strings.Contains(u.Host, "myrient") {
		return "myrient"
	}
	return ""
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
		return "", fmt.Errorf("cannot create file %s: %v", fp, err)
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

var vimmFormRe = regexp.MustCompile(`(?is)(<form\b[^>]*\bid=["'](?:dl_form|download_form)["'][^>]*>)(.*?)</form\s*>`)
var vimmActionRe = regexp.MustCompile(`(?i)\baction\s*=\s*["']([^"']+)["']`)
var vimmInputRe = regexp.MustCompile(`(?is)<input\b[^>]*>`)
var vimmMediaNameRe = regexp.MustCompile(`(?i)\bname\s*=\s*["']mediaId["']`)
var vimmValueRe = regexp.MustCompile(`(?i)\bvalue\s*=\s*["'](\d+)["']`)
var vimmMediaAssignmentRe = regexp.MustCompile(`(?i)\b(?:const|let|var)\s+(?:allMedia|media)\s*=\s*`)
var vimmJSMediaRe = regexp.MustCompile(`(?i)["']ID["']\s*:\s*["']?(\d+)`)
var vimmPositiveIDRe = regexp.MustCompile(`^[1-9]\d*$`)
var vimmDLRe = regexp.MustCompile(`(//dl\d*\.vimm\.net/[^"']*)`)
var vimmCDFilenameRe = regexp.MustCompile(`filename="?([^";\n]+)"?`)

// Vimm's Lair fronts its vault pages with a Cloudflare Turnstile challenge and
// only renders the download form to a session that passed it. A plain HTTP
// client never gets the form, so a page carrying these markers is the gate
// itself, not a parsing miss. A configured FlareSolverr instance can render
// the page; without one the existing actionable failure remains.
var vimmChallengeRe = regexp.MustCompile(`cf-turnstile|Checking if you are human`)

// vimmChallengeError is the job error for a Turnstile-gated page. It names the
// cause so the failure is a decision the user can act on, not a dead end.
const vimmChallengeError = "Vimm's Lair requires a Cloudflare Turnstile check. Set FLARESOLVERR_URL to enable Vimm downloads, or try another source for this title."

const vimmFlareSolverrChallengeError = "FlareSolverr returned Vimm's Turnstile page instead of the vault page. Check the FlareSolverr service and increase its max timeout."

// vimmIsChallenge reports whether a Vimm response is the Turnstile gate.
func vimmIsChallenge(pageText string) bool {
	return vimmChallengeRe.MatchString(pageText)
}

// vimmDownloadPause is the courtesy delay between fetching the vault page and
// hitting the download host. Tests set it to 0.
var vimmDownloadPause = 3 * time.Second

func parseVimmDownloadForm(pageText string) (actionURL, mediaID string) {
	if form := vimmFormRe.FindStringSubmatch(pageText); form != nil {
		if m := vimmActionRe.FindStringSubmatch(form[1]); m != nil {
			actionURL = m[1]
		}
		// This hidden field is the authoritative selected/default release. Scope
		// it to the download form so another form cannot supply a false ID.
		for _, input := range vimmInputRe.FindAllString(form[2], -1) {
			if !vimmMediaNameRe.MatchString(input) {
				continue
			}
			if m := vimmValueRe.FindStringSubmatch(input); m != nil && vimmPositiveIDRe.MatchString(m[1]) {
				mediaID = m[1]
				break
			}
		}
	}
	if mediaID == "" {
		mediaID = vimmMediaIDFromScript(pageText)
	}
	if actionURL == "" {
		if m := vimmDLRe.FindStringSubmatch(pageText); m != nil && mediaID != "" {
			actionURL = m[1]
		}
	}
	return actionURL, mediaID
}

// vimmMediaIDFromScript reads only Vimm's declared media array. Looking for a
// generic "ID" across the whole page can select analytics or ad data instead.
// A multi-disc page has two independent selectors, so without the rendered
// hidden field only a single entry or its explicit SortOrder=1 default is safe.
func vimmMediaIDFromScript(pageText string) string {
	for _, loc := range vimmMediaAssignmentRe.FindAllStringIndex(pageText, -1) {
		array := javascriptArrayAt(pageText, loc[1])
		if array == "" {
			continue
		}
		var entries []struct {
			ID        json.RawMessage `json:"ID"`
			SortOrder json.RawMessage `json:"SortOrder"`
		}
		if err := json.Unmarshal([]byte(array), &entries); err == nil {
			valid := make([]struct {
				id        string
				sortOrder int
			}, 0, len(entries))
			for _, entry := range entries {
				id := vimmJSONDecimal(entry.ID)
				if id == "" || id == "0" {
					continue
				}
				sortOrder, _ := strconv.Atoi(vimmJSONDecimal(entry.SortOrder))
				valid = append(valid, struct {
					id        string
					sortOrder int
				}{id: id, sortOrder: sortOrder})
			}
			if len(valid) == 1 {
				return valid[0].id
			}
			defaultID := ""
			for _, entry := range valid {
				if entry.sortOrder != 1 {
					continue
				}
				if defaultID != "" {
					defaultID = ""
					break
				}
				defaultID = entry.id
			}
			if defaultID != "" {
				return defaultID
			}
			continue
		}

		// Older page captures are not always strict JSON. Retain a conservative
		// fallback only when the scoped array contains exactly one ID.
		matches := vimmJSMediaRe.FindAllStringSubmatch(array, -1)
		if len(matches) == 1 && vimmPositiveIDRe.MatchString(matches[0][1]) {
			return matches[0][1]
		}
	}
	return ""
}

func vimmJSONDecimal(raw json.RawMessage) string {
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if json.Unmarshal(raw, &decoded) != nil {
			return ""
		}
		value = decoded
	}
	if !vimmPositiveIDRe.MatchString(value) {
		return ""
	}
	return value
}

// javascriptArrayAt returns the balanced array immediately after a variable
// assignment, ignoring brackets inside quoted strings.
func javascriptArrayAt(text string, offset int) string {
	for offset < len(text) && (text[offset] == ' ' || text[offset] == '\t' || text[offset] == '\r' || text[offset] == '\n') {
		offset++
	}
	if offset >= len(text) || text[offset] != '[' {
		return ""
	}
	start, depth := offset, 0
	var quote byte
	escaped := false
	for i := offset; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

func resolveVimmAction(gameURL, action string) string {
	if action == "" {
		return ""
	}
	if strings.HasPrefix(action, "//") {
		scheme := "https:"
		if u, err := url.Parse(gameURL); err == nil && u.Scheme != "" {
			scheme = u.Scheme + ":"
		}
		return scheme + action
	}
	base, err := url.Parse(gameURL)
	if err != nil {
		return action
	}
	ref, err := url.Parse(action)
	if err != nil {
		return action
	}
	return base.ResolveReference(ref).String()
}

func vimmGETURL(actionURL, mediaID string) string {
	u, err := url.Parse(actionURL)
	if err != nil {
		return actionURL
	}
	q := u.Query()
	q.Set("mediaId", mediaID)
	u.RawQuery = q.Encode()
	return u.String()
}

func vimmDownloadURLs(actionURL, mediaID string) []string {
	return []string{vimmGETURL(actionURL, mediaID)}
}

func vimmVaultURL(m *Manager, gameID string) string {
	base := "https://vimm.net/vault/"
	if m.cfg != nil && m.cfg.Sources != nil && m.cfg.Sources.Vimm.BaseURL != "" {
		base = m.cfg.Sources.Vimm.BaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + gameID
}

func vimmOrigin(gameURL string) string {
	u, err := url.Parse(gameURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "https://vimm.net"
	}
	return u.Scheme + "://" + u.Host
}

func vimmLooksLikeFile(r *http.Response) bool {
	// 206 Content-Length is the slice, not the file. We never send Range, so
	// only a full 200 is a complete download.
	if r.StatusCode != 200 {
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	return !strings.Contains(ct, "text/html")
}

// flareSolverrOptions resolves the environment-only startup configuration.
func (m *Manager) flareSolverrOptions() (string, int, int) {
	maxTimeout := m.cfg.FlareSolverrMaxTimeout
	if flaresolverr.ValidateMaxTimeout(maxTimeout) != nil {
		maxTimeout = flaresolverr.DefaultMaxTimeout
	}
	tabsTillVerify := m.cfg.FlareSolverrTabsTillVerify
	if flaresolverr.ValidateTabsTillVerify(tabsTillVerify) != nil {
		tabsTillVerify = flaresolverr.DefaultVimmTabsTillVerify
	}
	return strings.TrimSpace(m.cfg.FlareSolverrURL), maxTimeout, tabsTillVerify
}

func (m *Manager) fetchWithFlareSolverr(ctx context.Context, apiURL, targetURL string, maxTimeout, tabsTillVerify int) (flaresolverr.Solution, error) {
	// Jackett serializes solver requests too: every call launches a browser,
	// and an auto-download batch should not exhaust the solver's memory.
	m.flareSolverrMu.Lock()
	defer m.flareSolverrMu.Unlock()
	return flaresolverr.Fetch(ctx, apiURL, targetURL, maxTimeout, tabsTillVerify)
}

func (m *Manager) downloadVimmGame(gameID, destPath, jobID string) string {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: transport,
		Jar:       jar,
	}
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	gameURL := vimmVaultURL(m, gameID)
	origin := vimmOrigin(gameURL)
	m.jobs.Update(jobID, "detail", "Fetching game page...")

	req, _ := http.NewRequest("GET", gameURL, nil)
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Vimm fetch failed", "error", err)
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	pageText := string(body)

	usedFlareSolverr := false
	if vimmIsChallenge(pageText) {
		apiURL, maxTimeout, tabsTillVerify := m.flareSolverrOptions()
		if apiURL != "" {
			m.jobs.Update(jobID, "detail", "Fetching Vimm page through FlareSolverr...")
			solution, solveErr := m.fetchWithFlareSolverr(context.Background(), apiURL, gameURL, maxTimeout, tabsTillVerify)
			if solveErr != nil {
				slog.Warn("FlareSolverr could not fetch Vimm vault page", "game_id", gameID, "error", solveErr)
				m.jobs.UpdateMulti(jobID, map[string]interface{}{
					"status": "error", "error": solveErr.Error(),
				})
				return ""
			}
			// Only rendered HTML crosses this boundary. The media ID is sufficient
			// for Vimm's existing download endpoint, so solver cookies/UA are not
			// mixed into the separate direct-download session.
			pageText = solution.Response
			usedFlareSolverr = true
		}
	}

	if strings.Contains(pageText, "unavailable at the request of") {
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": "Game removed by DMCA takedown",
		})
		return ""
	}

	actionURL, mediaID := parseVimmDownloadForm(pageText)
	if mediaID == "0" {
		mediaID = ""
	}
	actionURL = resolveVimmAction(gameURL, actionURL)

	if actionURL == "" || mediaID == "" {
		errMsg := "Could not find download form on Vimm"
		if vimmIsChallenge(pageText) {
			if usedFlareSolverr {
				errMsg = vimmFlareSolverrChallengeError
			} else {
				errMsg = vimmChallengeError
			}
			slog.Warn("Vimm vault page is Turnstile-gated; download form withheld", "game_id", gameID)
		} else if usedFlareSolverr {
			errMsg = "FlareSolverr returned a Vimm page without a download media ID"
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": errMsg,
		})
		return ""
	}
	slog.Info("Vimm download", "action", actionURL, "mediaId", mediaID)

	m.jobs.Update(jobID, "detail", "Starting download from Vimm...")
	if vimmDownloadPause > 0 {
		time.Sleep(vimmDownloadPause)
	}

	// Current Vimm serves the file on GET ?mediaId=; POST returns 400.
	// Use the form action host only — downloadN.vimm.net is not a real hostname.
	dlURLs := vimmDownloadURLs(actionURL, mediaID)

	streamClient := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: transport,
		Jar:       jar,
	}

	var dlResp *http.Response
	sawHTML, sawChallenge := false, false
	for _, dlURL := range dlURLs {
		req, _ := http.NewRequest(http.MethodGet, dlURL, nil)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", gameURL)
		req.Header.Set("Origin", origin)
		r, err := streamClient.Do(req)
		if err != nil {
			slog.Warn("Vimm download failed", "url", dlURL, "error", err)
			continue
		}
		if vimmLooksLikeFile(r) {
			slog.Info("Vimm download started", "url", dlURL, "status", r.StatusCode)
			dlResp = r
			break
		}
		if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "text/html") {
			sawHTML = true
			// Read a bounded slice to tell the gate apart from any other page.
			peek, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if vimmIsChallenge(string(peek)) {
				sawChallenge = true
			}
		}
		r.Body.Close()
		slog.Warn("Vimm download rejected", "url", dlURL, "status", r.StatusCode, "content_type", r.Header.Get("Content-Type"))
	}

	if dlResp == nil {
		errMsg := fmt.Sprintf("Vimm download server rejected request (tried %d URLs)", len(dlURLs))
		if sawHTML {
			errMsg = "Vimm returned a web page instead of a file"
		}
		if sawChallenge {
			errMsg = vimmChallengeError
		}
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "error", "error": errMsg,
		})
		return ""
	}
	defer dlResp.Body.Close()

	total := dlResp.ContentLength
	cd := dlResp.Header.Get("Content-Disposition")
	filename := fmt.Sprintf("%s.7z", gameID)
	if fm := vimmCDFilenameRe.FindStringSubmatch(cd); fm != nil {
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
	platf, platSlug, isPC = m.resolvePlatform(jobID, fp, title, platf, platSlug, isPC)
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
		slog.Warn("no platform detected, left in staging", "title", sanitizeLog(title), "path", sanitizeLog(fp))
		m.jobs.UpdateMulti(jobID, map[string]interface{}{
			"status": "completed", "detail": "Downloaded (unknown platform, left in staging)",
		})
	}
}

// RecoverOrphanedTorrents checks for existing game torrents and re-links them.
func (m *Manager) RecoverOrphanedTorrents() {
	if !m.cfg.HasQBittorrent() {
		slog.Info("orphan torrent recovery disabled")
		return
	}

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

	torrents, err := m.qb.GetTorrents(m.cfg.QBCategory)
	if err != nil {
		slog.Warn("orphan recovery: could not read the download client", "error", err)
		return
	}
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
		// Reuse the row already tracking this torrent. Recovery is not a
		// once-per-install routine, so minting an id per pass accumulated a
		// duplicate row per torrent every time it ran.
		jobID, known := m.jobForTorrent(t.Hash, t.Name)
		if known {
			// A finished import is terminal, and organizeGame leaves the torrent
			// seeding under a source-preserving import, so a game already in the
			// library is still in the category on the next pass. Rewriting its row
			// to completed_unorganized would put an Organize button on a game that
			// is already organized, and pressing it imports it a second time.
			if job, ok := m.jobs.Get(jobID); ok {
				if status, _ := job["status"].(string); status == "completed" {
					continue
				}
			}
		} else {
			jobID = newJobID()
		}
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
				"info_hash":     t.Hash,
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
				"info_hash":     t.Hash,
				"platform":      platf,
				"platform_slug": platSlug,
				"is_pc":         isPC,
				"error":         nil,
				"detail":        "Recovered - watching download...",
			})
			go m.watchGameTorrent(jobID, t.Hash, t.Name, platf, platSlug, isPC)
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
func moveFile(src, dest string) error { return fileops.MoveFile(src, dest) }

// moveContent moves a file or directory tree. Content Gamarr fetched itself
// (DDL, Usenet) moves: nothing is seeding it, so leaving the staging copy behind
// would just leak disk. Torrent content goes through Manager.importContent,
// which honors the configured import mode. The one exception is a Usenet PC
// download written to the vault as an archive, which cannot move bytes and so
// leaves the staging copy for a layer that can confirm the archive is safe.
func moveContent(src, dest string) error { return fileops.MoveContent(src, dest) }

func copyFile(src, dest string) error { return fileops.CopyFile(src, dest) }

// importOptions resolves the import strategy for a torrent import. The
// runtime setting wins so the mode can be changed from the UI without a
// restart; the environment default applies when it is unset.
func (m *Manager) importOptions() fileops.Options {
	mode := m.effectiveImportMode()
	if s := m.LoadSettings(); s != nil {
		if parsed, err := fileops.ParseMode(s.ImportMode); err == nil && parsed.Valid() {
			mode = parsed
		}
	}
	return fileops.Options{Mode: mode, HardlinkFallback: m.cfg.ImportHardlinkFallback}
}

// importContent places completed torrent content into the library and returns
// the mode it used, so the caller knows whether the source survived — that is,
// whether the torrent can be left seeding.
func (m *Manager) importContent(src, dest string) (fileops.Mode, error) {
	opt := m.importOptions()
	return opt.Mode, fileops.Import(src, dest, opt)
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// LoadSettings loads settings from disk. Fields the stored file does not
// carry — including settings written before import modes existed — fall back
// to the environment configuration, so the API always reports the mode that
// imports will actually use.
func (m *Manager) LoadSettings() *Settings {
	defaults := func() *Settings {
		archive := m.cfg.VaultArchiveEnabled
		return &Settings{
			ExtractArchives:     m.cfg.ExtractArchives,
			ImportMode:          string(m.effectiveImportMode()),
			VaultArchiveEnabled: &archive,
		}
	}
	settingsFile := filepath.Join(m.cfg.DataDir, "settings.json")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return defaults()
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return defaults()
	}
	if mode, err := fileops.ParseMode(s.ImportMode); err != nil || s.ImportMode == "" {
		s.ImportMode = string(m.effectiveImportMode())
	} else {
		s.ImportMode = string(mode)
	}
	if s.VaultArchiveEnabled == nil {
		archive := m.cfg.VaultArchiveEnabled
		s.VaultArchiveEnabled = &archive
	}
	return &s
}

// vaultArchiveEnabled reports whether a PC game is written to the vault as one
// archive. The runtime setting wins so it can be changed from the UI without a
// restart; the environment default applies when it is unset.
func (m *Manager) vaultArchiveEnabled() bool {
	if s := m.LoadSettings(); s != nil && s.VaultArchiveEnabled != nil {
		return *s.VaultArchiveEnabled
	}
	return m.cfg.VaultArchiveEnabled
}

// effectiveImportMode is the configured default, guarding against a zero
// value on a hand-built Config.
func (m *Manager) effectiveImportMode() fileops.Mode {
	if m.cfg.ImportMode.Valid() {
		return m.cfg.ImportMode
	}
	return fileops.ModeMove
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
	// ImportMode overrides IMPORT_MODE at runtime. Empty means "follow the
	// environment default".
	ImportMode string `json:"import_mode"`
	// VaultArchiveEnabled overrides VAULT_ARCHIVE_ENABLED at runtime. A pointer
	// so an absent value is not the same as a stored false, which is what keeps
	// a settings file written before this option existed from turning archiving
	// off for an install that set the environment variable.
	VaultArchiveEnabled *bool `json:"vault_archive_enabled"`
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
