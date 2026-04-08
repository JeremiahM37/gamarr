package organize

import (
	"os"
	"path/filepath"
	"testing"

	"gamarr/internal/config"
)

func TestCleanForFilename(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips SKIDROW", "Game Name-SKIDROW", "Game Name"},
		{"strips CODEX", "Game CODEX", "Game"},
		{"strips FitGirl", "Game FitGirl Repack", "Game"},
		{"strips REPACK tag", "Game REPACK Edition", "Game Edition"},
		{"strips brackets", "Game [Region Free] Title", "Game Title"},
		{"strips dash group", "Game - PLAZA", "Game"},
		{"strips URL encoding %20", "Game%20Name%20v1.0", "Game Name v1.0"},
		{"strips URL encoding %28 %29", "Game%28Deluxe%29", "Game(Deluxe)"},
		{"collapses multi-space", "Game   Name", "Game Name"},
		{"trims edges", "  -Game Name-  ", "Game Name"},
		{"empty after clean", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanForFilename(tt.input)
			if got != tt.want {
				t.Errorf("cleanForFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Game: The Sequel", "Game The Sequel"},
		{`Game "Special" <Edition>`, "Game Special Edition"},
		{"Game|Title?2", "GameTitle2"},
		{"  ", "Unknown"},
		{"", "Unknown"},
		{"Normal Name", "Normal Name"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveCollision(t *testing.T) {
	dir := t.TempDir()

	// No collision
	path := filepath.Join(dir, "game.zip")
	got := resolveCollision(path)
	if got != path {
		t.Errorf("no collision: got %q, want %q", got, path)
	}

	// Create the file to force collision
	os.WriteFile(path, []byte("data"), 0644)

	got = resolveCollision(path)
	want := filepath.Join(dir, "game (1).zip")
	if got != want {
		t.Errorf("first collision: got %q, want %q", got, want)
	}

	// Create (1) too
	os.WriteFile(want, []byte("data"), 0644)
	got = resolveCollision(path)
	want2 := filepath.Join(dir, "game (2).zip")
	if got != want2 {
		t.Errorf("second collision: got %q, want %q", got, want2)
	}
}

func TestRenameOnImport_Disabled(t *testing.T) {
	cfg := &config.Config{RenameEnabled: false}
	rn := NewRenamer(cfg)

	path := "/some/path/game.zip"
	got, err := rn.RenameOnImport(path, "Game Title", "Switch", "switch", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("disabled rename should return original path, got %q", got)
	}
}

func TestRenameOnImport_DefaultPattern(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "original-file-CODEX.zip")
	os.WriteFile(src, []byte("test"), 0644)

	cfg := &config.Config{RenameEnabled: true, RenamePattern: ""}
	rn := NewRenamer(cfg)

	got, err := rn.RenameOnImport(src, "Super Mario", "Switch", "switch", false)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "Super Mario (Switch).zip")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Verify the file was actually renamed
	if _, err := os.Stat(got); os.IsNotExist(err) {
		t.Error("renamed file does not exist")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("original file should not exist after rename")
	}
}

func TestRenameOnImport_CustomPattern(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.nsp")
	os.WriteFile(src, []byte("test"), 0644)

	cfg := &config.Config{
		RenameEnabled: true,
		RenamePattern: "{slug} - {title}.{ext}",
	}
	rn := NewRenamer(cfg)

	got, err := rn.RenameOnImport(src, "Zelda", "Nintendo Switch", "switch", false)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "switch - Zelda.nsp")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenameOnImport_PCPlatformFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "file.exe")
	os.WriteFile(src, []byte("test"), 0644)

	cfg := &config.Config{RenameEnabled: true, RenamePattern: "{title} ({platform}).{ext}"}
	rn := NewRenamer(cfg)

	got, err := rn.RenameOnImport(src, "Halo", "", "", true)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "Halo (PC).exe")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenameOnImport_EmptyTitle(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "my-game.zip")
	os.WriteFile(src, []byte("test"), 0644)

	cfg := &config.Config{RenameEnabled: true, RenamePattern: "{title}.{ext}"}
	rn := NewRenamer(cfg)

	got, err := rn.RenameOnImport(src, "", "Switch", "switch", false)
	if err != nil {
		t.Fatal(err)
	}

	// Should fall back to cleaned filename
	if got == src {
		// If the cleaned filename happens to equal the original, that's fine
		return
	}
	if _, err := os.Stat(got); os.IsNotExist(err) {
		t.Error("renamed file does not exist")
	}
}

func TestRenameOnImport_CollisionHandling(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "original.zip")
	os.WriteFile(src, []byte("test"), 0644)

	// Create the target file to force a collision
	target := filepath.Join(dir, "Game (Switch).zip")
	os.WriteFile(target, []byte("existing"), 0644)

	cfg := &config.Config{RenameEnabled: true, RenamePattern: "{title} ({platform}).{ext}"}
	rn := NewRenamer(cfg)

	got, err := rn.RenameOnImport(src, "Game", "Switch", "switch", false)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(dir, "Game (Switch) (1).zip")
	if got != want {
		t.Errorf("collision: got %q, want %q", got, want)
	}
}
