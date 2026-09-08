package minerva

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTorrentRejectsUnsafeCollectionRoot(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/absolute", "../escape", "pack/../safe", `C:\pack`, "C:/pack", `\\server\share`, "bad\x00root"} {
		t.Run(name, func(t *testing.T) {
			data := fmt.Sprintf("d4:infod5:filesld6:lengthi3e4:pathl5:a.ndseee4:name%d:%see", len(name), name)
			if _, err := ParseTorrent([]byte(data)); err == nil {
				t.Fatalf("unsafe collection root accepted: %q", name)
			}
		})
	}
}

func TestParseTorrentSingleFile(t *testing.T) {
	// Mutation caught: hashing re-encoded info data (or the whole torrent) instead of its original bytes.
	data := []byte("d4:infod6:lengthi5e4:name7:rom.nesee")

	got, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "rom.nes" {
		t.Fatalf("Name=%q", got.Name)
	}
	if got.InfoHash != "5978c4196602cf0d4b848f107a7d0c36fade03f6" {
		t.Fatalf("InfoHash=%q", got.InfoHash)
	}
	if len(got.Files) != 1 {
		t.Fatalf("files=%+v", got.Files)
	}
	if got.Files[0] != (FileMeta{Index: 0, Path: "rom.nes", Name: "rom.nes", Size: 5}) {
		t.Fatalf("file=%+v", got.Files[0])
	}
}

func TestParseTorrentMultiFile(t *testing.T) {
	// Mutation caught: sorting files or assigning non-qBittorrent file indices while parsing a files list.
	data := []byte("d4:infod5:filesld6:lengthi4e4:pathl3:dir5:b.ndseed6:lengthi3e4:pathl5:a.ndseee4:name10:collectionee")

	got, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "collection" {
		t.Fatalf("Name=%q", got.Name)
	}
	if got.InfoHash != "331ff39814d5684c324e3d1c96b90d8f58b977ba" {
		t.Fatalf("InfoHash=%q", got.InfoHash)
	}
	want := []FileMeta{
		{Index: 0, Path: "collection/dir/b.nds", Name: "b.nds", Size: 4},
		{Index: 1, Path: "collection/a.nds", Name: "a.nds", Size: 3},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files=%+v", got.Files)
	}
	for i := range want {
		if got.Files[i] != want[i] {
			t.Fatalf("file[%d]=%+v, want %+v", i, got.Files[i], want[i])
		}
	}
}

func TestParseTorrentHashesOriginalInfoBytes(t *testing.T) {
	// Mutation caught: re-encoding only recognized info fields drops unknown metadata and changes the v1 info hash.
	data := []byte("d4:infod6:lengthi5e4:name7:rom.nes6:source3:oldee")

	got, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.InfoHash != "affc8c67dcd4232e26e2bb61a2eb98c73826f9de" {
		t.Fatalf("InfoHash=%q", got.InfoHash)
	}
}

func TestParseTorrentRejectsUnsafePaths(t *testing.T) {
	// Mutation caught: accepting a path which escapes the torrent root or contains a forbidden filename component.
	tests := []struct {
		name string
		data string
	}{
		{"parent", "d4:infod5:filesld6:lengthi1e4:pathl13:../escape.ndseee4:name4:packee"},
		{"absolute", "d4:infod5:filesld6:lengthi1e4:pathl11:/escape.ndseee4:name4:packee"},
		{"nul", "d4:infod5:filesld6:lengthi1e4:pathl8:bad\x00.ndseee4:name4:packee"},
		{"empty", "d4:infod5:filesld6:lengthi1e4:pathl0:eee4:name4:packee"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTorrent([]byte(tt.data)); err == nil {
				t.Fatal("ParseTorrent accepted unsafe path")
			}
		})
	}
}

func TestParseTorrentRejectsWindowsSingleFileNames(t *testing.T) {
	// Mutation caught: accepting a Windows traversal, volume, or UNC name that becomes unsafe downstream.
	tests := []struct {
		name string
		data string
	}{
		{"backslash_parent", `d4:infod6:lengthi1e4:name13:..\escape.ndsee`},
		{"backslash_volume", `d4:infod6:lengthi1e4:name13:C:\escape.ndsee`},
		{"slash_volume", `d4:infod6:lengthi1e4:name13:C:/escape.ndsee`},
		{"unc", `d4:infod6:lengthi1e4:name25:\\server\share\escape.ndsee`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTorrent([]byte(tt.data)); err == nil {
				t.Fatal("ParseTorrent accepted unsafe Windows single-file name")
			}
		})
	}
}

func TestParseTorrentRejectsMalformedFilePathComponents(t *testing.T) {
	// Mutation caught: joining invalid components before validation lets path.Clean erase them into safe-looking paths.
	tests := []struct {
		name string
		data string
	}{
		{"empty", "d4:infod5:filesld6:lengthi1e4:pathl3:dir0:eee4:name4:packee"},
		{"dot", "d4:infod5:filesld6:lengthi1e4:pathl3:dir1:.eee4:name4:packee"},
		{"absolute", "d4:infod5:filesld6:lengthi1e4:pathl3:dir11:/escape.ndseee4:name4:packee"},
		{"embedded_slash", "d4:infod5:filesld6:lengthi1e4:pathl3:dir9:nest/fileeee4:name4:packee"},
		{"embedded_backslash", `d4:infod5:filesld6:lengthi1e4:pathl3:dir9:nest\fileeee4:name4:packee`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTorrent([]byte(tt.data)); err == nil {
				t.Fatal("ParseTorrent accepted malformed file path component")
			}
		})
	}
}

func TestParseTorrentRejectsMalformedMetadata(t *testing.T) {
	// Mutation caught: treating invalid bencode or an absent/ambiguous top-level info dictionary as valid metadata.
	tests := []struct {
		name string
		data string
	}{
		{"malformed_integer", "d4:infod6:lengthi-0e4:name1:aee"},
		{"unterminated_list", "d4:infod5:filesl"},
		{"unterminated_dictionary", "d4:infod6:lengthi1e4:name1:ae"},
		{"info_not_dictionary", "d4:info1:ae"},
		{"length_not_integer", "d4:infod6:length1:x4:name1:aee"},
		{"files_not_list", "d4:infod5:files1:x4:name1:aee"},
		{"missing_info", "d4:fooi1ee"},
		{"duplicate_info", "d4:infod6:lengthi1e4:name1:ae4:infod6:lengthi2e4:name1:bee"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseTorrent([]byte(tt.data)); err == nil {
				t.Fatal("ParseTorrent accepted malformed metadata")
			}
		})
	}
}

func TestDecodeBencodeNestingBoundary(t *testing.T) {
	// Mutation caught: removing the decoder nesting limit accepts metadata beyond the allowed 64 containers.
	tests := []struct {
		depth   int
		wantErr bool
	}{
		{depth: 64, wantErr: false},
		{depth: 65, wantErr: true},
	}
	for _, tt := range tests {
		data := strings.Repeat("l", tt.depth) + "0:" + strings.Repeat("e", tt.depth)
		_, err := decodeBencode([]byte(data))
		if (err != nil) != tt.wantErr {
			t.Fatalf("depth %d: err=%v, wantErr=%t", tt.depth, err, tt.wantErr)
		}
	}
}
