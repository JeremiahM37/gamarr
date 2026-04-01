package safety

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gamarr/internal/qbit"
)

func TestScanTorrentFileList_SafeFiles(t *testing.T) {
	files := []qbit.TorrentFile{
		{Name: "game/setup.exe"},
		{Name: "game/data.bin"},
		{Name: "game/readme.txt"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if !safe {
		t.Errorf("expected safe, got issues: %v", issues)
	}
}

func TestScanTorrentFileList_DangerousFiles(t *testing.T) {
	files := []qbit.TorrentFile{
		{Name: "game/setup.exe"},
		{Name: "game/virus.scr"},
		{Name: "game/hack.bat"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if safe {
		t.Error("expected unsafe for dangerous files")
	}
	if len(issues) == 0 {
		t.Error("expected issues to be reported")
	}
}

func TestScanTorrentFileList_DoubleExtension(t *testing.T) {
	files := []qbit.TorrentFile{
		{Name: "game/readme.pdf.exe"},
		{Name: "game/data.bin"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if safe {
		t.Error("expected unsafe for double extension")
	}

	found := false
	for _, issue := range issues {
		if len(issue) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected non-empty issues")
	}
}

func TestScanTorrentFileList_OnlyExecutables(t *testing.T) {
	files := []qbit.TorrentFile{
		{Name: "setup.exe"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if safe {
		t.Error("expected unsafe for exe-only torrent")
	}
	if len(issues) == 0 {
		t.Error("expected issues")
	}
}

func TestScanTorrentFileList_EmptyFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode([]qbit.TorrentFile{})
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if !safe {
		t.Error("expected safe when no files (can't check)")
	}
	if issues != nil {
		t.Error("expected nil issues")
	}
}

func TestScanTorrentFileList_ArchQualifierNotFlagged(t *testing.T) {
	// x64.exe should NOT be flagged as double extension
	files := []qbit.TorrentFile{
		{Name: "game/setup.x64.exe"},
		{Name: "game/data.bin"},
		{Name: "game/readme.txt"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			w.Write([]byte("Ok."))
			return
		}
		if r.URL.Path == "/api/v2/torrents/files" {
			json.NewEncoder(w).Encode(files)
			return
		}
	}))
	defer srv.Close()

	qb := qbit.New(srv.URL, "admin", "pass")
	safe, issues := ScanTorrentFileList(qb, "abc123")
	if !safe {
		t.Errorf("x64.exe should not be flagged as double extension, issues: %v", issues)
	}
}

func TestDangerousExtensions(t *testing.T) {
	dangerous := []string{".scr", ".bat", ".cmd", ".vbs", ".ps1", ".hta"}
	for _, ext := range dangerous {
		if !dangerousExtensions[ext] {
			t.Errorf("expected %q to be dangerous", ext)
		}
	}

	safe := []string{".exe", ".dll", ".msi", ".bin", ".iso"}
	for _, ext := range safe {
		if dangerousExtensions[ext] {
			t.Errorf("expected %q to NOT be in dangerousExtensions", ext)
		}
	}
}

func TestArchQualifiers(t *testing.T) {
	expected := []string{"x64", "x86", "x86_64", "amd64", "arm64", "win32", "win64"}
	for _, q := range expected {
		if !archQualifiers[q] {
			t.Errorf("expected %q to be an arch qualifier", q)
		}
	}
}
