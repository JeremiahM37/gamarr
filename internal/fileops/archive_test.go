package fileops

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// readTar returns every entry in a tar keyed by name, directories included as
// empty strings.
func readTar(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	out := map[string]string{}
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar %s: %v", path, err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read entry %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(body)
	}
	return out
}

// vaultEntries lists what is visible in a vault directory.
func vaultEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestArchiveDest(t *testing.T) {
	// GameVault strips a (...) group to parse the release year, so the year has
	// to survive as part of the name.
	got := ArchiveDest(filepath.Join("/vault", "007 First Light (2025)"))
	want := filepath.Join("/vault", "007 First Light (2025).tar")
	if got != want {
		t.Errorf("ArchiveDest = %q, want %q", got, want)
	}
}

// The dot is what keeps an in-progress archive out of Gamarr's vault scan,
// which skips dotfiles, and out of a GameVault scan.
func TestPartialPatternIsHidden(t *testing.T) {
	p := partialPattern(filepath.Join("/vault", "Game.tar"))
	if !strings.HasPrefix(p, ".") {
		t.Errorf("partial pattern %q is not hidden", p)
	}
	if !strings.HasSuffix(p, partialSuffix) {
		t.Errorf("partial pattern %q does not end in %q", p, partialSuffix)
	}
}

func TestArchivable(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(dir, "setup.exe"), "SETUP")
	file := filepath.Join(root, "loose.exe")
	writeFile(t, file, "LOOSE")
	link := filepath.Join(root, "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if !Archivable(dir) {
		t.Error("a directory should be archivable")
	}
	if Archivable(file) {
		t.Error("a single file is already one file per game, so should not be archivable")
	}
	// Stat would follow this and call it a directory, while the walk that writes
	// the archive lstats the same root and descends into nothing.
	if Archivable(link) {
		t.Error("a symlink to a directory should not be archivable")
	}
	if Archivable(filepath.Join(root, "missing")) {
		t.Error("a missing path should not be archivable")
	}
}

// A repack arrives as a folder of parts. All of them have to reach the tar,
// nested ones included, or the archive is a game that cannot be installed.
func TestArchiveHoldsEveryInputFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "007 First Light")
	want := map[string]string{
		"setup.exe":            "SETUP",
		"fg-01.bin":            "PART ONE",
		"fg-02.bin":            "PART TWO",
		"MD5/fitgirl-bins.md5": "HASHES",
	}
	for name, content := range want {
		writeFile(t, filepath.Join(src, name), content)
	}

	// Repacks ship empty directories the installer writes into. The census
	// counts only regular files, so nothing else would notice these going
	// missing.
	for _, dir := range []string{"MD5", "Saves"} {
		if err := os.MkdirAll(filepath.Join(src, dir), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	vault := filepath.Join(root, "vault")
	dest := ArchiveDest(filepath.Join(vault, "007 First Light"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got := readTar(t, dest)
	for name, content := range want {
		if got[name] != content {
			t.Errorf("tar entry %q = %q, want %q", name, got[name], content)
		}
	}
	// Assert the directory entries are present before dropping them from the
	// count, or the comparison passes whether or not they were written.
	for _, dir := range []string{"MD5/", "Saves/"} {
		if _, ok := got[dir]; !ok {
			t.Errorf("tar is missing the directory entry %q", dir)
		}
		delete(got, dir)
	}
	if len(got) != len(want) {
		t.Errorf("tar holds %d file entries, want %d: %v", len(got), len(want), got)
	}

	if names := vaultEntries(t, vault); len(names) != 1 || names[0] != "007 First Light.tar" {
		t.Errorf("vault holds %v, want just the archive", names)
	}
}

// A deselected pack is a zero-filled placeholder at its full preallocated
// extent carrying the client's partial suffix. It never belongs in the vault
// archive, and the census that authorises dropping the download has to count
// the tar the same way the walk wrote it.
func TestArchiveSkipsQbPlaceholders(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Days Gone")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
	writeFile(t, filepath.Join(src, "fg-01.bin"), "PART ONE")
	writeFile(t, filepath.Join(src, "fg-02.bin"+qBPartSuffix), "PLACEHOLDER")

	dest := ArchiveDest(filepath.Join(root, "vault", "Days Gone"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got := readTar(t, dest)
	if _, ok := got["fg-02.bin"+qBPartSuffix]; ok {
		t.Error("tar carries the deselected pack placeholder")
	}
	if got["setup.exe"] != "SETUP" || got["fg-01.bin"] != "PART ONE" {
		t.Errorf("tar lost a wanted file: %v", got)
	}
	if err := VerifyArchive(dest, src); err != nil {
		t.Errorf("VerifyArchive: %v", err)
	}
}

// twoFileArchive builds a source of two regular files and its archive.
func twoFileArchive(t *testing.T) (string, string, int64) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), strings.Repeat("SETUP...", 448))
	writeFile(t, filepath.Join(src, "fg-01.bin"), strings.Repeat("PAYLOAD.", 256))

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	_, wantBytes, err := censusOf(src)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	return src, dest, wantBytes
}

// An archive whose tail is missing must not authorise dropping the download.
// Comparing the tar's file size against the source's content bytes cannot catch
// this: a tar also carries a header per entry, padding and a 1024-byte trailer,
// so it is always larger than the content by an amount that comparison treats
// as slack.
func TestVerifyArchiveRejectsATruncatedArchive(t *testing.T) {
	src, dest, wantBytes := twoFileArchive(t)

	// Longer than the source's content bytes, so a size comparison accepts it,
	// and short enough to cut into the last member.
	if err := os.Truncate(dest, wantBytes+512); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	if err := VerifyArchive(dest, src); err == nil {
		t.Error("accepted a truncated archive, which would authorise deleting the only other copy")
	}
}

// A symlink entry is header-only: the reader checks nothing against its declared
// size, so counting one as a file lets 512 bytes of header stand in for the
// entire payload.
func TestVerifyArchiveRejectsAHeaderOnlyEntry(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "fg-01.bin"), strings.Repeat("PAYLOAD.", 4096))
	_, wantBytes, err := censusOf(src)
	if err != nil {
		t.Fatalf("census: %v", err)
	}

	// One entry claiming the whole payload while carrying no body at all.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "fg-01.bin", Typeflag: tar.TypeSymlink, Linkname: "elsewhere",
		Size: wantBytes, Format: tar.FormatGNU,
	}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, buf.Bytes(), 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	if err := VerifyArchive(dest, src); err == nil {
		t.Errorf("accepted a %d-byte archive holding no content as a stand-in for %d bytes",
			buf.Len(), wantBytes)
	}
}

// Reading one header validates one entry. Everything after it has to be walked
// or a tar whose later entries are destroyed still passes.
func TestVerifyArchiveRejectsCorruptionPastTheFirstEntry(t *testing.T) {
	src, dest, _ := twoFileArchive(t)

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	// Derive where the second header starts rather than coupling to the
	// fixture: one header block plus the first body padded to a block boundary.
	first, err := tar.NewReader(bytes.NewReader(raw)).Next()
	if err != nil {
		t.Fatalf("read the first header: %v", err)
	}
	for i := 512 + int((first.Size+511)/512)*512; i < len(raw); i++ {
		raw[i] = 0xFF
	}
	if err := os.WriteFile(dest, raw, 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	if err := VerifyArchive(dest, src); err == nil {
		t.Error("accepted an archive destroyed past its first entry")
	}
}

// The format has to survive what FileInfoHeader actually produces on a real
// file, not just a hand-built header.
func TestArchiveWritesGNUHeaders(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
	writeFile(t, filepath.Join(src, "data", "fg-01.bin"), "PAYLOAD")

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if strings.Contains(string(raw), "PaxHeaders") {
		t.Error("a real archive still carries a PAX extended header")
	}
	// Bytes 345 to 500 of a header block are the ustar path prefix, and GNU
	// reuses them for atime and ctime. Go's reader knows the difference and a
	// prefix-reading one does not, so assert the bytes rather than the fields.
	for off := 0; off+512 <= len(raw); off += 512 {
		blk := raw[off : off+512]
		if string(blk[257:262]) != "ustar" {
			continue
		}
		for i, b := range blk[345:500] {
			if b != 0 {
				t.Fatalf("header at offset %d has %#x in the ustar prefix field at +%d, which a prefix-reading extractor prepends to the name", off, b, i)
			}
		}
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if hdr.Format != tar.FormatGNU {
			t.Errorf("%s written as %v, want GNU", hdr.Name, hdr.Format)
		}
	}
}

// A member past the ustar ceiling has to carry its size in the header, not in a
// PAX extended header. The header declares the size and the body is never
// written: asserting a header field is not worth 9 GiB of I/O. The over-long
// Uname is the input that would otherwise push the writer to PAX.
func TestWriteGNUHeaderKeepsALargeMemberOutOfPax(t *testing.T) {
	var buf bytes.Buffer
	hdr := &tar.Header{
		Name:     "fg-01.bin",
		Mode:     0644,
		Size:     9 << 30,
		Uname:    strings.Repeat("u", 40),
		ModTime:  time.Unix(1700000000, 0),
		Typeflag: tar.TypeReg,
	}
	if err := writeGNUHeader(tar.NewWriter(&buf), hdr); err != nil {
		t.Fatalf("writeGNUHeader: %v", err)
	}

	if strings.Contains(buf.String(), "PaxHeaders") {
		t.Error("fell back to PAX, which is the format this exists to avoid")
	}
	got, err := tar.NewReader(bytes.NewReader(buf.Bytes())).Next()
	if err != nil {
		t.Fatalf("read back the header: %v", err)
	}
	if got.Format != tar.FormatGNU {
		t.Errorf("format = %v, want GNU", got.Format)
	}
	if got.Uname != "" {
		t.Errorf("Uname = %q, want it cleared to keep the format", got.Uname)
	}
	if got.Name != "fg-01.bin" || got.Size != 9<<30 {
		t.Errorf("header = %q at %d bytes, want fg-01.bin at %d", got.Name, got.Size, int64(9<<30))
	}
}

// Forcing a format stops the writer normalising times, and atime and ctime then
// occupy the bytes a ustar reader takes for a path prefix.
func TestWriteGNUHeaderNormalisesTimes(t *testing.T) {
	var buf bytes.Buffer
	hdr := &tar.Header{
		Name: "setup.exe", Mode: 0644, Typeflag: tar.TypeReg,
		ModTime:    time.Unix(1700000000, 900000000),
		AccessTime: time.Unix(1600000000, 0),
		ChangeTime: time.Unix(1650000000, 0),
	}
	if err := writeGNUHeader(tar.NewWriter(&buf), hdr); err != nil {
		t.Fatalf("writeGNUHeader: %v", err)
	}

	got, err := tar.NewReader(bytes.NewReader(buf.Bytes())).Next()
	if err != nil {
		t.Fatalf("read back the header: %v", err)
	}
	if !got.AccessTime.IsZero() || !got.ChangeTime.IsZero() {
		t.Errorf("atime %v, ctime %v, want both cleared", got.AccessTime, got.ChangeTime)
	}
	// GNU stores whole seconds, so an unrounded mtime is floored rather than
	// rounded and this lands a second early.
	if got.ModTime.Unix() != 1700000001 {
		t.Errorf("mtime = %d, want 1700000001", got.ModTime.Unix())
	}
}

// VerifyArchive is what authorises dropping a source, so every way an occupant
// can fail to be an archive of src has to refuse. Size alone does not: a
// hand-placed file large enough to clear the census reads as one until the tar
// header is actually parsed.
func TestVerifyArchive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), strings.Repeat("PAYLOAD", 512))
	// A repack ships empty directories. The tar carries an entry for each and
	// the census counts none of them, so the walk has to agree or every real
	// archive fails by the directory count.
	if err := os.MkdirAll(filepath.Join(src, "Saves"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	good := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, good); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if err := VerifyArchive(good, src); err != nil {
		t.Errorf("VerifyArchive on a real archive of the source: %v", err)
	}

	// A real archive, but of less content than src. It parses as a tar, so only
	// the census comparison can refuse it.
	smallSrc := filepath.Join(root, "Small")
	writeFile(t, filepath.Join(smallSrc, "setup.exe"), "SETUP")
	short := ArchiveDest(filepath.Join(root, "other-vault", "Small"))
	if err := Archive(smallSrc, short); err != nil {
		t.Fatalf("Archive the smaller source: %v", err)
	}

	junk := filepath.Join(root, "junk.tar")
	writeFile(t, junk, strings.Repeat("not a tar", 1024))
	adir := filepath.Join(root, "adir.tar")
	if err := os.MkdirAll(adir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for name, path := range map[string]string{
		"a file too short to hold the source": short,
		"a file that is not a tar at all":     junk,
		"a directory":                         adir,
		"nothing at that name":                filepath.Join(root, "absent.tar"),
	} {
		if err := VerifyArchive(path, src); err == nil {
			t.Errorf("VerifyArchive accepted %s, which would authorise dropping the source", name)
		}
	}
}

// Archiving reads the source and writes a new file, so the archive step itself
// can never be a move: whatever IMPORT_MODE says, Archive leaves the source
// where it found it and any later decision about it is the caller's.
func TestArchiveLeavesSourceInPlace(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	if read(t, filepath.Join(src, "setup.exe")) != "SETUP" {
		t.Error("source file did not survive the archive")
	}
}

// The only failure the deletion gate has to survive is one after the tar is
// part-written. Walk order puts the readable file first, so bytes reach the
// partial before the symlink fails the archive.
func TestArchiveFailingMidWriteLeavesNothingBehind(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "a-payload.bin"), strings.Repeat("PAYLOAD!", 8192))
	if err := os.Symlink(filepath.Join(src, "a-payload.bin"), filepath.Join(src, "zz-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	vault := filepath.Join(root, "vault")
	dest := ArchiveDest(filepath.Join(vault, "Game"))
	err := Archive(src, dest)
	if err == nil {
		t.Fatal("want an error when the tree holds an irregular file")
	}
	// Names the symlink, which proves the regular file ahead of it was written
	// before the failure rather than the archive failing up front.
	if !strings.Contains(err.Error(), "zz-link") {
		t.Errorf("error = %v, want it to name the irregular entry", err)
	}

	if _, statErr := os.Lstat(dest); statErr == nil {
		t.Error("a failed archive was published to the vault")
	}
	if names := vaultEntries(t, vault); len(names) != 0 {
		t.Errorf("vault holds %v after a failed archive, want nothing", names)
	}
}

// Each of these returned a 1024-byte tar and nil before, which on the usenet
// path then authorized deleting the only copy of the download.
func TestArchiveRefusesAnEmptyResult(t *testing.T) {
	root := t.TempDir()

	empty := filepath.Join(root, "Empty")
	if err := os.MkdirAll(filepath.Join(empty, "nested"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	realDir := filepath.Join(root, "Real")
	writeFile(t, filepath.Join(realDir, "setup.exe"), "SETUP")
	linked := filepath.Join(root, "Linked")
	if err := os.Symlink(realDir, linked); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, src := range []string{empty, linked} {
		dest := ArchiveDest(filepath.Join(root, "vault", filepath.Base(src)))
		if err := Archive(src, dest); err == nil {
			t.Errorf("Archive(%s) = nil, want an error rather than an empty tar", src)
		}
		if _, err := os.Lstat(dest); err == nil {
			t.Errorf("Archive(%s) published an archive anyway", src)
		}
	}
}

// A rename overwrites atomically and totally, unlike the directory merge the
// archive path replaced. Both layouts count as occupied: a folder left by an
// unarchived import is the same game, and refusing only the archive stores it
// twice at full size.
func TestArchiveRefusesAnOccupiedVault(t *testing.T) {
	t.Run("an existing archive", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "Game")
		writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")

		dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
		if err := Archive(src, dest); err != nil {
			t.Fatalf("first Archive: %v", err)
		}
		before := read(t, dest)

		if err := Archive(src, dest); err == nil {
			t.Error("second Archive = nil, want a refusal")
		}
		if read(t, dest) != before {
			t.Error("the existing archive was modified")
		}
	})

	t.Run("a folder from an unarchived import", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "Game")
		writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")
		writeFile(t, filepath.Join(root, "vault", "Game", "setup.exe"), "OLD")

		dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
		if err := Archive(src, dest); err == nil {
			t.Error("Archive = nil, want a refusal: the vault already holds the folder")
		}
		if _, err := os.Lstat(dest); err == nil {
			t.Error("stored the same game a second time, as an archive beside the folder")
		}
	})
}

// Two flows can resolve to one vault name: a torrent and an NZB of the same
// game, or a re-grab. Exactly one may publish, and what it publishes has to be
// one source in full rather than a splice of both.
func TestArchiveConcurrentSameDestination(t *testing.T) {
	root := t.TempDir()
	sources := map[string]string{"A": strings.Repeat("AAAA", 4096), "B": strings.Repeat("BBBB", 4096)}
	for name, content := range sources {
		writeFile(t, filepath.Join(root, "src"+name, "payload.bin"), content)
	}

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"A", "B"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			errs[i] = Archive(filepath.Join(root, "src"+name), dest)
		}(i, name)
	}
	wg.Wait()

	okCount := 0
	for _, err := range errs {
		if err == nil {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("errs = %v, want exactly one success", errs)
	}

	got := readTar(t, dest)
	body, ok := got["payload.bin"]
	if !ok {
		t.Fatal("published archive has no payload entry")
	}
	if body != sources["A"] && body != sources["B"] {
		t.Errorf("published archive holds %d bytes matching neither source in full", len(body))
	}
	if names := vaultEntries(t, filepath.Dir(dest)); len(names) != 1 {
		t.Errorf("vault holds %v, want just the archive", names)
	}
}

// GameVault reads the vault from a separate container over a shared mount, so a
// mode only the writing uid can read publishes a game nothing can open. Every
// other import path preserves the source's mode; this one invents one, and
// os.CreateTemp's 0600 is not subject to umask.
func TestArchiveIsReadableByOtherUsers(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, dest); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0644 {
		t.Errorf("published archive mode = %v, want 0644", got)
	}
}

// The count comparison is the whole of the verification, and deleting the
// expression leaves everything else green: the zero case has its own guard, so
// only a census that disagrees with the walk reaches it.
func TestArchiveRefusesWhenTheCountsDisagree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Game")
	writeFile(t, filepath.Join(src, "setup.exe"), "SETUP")

	real := censusOf
	censusOf = func(string) (int64, int64, error) { return 7, 99999, nil }
	t.Cleanup(func() { censusOf = real })

	dest := ArchiveDest(filepath.Join(root, "vault", "Game"))
	if err := Archive(src, dest); err == nil {
		t.Fatal("Archive = nil, want an error when the archive does not match the source")
	}
	if _, err := os.Lstat(dest); err == nil {
		t.Error("published an archive that did not match the source")
	}
	if names := vaultEntries(t, filepath.Dir(dest)); len(names) != 0 {
		t.Errorf("vault holds %v, want nothing", names)
	}
}

func TestArchiveMissingSource(t *testing.T) {
	root := t.TempDir()
	err := Archive(filepath.Join(root, "nope"), ArchiveDest(filepath.Join(root, "vault", "nope")))
	if err == nil {
		t.Fatal("want an error archiving a source that does not exist")
	}
}

// A partial abandoned by a crash gets a unique name, so nothing reclaims it.
func TestSweepPartials(t *testing.T) {
	vault := t.TempDir()
	stale := filepath.Join(vault, ".Game.tar.123"+partialSuffix)
	fresh := filepath.Join(vault, ".Other.tar.456"+partialSuffix)
	keep := filepath.Join(vault, "Real Game.tar")
	for _, p := range []string{stale, fresh, keep} {
		writeFile(t, p, "x")
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// A partial whose mtime a network mount reports ahead of local time is
	// exactly the one a crash just left, so at minAge 0 age must not be
	// consulted at all.
	t.Run("minAge 0 ignores mtime entirely", func(t *testing.T) {
		dir := t.TempDir()
		future := filepath.Join(dir, ".Game.tar.789"+partialSuffix)
		writeFile(t, future, "x")
		ahead := time.Now().Add(time.Hour)
		if err := os.Chtimes(future, ahead, ahead); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		if n := SweepPartials(dir, 0); n != 1 {
			t.Errorf("swept %d, want 1", n)
		}
		if _, err := os.Lstat(future); err == nil {
			t.Error("a partial with a future mtime survived a floorless sweep")
		}
	})

	if n := SweepPartials(vault, 24*time.Hour); n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
	if _, err := os.Lstat(stale); err == nil {
		t.Error("the abandoned partial survived")
	}
	// A partial younger than the floor may belong to an import still writing.
	if _, err := os.Lstat(fresh); err != nil {
		t.Error("a recent partial was swept")
	}
	if _, err := os.Lstat(keep); err != nil {
		t.Error("a real archive was swept")
	}
}
