package platform

import "testing"

func TestIsROMHack(t *testing.T) {
	hacks := []string{
		"Super Mario World [h1].sfc",
		"Super Mario World [h Kaizo].sfc",
		"Pokemon Red [hM04].gb",
		"Mother 3 [T+Eng1.3_Tomato].gba",
		"Seiken Densetsu 3 [T-Eng].sfc",
		"Super Metroid (Hack) Redesign.sfc",
		"Zelda Parallel Worlds hack.sfc",
		"Kaizo Mario ROM hack",
	}
	for _, n := range hacks {
		if !IsROMHack(n) {
			t.Errorf("IsROMHack(%q) = false, want true", n)
		}
	}
	clean := []string{
		"Super Mario World (USA).sfc",
		"Halo [HD Remaster]",
		"Game [hdd install]",
		".hack//Infection (USA).iso",
		"Hacker Evolution",
		"Chrono Trigger [!].sfc",
		"Tetris (World) (Rev 1).gb",
	}
	for _, n := range clean {
		if IsROMHack(n) {
			t.Errorf("IsROMHack(%q) = true, want false", n)
		}
	}
}

func TestLibraryFolderHacksRouting(t *testing.T) {
	const hack = "Super Mario World [h1].sfc"
	if got := LibraryFolder("snes", false, hack); got != "snes" {
		t.Errorf("routing off: %q, want snes", got)
	}
	if got := LibraryFolder("snes", true, hack); got != "snes-hacks" {
		t.Errorf("routing on: %q, want snes-hacks", got)
	}
	if got := LibraryFolder("snes", true, "Super Mario World (USA).sfc"); got != "snes" {
		t.Errorf("routing on, clean ROM: %q, want snes", got)
	}
	if got := LibraryFolder("", true, hack); got != "" {
		t.Errorf("no platform: %q, want empty", got)
	}
}
