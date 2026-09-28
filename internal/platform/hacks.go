package platform

import "regexp"

var (
	// GoodTools/RHDN hack tags: [h], [h1], [h2C], [hM04], [h Bob]. The h is
	// lowercase in every convention, which keeps [HD] and [hdd] out.
	hackTagRe = regexp.MustCompile(`\[h(?:[0-9A-Z+][^\]]*|\s[^\]]*)?\]`)
	// Fan translation tags: [T+Eng], [T-Fre1.0_Author].
	translationTagRe = regexp.MustCompile(`(?i)\[t[+-]`)
	hackWordRe       = regexp.MustCompile(`(?i)\bhack\b|\brom\s*hack\b|\(hack\)`)
	// The .hack// franchise is a commercial series, not a hack.
	dotHackRe = regexp.MustCompile(`(?i)\.hack\s*//`)
)

// IsROMHack reports whether any of the given release or file names looks like
// a ROM hack or fan translation.
func IsROMHack(names ...string) bool {
	for _, n := range names {
		if n == "" {
			continue
		}
		if hackTagRe.MatchString(n) || translationTagRe.MatchString(n) {
			return true
		}
		if hackWordRe.MatchString(dotHackRe.ReplaceAllString(n, "")) {
			return true
		}
	}
	return false
}

// LibraryFolder returns the folder under GAMES_ROMS_PATH a ROM is filed in:
// the platform slug, or slug + "-hacks" when hacks routing is on and a name
// looks like a hack.
func LibraryFolder(slug string, hacksRouting bool, names ...string) string {
	if hacksRouting && slug != "" && IsROMHack(names...) {
		return slug + HacksSuffix
	}
	return slug
}
