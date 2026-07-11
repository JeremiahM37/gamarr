package download

import (
	"crypto/rand"
	"io"
	"strings"
)

func cryptoReader() io.Reader {
	return rand.Reader
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
