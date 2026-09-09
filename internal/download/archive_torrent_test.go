package download

import "testing"

func TestArchiveTorrentDisplayName(t *testing.T) {
	if got := archiveTorrentDisplayName("Super Nintendo", "snes"); got != "Super Nintendo" {
		t.Fatalf("got %q, want platform name", got)
	}
	if got := archiveTorrentDisplayName("", "gba"); got != "gba" {
		t.Fatalf("got %q, want slug fallback", got)
	}
	if got := archiveTorrentDisplayName("", ""); got != "Minerva Archive" {
		t.Fatalf("got %q, want default", got)
	}
}

func TestIsGenericArchiveTorrentName(t *testing.T) {
	for _, name := range []string{"Minerva_Myrient", "minerva myrient", " MINERVA_MYRIENT "} {
		if !isGenericArchiveTorrentName(name) {
			t.Fatalf("%q should be generic", name)
		}
	}
	if isGenericArchiveTorrentName("Super Nintendo") {
		t.Fatal("platform name should not be generic")
	}
}
