package api

import (
	"fmt"
	"strings"
	"testing"

	"gamarr/internal/db"
	"gamarr/internal/models"
)

// ── Wishlist CRUD ──────────────────────────────────────────────────────────────

func TestWishlistCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	// Empty list.
	rr := env.do("GET", "/api/wishlist", "")
	wantStatus(t, rr, 200)
	if items, _ := decodeMap(t, rr)["items"].([]interface{}); len(items) != 0 {
		t.Fatalf("expected empty wishlist, got %d items", len(items))
	}

	// Add.
	rr = env.do("POST", "/api/wishlist", `{"title":"Chrono Trigger","platform":"SNES","platform_slug":"snes"}`)
	wantStatus(t, rr, 200)
	added := decodeMap(t, rr)
	if added["success"] != true {
		t.Fatalf("add failed: %v", added)
	}
	id := int64(added["id"].(float64))

	// Missing title.
	rr = env.do("POST", "/api/wishlist", `{"platform":"SNES"}`)
	wantStatus(t, rr, 400)

	// List shows it.
	rr = env.do("GET", "/api/wishlist", "")
	wantStatus(t, rr, 200)
	items, _ := decodeMap(t, rr)["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 wishlist item, got %d", len(items))
	}
	item, _ := items[0].(map[string]interface{})
	if item["title"] != "Chrono Trigger" || item["platform_slug"] != "snes" {
		t.Errorf("unexpected item: %v", item)
	}

	// Delete through the router (numeric id).
	rr = env.do("DELETE", fmt.Sprintf("/api/wishlist/%d", id), "")
	wantStatus(t, rr, 200)

	// Non-numeric id → 400.
	rr = env.do("DELETE", "/api/wishlist/garbage", "")
	wantStatus(t, rr, 400)

	// Gone.
	rr = env.do("GET", "/api/wishlist", "")
	if items, _ := decodeMap(t, rr)["items"].([]interface{}); len(items) != 0 {
		t.Fatalf("expected empty wishlist after delete, got %d items", len(items))
	}
}

func TestWishlistBulkDelete(t *testing.T) {
	env := newTestEnv(t, nil)

	var ids []int64
	for _, title := range []string{"Game A", "Game B", "Game C"} {
		id, err := env.jobs.AddWishlistItem(title, "NES", "nes")
		if err != nil {
			t.Fatalf("seed wishlist: %v", err)
		}
		ids = append(ids, id)
	}

	t.Run("missing content type rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/wishlist/bulk-delete", "",
			withHeader("Content-Type", ""))
		// Empty body → no Content-Type header set by helper; handler wants 415.
		if rr.Code != 415 {
			t.Errorf("status = %d, want 415", rr.Code)
		}
	})

	t.Run("empty ids rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/wishlist/bulk-delete", `{"ids":[]}`)
		wantStatus(t, rr, 400)
	})

	t.Run("deletes listed ids and reports invalid ones", func(t *testing.T) {
		body := fmt.Sprintf(`{"ids":[%d,%d,-5]}`, ids[0], ids[1])
		rr := env.do("POST", "/api/wishlist/bulk-delete", body)
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["requested"] != float64(3) || m["succeeded"] != float64(2) {
			t.Errorf("requested/succeeded = %v/%v, want 3/2", m["requested"], m["succeeded"])
		}
		if left := env.jobs.GetWishlist(); len(left) != 1 || left[0].ID != ids[2] {
			t.Errorf("wishlist after bulk delete = %+v, want only id %d", left, ids[2])
		}
	})
}

// ── Library ────────────────────────────────────────────────────────────────────

func seedLibraryItem(t *testing.T, env *testEnv, title, platform, slug string) int64 {
	t.Helper()
	id, err := env.jobs.AddLibraryItem(&db.LibraryItem{
		Title:        title,
		Platform:     platform,
		PlatformSlug: slug,
		IsPC:         slug == "pc", // the "pc" library filter matches on is_pc
		Source:       "test",
		SourceType:   "manual",
	})
	if err != nil {
		t.Fatalf("seed library item: %v", err)
	}
	return id
}

func TestLibraryListAndDelete(t *testing.T) {
	env := newTestEnv(t, nil)
	id := seedLibraryItem(t, env, "Super Metroid", "SNES", "snes")
	seedLibraryItem(t, env, "Doom", "PC", "pc")

	rr := env.do("GET", "/api/library", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["total"] != float64(2) {
		t.Fatalf("total = %v, want 2 (body: %s)", m["total"], rr.Body.String())
	}

	t.Run("query filter", func(t *testing.T) {
		rr := env.do("GET", "/api/library?q=metroid", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["total"] != float64(1) {
			t.Errorf("filtered total = %v, want 1", m["total"])
		}
	})

	t.Run("platform filter", func(t *testing.T) {
		rr := env.do("GET", "/api/library?platform=pc", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["total"] != float64(1) {
			t.Errorf("platform-filtered total = %v, want 1", m["total"])
		}
	})

	t.Run("duplicate check endpoint", func(t *testing.T) {
		rr := env.do("GET", "/api/library/check?title=Super%20Metroid&platform=snes", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["exists"] != true {
			t.Errorf("exists = %v, want true", m["exists"])
		}
		rr = env.do("GET", "/api/library/check?title=Nonexistent", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["exists"] != false {
			t.Errorf("exists = %v, want false", m["exists"])
		}
		rr = env.do("GET", "/api/library/check", "")
		wantStatus(t, rr, 400)
	})

	t.Run("delete item", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/library/%d", id), "")
		wantStatus(t, rr, 200)
		rr = env.do("GET", "/api/library", "")
		if m := decodeMap(t, rr); m["total"] != float64(1) {
			t.Errorf("total after delete = %v, want 1", m["total"])
		}
	})
}

// ── Downloads (jobs) ───────────────────────────────────────────────────────────

func TestDownloadsListClearAndDelete(t *testing.T) {
	env := newTestEnv(t, nil)

	// Seed jobs directly: one finished, one failed, one active.
	env.jobs.Set("job-done", map[string]interface{}{"status": "completed", "title": "Done Game"})
	env.jobs.Set("job-err", map[string]interface{}{"status": "error", "title": "Broken Game", "error": "boom"})
	env.jobs.Set("job-live", map[string]interface{}{"status": "downloading", "title": "Live Game"})

	rr := env.do("GET", "/api/downloads", "")
	wantStatus(t, rr, 200)
	downloads, _ := decodeMap(t, rr)["downloads"].([]interface{})
	if len(downloads) != 3 {
		t.Fatalf("downloads = %d entries, want 3", len(downloads))
	}

	t.Run("clear removes finished and errored jobs", func(t *testing.T) {
		rr := env.do("POST", "/api/downloads/clear", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["cleared"] != float64(2) {
			t.Errorf("cleared = %v, want 2", m["cleared"])
		}
	})

	t.Run("delete existing job", func(t *testing.T) {
		rr := env.do("DELETE", "/api/downloads/job-live", "")
		wantStatus(t, rr, 200)
	})

	t.Run("delete missing job is 404", func(t *testing.T) {
		rr := env.do("DELETE", "/api/downloads/nope", "")
		wantStatus(t, rr, 404)
	})
}

func TestRetryJob(t *testing.T) {
	env := newTestEnv(t, nil)
	env.jobs.Set("job-err", map[string]interface{}{"status": "error", "title": "Broken"})

	t.Run("failed job requeued", func(t *testing.T) {
		rr := env.do("POST", "/api/downloads/job-err/retry", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != true {
			t.Errorf("retry failed: %v", m)
		}
		data, _ := env.jobs.Get("job-err")
		if data["status"] != "queued" {
			t.Errorf("status after retry = %v, want queued", data["status"])
		}
	})

	t.Run("missing job reports failure", func(t *testing.T) {
		rr := env.do("POST", "/api/downloads/missing/retry", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != false {
			t.Errorf("expected success=false for missing job, got %v", m)
		}
	})
}

func TestBulkRetryAndCancel(t *testing.T) {
	env := newTestEnv(t, nil)
	env.jobs.Set("f1", map[string]interface{}{"status": "error", "title": "F1"})
	env.jobs.Set("f2", map[string]interface{}{"status": "dead_letter", "title": "F2"})
	env.jobs.Set("a1", map[string]interface{}{"status": "downloading", "title": "A1"})

	t.Run("retry all failed with empty body", func(t *testing.T) {
		rr := env.do("POST", "/api/admin/bulk/retry", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["requested"] != float64(2) || m["succeeded"] != float64(2) {
			t.Errorf("bulk retry = %v, want requested=2 succeeded=2", m)
		}
	})

	t.Run("cancel explicit ids with unknown id reported", func(t *testing.T) {
		rr := env.do("POST", "/api/admin/bulk/cancel", `{"job_ids":["a1","ghost"]}`)
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["succeeded"] != float64(1) {
			t.Errorf("bulk cancel succeeded = %v, want 1", m["succeeded"])
		}
		if env.jobs.Contains("a1") {
			t.Error("a1 should be deleted after cancel")
		}
	})

	t.Run("wrong content type rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/admin/bulk/retry", `{"job_ids":[]}`,
			withHeader("Content-Type", "text/plain"))
		wantStatus(t, rr, 400)
	})
}

// ── DDL sources ────────────────────────────────────────────────────────────────

func TestDDLSourcesCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	rr := env.do("GET", "/api/ddl-sources", "")
	wantStatus(t, rr, 200)
	sources, _ := decodeMap(t, rr)["sources"].([]interface{})
	if len(sources) != 2 {
		t.Fatalf("expected 2 built-in sources, got %d", len(sources))
	}

	t.Run("add requires name and url", func(t *testing.T) {
		rr := env.do("POST", "/api/ddl-sources", `{"name":"only name"}`)
		wantStatus(t, rr, 400)
	})

	t.Run("add and delete custom source", func(t *testing.T) {
		rr := env.do("POST", "/api/ddl-sources", `{"name":"MySource","url":"http://roms.test"}`)
		wantStatus(t, rr, 200)

		rr = env.do("GET", "/api/ddl-sources", "")
		sources, _ := decodeMap(t, rr)["sources"].([]interface{})
		if len(sources) != 3 {
			t.Fatalf("expected 3 sources after add, got %d", len(sources))
		}

		// Custom sources are indexed within the custom list (index 0 here).
		rr = env.do("DELETE", "/api/ddl-sources/0", "")
		wantStatus(t, rr, 200)

		rr = env.do("GET", "/api/ddl-sources", "")
		sources, _ = decodeMap(t, rr)["sources"].([]interface{})
		if len(sources) != 2 {
			t.Errorf("expected 2 sources after delete, got %d", len(sources))
		}
	})

	t.Run("delete out of range is 404", func(t *testing.T) {
		rr := env.do("DELETE", "/api/ddl-sources/99", "")
		wantStatus(t, rr, 404)
	})
}

// ── Settings ───────────────────────────────────────────────────────────────────

func TestSettingsGetAndUpdate(t *testing.T) {
	env := newTestEnv(t, nil)

	rr := env.do("GET", "/api/settings", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["extract_archives"] != false {
		t.Errorf("extract_archives = %v, want false", m["extract_archives"])
	}

	rr = env.do("PUT", "/api/settings", `{"extract_archives":true}`)
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["extract_archives"] != true {
		t.Errorf("extract_archives after update = %v, want true", m["extract_archives"])
	}

	// Persisted to DataDir — a fresh GET re-reads from disk.
	rr = env.do("GET", "/api/settings", "")
	if m := decodeMap(t, rr); m["extract_archives"] != true {
		t.Errorf("extract_archives after reload = %v, want true", m["extract_archives"])
	}
}

// ── Import / Export ────────────────────────────────────────────────────────────

func TestLibraryExportImportRoundTrip(t *testing.T) {
	src := newTestEnv(t, nil)
	seedLibraryItem(t, src, "Ocarina of Time", "N64", "n64")
	seedLibraryItem(t, src, "Half-Life", "PC", "pc")

	rr := src.do("GET", "/api/export/library", "")
	wantStatus(t, rr, 200)
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "gamarr-library.json") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	exported := rr.Body.String()
	m := decodeMap(t, rr)
	if m["count"] != float64(2) || m["type"] != "library" {
		t.Fatalf("export wrapper = %v, want count=2 type=library", m)
	}

	// Import into a fresh instance.
	dst := newTestEnv(t, nil)
	rr = dst.do("POST", "/api/import/library", exported)
	wantStatus(t, rr, 200)
	im := decodeMap(t, rr)
	if im["imported"] != float64(2) || im["skipped"] != float64(0) {
		t.Fatalf("import = %v, want imported=2 skipped=0", im)
	}

	// Re-importing the same payload skips duplicates.
	rr = dst.do("POST", "/api/import/library", exported)
	im = decodeMap(t, rr)
	if im["imported"] != float64(0) || im["skipped"] != float64(2) {
		t.Errorf("re-import = %v, want imported=0 skipped=2", im)
	}

	t.Run("invalid JSON rejected", func(t *testing.T) {
		rr := dst.do("POST", "/api/import/library", `{broken`)
		wantStatus(t, rr, 400)
	})
}

func TestWishlistExportImportRoundTrip(t *testing.T) {
	src := newTestEnv(t, nil)
	if _, err := src.jobs.AddWishlistItem("Celeste", "PC", "pc"); err != nil {
		t.Fatal(err)
	}

	rr := src.do("GET", "/api/export/wishlist", "")
	wantStatus(t, rr, 200)
	exported := rr.Body.String()

	dst := newTestEnv(t, nil)
	rr = dst.do("POST", "/api/import/wishlist", exported)
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["imported"] != float64(1) {
		t.Errorf("imported = %v, want 1", m["imported"])
	}
	if items := dst.jobs.GetWishlist(); len(items) != 1 || items[0].Title != "Celeste" {
		t.Errorf("wishlist after import = %+v", items)
	}
}

func TestImportCSV(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("valid CSV imports rows", func(t *testing.T) {
		csvBody := "title,platform\nTetris,Game Boy\nPong,\n"
		rr := env.do("POST", "/api/import/csv", csvBody,
			withHeader("Content-Type", "text/csv"))
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["imported"] != float64(2) {
			t.Errorf("imported = %v, want 2 (body %s)", m["imported"], rr.Body.String())
		}
	})

	t.Run("missing title column rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/import/csv", "foo,bar\n1,2\n",
			withHeader("Content-Type", "text/csv"))
		wantStatus(t, rr, 400)
	})

	t.Run("header only rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/import/csv", "title\n",
			withHeader("Content-Type", "text/csv"))
		wantStatus(t, rr, 400)
	})
}

func TestExportRequests(t *testing.T) {
	env := newTestEnv(t, nil)
	rr := env.do("POST", "/api/requests", `{"title":"Portal 2","platform":"PC","platform_slug":"pc"}`)
	wantStatus(t, rr, 201)

	rr = env.do("GET", "/api/export/requests", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["count"] != float64(1) || m["type"] != "requests" {
		t.Errorf("requests export = %v, want count=1 type=requests", m)
	}
}

// ── Requests ───────────────────────────────────────────────────────────────────

func TestRequestsCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("missing title rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/requests", `{"platform":"PC"}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/requests", `{"title":"Hades","platform":"PC","platform_slug":"pc"}`)
	wantStatus(t, rr, 201)
	created, _ := decodeMap(t, rr)["request"].(map[string]interface{})
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created request missing id: %v", created)
	}
	if created["status"] != "pending" {
		t.Errorf("status = %v, want pending", created["status"])
	}

	t.Run("list", func(t *testing.T) {
		rr := env.do("GET", "/api/requests", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["total"] != float64(1) {
			t.Errorf("total = %v, want 1", m["total"])
		}
	})

	t.Run("get by id", func(t *testing.T) {
		rr := env.do("GET", "/api/requests/"+id, "")
		wantStatus(t, rr, 200)
	})

	t.Run("get unknown id is 404", func(t *testing.T) {
		rr := env.do("GET", "/api/requests/does-not-exist", "")
		wantStatus(t, rr, 404)
	})

	t.Run("delete (admin) removes it", func(t *testing.T) {
		rr := env.do("DELETE", "/api/requests/"+id, "")
		wantStatus(t, rr, 200)
		rr = env.do("GET", "/api/requests/"+id, "")
		wantStatus(t, rr, 404)
	})
}

// ── History CRUD ───────────────────────────────────────────────────────────────

func TestHistoryCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("missing game_title rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/history", `{"platform":"PC"}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/history", `{"game_title":"Zelda","platform":"NES","platform_slug":"nes","hours_played":2.5}`)
	wantStatus(t, rr, 201)
	id := int64(decodeMap(t, rr)["id"].(float64))

	t.Run("list", func(t *testing.T) {
		rr := env.do("GET", "/api/history", "")
		wantStatus(t, rr, 200)
		m := decodeMap(t, rr)
		if m["total"] != float64(1) {
			t.Fatalf("total = %v, want 1", m["total"])
		}
		entries, _ := m["entries"].([]interface{})
		first, _ := entries[0].(map[string]interface{})
		if first["game_title"] != "Zelda" {
			t.Errorf("game_title = %v, want Zelda", first["game_title"])
		}
	})

	t.Run("update", func(t *testing.T) {
		rr := env.do("PATCH", fmt.Sprintf("/api/history/%d", id), `{"rating":5,"notes":"classic"}`)
		wantStatus(t, rr, 200)
	})

	t.Run("update invalid id rejected", func(t *testing.T) {
		rr := env.do("PATCH", "/api/history/abc", `{"rating":5}`)
		wantStatus(t, rr, 400)
	})

	t.Run("stats", func(t *testing.T) {
		rr := env.do("GET", "/api/history/stats", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["success"] != true {
			t.Errorf("stats failed: %v", m)
		}
	})

	t.Run("delete", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/history/%d", id), "")
		wantStatus(t, rr, 200)
		rr = env.do("GET", "/api/history", "")
		if m := decodeMap(t, rr); m["total"] != float64(0) {
			t.Errorf("total after delete = %v, want 0", m["total"])
		}
	})
}

// ── Tags ───────────────────────────────────────────────────────────────────────

func TestTagsCRUD(t *testing.T) {
	env := newTestEnv(t, nil)
	itemID := seedLibraryItem(t, env, "Stardew Valley", "PC", "pc")

	// The tags migration seeds default tags; work relative to that baseline.
	baseline := len(env.jobs.GetTags())

	t.Run("empty name rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/tags", `{"color":"#fff"}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/tags", `{"name":"cozy","color":"#10b981"}`)
	wantStatus(t, rr, 201)
	tagID := int64(decodeMap(t, rr)["id"].(float64))

	t.Run("list tags", func(t *testing.T) {
		rr := env.do("GET", "/api/tags", "")
		wantStatus(t, rr, 200)
		tags, _ := decodeMap(t, rr)["tags"].([]interface{})
		if len(tags) != baseline+1 {
			t.Fatalf("tags = %d, want %d", len(tags), baseline+1)
		}
		found := false
		for _, tg := range tags {
			if tm, _ := tg.(map[string]interface{}); tm["name"] == "cozy" {
				found = true
			}
		}
		if !found {
			t.Error("created tag 'cozy' not in list")
		}
	})

	t.Run("attach tag to library item", func(t *testing.T) {
		rr := env.do("POST", fmt.Sprintf("/api/library/%d/tags", itemID),
			fmt.Sprintf(`{"tag_id":%d}`, tagID))
		wantStatus(t, rr, 200)
		tags, _ := decodeMap(t, rr)["tags"].([]interface{})
		if len(tags) != 1 {
			t.Fatalf("item tags = %d, want 1", len(tags))
		}
	})

	t.Run("library filter by tag", func(t *testing.T) {
		rr := env.do("GET", "/api/library?tag=cozy", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["total"] != float64(1) {
			t.Errorf("tag-filtered total = %v, want 1", m["total"])
		}
	})

	t.Run("get item tags", func(t *testing.T) {
		rr := env.do("GET", fmt.Sprintf("/api/library/%d/tags", itemID), "")
		wantStatus(t, rr, 200)
	})

	t.Run("remove tag from item", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/library/%d/tags/%d", itemID, tagID), "")
		wantStatus(t, rr, 200)
		tags, _ := decodeMap(t, rr)["tags"].([]interface{})
		if len(tags) != 0 {
			t.Errorf("item tags after remove = %d, want 0", len(tags))
		}
	})

	t.Run("delete tag", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/tags/%d", tagID), "")
		wantStatus(t, rr, 200)
	})
}

// ── Blocklist ──────────────────────────────────────────────────────────────────

func TestBlocklistCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("requires url or hash", func(t *testing.T) {
		rr := env.do("POST", "/api/blocklist", `{"title":"nope"}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/blocklist", `{"download_url":"http://bad.torrent","title":"Bad Release","reason":"fake"}`)
	wantStatus(t, rr, 201)
	id := int64(decodeMap(t, rr)["id"].(float64))

	rr = env.do("POST", "/api/blocklist", `{"info_hash":"abc123","title":"Bad Hash"}`)
	wantStatus(t, rr, 201)

	t.Run("list", func(t *testing.T) {
		rr := env.do("GET", "/api/blocklist", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["total"] != float64(2) {
			t.Fatalf("total = %v, want 2", m["total"])
		}
	})

	t.Run("delete one", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/blocklist/%d", id), "")
		wantStatus(t, rr, 200)
	})

	t.Run("clear removes the rest", func(t *testing.T) {
		rr := env.do("DELETE", "/api/blocklist/clear", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["deleted"] != float64(1) {
			t.Errorf("deleted = %v, want 1", m["deleted"])
		}
		rr = env.do("GET", "/api/blocklist", "")
		if m := decodeMap(t, rr); m["total"] != float64(0) {
			t.Errorf("total after clear = %v, want 0", m["total"])
		}
	})
}

// ── Quality profiles ───────────────────────────────────────────────────────────

func TestQualityProfilesCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	// The migration seeds default profiles; work relative to that baseline.
	baseline := len(env.jobs.GetQualityProfiles())

	// findProfile returns the listed profile with the given id, or nil.
	findProfile := func(t *testing.T, id int64) map[string]interface{} {
		t.Helper()
		rr := env.do("GET", "/api/quality-profiles", "")
		wantStatus(t, rr, 200)
		profiles, _ := decodeMap(t, rr)["profiles"].([]interface{})
		for _, p := range profiles {
			pm, _ := p.(map[string]interface{})
			if pm["id"] == float64(id) {
				return pm
			}
		}
		return nil
	}

	t.Run("missing name rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/quality-profiles", `{"source_ranking":["myrient"]}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/quality-profiles",
		`{"name":"Prefer DDL","source_ranking":["myrient","prowlarr"],"upgrade_allowed":true}`)
	wantStatus(t, rr, 201)
	id := int64(decodeMap(t, rr)["id"].(float64))

	t.Run("list includes new profile", func(t *testing.T) {
		rr := env.do("GET", "/api/quality-profiles", "")
		wantStatus(t, rr, 200)
		profiles, _ := decodeMap(t, rr)["profiles"].([]interface{})
		if len(profiles) != baseline+1 {
			t.Fatalf("profiles = %d, want %d", len(profiles), baseline+1)
		}
		p := findProfile(t, id)
		if p == nil || p["name"] != "Prefer DDL" {
			t.Errorf("created profile = %v, want name 'Prefer DDL'", p)
		}
	})

	t.Run("update", func(t *testing.T) {
		rr := env.do("PUT", fmt.Sprintf("/api/quality-profiles/%d", id),
			`{"name":"Renamed","source_ranking":["prowlarr"]}`)
		wantStatus(t, rr, 200)
		p := findProfile(t, id)
		if p == nil || p["name"] != "Renamed" {
			t.Errorf("profile after update = %v, want name Renamed", p)
		}
	})

	t.Run("delete", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/quality-profiles/%d", id), "")
		wantStatus(t, rr, 200)
		if p := findProfile(t, id); p != nil {
			t.Errorf("profile still listed after delete: %v", p)
		}
	})
}

// ── Release profiles ───────────────────────────────────────────────────────────

func TestReleaseProfilesCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	// The migration seeds default profiles; work relative to that baseline.
	baseline := len(env.jobs.GetReleaseProfiles())

	t.Run("missing name rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/release-profiles", `{"must_contain":["USA"]}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/release-profiles",
		`{"name":"No Betas","must_not_contain":["beta","proto"],"enabled":true}`)
	wantStatus(t, rr, 201)
	id := int64(decodeMap(t, rr)["id"].(float64))

	t.Run("list", func(t *testing.T) {
		rr := env.do("GET", "/api/release-profiles", "")
		wantStatus(t, rr, 200)
		profiles, _ := decodeMap(t, rr)["profiles"].([]interface{})
		if len(profiles) != baseline+1 {
			t.Fatalf("profiles = %d, want %d", len(profiles), baseline+1)
		}
	})

	t.Run("update and delete", func(t *testing.T) {
		rr := env.do("PUT", fmt.Sprintf("/api/release-profiles/%d", id),
			`{"name":"No Betas v2","must_not_contain":["beta"],"enabled":false}`)
		wantStatus(t, rr, 200)

		rr = env.do("DELETE", fmt.Sprintf("/api/release-profiles/%d", id), "")
		wantStatus(t, rr, 200)
	})
}

// ── Webhooks ───────────────────────────────────────────────────────────────────

func TestWebhooksCRUD(t *testing.T) {
	env := newTestEnv(t, nil)

	t.Run("requires name and url", func(t *testing.T) {
		rr := env.do("POST", "/api/webhooks", `{"name":"incomplete"}`)
		wantStatus(t, rr, 400)
	})

	rr := env.do("POST", "/api/webhooks",
		`{"name":"notify","url":"http://hooks.test/x","type":"generic","events":"*"}`)
	wantStatus(t, rr, 201)
	id := int64(decodeMap(t, rr)["id"].(float64))

	t.Run("list", func(t *testing.T) {
		rr := env.do("GET", "/api/webhooks", "")
		wantStatus(t, rr, 200)
		hooks, _ := decodeMap(t, rr)["webhooks"].([]interface{})
		if len(hooks) != 1 {
			t.Fatalf("webhooks = %d, want 1", len(hooks))
		}
	})

	t.Run("delete", func(t *testing.T) {
		rr := env.do("DELETE", fmt.Sprintf("/api/webhooks/%d", id), "")
		wantStatus(t, rr, 200)
	})

	t.Run("test without id or url rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/webhooks/test", `{}`)
		wantStatus(t, rr, 400)
	})
}

// ── Notifications ──────────────────────────────────────────────────────────────

func TestNotifications(t *testing.T) {
	env := newTestEnv(t, nil)
	if _, err := env.jobs.CreateNotification(&models.Notification{
		UserID: "default", Type: "download_complete", Title: "Done", Message: "Game finished",
	}); err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	rr := env.do("GET", "/api/notifications", "")
	wantStatus(t, rr, 200)
	notifs, _ := decodeMap(t, rr)["notifications"].([]interface{})
	if len(notifs) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifs))
	}
	first, _ := notifs[0].(map[string]interface{})
	id := int64(first["id"].(float64))

	t.Run("unread count", func(t *testing.T) {
		rr := env.do("GET", "/api/notifications/unread", "")
		wantStatus(t, rr, 200)
		if m := decodeMap(t, rr); m["count"] != float64(1) {
			t.Errorf("count = %v, want 1", m["count"])
		}
	})

	t.Run("mark read", func(t *testing.T) {
		rr := env.do("POST", fmt.Sprintf("/api/notifications/%d/read", id), "")
		wantStatus(t, rr, 200)
		rr = env.do("GET", "/api/notifications/unread", "")
		if m := decodeMap(t, rr); m["count"] != float64(0) {
			t.Errorf("count after read = %v, want 0", m["count"])
		}
	})

	t.Run("invalid id rejected", func(t *testing.T) {
		rr := env.do("POST", "/api/notifications/abc/read", "")
		wantStatus(t, rr, 400)
	})

	t.Run("read-all", func(t *testing.T) {
		rr := env.do("POST", "/api/notifications/read-all", "")
		wantStatus(t, rr, 200)
	})
}

// ── Calendar / scheduler / monitor (unconfigured fallbacks) ────────────────────

func TestCalendarWithoutRAWG(t *testing.T) {
	env := newTestEnv(t, nil)

	rr := env.do("GET", "/api/calendar", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["total"] != float64(0) {
		t.Errorf("total = %v, want 0 without RAWG key", m["total"])
	}
	if _, ok := m["entries"].([]interface{}); !ok {
		t.Errorf("entries should be a JSON array, got %T", m["entries"])
	}

	rr = env.do("GET", "/api/calendar/recent?days=7", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["days"] != float64(7) {
		t.Errorf("days = %v, want 7", m["days"])
	}
}

func TestSchedulerAndMonitorNil(t *testing.T) {
	env := newTestEnv(t, nil) // router built with nil scheduler + nil monitor

	rr := env.do("GET", "/api/scheduler/status", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["enabled"] != false {
		t.Errorf("scheduler enabled = %v, want false", m["enabled"])
	}

	rr = env.do("POST", "/api/scheduler/run", "")
	wantStatus(t, rr, 500)

	rr = env.do("GET", "/api/monitor/status", "")
	wantStatus(t, rr, 200)
	if m := decodeMap(t, rr); m["enabled"] != false {
		t.Errorf("monitor enabled = %v, want false", m["enabled"])
	}

	rr = env.do("POST", "/api/monitor/analyze", "")
	wantStatus(t, rr, 500)
}

func TestAdminDashboard(t *testing.T) {
	env := newTestEnv(t, nil)
	seedLibraryItem(t, env, "Portal", "PC", "pc")

	rr := env.do("GET", "/api/admin/dashboard", "")
	wantStatus(t, rr, 200)
	m := decodeMap(t, rr)
	if m["library_total"] != float64(1) {
		t.Errorf("library_total = %v, want 1", m["library_total"])
	}
	if _, ok := m["system"].(map[string]interface{}); !ok {
		t.Error("dashboard missing system section")
	}
}
