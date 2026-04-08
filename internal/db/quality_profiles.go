package db

import (
	"encoding/json"
	"log/slog"
)

// QualityProfile defines source ranking and size preferences for downloads.
type QualityProfile struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	SourceRanking    []string `json:"source_ranking"`    // ordered list: best first
	PreferredSizeMin int64    `json:"preferred_size_min"` // bytes
	PreferredSizeMax int64    `json:"preferred_size_max"` // bytes
	UpgradeAllowed   bool     `json:"upgrade_allowed"`
	CutoffSource     string   `json:"cutoff_source"` // stop upgrading once this source is reached
}

func (s *JobStore) migrateQualityProfiles() {
	ddl := `CREATE TABLE IF NOT EXISTS quality_profiles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		source_ranking TEXT NOT NULL DEFAULT '[]',
		preferred_size_min INTEGER NOT NULL DEFAULT 0,
		preferred_size_max INTEGER NOT NULL DEFAULT 0,
		upgrade_allowed INTEGER NOT NULL DEFAULT 0,
		cutoff_source TEXT NOT NULL DEFAULT ''
	)`
	if _, err := s.db.Exec(ddl); err != nil {
		slog.Warn("migrate quality_profiles", "error", err)
	}
	// Seed defaults if table is empty
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM quality_profiles").Scan(&count)
	if count == 0 {
		pcRanking, _ := json.Marshal([]string{"FitGirl", "DODI", "PLAZA", "Myrient", "Vimm"})
		romRanking, _ := json.Marshal([]string{"Myrient", "Vimm"})
		s.db.Exec(
			"INSERT INTO quality_profiles (name, source_ranking, preferred_size_min, preferred_size_max, upgrade_allowed, cutoff_source) VALUES (?, ?, ?, ?, ?, ?)",
			"PC Default", string(pcRanking), 50*1024*1024, 100*1024*1024*1024, 1, "FitGirl",
		)
		s.db.Exec(
			"INSERT INTO quality_profiles (name, source_ranking, preferred_size_min, preferred_size_max, upgrade_allowed, cutoff_source) VALUES (?, ?, ?, ?, ?, ?)",
			"ROM Default", string(romRanking), 0, 0, 0, "Myrient",
		)
	}
}

// GetQualityProfiles returns all quality profiles.
func (s *JobStore) GetQualityProfiles() []QualityProfile {
	rows, err := s.db.Query("SELECT id, name, source_ranking, preferred_size_min, preferred_size_max, upgrade_allowed, cutoff_source FROM quality_profiles ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var profiles []QualityProfile
	for rows.Next() {
		var p QualityProfile
		var rankingJSON string
		var upgradeAllowed int
		if err := rows.Scan(&p.ID, &p.Name, &rankingJSON, &p.PreferredSizeMin, &p.PreferredSizeMax, &upgradeAllowed, &p.CutoffSource); err != nil {
			continue
		}
		json.Unmarshal([]byte(rankingJSON), &p.SourceRanking)
		p.UpgradeAllowed = upgradeAllowed != 0
		profiles = append(profiles, p)
	}
	return profiles
}

// GetQualityProfile returns a single quality profile by ID.
func (s *JobStore) GetQualityProfile(id int64) (*QualityProfile, error) {
	row := s.db.QueryRow("SELECT id, name, source_ranking, preferred_size_min, preferred_size_max, upgrade_allowed, cutoff_source FROM quality_profiles WHERE id = ?", id)
	var p QualityProfile
	var rankingJSON string
	var upgradeAllowed int
	err := row.Scan(&p.ID, &p.Name, &rankingJSON, &p.PreferredSizeMin, &p.PreferredSizeMax, &upgradeAllowed, &p.CutoffSource)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(rankingJSON), &p.SourceRanking)
	p.UpgradeAllowed = upgradeAllowed != 0
	return &p, nil
}

// AddQualityProfile inserts a new quality profile.
func (s *JobStore) AddQualityProfile(p *QualityProfile) (int64, error) {
	rankingJSON, _ := json.Marshal(p.SourceRanking)
	result, err := s.db.Exec(
		"INSERT INTO quality_profiles (name, source_ranking, preferred_size_min, preferred_size_max, upgrade_allowed, cutoff_source) VALUES (?, ?, ?, ?, ?, ?)",
		p.Name, string(rankingJSON), p.PreferredSizeMin, p.PreferredSizeMax, boolToInt(p.UpgradeAllowed), p.CutoffSource,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateQualityProfile updates an existing quality profile.
func (s *JobStore) UpdateQualityProfile(p *QualityProfile) error {
	rankingJSON, _ := json.Marshal(p.SourceRanking)
	_, err := s.db.Exec(
		"UPDATE quality_profiles SET name = ?, source_ranking = ?, preferred_size_min = ?, preferred_size_max = ?, upgrade_allowed = ?, cutoff_source = ? WHERE id = ?",
		p.Name, string(rankingJSON), p.PreferredSizeMin, p.PreferredSizeMax, boolToInt(p.UpgradeAllowed), p.CutoffSource, p.ID,
	)
	return err
}

// DeleteQualityProfile removes a quality profile by ID.
func (s *JobStore) DeleteQualityProfile(id int64) error {
	_, err := s.db.Exec("DELETE FROM quality_profiles WHERE id = ?", id)
	return err
}

// SourceRankScore returns a score boost (higher is better) for a source/group name
// based on the first matching quality profile's source ranking.
func (s *JobStore) SourceRankScore(sourceOrGroup string) int {
	profiles := s.GetQualityProfiles()
	if len(profiles) == 0 {
		return 0
	}
	// Use first profile as default ranking
	p := profiles[0]
	for i, src := range p.SourceRanking {
		if equalsIgnoreCase(src, sourceOrGroup) {
			// Higher rank = higher score. Top rank gets len(ranking)*2 points.
			return (len(p.SourceRanking) - i) * 2
		}
	}
	return 0
}

func equalsIgnoreCase(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
