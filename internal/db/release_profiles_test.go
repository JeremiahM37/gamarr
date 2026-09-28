package db

import "testing"

func TestContainsWordIgnoreCase(t *testing.T) {
	tests := []struct {
		name  string
		title string
		word  string
		want  bool
	}{
		// The letters inside a longer word are not the word. These are the
		// releases the substring form dropped.
		{"letters mid-word", "entity the black day - v1.01 + operative dlc v0.2 [fitgirl repack]", "rat", false},
		{"followed by a word character", "ratchet", "rat", false},
		{"preceded by a word character", "burat", "rat", false},
		{"word ending in the letters", "separate", "rat", false},

		// The word itself still matches against the separators release names use.
		{"standalone", "rat", "rat", true},
		{"between spaces", "game rat v1.0", "rat", true},
		{"after a dot", "game.rat.repack", "rat", true},
		{"before a hyphen", "rat-repack", "rat", true},
		{"inside brackets", "game (rat) 2024", "rat", true},
		{"underscore separator", "game_rat_repack", "rat", true},

		// Multi-word entries are bounded at the outer edges only.
		{"phrase between spaces", "a free download here", "free download", true},
		{"phrase inside a word", "free downloadable", "free download", false},

		{"empty entry", "anything", "", false},
		{"no occurrence", "normal game release", "rat", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsWordIgnoreCase(tt.title, tt.word); got != tt.want {
				t.Errorf("containsWordIgnoreCase(%q, %q) = %v, want %v", tt.title, tt.word, got, tt.want)
			}
		})
	}
}

func TestApplyReleaseProfiles_WordBounds(t *testing.T) {
	store := newTestStore(t)

	// The seeded default profile carries RAT, MULTi and the rest, which would
	// score and exclude alongside this one. Drop it so these cases test the
	// profile below and nothing else.
	for _, p := range store.GetReleaseProfiles() {
		if err := store.DeleteReleaseProfile(p.ID); err != nil {
			t.Fatalf("DeleteReleaseProfile(%d): %v", p.ID, err)
		}
	}

	_, err := store.AddReleaseProfile(&ReleaseProfile{
		Name:           "Test",
		MustContain:    []string{"MULTi"},
		MustNotContain: []string{"RAT"},
		Preferred:      []PreferredWord{{Word: "MULTi", Score: 5}},
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("AddReleaseProfile: %v", err)
	}

	t.Run("exclusion does not fire inside a longer word", func(t *testing.T) {
		_, exclude := store.ApplyReleaseProfiles("ENTITY: THE BLACK DAY - v1.01 + Operative DLC v0.2 [FitGirl Repack]")
		if exclude {
			t.Error("excluded a title whose only RAT is inside \"Operative\"")
		}
	})

	t.Run("exclusion still fires on the word itself", func(t *testing.T) {
		_, exclude := store.ApplyReleaseProfiles("Some Game RAT v1.0")
		if !exclude {
			t.Error("did not exclude a title carrying RAT as a word")
		}
	})

	t.Run("must_contain and preferred keep matching fragments", func(t *testing.T) {
		score, exclude := store.ApplyReleaseProfiles("Stardew Valley (v1.6.0, MULTi12) [FitGirl Repack]")
		if exclude {
			t.Fatal("excluded a title carrying no exclusion entry")
		}
		// 5 requires both sides to stay on the substring form: bounding
		// must_contain skips the profile before scoring, and bounding preferred
		// matches nothing to add.
		if score != 5 {
			t.Errorf("score=%d, want 5: a fragment entry must still satisfy must_contain and still score", score)
		}
	})
}
