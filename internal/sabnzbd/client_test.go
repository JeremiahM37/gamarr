package sabnzbd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	nzbBody := []byte(`<?xml version="1.0"?><nzb></nzb>`)
	var sawAddfile bool
	var gotFilename string
	mux := http.NewServeMux()
	// NZB source (Prowlarr/indexer)
	mux.HandleFunc("/grab", func(w http.ResponseWriter, r *http.Request) {
		w.Write(nzbBody)
	})
	// SABnzbd API
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST addfile, got %s", r.Method)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if r.FormValue("mode") != "addfile" {
			t.Errorf("mode=%q, want addfile", r.FormValue("mode"))
		}
		if r.FormValue("apikey") != "testkey" {
			t.Errorf("apikey=%q", r.FormValue("apikey"))
		}
		file, hdr, err := r.FormFile("name")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		defer file.Close()
		gotFilename = hdr.Filename
		data, _ := io.ReadAll(file)
		if !bytes.Equal(data, nzbBody) {
			t.Errorf("nzb payload mismatch")
		}
		sawAddfile = true
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  true,
			"nzo_ids": []string{"SABnzbd_nzo_abc123"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "testkey")
	nzoID, err := c.AddNZBByURL(srv.URL+"/grab", "Test Game", "games")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawAddfile {
		t.Fatal("expected addfile upload to SABnzbd")
	}
	if nzoID != "SABnzbd_nzo_abc123" {
		t.Errorf("nzoID=%q", nzoID)
	}
	if gotFilename != "Test Game.nzb" {
		t.Errorf("filename=%q", gotFilename)
	}
}

func TestAddNZBByURL_FetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("missing"))
	}))
	defer srv.Close()

	secretURL := srv.URL + "/missing.nzb?apikey=supersecret"
	c := New(srv.URL, "testkey")
	_, err := c.AddNZBByURL(secretURL, "Bad", "games")
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("fetch error leaked URL secret: %v", err)
	}
}

func TestAddNZBByURL_Error(t *testing.T) {
	nzbBody := []byte(`<?xml version="1.0"?><nzb></nzb>`)
	mux := http.NewServeMux()
	mux.HandleFunc("/grab", func(w http.ResponseWriter, r *http.Request) { w.Write(nzbBody) })
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": false,
			"error":  "Invalid NZB",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "testkey")
	_, err := c.AddNZBByURL(srv.URL+"/grab", "Bad", "games")
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
