package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBuildDiscordPayload_Structure(t *testing.T) {
	p := Payload{
		Event:    EventDownloadComplete,
		Title:    "Test Game",
		Platform: "Switch",
		Status:   "completed",
		Message:  "Download finished",
		Time:     "2026-01-01T00:00:00Z",
	}

	body, err := buildDiscordPayload(p)
	if err != nil {
		t.Fatalf("buildDiscordPayload: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	embeds, ok := parsed["embeds"].([]interface{})
	if !ok || len(embeds) != 1 {
		t.Fatalf("expected 1 embed, got %v", parsed["embeds"])
	}

	embed := embeds[0].(map[string]interface{})
	if embed["title"] != "Gamarr: Test Game" {
		t.Errorf("embed title=%v, want 'Gamarr: Test Game'", embed["title"])
	}
	if embed["description"] != "Download finished" {
		t.Errorf("embed description=%v", embed["description"])
	}
	// Completed = green (0x10b981)
	if int(embed["color"].(float64)) != 0x10b981 {
		t.Errorf("embed color=%v, want green (0x10b981)", embed["color"])
	}

	fields := embed["fields"].([]interface{})
	if len(fields) < 2 {
		t.Fatalf("expected at least 2 fields (Platform, Status), got %d", len(fields))
	}
}

func TestBuildDiscordPayload_Colors(t *testing.T) {
	tests := []struct {
		status string
		color  int
	}{
		{"completed", 0x10b981},
		{"error", 0xef4444},
		{"failed", 0xef4444},
		{"approved", 0xf59e0b},
		{"downloading", 0x3b82f6},
		{"other", 0x6366f1}, // default indigo
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			body, _ := buildDiscordPayload(Payload{Status: tt.status})
			var parsed map[string]interface{}
			json.Unmarshal(body, &parsed)
			embed := parsed["embeds"].([]interface{})[0].(map[string]interface{})
			if int(embed["color"].(float64)) != tt.color {
				t.Errorf("color for status %q: got %v, want 0x%x", tt.status, embed["color"], tt.color)
			}
		})
	}
}

func TestBuildDiscordPayload_ErrorField(t *testing.T) {
	body, _ := buildDiscordPayload(Payload{
		Title:  "Game",
		Status: "error",
		Error:  "disk full",
	})
	var parsed map[string]interface{}
	json.Unmarshal(body, &parsed)
	embed := parsed["embeds"].([]interface{})[0].(map[string]interface{})
	fields := embed["fields"].([]interface{})

	found := false
	for _, f := range fields {
		field := f.(map[string]interface{})
		if field["name"] == "Error" && field["value"] == "disk full" {
			found = true
		}
	}
	if !found {
		t.Error("expected Error field in embed")
	}
}

func TestGenericPayload_Format(t *testing.T) {
	p := Payload{
		Event:   EventDownloadComplete,
		Title:   "Test Game",
		Status:  "completed",
		Message: "Done",
		Time:    "2026-01-01T00:00:00Z",
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var parsed Payload
	json.Unmarshal(body, &parsed)
	if parsed.Event != EventDownloadComplete {
		t.Errorf("event=%q", parsed.Event)
	}
	if parsed.Title != "Test Game" {
		t.Errorf("title=%q", parsed.Title)
	}
}

func TestMatchesEvent(t *testing.T) {
	tests := []struct {
		events string
		event  string
		want   bool
	}{
		{"*", EventDownloadComplete, true},
		{"", EventDownloadComplete, true},
		{"download_complete", EventDownloadComplete, true},
		{"download_complete,download_failed", EventDownloadFailed, true},
		{"download_complete, download_failed", EventDownloadFailed, true},
		{"download_complete", EventRequestCreated, false},
	}

	for _, tt := range tests {
		t.Run(tt.events+"->"+tt.event, func(t *testing.T) {
			got := matchesEvent(tt.events, tt.event)
			if got != tt.want {
				t.Errorf("matchesEvent(%q, %q) = %v, want %v", tt.events, tt.event, got, tt.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"a,b,c", 3},
		{"a, b , c", 3},
		{"single", 1},
		{"", 0},
		{" , , ", 0},
	}
	for _, tt := range tests {
		got := splitCSV(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitCSV(%q) = %v (len %d), want len %d", tt.input, got, len(got), tt.want)
		}
	}
}

func TestSend_HTTPCall(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()

	configs := []WebhookConfig{
		{Name: "test", URL: server.URL, Type: TypeGeneric, Enabled: true, Events: "*"},
	}
	payload := Payload{
		Event: EventDownloadComplete,
		Title: "Test Game",
	}

	Send(configs, payload)

	// Wait briefly for the async goroutine
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("expected webhook to be received")
	}

	var parsed Payload
	json.Unmarshal(received, &parsed)
	if parsed.Event != EventDownloadComplete {
		t.Errorf("received event=%q", parsed.Event)
	}
	if parsed.Title != "Test Game" {
		t.Errorf("received title=%q", parsed.Title)
	}
	if parsed.Time == "" {
		t.Error("expected Time to be set")
	}
}

func TestSend_DisabledWebhook(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer server.Close()

	configs := []WebhookConfig{
		{Name: "disabled", URL: server.URL, Type: TypeGeneric, Enabled: false, Events: "*"},
	}
	Send(configs, Payload{Event: "test"})

	time.Sleep(100 * time.Millisecond)
	if called {
		t.Error("disabled webhook should not be called")
	}
}

func TestSend_EventFilter(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer server.Close()

	configs := []WebhookConfig{
		{Name: "filtered", URL: server.URL, Type: TypeGeneric, Enabled: true, Events: "download_complete"},
	}
	Send(configs, Payload{Event: EventRequestCreated})

	time.Sleep(100 * time.Millisecond)
	if called {
		t.Error("webhook should not be called for non-matching event")
	}
}

func TestSend_DiscordFormat(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()

	configs := []WebhookConfig{
		{Name: "discord", URL: server.URL, Type: TypeDiscord, Enabled: true, Events: "*"},
	}
	Send(configs, Payload{Event: EventDownloadComplete, Title: "Game", Status: "completed"})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) == 0 {
		t.Fatal("expected discord webhook to be received")
	}

	var parsed map[string]interface{}
	json.Unmarshal(received, &parsed)
	if _, ok := parsed["embeds"]; !ok {
		t.Error("expected Discord payload to have 'embeds' key")
	}
}

func TestSendTest_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer server.Close()

	err := SendTest(WebhookConfig{URL: server.URL, Type: TypeGeneric})
	if err != nil {
		t.Errorf("SendTest: %v", err)
	}
}

func TestSendTest_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	err := SendTest(WebhookConfig{URL: server.URL, Type: TypeGeneric})
	if err == nil {
		t.Error("expected error for 500 status")
	}
}
