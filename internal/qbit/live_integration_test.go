package qbit

import (
	"os"
	"testing"
	"time"
)

// TestLiveQBittorrent exercises the client against a real qBittorrent
// instance. Skipped unless QBIT_LIVE_URL/QBIT_LIVE_USER/QBIT_LIVE_PASS are set.
func TestLiveQBittorrent(t *testing.T) {
	url := os.Getenv("QBIT_LIVE_URL")
	if url == "" {
		t.Skip("QBIT_LIVE_URL not set")
	}
	c := New(url, os.Getenv("QBIT_LIVE_USER"), os.Getenv("QBIT_LIVE_PASS"))

	if !c.Login() {
		t.Fatal("Login failed against live qBittorrent")
	}
	t.Log("Login OK")

	bad := New(url, os.Getenv("QBIT_LIVE_USER"), "definitely-wrong-password")
	if bad.Login() {
		t.Fatal("Login with wrong password unexpectedly succeeded")
	}
	t.Log("Bad-credential login correctly rejected")

	hash := "cccccccccccccccccccccccccccccccccccccccc"
	if !c.AddTorrent("magnet:?xt=urn:btih:"+hash, "gamarr-live-test", "/tmp", "gamarr-test") {
		t.Fatal("AddTorrent failed against live qBittorrent")
	}
	t.Log("AddTorrent OK")

	deadline := time.Now().Add(10 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		for _, tor := range c.GetTorrents("gamarr-test") {
			if tor.Hash == hash {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Fatal("added torrent not visible via GetTorrents")
	}
	t.Log("GetTorrents OK")

	c.GetTorrentFiles(hash) // metadata won't resolve for a fake hash; just ensure no panic
	t.Log("GetTorrentFiles OK (no panic)")

	if !c.DeleteTorrent(hash, true) {
		t.Fatal("DeleteTorrent failed against live qBittorrent")
	}
	t.Log("DeleteTorrent OK")
}
