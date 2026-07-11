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
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "download"
	}
	return name
}

// safeChild joins name onto dir and guarantees the result stays inside dir,
// defeating path traversal via crafted names.
func safeChild(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	cleanDir := filepath.Clean(dir)
	if p != cleanDir && !strings.HasPrefix(p, cleanDir+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q escapes %q", name, dir)
	}
	return p, nil
}

// sanitizeLog strips newlines from externally supplied values before they
// reach the log, preventing forged log entries.
func sanitizeLog(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}

// titlesMatch reports whether a tracked job title and a torrent name refer to
// the same release. Trackers often rename torrents, so it matches
// case-insensitively when either string contains the other. Shared by
// Manager.watchGameTorrent and Watcher.hasMatchingJob so the two cannot
// disagree (a disagreement lets the watcher double-import a job's torrent).
func titlesMatch(title, torrentName string) bool {
	if title == "" || torrentName == "" {
		return false
	}
	a := strings.ToLower(title)
	b := strings.ToLower(torrentName)
	return strings.Contains(a, b) || strings.Contains(b, a)
}
