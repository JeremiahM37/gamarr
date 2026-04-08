package organize

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gamarr/internal/config"
)

// Renamer handles file renaming on import using configurable patterns.
type Renamer struct {
	cfg *config.Config
}

// NewRenamer creates a new Renamer.
func NewRenamer(cfg *config.Config) *Renamer {
	return &Renamer{cfg: cfg}
}

// RenameOnImport renames a file according to the configured pattern.
// Returns the new path. If renaming is disabled, returns the original path.
func (rn *Renamer) RenameOnImport(filePath, title, platform, platformSlug string, isPC bool) (string, error) {
	if !rn.cfg.RenameEnabled {
		return filePath, nil
	}

	pattern := rn.cfg.RenamePattern
	if pattern == "" {
		pattern = "{title} ({platform}).{ext}"
	}

	ext := filepath.Ext(filePath)
	if ext != "" {
		ext = ext[1:] // Remove leading dot
	}

	displayPlatform := platform
	if displayPlatform == "" && isPC {
		displayPlatform = "PC"
	}
	if displayPlatform == "" {
		displayPlatform = platformSlug
	}

	// Clean the title for use as a filename
	cleanTitle := cleanForFilename(title)
	if cleanTitle == "" {
		cleanTitle = cleanForFilename(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
	}
	if cleanTitle == "" {
		cleanTitle = "Unknown"
	}

	// Apply the pattern
	newName := pattern
	newName = strings.ReplaceAll(newName, "{title}", cleanTitle)
	newName = strings.ReplaceAll(newName, "{platform}", displayPlatform)
	newName = strings.ReplaceAll(newName, "{ext}", ext)
	newName = strings.ReplaceAll(newName, "{slug}", platformSlug)

	// Sanitize the final filename
	newName = sanitizeFilename(newName)

	dir := filepath.Dir(filePath)
	newPath := filepath.Join(dir, newName)

	// Handle collision: add (1), (2), etc.
	if newPath != filePath {
		newPath = resolveCollision(newPath)
	}

	if newPath == filePath {
		return filePath, nil
	}

	// Perform the rename
	if err := os.Rename(filePath, newPath); err != nil {
		return filePath, fmt.Errorf("rename failed: %w", err)
	}

	return newPath, nil
}

// cleanForFilename removes scene tags and cleans a title for use as a filename.
func cleanForFilename(name string) string {
	// Remove scene tags, URL encoding artifacts
	name = sceneGroupRe.ReplaceAllString(name, "")
	name = sceneTagsRe.ReplaceAllString(name, "")
	name = bracketRe.ReplaceAllString(name, "")
	name = dashGroupRe.ReplaceAllString(name, "")

	// Replace URL encoding
	name = strings.ReplaceAll(name, "%20", " ")
	name = strings.ReplaceAll(name, "%28", "(")
	name = strings.ReplaceAll(name, "%29", ")")

	// Clean up
	name = multiSpaceRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	name = strings.Trim(name, ".-_ ")

	return name
}

// sanitizeFilename removes characters that are unsafe in filenames.
func sanitizeFilename(name string) string {
	name = unsafeCharsRe.ReplaceAllString(name, "")
	name = multiSpaceRe.ReplaceAllString(name, " ")
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown"
	}
	return name
}

// resolveCollision appends a number suffix if the file already exists.
func resolveCollision(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)

	for i := 1; i <= 99; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}

	// Give up after 99 collisions
	return path
}
