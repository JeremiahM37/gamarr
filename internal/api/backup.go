package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxBackups = 5

// handleBackupDownload handles GET /api/backup — downloads current state as ZIP.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	dataDir := s.cfg.DataDir

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=gamarr-backup-%s.zip", time.Now().Format("20060102-150405")))

	zw := zip.NewWriter(w)
	defer zw.Close()

	// Add database file
	addFileToZip(zw, filepath.Join(dataDir, "gamarr.db"), "gamarr.db")

	// Add settings file
	addFileToZip(zw, filepath.Join(dataDir, "settings.json"), "settings.json")

	// Add DDL sources
	addFileToZip(zw, filepath.Join(dataDir, "ddl_sources.json"), "ddl_sources.json")
}

// handleBackupCreate handles POST /api/backup/create — creates a named backup.
func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	name := req.Name
	if name == "" {
		name = time.Now().Format("20060102-150405")
	}
	// Sanitize name
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)

	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	os.MkdirAll(backupDir, 0755)

	zipPath := filepath.Join(backupDir, fmt.Sprintf("gamarr-backup-%s.zip", name))

	f, err := os.Create(zipPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create backup file: "+err.Error())
		return
	}

	zw := zip.NewWriter(f)

	dataDir := s.cfg.DataDir
	addFileToZip(zw, filepath.Join(dataDir, "gamarr.db"), "gamarr.db")
	addFileToZip(zw, filepath.Join(dataDir, "settings.json"), "settings.json")
	addFileToZip(zw, filepath.Join(dataDir, "ddl_sources.json"), "ddl_sources.json")

	zw.Close()
	f.Close()

	// Enforce max backups
	pruneBackups(backupDir)

	info, _ := os.Stat(zipPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"name":    name,
		"path":    zipPath,
		"size":    size,
	})
}

// handleBackupList handles GET /api/backup/list — lists available backups.
func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	backupDir := filepath.Join(s.cfg.DataDir, "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"backups": []interface{}{},
		})
		return
	}

	type backupInfo struct {
		Name      string `json:"name"`
		Filename  string `json:"filename"`
		Size      int64  `json:"size"`
		CreatedAt string `json:"created_at"`
	}

	var backups []backupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := strings.TrimPrefix(e.Name(), "gamarr-backup-")
		name = strings.TrimSuffix(name, ".zip")
		backups = append(backups, backupInfo{
			Name:      name,
			Filename:  e.Name(),
			Size:      info.Size(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}

	// Sort by creation time descending
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt > backups[j].CreatedAt
	})

	if backups == nil {
		backups = []backupInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"backups": backups,
	})
}

// handleRestore handles POST /api/restore — restores from an uploaded ZIP.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 100MB
	r.Body = http.MaxBytesReader(w, r.Body, 100*1024*1024)

	// Save uploaded file to temp
	tmpFile, err := os.CreateTemp("", "gamarr-restore-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create temp file")
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, r.Body); err != nil {
		tmpFile.Close()
		writeError(w, http.StatusBadRequest, "Failed to read upload")
		return
	}
	tmpFile.Close()

	// Open ZIP
	zr, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid ZIP file")
		return
	}
	defer zr.Close()

	dataDir := s.cfg.DataDir
	restored := []string{}

	for _, f := range zr.File {
		// Only restore known files
		switch f.Name {
		case "gamarr.db", "settings.json", "ddl_sources.json":
			// OK
		default:
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}

		destPath := filepath.Join(dataDir, f.Name)

		// Backup current file before overwriting
		if _, err := os.Stat(destPath); err == nil {
			backupPath := destPath + ".pre-restore"
			os.Rename(destPath, backupPath)
		}

		outFile, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			continue
		}

		io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		restored = append(restored, f.Name)
	}

	slog.Info("backup restored", "files", restored)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"restored": restored,
		"message":  "Restore complete. Restart Gamarr to apply database changes.",
	})
}

// addFileToZip adds a file to the ZIP writer. Skips silently if file doesn't exist.
func addFileToZip(zw *zip.Writer, srcPath, zipName string) {
	f, err := os.Open(srcPath)
	if err != nil {
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return
	}
	header.Name = zipName
	header.Method = zip.Deflate

	writer, err := zw.CreateHeader(header)
	if err != nil {
		return
	}
	io.Copy(writer, f)
}

// pruneBackups removes oldest backups when count exceeds maxBackups.
func pruneBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	type fileEntry struct {
		name    string
		modTime time.Time
	}
	var zips []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		zips = append(zips, fileEntry{name: e.Name(), modTime: info.ModTime()})
	}

	if len(zips) <= maxBackups {
		return
	}

	// Sort oldest first
	sort.Slice(zips, func(i, j int) bool {
		return zips[i].modTime.Before(zips[j].modTime)
	})

	// Remove oldest
	for i := 0; i < len(zips)-maxBackups; i++ {
		os.Remove(filepath.Join(dir, zips[i].name))
		slog.Info("pruned old backup", "name", zips[i].name)
	}
}
