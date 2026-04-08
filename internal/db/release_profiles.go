package db

import (
	"encoding/json"
	"log/slog"
)

// PreferredWord is a word with a score boost/penalty.
type PreferredWord struct {
	Word  string `json:"word"`
	Score int    `json:"score"`
}

// ReleaseProfile defines filtering and scoring preferences for releases.
type ReleaseProfile struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	MustContain    []string        `json:"must_contain"`
	MustNotContain []string        `json:"must_not_contain"`
	Preferred      []PreferredWord `json:"preferred"`
	Enabled        bool            `json:"enabled"`
}

func (s *JobStore) migrateReleaseProfiles() {
	ddl := `CREATE TABLE IF NOT EXISTS release_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		must_contain TEXT NOT NULL DEFAULT '[]',
		must_not_contain TEXT NOT NULL DEFAULT '[]',
		preferred TEXT NOT NULL DEFAULT '[]',
		enabled INTEGER NOT NULL DEFAULT 1
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate release_profiles", "error", err)
	}

	// Seed default profile
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM release_profiles").Scan(&count)
	if count == 0 {
		mustNot, _ := json.Marshal([]string{"password", "RAT", "crypto", "miner", "survey", "keygen"})
		preferred, _ := json.Marshal([]PreferredWord{
			{Word: "FitGirl", Score: 15},
			{Word: "DODI", Score: 12},
			{Word: "repack", Score: 10},
			{Word: "MULTi", Score: 5},
			{Word: "GOG", Score: 8},
			{Word: "CODEX", Score: 7},
			{Word: "PLAZA", Score: 7},
			{Word: "EMPRESS", Score: 10},
		})
		s.db.Exec(
			"INSERT INTO release_profiles (name, must_contain, must_not_contain, preferred, enabled) VALUES (?, '[]', ?, ?, 1)",
			"Game Scene Default", string(mustNot), string(preferred),
		)
	}
}

// GetReleaseProfiles returns all release profiles.
func (s *JobStore) GetReleaseProfiles() []ReleaseProfile {
	rows, err := s.db.Query("SELECT id, name, must_contain, must_not_contain, preferred, enabled FROM release_profiles ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var profiles []ReleaseProfile
	for rows.Next() {
		var p ReleaseProfile
		var mustContainJSON, mustNotContainJSON, preferredJSON string
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &mustContainJSON, &mustNotContainJSON, &preferredJSON, &enabled); err != nil {
			continue
		}
		json.Unmarshal([]byte(mustContainJSON), &p.MustContain)
		json.Unmarshal([]byte(mustNotContainJSON), &p.MustNotContain)
		json.Unmarshal([]byte(preferredJSON), &p.Preferred)
		p.Enabled = enabled != 0
		if p.MustContain == nil {
			p.MustContain = []string{}
		}
		if p.MustNotContain == nil {
			p.MustNotContain = []string{}
		}
		if p.Preferred == nil {
			p.Preferred = []PreferredWord{}
		}
		profiles = append(profiles, p)
	}
	return profiles
}

// GetReleaseProfile returns a single release profile by ID.
func (s *JobStore) GetReleaseProfile(id int64) (*ReleaseProfile, error) {
	row := s.db.QueryRow("SELECT id, name, must_contain, must_not_contain, preferred, enabled FROM release_profiles WHERE id = ?", id)
	var p ReleaseProfile
	var mustContainJSON, mustNotContainJSON, preferredJSON string
	var enabled int
	err := row.Scan(&p.ID, &p.Name, &mustContainJSON, &mustNotContainJSON, &preferredJSON, &enabled)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(mustContainJSON), &p.MustContain)
	json.Unmarshal([]byte(mustNotContainJSON), &p.MustNotContain)
	json.Unmarshal([]byte(preferredJSON), &p.Preferred)
	p.Enabled = enabled != 0
	return &p, nil
}

// AddReleaseProfile inserts a new release profile.
func (s *JobStore) AddReleaseProfile(p *ReleaseProfile) (int64, error) {
	mustContainJSON, _ := json.Marshal(p.MustContain)
	mustNotContainJSON, _ := json.Marshal(p.MustNotContain)
	preferredJSON, _ := json.Marshal(p.Preferred)
	result, err := s.db.Exec(
		"INSERT INTO release_profiles (name, must_contain, must_not_contain, preferred, enabled) VALUES (?, ?, ?, ?, ?)",
		p.Name, string(mustContainJSON), string(mustNotContainJSON), string(preferredJSON), boolToInt(p.Enabled),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateReleaseProfile updates an existing release profile.
func (s *JobStore) UpdateReleaseProfile(p *ReleaseProfile) error {
	mustContainJSON, _ := json.Marshal(p.MustContain)
	mustNotContainJSON, _ := json.Marshal(p.MustNotContain)
	preferredJSON, _ := json.Marshal(p.Preferred)
	_, err := s.db.Exec(
		"UPDATE release_profiles SET name = ?, must_contain = ?, must_not_contain = ?, preferred = ?, enabled = ? WHERE id = ?",
		p.Name, string(mustContainJSON), string(mustNotContainJSON), string(preferredJSON), boolToInt(p.Enabled), p.ID,
	)
	return err
}

// DeleteReleaseProfile removes a release profile by ID.
func (s *JobStore) DeleteReleaseProfile(id int64) error {
	_, err := s.db.Exec("DELETE FROM release_profiles WHERE id = ?", id)
	return err
}

// ApplyReleaseProfiles scores a title against enabled release profiles.
// Returns: score adjustment, whether it should be excluded.
func (s *JobStore) ApplyReleaseProfiles(title string) (int, bool) {
	profiles := s.GetReleaseProfiles()
	totalScore := 0
	titleLower := toLower(title)

	for _, p := range profiles {
		if !p.Enabled {
			continue
		}

		// Check must_not_contain (exclude)
		for _, word := range p.MustNotContain {
			if containsIgnoreCase(titleLower, toLower(word)) {
				return 0, true
			}
		}

		// Check must_contain (all must match if any specified)
		if len(p.MustContain) > 0 {
			allFound := true
			for _, word := range p.MustContain {
				if !containsIgnoreCase(titleLower, toLower(word)) {
					allFound = false
					break
				}
			}
			if !allFound {
				continue
			}
		}

		// Apply preferred word scores
		for _, pw := range p.Preferred {
			if containsIgnoreCase(titleLower, toLower(pw.Word)) {
				totalScore += pw.Score
			}
		}
	}

	return totalScore, false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		b[i] = c
	}
	return string(b)
}

func containsIgnoreCase(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
