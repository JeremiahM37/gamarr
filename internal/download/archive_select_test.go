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
