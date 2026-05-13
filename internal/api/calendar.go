package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// calendarCache stores cached RAWG API results.
var calendarCache struct {
	mu          sync.RWMutex
	upcoming    []calendarEntry
	recent      []calendarEntry
	lastFetched time.Time
	cacheTTL    time.Duration
}

func init() {
	calendarCache.cacheTTL = 6 * time.Hour
}

// calendarEntry represents a game release.
type calendarEntry struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	ReleaseDate     string   `json:"release_date"`
	Platforms       []string `json:"platforms"`
	BackgroundImage string   `json:"background_image,omitempty"`
	Rating          float64  `json:"rating"`
	OnWishlist      bool     `json:"on_wishlist"`
}

// rawgGame represents a game from the RAWG API.
type rawgGame struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Released        string  `json:"released"`
	BackgroundImage string  `json:"background_image"`
	Rating          float64 `json:"rating"`
	Platforms       []struct {
		Platform struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"platform"`
	} `json:"platforms"`
}

type rawgResponse struct {
	Results []rawgGame `json:"results"`
}

// handleCalendar handles GET /api/calendar — upcoming game releases.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	platformFilter := r.URL.Query().Get("platform")

	entries := s.getUpcomingReleases(days)

	// Filter by platform if specified
	if platformFilter != "" && platformFilter != "all" {
		var filtered []calendarEntry
		for _, e := range entries {
			for _, p := range e.Platforms {
				if strings.EqualFold(p, platformFilter) || strings.EqualFold(slugify(p), platformFilter) {
					filtered = append(filtered, e)
					break
				}
			}
		}
		entries = filtered
	}

	// Cross-reference with wishlist
	wishlist := s.mgr.Jobs().GetWishlist()
	wishlistMap := make(map[string]bool)
	for _, w := range wishlist {
		wishlistMap[strings.ToLower(strings.TrimSpace(w.Title))] = true
	}
	for i := range entries {
		if wishlistMap[strings.ToLower(strings.TrimSpace(entries[i].Name))] {
			entries[i].OnWishlist = true
		}
	}

	if entries == nil {
		entries = []calendarEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"entries": entries,
		"total":   len(entries),
		"days":    days,
	})
}

// handleCalendarRecent handles GET /api/calendar/recent — recently released games.
func (s *Server) handleCalendarRecent(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 30
	}
	platformFilter := r.URL.Query().Get("platform")

	entries := s.getRecentReleases(days)

	if platformFilter != "" && platformFilter != "all" {
		var filtered []calendarEntry
		for _, e := range entries {
			for _, p := range e.Platforms {
				if strings.EqualFold(p, platformFilter) || strings.EqualFold(slugify(p), platformFilter) {
					filtered = append(filtered, e)
					break
				}
			}
		}
		entries = filtered
	}

	// Cross-reference with wishlist
	wishlist := s.mgr.Jobs().GetWishlist()
	wishlistMap := make(map[string]bool)
	for _, w := range wishlist {
		wishlistMap[strings.ToLower(strings.TrimSpace(w.Title))] = true
	}
	for i := range entries {
		if wishlistMap[strings.ToLower(strings.TrimSpace(entries[i].Name))] {
			entries[i].OnWishlist = true
		}
	}

	if entries == nil {
		entries = []calendarEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"entries": entries,
		"total":   len(entries),
		"days":    days,
	})
}

func (s *Server) getUpcomingReleases(days int) []calendarEntry {
	calendarCache.mu.RLock()
	if time.Since(calendarCache.lastFetched) < calendarCache.cacheTTL && calendarCache.upcoming != nil {
		result := make([]calendarEntry, len(calendarCache.upcoming))
		copy(result, calendarCache.upcoming)
		calendarCache.mu.RUnlock()

		// Filter to requested day range
		cutoff := time.Now().AddDate(0, 0, days).Format("2006-01-02")
		var filtered []calendarEntry
		for _, e := range result {
			if e.ReleaseDate <= cutoff {
				filtered = append(filtered, e)
			}
		}
		return filtered
	}
	calendarCache.mu.RUnlock()

	// Fetch from RAWG
	s.refreshCalendarCache()

	calendarCache.mu.RLock()
	defer calendarCache.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, days).Format("2006-01-02")
	var filtered []calendarEntry
	for _, e := range calendarCache.upcoming {
		if e.ReleaseDate <= cutoff {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (s *Server) getRecentReleases(days int) []calendarEntry {
	calendarCache.mu.RLock()
	if time.Since(calendarCache.lastFetched) < calendarCache.cacheTTL && calendarCache.recent != nil {
		result := make([]calendarEntry, len(calendarCache.recent))
		copy(result, calendarCache.recent)
		calendarCache.mu.RUnlock()

		cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
		var filtered []calendarEntry
		for _, e := range result {
			if e.ReleaseDate >= cutoff {
				filtered = append(filtered, e)
			}
		}
		return filtered
	}
	calendarCache.mu.RUnlock()

	s.refreshCalendarCache()

	calendarCache.mu.RLock()
	defer calendarCache.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	var filtered []calendarEntry
	for _, e := range calendarCache.recent {
		if e.ReleaseDate >= cutoff {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (s *Server) refreshCalendarCache() {
	if !s.cfg.HasRAWG() {
		// No RAWG API key, return empty
		calendarCache.mu.Lock()
		calendarCache.upcoming = []calendarEntry{}
		calendarCache.recent = []calendarEntry{}
		calendarCache.lastFetched = time.Now()
		calendarCache.mu.Unlock()
		return
	}

	now := time.Now()
	today := now.Format("2006-01-02")
	futureDate := now.AddDate(0, 0, 90).Format("2006-01-02") // Cache 90 days ahead
	pastDate := now.AddDate(0, 0, -90).Format("2006-01-02")  // Cache 90 days back

	// Fetch upcoming
	upcoming := s.fetchRAWGGames(today, futureDate, "released")
	// Fetch recent
	recent := s.fetchRAWGGames(pastDate, today, "-released")

	calendarCache.mu.Lock()
	calendarCache.upcoming = upcoming
	calendarCache.recent = recent
	calendarCache.lastFetched = time.Now()
	calendarCache.mu.Unlock()
}

func (s *Server) fetchRAWGGames(dateFrom, dateTo, ordering string) []calendarEntry {
	url := fmt.Sprintf(
		"https://api.rawg.io/api/games?key=%s&dates=%s,%s&ordering=%s&page_size=40",
		s.cfg.RAWGAPIKey, dateFrom, dateTo, ordering,
	)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		slog.Warn("RAWG API error", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		slog.Warn("RAWG API bad status", "status", resp.StatusCode)
		return nil
	}

	var rawgResp rawgResponse
	if err := json.NewDecoder(resp.Body).Decode(&rawgResp); err != nil {
		slog.Warn("RAWG API decode error", "error", err)
		return nil
	}

	var entries []calendarEntry
	for _, g := range rawgResp.Results {
		var platforms []string
		for _, p := range g.Platforms {
			platforms = append(platforms, p.Platform.Name)
		}
		entries = append(entries, calendarEntry{
			ID:              g.ID,
			Name:            g.Name,
			ReleaseDate:     g.Released,
			Platforms:       platforms,
			BackgroundImage: g.BackgroundImage,
			Rating:          g.Rating,
		})
	}
	return entries
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
