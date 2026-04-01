package models

import (
	"encoding/json"
	"testing"
)

func TestSearchResult_JSONSerialization(t *testing.T) {
	r := &SearchResult{
		Title:          "Test Game",
		Size:           5000000000,
		SizeHuman:      "4.7 GB",
		Seeders:        100,
		Leechers:       10,
		Indexer:        "TestIndexer",
		DownloadURL:    "http://example.com/download",
		MagnetURL:      "magnet:?xt=urn:btih:abc",
		InfoHash:       "abc123",
		GUID:           "http://example.com/guid",
		Platform:       "Switch",
		PlatformSlug:   "switch",
		IsPC:           false,
		Age:            5,
		SourceType:     "torrent",
		SafetyScore:    85,
		SafetyWarnings: []string{"warning1"},
	}

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded SearchResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Title != r.Title {
		t.Errorf("title=%q, want %q", decoded.Title, r.Title)
	}
	if decoded.Size != r.Size {
		t.Errorf("size=%d, want %d", decoded.Size, r.Size)
	}
	if decoded.Platform != "Switch" {
		t.Errorf("platform=%q", decoded.Platform)
	}
	if decoded.SafetyScore != 85 {
		t.Errorf("safety_score=%d", decoded.SafetyScore)
	}
}

func TestDownloadRequest_JSONSerialization(t *testing.T) {
	req := DownloadRequest{
		DownloadURL:      "http://example.com/dl",
		MagnetURL:        "",
		Title:            "Game Title",
		Platform:         "PC",
		PlatformSlug:     "",
		IsPC:             true,
		SourceType:       "torrent",
		DownloadProtocol: "torrent",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded DownloadRequest
	json.Unmarshal(data, &decoded)

	if decoded.IsPC != true {
		t.Error("expected IsPC=true")
	}
	if decoded.DownloadProtocol != "torrent" {
		t.Errorf("download_protocol=%q", decoded.DownloadProtocol)
	}
}

func TestSearchResult_VimmIDOmitempty(t *testing.T) {
	r := &SearchResult{Title: "Game", SafetyWarnings: []string{}}
	data, _ := json.Marshal(r)

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	// VimmID should be omitted when empty
	if _, exists := m["vimm_id"]; exists {
		t.Error("expected vimm_id to be omitted when empty")
	}
}

func TestDownloadEntry_JSON(t *testing.T) {
	e := DownloadEntry{
		Type:     "job",
		Title:    "Test",
		Status:   "downloading",
		Progress: 45.5,
		Speed:    "5.0 MB/s",
	}

	data, _ := json.Marshal(e)
	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)

	if decoded["type"] != "job" {
		t.Errorf("type=%v", decoded["type"])
	}
	if decoded["progress"].(float64) != 45.5 {
		t.Errorf("progress=%v", decoded["progress"])
	}
}

func TestDDLSource_JSON(t *testing.T) {
	s := DDLSource{
		Name:      "Myrient",
		URL:       "https://myrient.erista.me",
		Type:      "myrient",
		Builtin:   true,
		Platforms: []string{"nes", "snes"},
	}

	data, _ := json.Marshal(s)
	var decoded DDLSource
	json.Unmarshal(data, &decoded)

	if decoded.Name != "Myrient" {
		t.Errorf("name=%q", decoded.Name)
	}
	if len(decoded.Platforms) != 2 {
		t.Errorf("platforms=%d", len(decoded.Platforms))
	}
}

func TestMonitorStatus_JSON(t *testing.T) {
	status := MonitorStatus{
		Enabled:  true,
		Provider: "ollama",
		Model:    "qwen3:1.7b",
		AutoFix:  true,
		Interval: 300,
	}

	data, _ := json.Marshal(status)
	var decoded map[string]interface{}
	json.Unmarshal(data, &decoded)

	if decoded["enabled"] != true {
		t.Error("expected enabled=true")
	}
	if decoded["provider"] != "ollama" {
		t.Errorf("provider=%v", decoded["provider"])
	}
}
