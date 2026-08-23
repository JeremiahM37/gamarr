package fileops

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TarExt is the extension of a vault archive.
const TarExt = ".tar"

// partialSuffix marks an archive that is still being written.
const partialSuffix = ".partial"

// ErrDestinationOccupied reports that the library already holds something at
// this destination. One sentinel covers the vault in either layout and the ROM
// library, so a caller cannot match one path and miss the other.
var ErrDestinationOccupied = errors.New("already exists at destination")

// ArchiveDest returns the archive path for a vault destination.
func ArchiveDest(dest string) string { return dest + TarExt }

// VaultOccupied reports the path already holding base, checking both layouts: a
// folder from an unarchived import and an archive from an archived one.
//
// Checking only the layout the archive flag currently selects stores the same
// game twice at full size, on the exact operation an operator performs when
// adopting the flag: turn it on, re-import something already there.
//
// Content placed in the vault by anything other than Gamarr is out of scope: on
// a cached mount this reads a directory listing that can be badly stale.
func VaultOccupied(base string) (string, bool) {
	for _, p := range []string{base, ArchiveDest(base)} {
		if _, err := os.Lstat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// ArchiveHolds reports whether path could be an archive of src: a regular file
// at least as large as src's payload.
//
// A necessary condition, not proof, and that is the point. "Something is at this
// name" is not evidence that this content was ever published there, and a caller
// that reports a finished import on that basis hands a release decision to a
// layer that acts on it. An uncompressed tar cannot be smaller than the files it
// holds, so a short occupant, a directory, or a hand-placed file is definitely
// not ours.
func ArchiveHolds(path, src string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	_, wantBytes, err := censusOf(src)
	return err == nil && fi.Size() >= wantBytes
}

// VerifyArchive reports whether the archive at dest can stand in for src: every
// regular file the source held, present in the tar at the size its header
// declares. Bodies are not read, so their contents are not checked.
//
// This is what authorises dropping src, so it walks the whole archive to EOF
// rather than sampling the front of it. Bodies are skipped rather than read,
// which keeps it cheap on a large archive, and the reader turns a body shorter
// than its header declares into an error rather than a quiet early end.
//
// Counts and sizes come from the same census Archive wrote from, so both sides
// count regular files and neither counts the directory entries the tar also
// carries.
func VerifyArchive(dest, src string) error {
	fi, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", dest)
	}
	wantFiles, wantBytes, err := censusOf(src)
	if err != nil {
		return err
	}
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	var gotFiles, gotBytes int64
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("archive %s is not readable past %d files: %w", dest, gotFiles, err)
		}
		// Positively, not by excluding directories: a symlink or device entry
		// is header-only, so the reader checks nothing against its declared
		// size and 512 bytes of header would stand in for a whole payload.
		if !hdr.FileInfo().Mode().IsRegular() {
			continue
		}
		gotFiles++
		gotBytes += hdr.Size
	}
	if gotFiles != wantFiles || gotBytes != wantBytes {
		return fmt.Errorf("archive %s holds %d files and %d bytes, source has %d and %d",
			dest, gotFiles, gotBytes, wantFiles, wantBytes)
	}
	return nil
}

// partialPattern is the os.CreateTemp pattern for an in-progress archive. The
// leading dot is load-bearing: it keeps a partial out of Gamarr's own vault
// scan, which skips dotfiles, and out of a GameVault library scan.
func partialPattern(dest string) string {
	return "." + filepath.Base(dest) + ".*" + partialSuffix
}

// archiveLocks serializes archives by destination. Checking that the
// destination is free and then publishing are two steps, so without this two
// imports resolving to the same vault name both pass the check and one replaces
// the other. Keyed by path, not by torrent hash: the same game can arrive over
// both a torrent and an NZB.
var archiveLocks sync.Map

func lockDest(dest string) func() {
	v, _ := archiveLocks.LoadOrStore(dest, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Archivable reports whether src is worth writing as an archive. Only a real
// directory is: a single file already is the one-file-per-game unit a GameVault
// library indexes.
//
// Lstat rather than Stat, because Stat follows a symlink to a directory and
// would report one here, while the walk that writes the archive lstats the same
// root, sees a link, and descends into nothing.
func Archivable(src string) bool {
	fi, err := os.Lstat(src)
	return err == nil && fi.IsDir()
}

// Archive writes the tree at src into a single uncompressed tar at dest,
// leaving src in place.
//
// The tar is a pure container, never compressed: a repack's payload is already
// compressed, so a second pass costs hours of CPU for no size change.
//
// A nil return means every regular file under src reached the archive and the
// archive is published at dest, on a name nothing else held. Dropping src takes
// VerifyArchive on top of that: what a write reported and what the destination
// now holds are separate questions. Durability past that point belongs to
// whatever storage backs the vault.
func Archive(src, dest string) error {
	defer lockDest(dest)()

	wantFiles, wantBytes, err := censusOf(src)
	if err != nil {
		return err
	}
	if wantFiles == 0 {
		return fmt.Errorf("refusing to archive %s: it holds no regular files", src)
	}
	if occupied, ok := VaultOccupied(strings.TrimSuffix(dest, TarExt)); ok {
		return fmt.Errorf("%w: %s", ErrDestinationOccupied, occupied)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	// CreateTemp is O_EXCL, so two imports resolving to the same vault name
	// cannot open one inode and interleave into each other's entries.
	f, err := os.CreateTemp(filepath.Dir(dest), partialPattern(dest))
	if err != nil {
		return err
	}
	partial := f.Name()

	gotFiles, gotBytes, err := writeTar(f, src)
	if err == nil && (gotFiles != wantFiles || gotBytes != wantBytes) {
		err = fmt.Errorf("archive of %s holds %d files and %d bytes, source has %d and %d",
			src, gotFiles, gotBytes, wantFiles, wantBytes)
	}
	if err == nil {
		// CreateTemp makes the file 0600 and is not subject to umask, and
		// GameVault reads the vault from another container: a mode only this uid
		// can read publishes a game nothing else can open. Every other import
		// path carries the source's mode over; this one has to pick.
		err = f.Chmod(0644)
	}
	if err == nil {
		// Pushes the page cache into the vault's own storage before the rename
		// publishes the name. Not durability on a cached mount; see above.
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(partial, dest)
	}
	if err != nil {
		removePartial(partial)
		return err
	}
	return nil
}

// removePartial drops an abandoned partial. Each attempt gets a unique name, so
// nothing reuses it and a failure here leaks disk until the next sweep.
func removePartial(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Error("leaked a partial vault archive", "path", path, "error", err)
	}
}

// SweepPartials removes partial archives abandoned in dir by a process that
// died mid-write, returning how many it dropped. minAge keeps it clear of an
// archive still being written.
func SweepPartials(dir string, minAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Otherwise indistinguishable from a vault with nothing to sweep, and a
		// listing failure is realistic on a network-backed mount.
		slog.Warn("could not scan the vault for abandoned partial archives", "dir", dir, "error", err)
		return 0
	}
	cutoff := time.Now().Add(-minAge)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, partialSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			slog.Warn("could not stat a partial archive", "dir", dir, "name", name, "error", err)
			continue
		}
		// At minAge 0 the caller has established nothing is in flight, so age
		// carries no information. Comparing anyway would spare a partial whose
		// mtime a network mount reports a few seconds ahead of local time, which
		// is exactly the one the crash just left.
		if minAge > 0 && info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			slog.Warn("could not remove an abandoned partial archive", "path", path, "error", err)
			continue
		}
		slog.Info("removed an abandoned partial archive", "path", path)
		removed++
	}
	return removed
}

// censusOf indirects census so a test can make the census and the walk disagree.
// The count comparison is the whole of the verification and is otherwise
// unreachable, since the zero case has its own guard.
var censusOf = census

// census counts the regular files under src and their total size: the figures a
// finished archive has to match before it can stand in for the source.
func census(src string) (files, bytes int64, err error) {
	err = filepath.Walk(src, func(_ string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes, err
}

func writeTar(f *os.File, src string) (files, bytes int64, err error) {
	tw := tar.NewWriter(f)
	files, bytes, err = writeTree(tw, src)
	// tar buffers, so a short write on the final entry surfaces at Close.
	if cerr := tw.Close(); err == nil {
		err = cerr
	}
	return files, bytes, err
}

// writeGNUHeader writes hdr in GNU format, which carries a size past ustar's
// 8 GiB ceiling in the header itself. PAX instead zeroes the real size field
// and moves the size to an extended header, which the p7zip 16.02 GameVault
// runs ignores: it reads the member as empty and everything after it as
// garbage. Ownership names go unconditionally, since GNU refuses one past 32
// bytes and they are cosmetic here where the format is not; atime and ctime go
// because they sit in the bytes a ustar reader takes for a path prefix.
func writeGNUHeader(tw *tar.Writer, hdr *tar.Header) error {
	hdr.ModTime = hdr.ModTime.Round(time.Second)
	hdr.AccessTime = time.Time{}
	hdr.ChangeTime = time.Time{}
	hdr.Uname, hdr.Gname = "", ""
	hdr.Format = tar.FormatGNU
	return tw.WriteHeader(hdr)
}

// writeTree adds every file under src to tw, named relative to src, and reports
// what it wrote so the caller can compare against the source.
func writeTree(tw *tar.Writer, src string) (files, bytes int64, err error) {
	err = filepath.Walk(src, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Redundant, since rel comes from Rel over Walk output and can be
		// neither absolute nor contain "..", but it costs nothing.
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("unsafe path %q escapes %q", rel, src)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			// Skipping one and returning success is the worse failure: on the
			// usenet path a nil return authorizes deleting the source.
			return fmt.Errorf("refusing to archive %s: %s is not a regular file", src, rel)
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
			return writeGNUHeader(tw, hdr)
		}
		if err := writeGNUHeader(tw, hdr); err != nil {
			return err
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		// tar declares each entry's size up front, so a file that changed size
		// since the walk fails the write rather than landing truncated.
		n, err := io.Copy(tw, in)
		if err != nil {
			return err
		}
		files++
		bytes += n
		return nil
	})
	return files, bytes, err
}
