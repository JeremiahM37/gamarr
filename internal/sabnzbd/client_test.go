package sabnzbd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	c := New("http://localhost:8080", "apikey123")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("baseURL=%q", c.baseURL)
	}
	if c.apiKey != "apikey123" {
		t.Errorf("apiKey=%q", c.apiKey)
	}
}

func TestNew_TrailingSlash(t *testing.T) {
	c := New("http://localhost:8080/", "key")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected trailing slash stripped, got %q", c.baseURL)
	}
}

func TestAddNZBByURL_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "addurl" {
			t.Errorf("expected mode=addurl, got %q", r.URL.Query().Get("mode"))
		}
		if r.URL.Query().Get("apikey") != "testkey" {
			t.Errorf("expected apikey=testkey")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  true,
			"nzo_ids": []string{"SABnzbd_nzo_abc123"},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey")
	nzoID, err := c.AddNZBByURL("http://example.com/nzb", "Test Game", "games")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nzoID != "SABnzbd_nzo_abc123" {
		t.Errorf("nzoID=%q", nzoID)
	}
}

func TestAddNZBByURL_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": false,
			"error":  "Invalid NZB",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey")
	_, err := c.AddNZBByURL("http://bad.com/nzb", "Bad", "games")
	if err == nil {
		t.Error("expected error for failed status")
	}
}

func TestGetQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "queue" {
			t.Errorf("expected mode=queue")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"queue": map[string]interface{}{
				"slots": []map[string]interface{}{
					{"nzo_id": "nzo_1", "filename": "Game.nzb", "status": "Downloading", "mb": 5000.0, "mbleft": 2500.0},
				},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	slots, err := c.GetQueue()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].NZOID != "nzo_1" {
		t.Errorf("nzo_id=%q", slots[0].NZOID)
	}
}

func TestGetHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "history" {
			t.Errorf("expected mode=history")
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"history": map[string]interface{}{
				"slots": []map[string]interface{}{
					{"nzo_id": "nzo_done", "status": "Completed", "storage": "/data/game"},
				},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	slots, err := c.GetHistory(50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].Status != "Completed" {
		t.Errorf("status=%q", slots[0].Status)
	}
}

func TestTestConnection_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "version" {
			t.Errorf("expected mode=version")
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"version": "4.0.0"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	err := c.TestConnection()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestTestConnection_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey")
	err := c.TestConnection()
	if err == nil {
		t.Error("expected error for 401 response")
	}
}

func TestDeleteHistoryItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "history" || r.URL.Query().Get("name") != "delete" {
			t.Error("expected history delete params")
		}
		if r.URL.Query().Get("value") != "nzo_test" {
			t.Errorf("expected value=nzo_test, got %q", r.URL.Query().Get("value"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	err := c.DeleteHistoryItem("nzo_test")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
