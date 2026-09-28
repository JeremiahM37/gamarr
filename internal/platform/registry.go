package platform

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Platform is one entry in the platform registry: everything Gamarr knows about
// a platform, in one place. Category maps, extension detection, title hints,
// metadata.json names, size bands, RAWG ids, Torznab routing and the platform
// list the UI shows are all derived from Registry.
type Platform struct {
	// Slug is the RomM fs_slug, which is also the folder name under
	// GAMES_ROMS_PATH. PC keeps the slug "pc" here but is filed in the vault,
	// so PlatformInfo reports it with an empty slug.
	Slug string
	Name string
	IsPC bool

	// Categories are the Prowlarr/Newznab category IDs that positively identify
	// this platform: tracker custom IDs (100000+) and the standard Newznab
	// subcategory where one exists. A category may belong to one platform only.
	Categories []int
	// SearchCategories are requested alongside Categories in a search filtered
	// to this platform, although they identify something else (or nothing):
	// Switch still asks for PC/Games because Nyaa files Switch ROMs there.
	SearchCategories []int
	// TorznabCategory is the category Gamarr's own Torznab feed reports for
	// this platform. Zero derives it from Categories, falling back to
	// Console/Other.
	TorznabCategory int

	// Extensions are single-file ROM formats that belong to this platform and
	// no other, and that are not everyday non-ROM file types. They drive
	// automatic detection, so every one must be unique across the registry.
	Extensions []string
	// Formats are other formats the platform uses: disc images, formats shared
	// with other platforms, and extensions that collide with common non-ROM
	// files (.md is Markdown, .crt a certificate). They are hints only - used
	// for the library scan, manual import and the post-download sanity check,
	// never to classify unknown content on their own.
	Formats []string

	// MinSize and MaxSize bound a plausible release size in bytes for search
	// scoring. Zero leaves the default band.
	MinSize, MaxSize int64

	// RAWGIDs are RAWG.io platform ids; the first is used to filter searches.
	RAWGIDs []int
	// RAWGNames are lowercase RAWG platform names (substring-matched,
	// longest first) that map back to this platform.
	RAWGNames []string

	// Aliases are extra lowercase names accepted as this platform in a
	// metadata.json sidecar. The slug and the lowercased Name always are.
	Aliases []string
	// TitleHint is a case-insensitive pattern matching a release title that
	// names this platform. When several platforms match, the longest match
	// wins, so "Xbox 360" beats "Xbox" and "Game Boy Color" beats "Game Boy".
	TitleHint string
}

// Registry is the single source of truth for supported platforms. Order is the
// display order of the platform list and breaks ties between title hints.
//
// Arcade is deliberately absent: MAME sets are tied to an emulator version and
// cannot be filed by extension or category. Current-generation consoles are
// out of scope as well.
var Registry = []Platform{
	{
		Slug: "pc", Name: "PC", IsPC: true,
		Categories:       []int{4000, 100010, 4050},
		SearchCategories: []int{1000},
		Formats:          []string{".exe", ".msi"},
		MinSize:          50e6, MaxSize: 100e9,
		RAWGIDs: []int{4}, RAWGNames: []string{"pc"},
		Aliases: []string{"windows"},
	},

	// Nintendo
	{
		Slug: "nes", Name: "NES",
		Extensions: []string{".nes", ".unf", ".unif"},
		MinSize:    10e3, MaxSize: 5e6,
		RAWGIDs: []int{49}, RAWGNames: []string{"nes"},
		Aliases:   []string{"nintendo entertainment system", "famicom"},
		TitleHint: `\bnes\b|\bfamicom\b`,
	},
	{
		Slug: "fds", Name: "Famicom Disk System",
		Extensions: []string{".fds"},
		MinSize:    10e3, MaxSize: 2e6,
		Aliases:   []string{"famicom disk system"},
		TitleHint: `\bfds\b|famicom\s*disk\s*system`,
	},
	{
		Slug: "snes", Name: "SNES",
		Extensions: []string{".sfc", ".smc", ".fig", ".swc"},
		MinSize:    100e3, MaxSize: 10e6,
		RAWGIDs: []int{79}, RAWGNames: []string{"snes", "super nintendo"},
		Aliases:   []string{"super nintendo", "super famicom"},
		TitleHint: `\bsnes\b|super\s*nintendo|super\s*famicom`,
	},
	{
		Slug: "satellaview", Name: "Satellaview",
		Extensions: []string{".bs"},
		MinSize:    100e3, MaxSize: 4e6,
		TitleHint: `\bsatellaview\b`,
	},
	{
		Slug: "n64", Name: "Nintendo 64",
		Extensions: []string{".n64", ".z64", ".v64"},
		MinSize:    1e6, MaxSize: 100e6,
		RAWGIDs: []int{83}, RAWGNames: []string{"nintendo 64"},
		TitleHint: `\bn64\b|nintendo\s*64`,
	},
	{
		Slug: "ngc", Name: "GameCube",
		Categories: []int{100046},
		Extensions: []string{".gcm", ".gcz"},
		Formats:    []string{".iso", ".rvz", ".ciso"},
		MinSize:    100e6, MaxSize: 4e9,
		RAWGIDs: []int{105}, RAWGNames: []string{"gamecube"},
		Aliases:   []string{"nintendo gamecube"},
		TitleHint: `\bgamecube\b|\bngc\b|\bgcn\b`,
	},
	{
		Slug: "wii", Name: "Wii",
		Categories: []int{100044, 1030, 1060},
		Extensions: []string{".wbfs", ".wad"},
		Formats:    []string{".iso", ".rvz", ".gcz", ".wia"},
		MinSize:    100e6, MaxSize: 8e9,
		RAWGIDs: []int{11}, RAWGNames: []string{"wii"},
		Aliases:   []string{"nintendo wii"},
		TitleHint: `\bwii\b`,
	},
	{
		Slug: "wiiu", Name: "Wii U",
		Categories:      []int{1130},
		TorznabCategory: 1030,
		Extensions:      []string{".rpx", ".wux", ".wud"},
		RAWGIDs:         []int{10}, RAWGNames: []string{"wii u"},
		Aliases:   []string{"nintendo wii u"},
		TitleHint: `\bwiiu\b|wii\s*u`,
	},
	{
		Slug: "switch", Name: "Switch",
		Categories: []int{100082},
		// Nyaa has no console categories, so Switch releases there come back
		// as PC/Games; keep requesting it and let file and title hints classify.
		SearchCategories: []int{4050},
		Extensions:       []string{".nsp", ".xci", ".nsz", ".xcz"},
		MinSize:          50e6, MaxSize: 32e9,
		RAWGIDs: []int{7}, RAWGNames: []string{"nintendo switch"},
		Aliases:   []string{"nintendo switch"},
		TitleHint: `\[nsp\]|\bnsp\b|switch|\[xci\]|\bxci\b`,
	},
	{
		Slug: "g-and-w", Name: "Game & Watch",
		Extensions: []string{".mgw"},
		MinSize:    1e3, MaxSize: 10e6,
		Aliases:   []string{"game & watch", "game and watch"},
		TitleHint: `game\s*(?:&|and)\s*watch`,
	},
	{
		Slug: "gb", Name: "Game Boy",
		Extensions: []string{".gb"},
		MinSize:    10e3, MaxSize: 5e6,
		RAWGIDs: []int{26}, RAWGNames: []string{"game boy"},
		TitleHint: `\bgame\s*boy\b`,
	},
	{
		Slug: "gbc", Name: "Game Boy Color",
		Extensions: []string{".gbc"},
		MinSize:    10e3, MaxSize: 10e6,
		RAWGIDs: []int{43}, RAWGNames: []string{"game boy color"},
		Aliases:   []string{"game boy colour"},
		TitleHint: `\bgbc\b|game\s*boy\s*colou?r`,
	},
	{
		Slug: "gba", Name: "Game Boy Advance",
		Extensions: []string{".gba"},
		MinSize:    100e3, MaxSize: 50e6,
		RAWGIDs: []int{24}, RAWGNames: []string{"game boy advance"},
		TitleHint: `\bgba\b|game\s*boy\s*advance`,
	},
	{
		Slug: "virtualboy", Name: "Virtual Boy",
		Extensions: []string{".vboy"},
		// .vb is the usual dump extension, but it is also Visual Basic source.
		Formats: []string{".vb"},
		MinSize: 100e3, MaxSize: 4e6,
		TitleHint: `\bvirtual\s*boy\b`,
	},
	{
		Slug: "nds", Name: "DS",
		Categories: []int{100045, 1010},
		Extensions: []string{".nds", ".dsi"},
		MinSize:    1e6, MaxSize: 512e6,
		RAWGIDs: []int{9}, RAWGNames: []string{"nintendo ds"},
		Aliases:   []string{"nintendo ds"},
		TitleHint: `\bnds\b|\bnintendo\s*ds\b`,
	},
	{
		Slug: "3ds", Name: "3DS",
		Categories:      []int{100072, 1110},
		TorznabCategory: 1010,
		Extensions:      []string{".3ds", ".cia", ".cci"},
		MinSize:         10e6, MaxSize: 4e9,
		RAWGIDs: []int{8}, RAWGNames: []string{"nintendo 3ds"},
		Aliases:   []string{"nintendo 3ds"},
		TitleHint: `\b3ds\b|nintendo\s*3ds`,
	},

	// Sony
	{
		Slug: "psx", Name: "PS1",
		Categories: []int{100015},
		Formats:    []string{".bin", ".cue", ".chd", ".iso", ".pbp"},
		MinSize:    50e6, MaxSize: 1e9,
		RAWGIDs: []int{27}, RAWGNames: []string{"playstation"},
		Aliases:   []string{"ps1", "playstation", "playstation 1"},
		TitleHint: `\bps1\b|\bpsx\b`,
	},
	{
		Slug: "ps2", Name: "PS2",
		Categories: []int{100011},
		Formats:    []string{".iso", ".chd", ".bin", ".cue"},
		MinSize:    100e6, MaxSize: 8e9,
		RAWGIDs: []int{15}, RAWGNames: []string{"playstation 2"},
		Aliases:   []string{"playstation 2"},
		TitleHint: `\bps2\b|playstation\s*2`,
	},
	{
		Slug: "ps3", Name: "PS3",
		Categories: []int{100043, 1080},
		Formats:    []string{".iso", ".pkg"},
		MinSize:    500e6, MaxSize: 50e9,
		RAWGIDs: []int{16}, RAWGNames: []string{"playstation 3"},
		Aliases:   []string{"playstation 3"},
		TitleHint: `\bps3\b|playstation\s*3`,
	},
	{
		Slug: "ps4", Name: "PS4",
		Categories: []int{100077, 1180},
		RAWGIDs:    []int{18}, RAWGNames: []string{"playstation 4"},
		Aliases:   []string{"playstation 4"},
		TitleHint: `\bps4\b|playstation\s*4`,
	},
	{
		Slug: "psp", Name: "PSP",
		Categories: []int{100012, 1020},
		Extensions: []string{".pbp", ".cso"},
		Formats:    []string{".iso", ".chd"},
		MinSize:    50e6, MaxSize: 4e9,
		RAWGIDs: []int{17}, RAWGNames: []string{"psp"},
		Aliases:   []string{"playstation portable"},
		TitleHint: `\bpsp\b|playstation\s*portable`,
	},
	{
		Slug: "psvita", Name: "PS Vita",
		Categories:      []int{1120},
		TorznabCategory: 1020,
		RAWGIDs:         []int{19}, RAWGNames: []string{"ps vita"},
		Aliases:   []string{"ps vita", "vita", "playstation vita"},
		TitleHint: `\bps\s*vita\b|playstation\s*vita`,
	},

	// Microsoft
	{
		Slug: "xbox", Name: "Xbox",
		Categories: []int{100013, 1040},
		Formats:    []string{".iso", ".xiso"},
		MinSize:    500e6, MaxSize: 8e9,
		RAWGIDs: []int{80}, RAWGNames: []string{"xbox"},
		TitleHint: `\bxbox\b`,
	},
	{
		Slug: "xbox360", Name: "Xbox 360",
		Categories: []int{100014, 1050},
		Formats:    []string{".iso"},
		MinSize:    500e6, MaxSize: 16e9,
		RAWGIDs: []int{14}, RAWGNames: []string{"xbox 360"},
		TitleHint: `\bxbox\s*360`,
	},

	// Sega
	{
		Slug: "sms", Name: "Master System",
		Extensions: []string{".sms"},
		MinSize:    8e3, MaxSize: 4e6,
		RAWGIDs: []int{74}, RAWGNames: []string{"sega master system"},
		Aliases:   []string{"sega master system", "mark iii"},
		TitleHint: `master\s*system`,
	},
	{
		Slug: "genesis", Name: "Sega Genesis",
		Extensions: []string{".gen", ".smd"},
		// .md is the No-Intro extension, but it is also Markdown.
		Formats: []string{".md", ".bin"},
		MinSize: 10e3, MaxSize: 10e6,
		RAWGIDs: []int{167}, RAWGNames: []string{"sega genesis", "sega mega drive"},
		Aliases:   []string{"mega drive", "sega mega drive", "megadrive"},
		TitleHint: `\bgenesis\b|mega\s*drive`,
	},
	{
		Slug: "segacd", Name: "Sega CD",
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 20e6, MaxSize: 1e9,
		RAWGIDs: []int{119}, RAWGNames: []string{"sega cd"},
		Aliases:   []string{"mega cd", "sega mega cd"},
		TitleHint: `\bsega\s*cd\b|\bmega\s*cd\b`,
	},
	{
		Slug: "sega32", Name: "Sega 32X",
		Extensions: []string{".32x"},
		MinSize:    100e3, MaxSize: 10e6,
		RAWGIDs: []int{117}, RAWGNames: []string{"sega 32x"},
		Aliases:   []string{"32x", "sega 32x"},
		TitleHint: `\b32x\b`,
	},
	{
		Slug: "saturn", Name: "Sega Saturn",
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 50e6, MaxSize: 1e9,
		RAWGIDs: []int{107}, RAWGNames: []string{"sega saturn"},
		TitleHint: `\bsega\s*saturn\b`,
	},
	{
		Slug: "dc", Name: "Dreamcast",
		Categories: []int{100016},
		Extensions: []string{".gdi", ".cdi"},
		Formats:    []string{".chd"},
		MinSize:    50e6, MaxSize: 2e9,
		RAWGIDs: []int{106}, RAWGNames: []string{"dreamcast"},
		Aliases:   []string{"sega dreamcast"},
		TitleHint: `\bdreamcast\b`,
	},
	{
		Slug: "gamegear", Name: "Game Gear",
		Extensions: []string{".gg"},
		MinSize:    8e3, MaxSize: 4e6,
		RAWGIDs: []int{77}, RAWGNames: []string{"game gear"},
		Aliases:   []string{"sega game gear"},
		TitleHint: `\bgame\s*gear\b`,
	},
	{
		Slug: "sega-pico", Name: "Sega Pico",
		// Pico dumps use .md too, but that is left to Genesis so a lone .md
		// still has one owner; Pico is reached through search context.
		Formats: []string{".bin"},
		MinSize: 100e3, MaxSize: 8e6,
		TitleHint: `\bsega\s*pico\b`,
	},

	// NEC
	{
		Slug: "tg16", Name: "PC Engine / TurboGrafx-16",
		Extensions: []string{".pce", ".sgx"},
		MinSize:    8e3, MaxSize: 4e6,
		Aliases:   []string{"pc engine", "turbografx-16", "turbografx 16", "turbografx16", "pce"},
		TitleHint: `\bpc\s*engine\b|\bturbografx(?:[\s-]*16)?\b|\btg-?16\b|\bsupergrafx\b`,
	},
	{
		Slug: "turbografx-cd", Name: "PC Engine CD / TurboGrafx-CD",
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 20e6, MaxSize: 1e9,
		Aliases:   []string{"pc engine cd", "turbografx cd", "pce-cd", "pce cd"},
		TitleHint: `\bpc\s*engine\s*cd\b|\bturbografx[\s-]*cd\b|\bturbo\s*cd\b|\bpce[\s-]?cd\b`,
	},
	{
		Slug: "pc98", Name: "PC-98",
		Extensions: []string{".hdi", ".fdi", ".hdm"},
		Formats:    []string{".d88"},
		MinSize:    100e3, MaxSize: 1e9,
		Aliases:   []string{"pc-98", "pc-9801", "nec pc-98"},
		TitleHint: `\bpc-?98(?:01)?\b`,
	},

	// SNK
	{
		Slug: "neogeoaes", Name: "Neo Geo AES/MVS",
		Extensions: []string{".neo"},
		MinSize:    100e3, MaxSize: 150e6,
		RAWGIDs: []int{12}, RAWGNames: []string{"neo geo"},
		Aliases:   []string{"neo geo", "neogeo", "neo geo aes", "neo geo mvs"},
		TitleHint: `\bneo[\s-]*geo\b`,
	},
	{
		Slug: "neo-geo-cd", Name: "Neo Geo CD",
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 20e6, MaxSize: 1e9,
		Aliases:   []string{"neogeo cd", "neo-geo cd"},
		TitleHint: `\bneo[\s-]*geo[\s-]*cd\b`,
	},
	{
		Slug: "ngp", Name: "Neo Geo Pocket / Color",
		// .ngc is the Pocket Color dump extension; GameCube images never use it.
		Extensions: []string{".ngp", ".ngc"},
		MinSize:    100e3, MaxSize: 8e6,
		Aliases:   []string{"neo geo pocket", "neo geo pocket color", "ngpc"},
		TitleHint: `\bneo[\s-]*geo[\s-]*pocket\b|\bngpc?\b`,
	},

	// Atari
	{
		Slug: "atari2600", Name: "Atari 2600",
		Extensions: []string{".a26"},
		Formats:    []string{".bin"},
		MinSize:    1e3, MaxSize: 1e6,
		RAWGIDs: []int{23}, RAWGNames: []string{"atari 2600"},
		TitleHint: `\batari[\s-]*2600\b`,
	},
	{
		Slug: "atari5200", Name: "Atari 5200",
		Extensions: []string{".a52"},
		Formats:    []string{".bin"},
		MinSize:    1e3, MaxSize: 1e6,
		RAWGIDs: []int{31}, RAWGNames: []string{"atari 5200"},
		TitleHint: `\batari[\s-]*5200\b`,
	},
	{
		Slug: "atari7800", Name: "Atari 7800",
		Extensions: []string{".a78"},
		Formats:    []string{".bin"},
		MinSize:    1e3, MaxSize: 1e6,
		RAWGIDs: []int{28}, RAWGNames: []string{"atari 7800"},
		TitleHint: `\batari[\s-]*7800\b`,
	},
	{
		Slug: "lynx", Name: "Atari Lynx",
		Extensions: []string{".lnx"},
		MinSize:    10e3, MaxSize: 2e6,
		RAWGIDs: []int{46}, RAWGNames: []string{"atari lynx"},
		TitleHint: `\batari[\s-]*lynx\b`,
	},

	// Everything else
	{
		Slug: "colecovision", Name: "ColecoVision",
		Extensions: []string{".col"},
		Formats:    []string{".rom", ".bin"},
		MinSize:    1e3, MaxSize: 1e6,
		TitleHint: `\bcolecovision\b`,
	},
	{
		Slug: "vectrex", Name: "Vectrex",
		Extensions: []string{".vec"},
		Formats:    []string{".bin"},
		MinSize:    1e3, MaxSize: 1e6,
		TitleHint: `\bvectrex\b`,
	},
	{
		Slug: "3do", Name: "3DO",
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 50e6, MaxSize: 1e9,
		RAWGIDs: []int{111}, RAWGNames: []string{"3do"},
		Aliases:   []string{"panasonic 3do"},
		TitleHint: `\b3do\b`,
	},
	{
		Slug: "cdi", Name: "Philips CD-i",
		// Not .cdi: that is the DiscJuggler image Dreamcast releases use.
		Formats: []string{".chd", ".cue", ".bin", ".iso"},
		MinSize: 50e6, MaxSize: 1e9,
		Aliases:   []string{"cd-i", "philips cd-i"},
		TitleHint: `\bcd-i\b|\bphilips\s*cd-?i\b`,
	},
	{
		Slug: "wonderswan-color", Name: "WonderSwan / Color",
		Extensions: []string{".ws", ".wsc"},
		MinSize:    100e3, MaxSize: 16e6,
		Aliases:   []string{"wonderswan", "wonderswan color"},
		TitleHint: `\bwonder\s*swan\b`,
	},
	{
		Slug: "msx", Name: "MSX",
		Extensions: []string{".mx1", ".mx2"},
		Formats:    []string{".rom", ".dsk", ".cas"},
		MinSize:    1e3, MaxSize: 10e6,
		Aliases:   []string{"msx2"},
		TitleHint: `\bmsx2?\b`,
	},
	{
		Slug: "c64", Name: "Commodore 64",
		Extensions: []string{".d64", ".t64", ".g64"},
		// .crt is the cartridge format and also an X.509 certificate.
		Formats: []string{".crt", ".tap", ".prg"},
		MinSize: 1e3, MaxSize: 10e6,
		RAWGIDs: []int{166},
		Aliases: []string{"commodore 64"},
		// RAWG files the C64 under "Commodore / Amiga", which maps back to amiga.
		TitleHint: `\bc64\b|\bcommodore\s*64\b`,
	},
	{
		Slug: "amiga", Name: "Amiga",
		Extensions: []string{".adf", ".adz", ".dms"},
		Formats:    []string{".ipf", ".lha", ".hdf"},
		MinSize:    100e3, MaxSize: 50e6,
		RAWGIDs: []int{166}, RAWGNames: []string{"commodore / amiga"},
		Aliases:   []string{"commodore amiga"},
		TitleHint: `\bamiga\b`,
	},
	{
		Slug: "dos", Name: "DOS",
		MinSize: 100e3, MaxSize: 1e9,
		Aliases:   []string{"ms-dos", "msdos"},
		TitleHint: `\bms-?dos\b`,
	},
}

// otherCategory is a tracker custom "Other" category that predates the
// registry. It identifies no platform, but DetectPlatform has always reported
// it by name, so it stays in PlatformMap.
const otherCategory = 100017

// HacksSuffix is appended to a platform folder when ROM hacks are routed
// separately (HACKS_SUFFIX_ROUTING). No platform slug may end in it.
const HacksSuffix = "-hacks"

// genericCategories identify no particular platform: Console, Console/Other
// and the tracker "Other" bucket. A platform-filtered search keeps results
// tagged only with these and files them under the searched platform.
var genericCategories = map[int]bool{1000: true, 1090: true, otherCategory: true}

// torznabSubcategories are the Newznab subcategories Gamarr's Torznab caps
// advertise, so the only ones CategoryForPlatform may report.
var torznabSubcategories = map[int]bool{1010: true, 1020: true, 1030: true, 1040: true, 1050: true, 1080: true, 1090: true, 4050: true}

// Info returns the PlatformInfo this platform is reported as.
func (p Platform) Info() PlatformInfo {
	slug := p.Slug
	if p.IsPC {
		// PC games are filed in the vault, not under a ROM folder.
		slug = ""
	}
	return PlatformInfo{Name: p.Name, Slug: slug, IsPC: p.IsPC}
}

// Accepts reports whether ext is a format this platform uses.
func (p Platform) Accepts(ext string) bool {
	ext = strings.ToLower(ext)
	for _, e := range p.Extensions {
		if e == ext {
			return true
		}
	}
	for _, e := range p.Formats {
		if e == ext {
			return true
		}
	}
	return false
}

// TorznabCategoryID is the Newznab category Gamarr's Torznab feed reports.
func (p Platform) TorznabCategoryID() int {
	if p.TorznabCategory != 0 {
		return p.TorznabCategory
	}
	for _, c := range p.Categories {
		if torznabSubcategories[c] {
			return c
		}
	}
	return 1090
}

type titleHint struct {
	re   *regexp.Regexp
	plat *Platform
}

var (
	bySlug       map[string]*Platform
	byExtension  map[string]*Platform // Extensions only: unique, auto-detect safe
	byCategory   map[int]*Platform
	byAlias      map[string]*Platform
	titleHints   []titleHint
	ambiguousExt map[string]bool // Formats shared by more than one platform
)

func init() {
	buildIndexes()
}

// buildIndexes derives every lookup from Registry. First entry wins on a
// conflict; TestRegistryIntegrity reports conflicts rather than letting a
// later entry silently lose.
func buildIndexes() {
	bySlug = make(map[string]*Platform, len(Registry))
	byExtension = make(map[string]*Platform)
	byCategory = make(map[int]*Platform)
	byAlias = make(map[string]*Platform)
	titleHints = nil
	formatOwners := make(map[string]int)

	PlatformMap = make(map[int]PlatformInfo)
	ExtraPlatforms = nil
	extPlatformMap = make(map[string]PlatformInfo)
	metadataPlatformMap = make(map[string]PlatformInfo)

	for i := range Registry {
		p := &Registry[i]
		if _, dup := bySlug[p.Slug]; !dup {
			bySlug[p.Slug] = p
		}
		for _, c := range p.Categories {
			if _, dup := byCategory[c]; !dup {
				byCategory[c] = p
				PlatformMap[c] = p.Info()
			}
		}
		if len(p.Categories) == 0 && !p.IsPC {
			ExtraPlatforms = append(ExtraPlatforms, ExtraPlatform{Slug: p.Slug, Name: p.Name})
		}
		for _, e := range p.Extensions {
			if _, dup := byExtension[e]; !dup {
				byExtension[e] = p
				extPlatformMap[e] = p.Info()
			}
		}
		seenFormat := map[string]bool{}
		for _, e := range append(append([]string{}, p.Extensions...), p.Formats...) {
			if !seenFormat[e] {
				seenFormat[e] = true
				formatOwners[e]++
			}
		}
		names := append([]string{p.Slug, strings.ToLower(p.Name)}, p.Aliases...)
		for _, n := range names {
			if _, dup := byAlias[n]; !dup {
				byAlias[n] = p
				metadataPlatformMap[n] = p.Info()
			}
		}
		if p.TitleHint != "" {
			re := regexp.MustCompile(`(?i)` + p.TitleHint)
			re.Longest()
			titleHints = append(titleHints, titleHint{re: re, plat: p})
		}
	}
	PlatformMap[otherCategory] = PlatformInfo{Name: "Other", Slug: ""}

	ambiguousExt = make(map[string]bool)
	for e, n := range formatOwners {
		if n > 1 {
			ambiguousExt[e] = true
		}
	}

	consoleROMExts = make(map[string]PlatformInfo, len(pcOverrideExts))
	for _, e := range pcOverrideExts {
		if p, ok := byExtension[e]; ok {
			consoleROMExts[e] = p.Info()
		}
	}
}

// Lookup returns the registry entry for a slug. "pc" is PC.
func Lookup(slug string) (Platform, bool) {
	p, ok := bySlug[strings.ToLower(strings.TrimSpace(slug))]
	if !ok {
		return Platform{}, false
	}
	return *p, true
}

// NameForSlug returns the display name for a slug, or "" when the slug is not
// a registry platform.
func NameForSlug(slug string) string {
	if p, ok := Lookup(slug); ok {
		return p.Name
	}
	return ""
}

// Slugs returns every registry slug in registry order.
func Slugs() []string {
	out := make([]string, 0, len(Registry))
	for _, p := range Registry {
		out = append(out, p.Slug)
	}
	return out
}

// CategoryOwner returns the platform a category positively identifies.
func CategoryOwner(id int) (Platform, bool) {
	p, ok := byCategory[id]
	if !ok {
		return Platform{}, false
	}
	return *p, true
}

// IsGenericCategory reports whether a category identifies no platform:
// Console, Console/Other or the tracker "Other" bucket.
func IsGenericCategory(id int) bool {
	return genericCategories[id]
}

// PlatformForExtension returns the platform a unique ROM extension belongs to.
// Shared and hint-only formats (.iso, .bin, .chd, .md, ...) return false.
func PlatformForExtension(ext string) (PlatformInfo, bool) {
	p, ok := byExtension[strings.ToLower(ext)]
	if !ok {
		return PlatformInfo{}, false
	}
	return p.Info(), true
}

// PlatformsAccepting returns every platform, in registry order, that lists
// ext among its Extensions or Formats.
func PlatformsAccepting(ext string) []Platform {
	ext = strings.ToLower(ext)
	var out []Platform
	for _, p := range Registry {
		if p.Accepts(ext) {
			out = append(out, p)
		}
	}
	return out
}

// nonROMCollisions are registry formats that are also everyday non-ROM files:
// Markdown, Visual Basic source, certificates, firmware images. They are
// game files only inside their own platform's folder.
var nonROMCollisions = map[string]bool{".md": true, ".vb": true, ".crt": true, ".rom": true}

// KnownExtensions returns every extension and format in the registry that is
// a game file wherever it turns up, sorted. Formats that collide with common
// non-ROM files are left out; Platform.Accepts still reports them for their
// own platform.
func KnownExtensions() []string {
	seen := map[string]bool{}
	for _, p := range Registry {
		for _, e := range append(append([]string{}, p.Extensions...), p.Formats...) {
			if !nonROMCollisions[e] {
				seen[e] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// SizeRange returns the plausible release size band for a slug.
func SizeRange(slug string) (min, max int64, ok bool) {
	p, found := Lookup(slug)
	if !found || p.MinSize == 0 || p.MaxSize == 0 {
		return 0, 0, false
	}
	return p.MinSize, p.MaxSize, true
}

// RAWGPlatformID returns the RAWG id used to filter searches for a slug.
func RAWGPlatformID(slug string) (int, bool) {
	p, ok := Lookup(slug)
	if !ok || len(p.RAWGIDs) == 0 {
		return 0, false
	}
	return p.RAWGIDs[0], true
}

// RAWGNameSlugs maps each lowercase RAWG platform name in the registry to its
// slug.
func RAWGNameSlugs() map[string]string {
	out := make(map[string]string)
	for _, p := range Registry {
		for _, n := range p.RAWGNames {
			if _, dup := out[n]; !dup {
				out[n] = p.Slug
			}
		}
	}
	return out
}

// DetectPlatformFromTitle matches a release title against the registry's title
// hints. The longest match wins, so a title naming "Xbox 360" is not read as
// "Xbox"; equal lengths fall back to registry order.
func DetectPlatformFromTitle(title string) (PlatformInfo, bool) {
	return titleHintAmong(title, nil)
}

// titleHintAmong is DetectPlatformFromTitle restricted to the given
// candidates when the set is non-nil.
func titleHintAmong(title string, candidates map[string]bool) (PlatformInfo, bool) {
	var best *Platform
	bestLen := 0
	for _, h := range titleHints {
		if candidates != nil && !candidates[h.plat.Slug] {
			continue
		}
		for _, loc := range h.re.FindAllStringIndex(title, -1) {
			if n := loc[1] - loc[0]; n > bestLen {
				best, bestLen = h.plat, n
			}
		}
	}
	if best == nil {
		return PlatformInfo{}, false
	}
	return best.Info(), true
}

// DetectPlatformFromFilename classifies one file by extension for a manual
// import: a unique ROM extension decides outright, a format only one platform
// uses decides next, and a shared format (.iso, .chd, .bin) is settled by the
// file name among the platforms that use it.
func DetectPlatformFromFilename(filename string) (PlatformInfo, bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return PlatformInfo{}, false
	}
	if info, ok := PlatformForExtension(ext); ok {
		return info, true
	}
	cands := PlatformsAccepting(ext)
	switch len(cands) {
	case 0:
		return PlatformInfo{}, false
	case 1:
		return cands[0].Info(), true
	}
	set := make(map[string]bool, len(cands))
	for _, c := range cands {
		set[c.Slug] = true
	}
	return titleHintAmong(filename, set)
}

// SearchContext decides whether a Prowlarr result belongs in a search filtered
// to slug, and which platform to file it under.
//
// For a registry console platform:
//   - a category the platform owns keeps the result under that platform;
//   - a category the platform's search also requests (Switch asks for
//     PC/Games) keeps it with whatever the categories say, as before;
//   - a category that positively identifies anything else - another
//     platform, PC, or a non-game Newznab range - drops it;
//   - otherwise the result carries only generic console categories, unknown
//     tracker categories or none at all, and it is kept and assigned the
//     searched platform (assigned is true).
//
// PC and slugs outside the registry keep the category-list filter they have
// always had.
func SearchContext(slug string, catIDs []int) (info PlatformInfo, keep, assigned bool) {
	p, ok := Lookup(slug)
	if !ok || p.IsPC {
		wanted := map[int]bool{}
		for _, c := range GetCategoriesForPlatform(slug) {
			wanted[c] = true
		}
		for _, c := range catIDs {
			if wanted[c] {
				return detectFromIDs(catIDs), true, false
			}
		}
		return PlatformInfo{}, false, false
	}
	for _, c := range catIDs {
		if owner, ok := byCategory[c]; ok && owner.Slug == p.Slug {
			return p.Info(), true, false
		}
	}
	for _, c := range catIDs {
		for _, extra := range p.SearchCategories {
			if c == extra {
				return detectFromIDs(catIDs), true, false
			}
		}
	}
	for _, c := range catIDs {
		if identifiesSomethingElse(c) {
			return PlatformInfo{}, false, false
		}
	}
	return p.Info(), true, true
}

// identifiesSomethingElse reports whether a category is positive evidence of
// content other than a particular console: another registry platform, PC, a
// Newznab console subcategory Gamarr does not map (Xbox One, 360 DLC), or a
// non-game Newznab range (Movies, Audio, TV, XXX, Books). Unmapped tracker
// custom categories (100000+) carry no meaning here and are not evidence.
func identifiesSomethingElse(id int) bool {
	if genericCategories[id] {
		return false
	}
	if _, ok := byCategory[id]; ok {
		return true
	}
	return id >= 1000 && id < 8000
}

func detectFromIDs(ids []int) PlatformInfo {
	for _, id := range ids {
		if info, ok := PlatformMap[id]; ok {
			return info
		}
	}
	return PlatformInfo{Name: "Unknown"}
}
