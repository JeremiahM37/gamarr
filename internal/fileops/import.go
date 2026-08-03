// Package fileops implements the strategies Gamarr uses to place a finished
// download into the library.
//
// The default strategy is a move, which is what a download that Gamarr itself
// fetched wants: the staging copy has no further purpose. Content that arrived
// over BitTorrent is different — moving it out of the download client's
// completed directory stops the torrent seeding, which on a private tracker
// costs the user ratio and can trigger hit-and-run penalties. For that case
// the source-preserving modes (hardlink, symlink, copy) leave the seeded data
// exactly where the client expects it.
//
// Hardlink is the mode to prefer, and the convention the *arr apps and
// cross-seed use: the library entry stays valid even after the torrent is
// removed from the client, and it costs no extra disk. Its one requirement is
// that source and destination live on the same filesystem.
package fileops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
)

// Mode selects how Import places content at its destination.
type Mode string

const (
	// ModeMove renames (or copies then deletes) the source. The source does
	// not survive the import.
	ModeMove Mode = "move"
	// ModeHardlink creates hardlinks, so source and destination are the same
	// data on disk. Requires a single filesystem.
	ModeHardlink Mode = "hardlink"
	// ModeSymlink creates one symlink pointing at the source. The library
	// entry breaks if the source is later deleted.
	ModeSymlink Mode = "symlink"
	// ModeCopy duplicates the content, leaving the source intact at the cost
	// of double the disk space.
	ModeCopy Mode = "copy"
)

// Modes lists every valid import mode, in the order the UI presents them.
var Modes = []Mode{ModeMove, ModeHardlink, ModeSymlink, ModeCopy}

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool {
	for _, known := range Modes {
		if m == known {
			return true
		}
	}
	return false
}

// PreservesSource reports whether an import in this mode leaves the source
// files in place. Callers use it to decide whether a seeding torrent can be
// left running after the import.
func (m Mode) PreservesSource() bool {
	return m == ModeHardlink || m == ModeSymlink || m == ModeCopy
}

// ParseMode resolves a configured value to a Mode. An empty value selects the
// default (move), preserving the behavior of installs that never set it.
func ParseMode(s string) (Mode, error) {
	if s == "" {
		return ModeMove, nil
	}
	m := Mode(s)
	if !m.Valid() {
		return "", fmt.Errorf("unknown import mode %q (want one of move, hardlink, symlink, copy)", s)
	}
	return m, nil
}

// Fallback names what Import does when a hardlink import hits a filesystem
// boundary.
type Fallback string

const (
	// FallbackError reports the boundary instead of silently importing in a
	// mode the user did not ask for. This is the default: a hardlink import
	// that quietly becomes a copy doubles disk usage without saying so.
	FallbackError Fallback = "error"
	// FallbackCopy copies instead, leaving the source seedable.
	FallbackCopy Fallback = "copy"
	// FallbackSymlink symlinks instead, leaving the source seedable.
	FallbackSymlink Fallback = "symlink"
	// FallbackMove falls back to the pre-import-mode behavior. The source does
	// not survive, so seeding stops.
	FallbackMove Fallback = "move"
)

// Fallbacks lists every valid hardlink fallback.
var Fallbacks = []Fallback{FallbackError, FallbackCopy, FallbackSymlink, FallbackMove}

// Valid reports whether f is a known fallback.
func (f Fallback) Valid() bool {
	for _, known := range Fallbacks {
		if f == known {
			return true
		}
	}
	return false
}

// ParseFallback resolves a configured value to a Fallback. An empty value
// selects FallbackError.
func ParseFallback(s string) (Fallback, error) {
	if s == "" {
		return FallbackError, nil
	}
	f := Fallback(s)
	if !f.Valid() {
		return "", fmt.Errorf("unknown hardlink fallback %q (want one of error, copy, symlink, move)", s)
	}
	return f, nil
}

// Options configures a single import.
type Options struct {
	// Mode is the import strategy. The zero value means ModeMove.
	Mode Mode
	// HardlinkFallback applies only when Mode is ModeHardlink and source and
	// destination turn out to be on different filesystems. The zero value
	// means FallbackError.
	HardlinkFallback Fallback
}

// ErrCrossDevice is returned when a hardlink import cannot span the
// filesystem boundary between the download directory and the library, and no
// fallback is configured. It wraps syscall.EXDEV so callers can match either.
var ErrCrossDevice = errors.New("hardlink import requires source and destination on the same filesystem")

// Import places the file or directory at src into dest using opt.Mode.
//
// Directories are handled per mode: a hardlink import walks the tree and links
// each file individually (a directory cannot itself be hardlinked), a symlink
// import creates a single link at dest, and copy/move keep their existing
// recursive behavior.
func Import(src, dest string, opt Options) error {
	mode := opt.Mode
	if mode == "" {
		mode = ModeMove
	}
	if !mode.Valid() {
		return fmt.Errorf("unknown import mode %q", mode)
	}
	if _, err := os.Lstat(src); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	switch mode {
	case ModeMove:
		return MoveContent(src, dest)
	case ModeCopy:
		return copyContent(src, dest)
	case ModeSymlink:
		return symlinkContent(src, dest)
	}

	// Hardlink. Remember whether dest already existed so a failed attempt can
	// be rolled back without deleting anything the user already had there.
	_, destErr := os.Lstat(dest)
	destExisted := destErr == nil

	err := hardlinkContent(src, dest)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return err
	}
	if !destExisted {
		os.RemoveAll(dest) // drop the half-linked tree before retrying
	}

	fallback := opt.HardlinkFallback
	if fallback == "" {
		fallback = FallbackError
	}
	if !fallback.Valid() {
		return fmt.Errorf("unknown hardlink fallback %q", fallback)
	}
	if fallback == FallbackError {
		return fmt.Errorf("%w: %s and %s are on different mounts — put the download "+
			"directory and the library on one filesystem, or set IMPORT_HARDLINK_FALLBACK "+
			"to copy, symlink or move: %w", ErrCrossDevice, src, dest, err)
	}

	slog.Warn("hardlink import crossed a filesystem boundary, using fallback",
		"src", src, "dest", dest, "fallback", string(fallback))
	switch fallback {
	case FallbackCopy:
		return copyContent(src, dest)
	case FallbackSymlink:
		return symlinkContent(src, dest)
	default:
		return MoveContent(src, dest)
	}
}

// isCrossDevice reports whether err is the kernel refusing to link or rename
// across a filesystem boundary.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

// MoveContent moves a file or directory tree to dest, falling back to
// copy+delete across filesystems. The source does not survive.
func MoveContent(src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := os.Rename(src, dest); err == nil {
			return nil
		}
		if err := copyDir(src, dest); err != nil {
			return err
		}
		return os.RemoveAll(src)
	}
	return MoveFile(src, dest)
}

// MoveFile moves a single file, falling back to copy+delete for cross-device
// moves.
func MoveFile(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := CopyFile(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyContent(src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return copyDir(src, dest)
	}
	return CopyFile(src, dest)
}

// symlinkContent points dest at src with a single absolute symlink, whether
// src is a file or a directory.
func symlinkContent(src, dest string) error {
	target, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	return os.Symlink(target, dest)
}

// hardlinkContent links src to dest. A directory is walked and linked file by
// file, since only files can be hardlinked.
func hardlinkContent(src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return os.Link(src, dest)
	}
	return walkTree(src, dest, func(path, target string, info fs.FileInfo) error {
		return os.Link(path, target)
	})
}

func copyDir(src, dest string) error {
	return walkTree(src, dest, func(path, target string, info fs.FileInfo) error {
		return CopyFile(path, target)
	})
}

// walkTree mirrors the directory structure of src under dest and calls fn for
// every file. Entries whose relative path would escape dest are rejected
// rather than written outside the library root.
func walkTree(src, dest string, fn func(path, target string, info fs.FileInfo) error) error {
	return filepath.Walk(src, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("path %q escapes source dir: %w", path, err)
		}
		if rel != "." && !filepath.IsLocal(rel) {
			return fmt.Errorf("unsafe path %q escapes %q", rel, dest)
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return fn(path, target, info)
	})
}

// CopyFile copies a single file, preserving its mode.
func CopyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	mode := os.FileMode(0644)
	if err == nil {
		mode = fi.Mode().Perm()
	}

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
