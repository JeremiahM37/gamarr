package minerva

import (
	"strings"
	"testing"
)

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
	data := []byte("d4:infod5:filesld6:lengthi3e4:pathl5:a.ndseed6:lengthi4e4:pathl3:dir5:b.ndseee4:name10:collectionee")

	got, err := ParseTorrent(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "collection" {
		t.Fatalf("Name=%q", got.Name)
	}
	if got.InfoHash != "8bb2f8c3d23c4a8f85247b8ffb8de2c227637a24" {
		t.Fatalf("InfoHash=%q", got.InfoHash)
	}
	want := []FileMeta{
		{Index: 0, Path: "a.nds", Name: "a.nds", Size: 3},
		{Index: 1, Path: "dir/b.nds", Name: "b.nds", Size: 4},
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

func TestParseTorrentRejectsExcessiveNesting(t *testing.T) {
	// Mutation caught: removing the decoder nesting limit lets hostile metadata consume unbounded stack space.
	data := "d4:info" + strings.Repeat("d1:a", 64) + "i1e" + strings.Repeat("e", 65)

	if _, err := ParseTorrent([]byte(data)); err == nil {
		t.Fatal("ParseTorrent accepted metadata nested beyond 64 levels")
	}
}
