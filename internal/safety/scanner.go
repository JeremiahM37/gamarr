// Package safety screens downloads for dangerous file types and can scan
// completed files with an on-demand ClamAV container.
package safety

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gamarr/internal/qbit"
)

var dangerousExtensions = map[string]bool{
	".scr": true, ".bat": true, ".cmd": true, ".vbs": true, ".vbe": true,
	".js": true, ".jse": true, ".wsf": true, ".wsh": true, ".ps1": true,
	".hta": true, ".cpl": true, ".reg": true, ".pif": true, ".com": true,
	".msp": true, ".mst": true,
}

var archQualifiers = map[string]bool{
	"x64": true, "x86": true, "x86_64": true, "amd64": true,
	"arm64": true, "arm": true, "win32": true, "win64": true,
}

// ScanTorrentFileList checks a torrent's file list for dangerous extensions.
func ScanTorrentFileList(qb *qbit.Client, torrentHash string) (bool, []string) {
	files := qb.GetTorrentFiles(torrentHash)
	if len(files) == 0 {
		return true, nil // Can't check, assume OK
	}

	var issues []string
	var dangerousFiles []string
	exeCount := 0
	totalFiles := len(files)

	for _, f := range files {
		name := strings.ToLower(f.Name)
		ext := filepath.Ext(name)

		if dangerousExtensions[ext] {
			dangerousFiles = append(dangerousFiles, name)
		}
		if ext == ".exe" {
			exeCount++
		}

		// Double extension check (basename only)
		base := filepath.Base(name)
		parts := strings.Split(base, ".")
		if len(parts) >= 3 {
			doubleExt := "." + parts[len(parts)-1]
			middle := strings.ToLower(parts[len(parts)-2])
			if (doubleExt == ".exe" || doubleExt == ".scr" || doubleExt == ".bat" || doubleExt == ".cmd" || doubleExt == ".msi") && !archQualifiers[middle] {
				issues = append(issues, fmt.Sprintf("Double extension: %s", base))
			}
		}
	}

	if len(dangerousFiles) > 0 {
		names := dangerousFiles
		if len(names) > 5 {
			names = names[:5]
		}
		baseNames := make([]string, len(names))
		for i, n := range names {
			baseNames[i] = filepath.Base(n)
		}
		issues = append(issues, fmt.Sprintf("Dangerous files found: %s", strings.Join(baseNames, ", ")))
	}

	// Heuristic: only executables and very few files
	if exeCount > 0 && totalFiles <= 3 {
		hasOnlyExe := true
		for _, f := range files {
			ext := filepath.Ext(strings.ToLower(f.Name))
			if ext != ".exe" && ext != ".dll" && ext != ".msi" {
				hasOnlyExe = false
				break
			}
		}
		if hasOnlyExe && totalFiles <= 2 {
			issues = append(issues, "Torrent contains only executables - likely malware")
		}
	}

	return len(issues) == 0, issues
}

// ScanWithClamAV starts ClamAV on demand, scans, then stops it.
func ScanWithClamAV(path, clamavContainer, clamavSocket, dockerSocket string) (bool, []string) {
	if !startClamAV(clamavContainer, clamavSocket, dockerSocket) {
		slog.Warn("ClamAV not available, skipping virus scan")
		return true, nil
	}
	defer stopClamAV(clamavContainer, dockerSocket)

	cmd := exec.Command("clamdscan", "--config-file=/app/clamd.conf", "--no-summary", "--infected", "--stream", "-r", path)
	output, err := cmd.Output()
	if err != nil {
		// clamdscan returns exit code 1 if infected files found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Process output for infected lines
		} else if _, ok := err.(*exec.Error); ok {
			slog.Warn("clamdscan not installed, skipping virus scan")
			return true, nil
		} else {
			slog.Warn("ClamAV scan error", "error", err)
			return true, nil
		}
	}

	var infected []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "FOUND") {
			infected = append(infected, line)
		}
	}
	if len(infected) > 0 {
		return false, infected
	}
	return true, nil
}

func startClamAV(container, socketPath, dockerSocket string) bool {
	if _, err := os.Stat(socketPath); err == nil {
		return true // Already running
	}
	if _, err := os.Stat(dockerSocket); err != nil {
		slog.Warn("Docker socket not available, cannot start ClamAV")
		return false
	}

	status := dockerAPI("POST", fmt.Sprintf("/containers/%s/start", container), dockerSocket)
	if status != 204 && status != 304 {
		slog.Warn("failed to start ClamAV container", "status", status)
		return false
	}
	slog.Info("starting ClamAV container, waiting for socket...")
	for i := 0; i < 150; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			slog.Info("ClamAV socket ready")
			return true
		}
		time.Sleep(time.Second)
	}
	slog.Warn("ClamAV socket never appeared after start")
	return false
}

func stopClamAV(container, dockerSocket string) {
	status := dockerAPI("POST", fmt.Sprintf("/containers/%s/stop", container), dockerSocket)
	if status == 204 || status == 304 {
		slog.Info("ClamAV container stopped")
	}
}

func dockerAPI(method, path, socketPath string) int {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		slog.Warn("Docker socket error", "error", err)
		return 0
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := fmt.Sprintf("%s %s HTTP/1.0\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n", method, path)
	conn.Write([]byte(req))

	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	resp := string(buf[:n])

	// Parse HTTP status
	if strings.Contains(resp, "204") {
		return 204
	}
	if strings.Contains(resp, "304") {
		return 304
	}
	if strings.Contains(resp, "200") {
		return 200
	}
	return 0
}
