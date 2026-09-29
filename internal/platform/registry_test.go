package platform

import (
	"archive/zip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gamarr/internal/sources"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func TestRegistryIntegrity(t *testing.T) {
	slugs := map[string]bool{}
	cats := map[int]string{}
	exts := map[string]string{}
	aliases := map[string]string{}
	for _, p := range Registry {
		if !slugRe.MatchString(p.Slug) {
			t.Errorf("slug %q is not a plain lowercase folder name", p.Slug)
		}
		if strings.HasSuffix(p.Slug, HacksSuffix) {
			t.Errorf("slug %q collides with the hacks folder suffix", p.Slug)
		}
		if slugs[p.Slug] {
			t.Errorf("duplicate slug %q", p.Slug)
		}
		slugs[p.Slug] = true
		if p.Name == "" {
			t.Errorf("%s has no name", p.Slug)
		}
		for _, c := range p.Categories {
			if owner, dup := cats[c]; dup {
				t.Errorf("category %d maps to both %s and %s", c, owner, p.Slug)
			}
			cats[c] = p.Slug
			if IsGenericCategory(c) {
				t.Errorf("%s claims generic category %d", p.Slug, c)
			}
		}
		for _, e := range p.Extensions {
			if !strings.HasPrefix(e, ".") || e != strings.ToLower(e) {
				t.Errorf("%s extension %q must be lowercase with a dot", p.Slug, e)
			}
			if owner, dup := exts[e]; dup {
				t.Errorf("extension %s is unique to both %s and %s", e, owner, p.Slug)
			}
			exts[e] = p.Slug
			if nonROMCollisions[e] {
				t.Errorf("%s: %s collides with everyday files and belongs in Formats", p.Slug, e)
			}
		}
		for _, e := range p.Formats {
			if !strings.HasPrefix(e, ".") || e != strings.ToLower(e) {
				t.Errorf("%s format %q must be lowercase with a dot", p.Slug, e)
			}
		}
		names := append([]string{p.Slug, strings.ToLower(p.Name)}, p.Aliases...)
		for _, n := range names {
			if owner, dup := aliases[n]; dup && owner != p.Slug {
				t.Errorf("metadata name %q maps to both %s and %s", n, owner, p.Slug)
			}
			aliases[n] = p.Slug
		}
		if (p.MinSize == 0) != (p.MaxSize == 0) || p.MinSize > p.MaxSize {
			t.Errorf("%s size band %d-%d is inconsistent", p.Slug, p.MinSize, p.MaxSize)
		}
	}
	for c := range genericCategories {
		if info, ok := PlatformMap[c]; ok && info.Slug != "" {
			t.Errorf("generic category %d maps to platform %s", c, info.Slug)
		}
	}
}

func TestRegistryExcludesArcadeAndCurrentGen(t *testing.T) {
	for _, slug := range []string{"arcade", "mame", "fbneo", "ps5", "xboxone", "xbox-series", "series", "switch2"} {
		if _, ok := Lookup(slug); ok {
			t.Errorf("%s must not be a platform", slug)
		}
	}
}

func TestRegistryHasRetroPlatforms(t *testing.T) {
	for _, slug := range []string{
		"gbc", "sms", "gamegear", "segacd", "sega32", "tg16", "turbografx-cd",
		"neogeoaes", "neo-geo-cd", "atari2600", "atari5200", "atari7800", "lynx",
		"colecovision", "fds", "virtualboy", "wonderswan-color", "ngp", "3do",
		"cdi", "msx", "c64", "amiga", "dos", "pc98", "vectrex", "satellaview",
		"sega-pico", "g-and-w",
	} {
		if _, ok := Lookup(slug); !ok {
			t.Errorf("platform %s missing from the registry", slug)
		}
	}
}

// Every slug the source registry files things under must be a platform, or a
// Myrient/Minerva/Vimm hit lands in a folder Gamarr knows nothing about.
func TestSourceRegistrySlugsArePlatforms(t *testing.T) {
	reg, err := sources.Default()
	if err != nil {
		t.Fatal(err)
	}
	check := func(where, slug string) {
		if _, ok := Lookup(slug); !ok {
			t.Errorf("%s uses slug %q, which is not a registry platform", where, slug)
		}
	}
	for slug := range reg.Myrient.PlatformPaths {
		check("myrient", slug)
	}
	for slug := range reg.Minerva.PlatformPaths {
		check("minerva", slug)
	}
	for slug := range reg.Vimm.PlatformSystems {
		check("vimm", slug)
	}
	for alias, slug := range reg.Vimm.PlatformAliases {
		check("vimm alias "+alias, slug)
	}
}

func TestDerivedTablesFollowRegistry(t *testing.T) {
	if info := PlatformMap[1060]; info.Slug != "wii" {
		t.Errorf("1060 Console/WiiWare = %+v, want wii", info)
	}
	if info := PlatformMap[100017]; info.Name != "Other" || info.Slug != "" {
		t.Errorf("100017 = %+v, want the legacy Other bucket", info)
	}
	for _, ep := range ExtraPlatforms {
		p, ok := Lookup(ep.Slug)
		if !ok || len(p.Categories) != 0 || p.IsPC {
			t.Errorf("extra platform %s should be a category-less console", ep.Slug)
		}
	}
	for ext, info := range extPlatformMap {
		if p, _ := Lookup(info.Slug); !p.Accepts(ext) {
			t.Errorf("extPlatformMap %s -> %s not backed by the registry", ext, info.Slug)
		}
	}
	for name, info := range metadataPlatformMap {
		if info.Name == "" {
			t.Errorf("metadata name %q maps to an unnamed platform", name)
		}
	}
	if min, max, ok := SizeRange("gbc"); !ok || min != 10e3 || max != 10e6 {
		t.Errorf("gbc size range = %v-%v", min, max)
	}
}

func TestDetectPlatformGenericCategoriesStayUnknown(t *testing.T) {
	for _, cats := range [][]interface{}{
		{float64(1000)}, {float64(1090)}, {float64(1000), float64(1090)},
	} {
		if got := DetectPlatform(cats); got.Name != "Unknown" {
			t.Errorf("DetectPlatform(%v) = %+v, want Unknown", cats, got)
		}
	}
	if got := DetectPlatform([]interface{}{float64(1090), float64(1060)}); got.Slug != "wii" {
		t.Errorf("WiiWare = %+v, want wii", got)
	}
}

func TestSearchContext(t *testing.T) {
	tests := []struct {
		name         string
		slug         string
		cats         []int
		wantKeep     bool
		wantSlug     string
		wantAssigned bool
	}{
		{"generic Console/Other kept as searched platform", "snes", []int{1090}, true, "snes", true},
		{"generic Console kept", "gbc", []int{1000}, true, "gbc", true},
		{"no categories kept", "sms", nil, true, "sms", true},
		{"unknown tracker category kept", "tg16", []int{100999}, true, "tg16", true},
		{"generic plus unknown tracker category kept", "amiga", []int{1000, 100999}, true, "amiga", true},
		{"other platform's category dropped", "snes", []int{100011}, false, "", false},
		{"other platform's Newznab category dropped", "gba", []int{1000, 1010}, false, "", false},
		{"PC category dropped from console search", "n64", []int{4000}, false, "", false},
		{"non-game Newznab range dropped", "genesis", []int{2000}, false, "", false},
		{"unmapped console subcategory (Xbox One) dropped", "snes", []int{1140}, false, "", false},
		{"own category kept and not flagged assigned", "ps2", []int{100011}, true, "ps2", false},
		{"own category wins over a stray other", "nds", []int{1010, 4000}, true, "nds", false},
		{"switch still keeps PC/Games for Nyaa, reported as PC", "switch", []int{4050}, true, "", false},
		{"switch drops plain PC", "switch", []int{4000}, false, "", false},
		{"pc search unchanged: 1000 still requested", "pc", []int{1000}, true, "", false},
		{"pc search unchanged: generic 1090 dropped", "pc", []int{1090}, false, "", false},
		{"unknown slug keeps legacy category filter", "ps5", []int{100011}, true, "ps2", false},
		{"unknown slug drops uncategorised", "ps5", nil, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, keep, assigned := SearchContext(tt.slug, tt.cats)
			if keep != tt.wantKeep {
				t.Fatalf("keep = %v, want %v (info %+v)", keep, tt.wantKeep, info)
			}
			if !keep {
				return
			}
			if info.Slug != tt.wantSlug {
				t.Errorf("slug = %q, want %q", info.Slug, tt.wantSlug)
			}
			if assigned != tt.wantAssigned {
				t.Errorf("assigned = %v, want %v", assigned, tt.wantAssigned)
			}
		})
	}
}

func writeFiles(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		full := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("rom"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeZip(t *testing.T, path string, members ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, m := range members {
		w, err := zw.Create(m)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("rom"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestDetectROMPlatformRetroExtensions(t *testing.T) {
	tests := []struct {
		files []string
		want  string
	}{
		{[]string{"Zelda DX (USA).gbc"}, "gbc"},
		{[]string{"Alex Kidd (USA).sms"}, "sms"},
		{[]string{"Sonic (World).gg"}, "gamegear"},
		{[]string{"Knuckles Chaotix (USA).32x"}, "sega32"},
		{[]string{"Bonk (USA).pce"}, "tg16"},
		{[]string{"Gunpey (Japan).ws"}, "wonderswan-color"},
		{[]string{"Metal Slug (Europe).ngc"}, "ngp"},
		{[]string{"Ninja Golf (USA).a78"}, "atari7800"},
		{[]string{"Pac-Man (USA).a52"}, "atari5200"},
		{[]string{"Chips Challenge (USA).lnx"}, "lynx"},
		{[]string{"Donkey Kong (USA).col"}, "colecovision"},
		{[]string{"Metroid (Japan).fds"}, "fds"},
		{[]string{"Mine Storm (World).vec"}, "vectrex"},
		{[]string{"BS Zelda (Japan).bs"}, "satellaview"},
		{[]string{"Nemesis (Japan).mx1"}, "msx"},
		{[]string{"disk1.d64", "disk2.d64"}, "c64"},
		{[]string{"Lemmings Disk1.adf", "Lemmings Disk2.adf"}, "amiga"},
		{[]string{"Streets of Rage (USA).gen"}, "genesis"},
		// Nested inside the downloaded folder.
		{[]string{"Pack/roms/Alex Kidd.sms", "Pack/readme.txt"}, "sms"},
		// Majority of unique-extension files wins.
		{[]string{"a.gb", "b.gbc", "c.gbc"}, "gbc"},
	}
	for _, tt := range tests {
		t.Run(tt.want+"/"+tt.files[0], func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files...)
			info, ok := DetectROMPlatform(dir)
			if !ok || info.Slug != tt.want {
				t.Errorf("DetectROMPlatform(%v) = %+v, %v; want %s", tt.files, info, ok, tt.want)
			}
		})
	}
}

func TestDetectROMPlatformLooksInsideZips(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "Chrono Trigger (USA).zip"), "Chrono Trigger (USA).sfc")
	if info, ok := DetectROMPlatform(dir); !ok || info.Slug != "snes" {
		t.Errorf("zip holding an .sfc = %+v, %v; want snes", info, ok)
	}

	single := filepath.Join(t.TempDir(), "Sonic (USA).zip")
	writeZip(t, single, "Sonic (USA).md")
	if info, ok := DetectROMPlatform(single); ok {
		t.Errorf("ambiguous .md must require a platform selection, got %+v", info)
	}
}

func TestDetectROMPlatformIgnoresHintFormats(t *testing.T) {
	for name, files := range map[string][]string{
		"readme next to other files":   {"README.md", "data.bin", "setup.dat"},
		"cd image is ambiguous":        {"Game (USA).cue", "Game (USA).bin"},
		"chd is ambiguous":             {"Game (USA).chd"},
		"visual basic source in a dir": {"Form1.vb", "Module1.vb", "project.sln"},
		"exe is never a console":       {"setup.exe"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, files...)
			if info, ok := DetectROMPlatform(dir); ok {
				t.Errorf("DetectROMPlatform(%v) = %+v, want no platform", files, info)
			}
		})
	}
}

func TestContentConflict(t *testing.T) {
	tests := []struct {
		name  string
		slug  string
		files []string
		want  string // "" = no conflict
	}{
		{"generic SNES result that is a GBA ROM", "snes", []string{"Pokemon Emerald.gba"}, "gba"},
		{"GB result that is a GBC ROM", "gb", []string{"Zelda DX.gbc"}, "gbc"},
		{"matching content", "snes", []string{"Chrono Trigger.sfc"}, ""},
		{"own format present beside a stray ROM", "switch", []string{"Game.nsp", "extras/bonus.nes"}, ""},
		{"shared format the platform uses", "wii", []string{"Game.gcz"}, ""},
		{"no ROM evidence", "segacd", []string{"Game.cue", "Game.bin"}, ""},
		{"PC is never second-guessed here", "pc", []string{"Game.nsp"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files...)
			info, ok := ContentConflict(tt.slug, dir)
			if tt.want == "" {
				if ok {
					t.Errorf("unexpected conflict: %+v", info)
				}
				return
			}
			if !ok || info.Slug != tt.want {
				t.Errorf("ContentConflict = %+v, %v; want %s", info, ok, tt.want)
			}
		})
	}
}

func TestDetectPlatformFromTitleLongestMatchWins(t *testing.T) {
	tests := map[string]string{
		"Halo 3 Xbox 360":                       "xbox360",
		"Halo Xbox":                             "xbox",
		"Mario Kart 8 Wii U":                    "wiiu",
		"Wii Sports (Wii)":                      "wii",
		"Link's Awakening DX (Game Boy Color)":  "gbc",
		"Pokemon Emerald Game Boy Advance":      "gba",
		"Tetris (Game Boy)":                     "gb",
		"Metal Slug 1st Mission Neo Geo Pocket": "ngp",
		"Samurai Shodown Neo Geo CD":            "neo-geo-cd",
		"Metal Slug Neo Geo":                    "neogeoaes",
		"Ys Book PC Engine CD":                  "turbografx-cd",
		"Bonk's Adventure PC Engine":            "tg16",
		"Sonic CD (Sega CD)":                    "segacd",
		"Metroid Famicom Disk System":           "fds",
		"Lemmings Amiga":                        "amiga",
		"Commander Keen MS-DOS":                 "dos",
		"Game & Watch Gallery":                  "g-and-w",
	}
	for title, want := range tests {
		info, ok := DetectPlatformFromTitle(title)
		if !ok || info.Slug != want {
			t.Errorf("DetectPlatformFromTitle(%q) = %+v, %v; want %s", title, info, ok, want)
		}
	}
	if info, ok := DetectPlatformFromTitle("Cyberpunk 2077"); ok {
		t.Errorf("plain PC title matched %+v", info)
	}
}

func TestDetectPlatformFromFilename(t *testing.T) {
	tests := map[string]string{
		"game.gbc":               "gbc",
		"game.sms":               "sms",
		"game.gg":                "gamegear",
		"game.pce":               "tg16",
		"game.wsc":               "wonderswan-color",
		"game.lnx":               "lynx",
		"game.adf":               "amiga",
		"game.d64":               "c64",
		"game.rpx":               "wiiu",
		"Sonic CD (Sega CD).chd": "segacd",
		"Mystery Disc.chd":       "",
		"game.txt":               "",
	}
	for name, want := range tests {
		info, ok := DetectPlatformFromFilename(name)
		if want == "" {
			if ok {
				t.Errorf("%s: got %+v, want nothing", name, info)
			}
			continue
		}
		if !ok || info.Slug != want {
			t.Errorf("%s: got %+v, %v; want %s", name, info, ok, want)
		}
	}
}

func TestAutomaticDetectionDoesNotClassifyDocuments(t *testing.T) {
	for _, filename := range []string{"README.md", "Form.vb", "root.crt"} {
		t.Run(filename, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, filename)
			if info, ok := DetectROMPlatform(dir); ok {
				t.Fatalf("document classified as ROM: %+v", info)
			}
			zipped := filepath.Join(t.TempDir(), "documents.zip")
			writeZip(t, zipped, filename)
			if info, ok := DetectROMPlatform(zipped); ok {
				t.Fatalf("zipped document classified as ROM: %+v", info)
			}
		})
	}
}
