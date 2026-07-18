package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"game.zip", "game.zip"},
		{"../../etc/evil", "evil"},
		{"..\\..\\evil.exe", "evil.exe"},
		{"/abs/path/rom.gb", "rom.gb"},
		{"..", "download"},
		{".", "download"},
		{"", "download"},
		{"  spaced.zip  ", "spaced.zip"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeFilename(tc.in); got != tc.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeChild(t *testing.T) {
	dir := t.TempDir()
	t.Run("normal child ok", func(t *testing.T) {
		p, err := safeChild(dir, "file.zip")
		if err != nil || p != filepath.Join(dir, "file.zip") {
			t.Errorf("safeChild = %q, %v", p, err)
		}
	})
	t.Run("nested child ok", func(t *testing.T) {
		if _, err := safeChild(dir, "sub/file.zip"); err != nil {
			t.Errorf("nested child rejected: %v", err)
		}
	})
	t.Run("traversal rejected", func(t *testing.T) {
		if _, err := safeChild(dir, "../escape.zip"); err == nil {
			t.Error("expected traversal to be rejected")
		}
	})
	t.Run("deep traversal rejected", func(t *testing.T) {
		if _, err := safeChild(dir, "a/../../escape.zip"); err == nil {
			t.Error("expected nested traversal to be rejected")
		}
	})
}

// TestDownloadDDLTraversalFilename proves a malicious server cannot use
// Content-Disposition to write outside the staging directory.
func TestDownloadDDLTraversalFilename(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="../../pwned.zip"`)
		w.Write([]byte("payload"))
	}))
	defer srv.Close()

	root := t.TempDir()
	staging := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(staging, 0755); err != nil {
		t.Fatal(err)
	}

	m := New(context.Background(), newTestConfig(t), newTestJobs(t), nil)
	fp, err := m.downloadDDL(srv.URL+"/x.zip", staging, "job-traversal")
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	abs, _ := filepath.Abs(fp)
	if !strings.HasPrefix(abs, filepath.Clean(staging)+string(filepath.Separator)) {
		t.Fatalf("file written outside staging dir: %q", abs)
	}
	if filepath.Base(abs) != "pwned.zip" {
		t.Errorf("unexpected filename %q", filepath.Base(abs))
	}
	if _, err := os.Stat(filepath.Join(root, "pwned.zip")); err == nil {
		t.Error("traversal succeeded: file exists at root")
	}
}
