package download

import (
	"testing"

	"gamarr/internal/qbit"
)

func TestMatchTorrentFileIndexes(t *testing.T) {
	files := []qbit.TorrentFile{
		{Name: "No-Intro/Wii/Other Game (USA).zip", Size: 1, Index: 0},
		{Name: "No-Intro/Wii/Super Paper Mario (Europe, Australia) (En,Fr,De,Es,It).zip", Size: 2, Index: 1},
		{Name: "No-Intro/Wii/Super Mario Galaxy (USA).zip", Size: 3, Index: 2},
	}
	got := matchTorrentFileIndexes(files, "Super Paper Mario (Europe, Australia) (En,Fr,De,Es,It).zip")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("got %v, want [1]", got)
	}
	got = matchTorrentFileIndexes(files, "Super Paper Mario (Europe, Australia) (En,Fr,De,Es,It)")
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("stem match got %v, want [1]", got)
	}
	if got := matchTorrentFileIndexes(files, "Missing Title.zip"); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

// Early qBittorrent API versions report every file with index 0, where position
// is the id. Selecting by the raw field would skip and enable the same file.
func TestTorrentFileIndexes_AllZeroUsesPosition(t *testing.T) {
	files := []qbit.TorrentFile{{Name: "a.zip"}, {Name: "b.zip"}, {Name: "c.zip"}}
	got := torrentFileIndexes(files)
	if len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("got %v", got)
	}
	if err := archiveSelectionMismatch([]qbit.TorrentFile{{Name: "a.zip"}, {Name: "b.zip", Priority: 1}}, map[int]bool{1: true}); err != nil {
		t.Fatalf("valid selection rejected: %v", err)
	}
}

func TestArchiveSelectionMismatch(t *testing.T) {
	wanted := map[int]bool{1: true}
	tests := []struct {
		name  string
		files []qbit.TorrentFile
		ok    bool
	}{
		{"only the wanted file", []qbit.TorrentFile{{Index: 0}, {Index: 1, Priority: 1}, {Index: 2}}, true},
		{"wanted file at a higher priority", []qbit.TorrentFile{{Index: 0}, {Index: 1, Priority: 7}, {Index: 2}}, true},
		{"unwanted file still enabled", []qbit.TorrentFile{{Index: 0, Priority: 1}, {Index: 1, Priority: 1}, {Index: 2}}, false},
		{"wanted file skipped", []qbit.TorrentFile{{Index: 0}, {Index: 1}, {Index: 2}}, false},
		{"list unreadable", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := archiveSelectionMismatch(tt.files, wanted); (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
