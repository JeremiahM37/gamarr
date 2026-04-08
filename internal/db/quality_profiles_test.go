package db

import (
	"testing"
)

func TestQualityProfile_CRUD(t *testing.T) {
	store := newTestStore(t)

	// Default profiles are seeded
	defaults := store.GetQualityProfiles()
	defaultCount := len(defaults)
	if defaultCount < 2 {
		t.Fatalf("expected at least 2 default profiles, got %d", defaultCount)
	}

	// Verify default "PC Default" profile
	found := false
	for _, p := range defaults {
		if p.Name == "PC Default" {
			found = true
			if len(p.SourceRanking) == 0 {
				t.Error("PC Default should have source ranking")
			}
			if !p.UpgradeAllowed {
				t.Error("PC Default should have upgrade_allowed=true")
			}
			if p.CutoffSource != "FitGirl" {
				t.Errorf("CutoffSource=%q, want 'FitGirl'", p.CutoffSource)
			}
		}
	}
	if !found {
		t.Error("expected 'PC Default' profile to be seeded")
	}

	// Add custom profile
	profile := &QualityProfile{
		Name:             "Custom",
		SourceRanking:    []string{"Myrient", "Vimm", "Prowlarr"},
		PreferredSizeMin: 1_000_000,
		PreferredSizeMax: 10_000_000_000,
		UpgradeAllowed:   false,
		CutoffSource:     "Myrient",
	}
	id, err := store.AddQualityProfile(profile)
	if err != nil {
		t.Fatalf("AddQualityProfile: %v", err)
	}
	if id <= 0 {
		t.Error("expected positive ID")
	}

	// Get by ID
	got, err := store.GetQualityProfile(id)
	if err != nil {
		t.Fatalf("GetQualityProfile: %v", err)
	}
	if got.Name != "Custom" {
		t.Errorf("Name=%q, want 'Custom'", got.Name)
	}
	if len(got.SourceRanking) != 3 {
		t.Errorf("SourceRanking len=%d, want 3", len(got.SourceRanking))
	}
	if got.SourceRanking[0] != "Myrient" {
		t.Errorf("SourceRanking[0]=%q, want 'Myrient'", got.SourceRanking[0])
	}
	if got.PreferredSizeMin != 1_000_000 {
		t.Errorf("PreferredSizeMin=%d", got.PreferredSizeMin)
	}
	if got.PreferredSizeMax != 10_000_000_000 {
		t.Errorf("PreferredSizeMax=%d", got.PreferredSizeMax)
	}
	if got.UpgradeAllowed {
		t.Error("UpgradeAllowed should be false")
	}
	if got.CutoffSource != "Myrient" {
		t.Errorf("CutoffSource=%q", got.CutoffSource)
	}

	// Update
	got.Name = "Updated Custom"
	got.UpgradeAllowed = true
	err = store.UpdateQualityProfile(got)
	if err != nil {
		t.Fatalf("UpdateQualityProfile: %v", err)
	}
	updated, _ := store.GetQualityProfile(id)
	if updated.Name != "Updated Custom" {
		t.Errorf("after update: Name=%q", updated.Name)
	}
	if !updated.UpgradeAllowed {
		t.Error("after update: UpgradeAllowed should be true")
	}

	// Delete
	err = store.DeleteQualityProfile(id)
	if err != nil {
		t.Fatalf("DeleteQualityProfile: %v", err)
	}
	profiles := store.GetQualityProfiles()
	if len(profiles) != defaultCount {
		t.Errorf("after delete: len=%d, want %d", len(profiles), defaultCount)
	}
}

func TestQualityProfile_GetNonExistent(t *testing.T) {
	store := newTestStore(t)

	_, err := store.GetQualityProfile(99999)
	if err == nil {
		t.Error("expected error for nonexistent profile")
	}
}

func TestQualityProfile_DuplicateName(t *testing.T) {
	store := newTestStore(t)

	store.AddQualityProfile(&QualityProfile{
		Name:          "Unique",
		SourceRanking: []string{"A"},
	})
	_, err := store.AddQualityProfile(&QualityProfile{
		Name:          "Unique",
		SourceRanking: []string{"B"},
	})
	if err == nil {
		t.Error("expected error for duplicate profile name")
	}
}

func TestSourceRankScore(t *testing.T) {
	store := newTestStore(t)

	// Default "PC Default" profile: ["FitGirl", "DODI", "PLAZA", "Myrient", "Vimm"]
	// FitGirl is index 0 -> score = (5-0)*2 = 10
	// Vimm is index 4 -> score = (5-4)*2 = 2

	score := store.SourceRankScore("FitGirl")
	if score != 10 {
		t.Errorf("FitGirl score=%d, want 10", score)
	}

	score = store.SourceRankScore("Vimm")
	if score != 2 {
		t.Errorf("Vimm score=%d, want 2", score)
	}

	score = store.SourceRankScore("DODI")
	if score != 8 {
		t.Errorf("DODI score=%d, want 8", score)
	}

	// Case insensitive
	score = store.SourceRankScore("fitgirl")
	if score != 10 {
		t.Errorf("fitgirl (lowercase) score=%d, want 10", score)
	}

	// Unknown source
	score = store.SourceRankScore("Unknown")
	if score != 0 {
		t.Errorf("Unknown score=%d, want 0", score)
	}
}

func TestEqualsIgnoreCase(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"abc", "ABC", true},
		{"abc", "abc", true},
		{"ABC", "ABC", true},
		{"abc", "abd", false},
		{"ab", "abc", false},
		{"", "", true},
	}
	for _, tt := range tests {
		got := equalsIgnoreCase(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("equalsIgnoreCase(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestQualityProfile_EmptySourceRanking(t *testing.T) {
	store := newTestStore(t)

	id, err := store.AddQualityProfile(&QualityProfile{
		Name:          "Empty Ranking",
		SourceRanking: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetQualityProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	// Empty slice should be preserved (or nil, either is fine)
	if len(got.SourceRanking) != 0 {
		t.Errorf("SourceRanking len=%d, want 0", len(got.SourceRanking))
	}
}
