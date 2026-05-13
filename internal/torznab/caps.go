package torznab

// BuildCaps returns the Torznab capabilities document, advertising game-
// specific Newznab categories so Prowlarr can route searches correctly.
//
// Newznab category numbering reference:
//
//	1000 Console
//	  1010 Console/NDS    1030 Console/Wii   1080 Console/PS3
//	  1020 Console/PSP    1040 Console/Xbox  1090 Console/Other
//	                      1050 Console/Xbox 360
//	4000 PC
//	  4020 PC/0day        4050 PC/Mac        4070 PC/Games
//	  4030 PC/ISO         4060 PC/Mobile-Other
func BuildCaps() *Caps {
	return &Caps{
		Server: CapsServer{Title: "Gamarr"},
		Limits: CapsLimits{Max: 100, Default: 50},
		Searching: CapsSearching{
			Search:        CapsSearchOp{Available: "yes", SupportedParams: "q"},
			ConsoleSearch: CapsSearchOp{Available: "yes", SupportedParams: "q,platform"},
			PCSearch:      CapsSearchOp{Available: "yes", SupportedParams: "q"},
		},
		Categories: CapsCategories{
			Categories: []CapsCategory{
				{
					ID: "1000", Name: "Console",
					Subs: []CapsSubCategory{
						{ID: "1010", Name: "Console/NDS"},
						{ID: "1020", Name: "Console/PSP"},
						{ID: "1030", Name: "Console/Wii"},
						{ID: "1040", Name: "Console/Xbox"},
						{ID: "1050", Name: "Console/Xbox 360"},
						{ID: "1080", Name: "Console/PS3"},
						{ID: "1090", Name: "Console/Other"},
					},
				},
				{
					ID: "4000", Name: "PC",
					Subs: []CapsSubCategory{
						{ID: "4070", Name: "PC/Games"},
					},
				},
			},
		},
	}
}

// CategoryForPlatform maps Gamarr's platform slugs to a Torznab category ID.
// Used so Prowlarr can route results back to the right *arr instance.
func CategoryForPlatform(slug string) string {
	switch slug {
	case "pc":
		return "4070"
	case "nds", "3ds":
		return "1010"
	case "psp", "psvita":
		return "1020"
	case "wii", "wiiu":
		return "1030"
	case "xbox":
		return "1040"
	case "xbox360":
		return "1050"
	case "ps3":
		return "1080"
	case "nes", "snes", "n64", "ngc", "gb", "gbc", "gba", "genesis", "saturn", "dc", "psx", "ps2", "ps4", "ps5", "switch", "atari2600":
		return "1090"
	}
	return "1090" // Console/Other as a safe default
}

func categoryName(id string) string {
	switch id {
	case "1000":
		return "Console"
	case "1010":
		return "Console/NDS"
	case "1020":
		return "Console/PSP"
	case "1030":
		return "Console/Wii"
	case "1040":
		return "Console/Xbox"
	case "1050":
		return "Console/Xbox 360"
	case "1080":
		return "Console/PS3"
	case "1090":
		return "Console/Other"
	case "4000":
		return "PC"
	case "4070":
		return "PC/Games"
	}
	return "Console"
}
