package api

import (
	"fmt"
	"net/http"
	"runtime"
	"time"
)

var startTime = time.Now()

func (s *Server) handleAdminDashboard(w http.ResponseWriter, _ *http.Request) {
	// Library stats by platform.
	libStats := s.mgr.Jobs().LibraryStats()
	libTotal := s.mgr.Jobs().LibraryTotal()

	// Active downloads.
	activeCount := 0
	for _, item := range s.mgr.Jobs().Items() {
		status := item.Data.Status()
		if status == "downloading" || status == "queued" || status == "scanning" {
			activeCount++
		}
	}

	// Source health.
	sourcesHealth := []map[string]interface{}{
		{"name": "prowlarr", "label": "Prowlarr", "status": sourceStatus(s.cfg.HasProwlarr())},
		{"name": "myrient", "label": "Myrient", "status": "ok"},
		{"name": "vimm", "label": "Vimm's Lair", "status": "ok"},
	}

	// Total users.
	userCount, _ := s.mgr.Jobs().CountUsers()

	// Uptime.
	uptime := time.Since(startTime)
	uptimeStr := formatUptime(uptime)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"library_stats":    libStats,
		"library_total":    libTotal,
		"active_downloads": activeCount,
		"sources_health":   sourcesHealth,
		"total_users":      userCount,
		"system": map[string]string{
			"version":    "1.0.0",
			"uptime":     uptimeStr,
			"go_version": runtime.Version(),
		},
	})
}

func sourceStatus(configured bool) string {
	if configured {
		return "ok"
	}
	return "not_configured"
}

func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
