package download

import (
	"crypto/rand"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func cryptoReader() io.Reader {
	return rand.Reader
}

// sanitizeFilename reduces an externally supplied name (Content-Disposition
// header, URL segment, platform slug, …) to a single safe path component:
// no separators, no traversal. Falls back to "download" if nothing is left.
// The filepath.IsLocal gate is what makes the result safe to join onto a
// trusted base directory.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSpace(filepath.Base(name))
	if name == "" || name == "." || !filepath.IsLocal(name) {
		return "download"
	}
	return name
}

// safeChild joins name onto dir and guarantees the result stays inside dir,
// defeating path traversal via crafted names. name may itself contain
// separators (a relative subpath) as long as it stays local.
func safeChild(dir, name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("unsafe path %q escapes %q", name, dir)
	}
	return filepath.Join(dir, name), nil
}

// sanitizeLog strips newlines from externally supplied values before they
// reach the log, preventing forged log entries.
func sanitizeLog(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// titlesMatch reports whether a tracked job title and a torrent name refer to
// the same release. Trackers often rename torrents, so it matches
// case-insensitively when either string contains the other.
func titlesMatch(title, torrentName string) bool {
	if title == "" || torrentName == "" {
		return false
	}
	a := strings.ToLower(title)
	b := strings.ToLower(torrentName)
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// JobMatchesTorrent reports whether a tracked job refers to this torrent. Jobs
// recorded with an infohash match on that alone; rows from before it was stored
// fall back to the title. Shared by Manager.watchGameTorrent, Watcher.hasMatchingJob
// and the downloads API so they cannot disagree - a disagreement lets the watcher
// double-import a job's torrent, and made the API list one download twice.
func JobMatchesTorrent(infoHash, title, torrentHash, torrentName string) bool {
	if infoHash != "" {
		return strings.EqualFold(infoHash, torrentHash)
	}
	return titlesMatch(title, torrentName)
}

// jobForTorrent returns the id of the job already tracking this torrent. It
// shares jobMatchesTorrent with the watcher so the two cannot disagree about
// which row belongs to a torrent.
func (m *Manager) jobForTorrent(torrentHash, torrentName string) (string, bool) {
	for _, item := range m.jobs.Items() {
		infoHash, _ := item.Data["info_hash"].(string)
		title, _ := item.Data["title"].(string)
		if jobMatchesTorrent(infoHash, title, torrentHash, torrentName) {
			return item.ID, true
		}
	}
	return "", false
}
