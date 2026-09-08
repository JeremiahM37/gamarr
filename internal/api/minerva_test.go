package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/minerva"
	"gamarr/internal/models"
	"gamarr/internal/search"
	"gamarr/internal/sources"
)

func minervaRequest(t *testing.T, env *testEnv) string {
	t.Helper()
	rr := env.do("POST", "/api/requests", `{"title":"Pokemon - HeartGold (USA).nds","platform":"DS","platform_slug":"nds"}`)
	wantStatus(t, rr, 201)
	created := decodeMap(t, rr)["request"].(map[string]interface{})
	return created["id"].(string)
}

func selectiveBody() map[string]interface{} {
	return map[string]interface{}{
		"title": "Pokemon - HeartGold (USA).nds", "platform": "DS", "platform_slug": "nds",
		"download_url": "https://minerva.invalid/nds.torrent", "info_hash": "0123456789012345678901234567890123456789",
		"torrent_file_index": 7, "torrent_file_path": "Nintendo - DS/Pokemon - HeartGold (USA).nds", "torrent_file_size": 134217728,
		"source_type": "torrent", "download_protocol": "torrent",
	}
}

func TestSelectiveDownloadRouting(t *testing.T) {
	for _, endpoint := range []string{"download", "request"} {
		for _, tc := range []struct {
			name   string
			change func(map[string]interface{})
			want   string
		}{
			{"requires qbit", func(b map[string]interface{}) {}, "Minerva requires qBittorrent"},
			{"index zero", func(b map[string]interface{}) { b["torrent_file_index"] = 0 }, "Minerva requires qBittorrent"},
			{"before ddl", func(b map[string]interface{}) { b["source_type"] = "ddl"; delete(b, "download_url") }, "Selective torrent requires"},
			{"before nzb", func(b map[string]interface{}) { b["download_protocol"] = "nzb" }, "Minerva requires qBittorrent"},
			{"missing url with magnet", func(b map[string]interface{}) {
				delete(b, "download_url")
				b["magnet_url"] = "magnet:?xt=urn:btih:0123456789012345678901234567890123456789"
			}, "Selective torrent requires"},
			{"missing hash", func(b map[string]interface{}) { delete(b, "info_hash") }, "Selective torrent requires"},
			{"missing path", func(b map[string]interface{}) { delete(b, "torrent_file_path") }, "Selective torrent requires"},
			{"blank path", func(b map[string]interface{}) { b["torrent_file_path"] = " \t" }, "Selective torrent requires"},
			{"negative index", func(b map[string]interface{}) { b["torrent_file_index"] = -1 }, "Selective torrent requires"},
			{"negative size", func(b map[string]interface{}) { b["torrent_file_size"] = -1 }, "Selective torrent requires"},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				env := newTestEnv(t, nil)
				path, id := "/api/download", ""
				if endpoint == "request" {
					id = minervaRequest(t, env)
					path = "/api/requests/" + id + "/download"
				}
				body := selectiveBody()
				tc.change(body)
				encoded, _ := json.Marshal(body)
				rr := env.do("POST", path, string(encoded))
				wantStatus(t, rr, 400)
				if !strings.Contains(rr.Body.String(), tc.want) {
					t.Fatalf("body = %s, want %q", rr.Body.String(), tc.want)
				}
				if len(env.jobs.Items()) != 0 {
					t.Fatalf("rejected selection created a generic job: %+v", env.jobs.Items())
				}
				if id != "" {
					req, err := env.jobs.GetRequest(id)
					if err != nil || req.Status != "pending" {
						t.Fatalf("rejected selection changed request: %+v, %v", req, err)
					}
				}
			})
		}
	}
}

func TestSelectiveDownloadPersistsSelection(t *testing.T) {
	for _, endpoint := range []string{"download", "request"} {
		t.Run(endpoint, func(t *testing.T) {
			qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Stop the real worker at the external boundary after its input is persisted.
				if r.URL.Path == "/api/v2/auth/login" {
					_, _ = w.Write([]byte("Ok."))
					return
				}
				http.Error(w, "client unavailable", http.StatusServiceUnavailable)
			}))
			defer qb.Close()
			env := newTestEnv(t, func(c *config.Config) { c.QBURL = qb.URL })
			body := selectiveBody()
			path, id := "/api/download", ""
			if endpoint == "request" {
				id = minervaRequest(t, env)
				path = "/api/requests/" + id + "/download"
				delete(body, "title")
				delete(body, "platform")
				delete(body, "platform_slug")
			}
			encoded, _ := json.Marshal(body)
			rr := env.do("POST", path, string(encoded))
			wantStatus(t, rr, 200)
			jobID := decodeMap(t, rr)["job_id"].(string)
			job, ok := env.jobs.Get(jobID)
			if !ok {
				t.Fatal("no persisted job")
			}
			for key, want := range map[string]interface{}{
				"source": "minerva", "download_url": "https://minerva.invalid/nds.torrent", "info_hash": "0123456789012345678901234567890123456789",
				"torrent_file_path": "Nintendo - DS/Pokemon - HeartGold (USA).nds", "title": "Pokemon - HeartGold (USA).nds", "platform": "DS", "platform_slug": "nds",
			} {
				if job[key] != want {
					t.Errorf("job[%s] = %v, want %v", key, job[key], want)
				}
			}
			// The job store's public JSON preserves the numeric selection fields.
			encoded, _ = json.Marshal(job)
			var saved map[string]interface{}
			_ = json.Unmarshal(encoded, &saved)
			if saved["torrent_file_index"] != float64(7) || saved["torrent_file_size"] != float64(134217728) {
				t.Fatalf("selection = %v", saved)
			}
			deadline := time.Now().Add(7 * time.Second)
			for {
				job, _ = env.jobs.Get(jobID)
				done := job["status"] == "error"
				if id != "" {
					req, _ := env.jobs.GetRequest(id)
					done = done && req.Status == "failed"
				}
				if done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker/request did not finish after client failure")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestMinervaSearchFanouts(t *testing.T) {
	search.SetVimmMinIntervalForTest(0)
	t.Cleanup(func() { search.SetVimmMinIntervalForTest(5 * time.Second) })
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		if !enabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			search.ResetCircuit("minerva")
			search.ResetCircuit("vimm")
			var networkCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/minerva") {
					networkCalls.Add(1)
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer upstream.Close()
			env := newTestEnv(t, func(c *config.Config) {
				c.Sources = &sources.Registry{Vimm: sources.VimmSpec{BaseURL: upstream.URL}, Minerva: sources.MinervaSpec{Enabled: enabled, AssetsURL: upstream.URL + "/minerva"}}
			})
			var svc *minerva.Service
			if enabled {
				var err error
				svc, err = minerva.Open(env.cfg.DataDir, env.cfg.Sources.Minerva)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = svc.Close() })
				idx, err := minerva.OpenIndex(filepath.Join(env.cfg.DataDir, "minerva", "index.db"))
				if err != nil {
					t.Fatal(err)
				}
				err = idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{PlatformSlug: "nds", BrowsePath: "NDS/", BundleVersion: "v1", TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: "0123456789012345678901234567890123456789", ContentSHA256: "fixture"}, []minerva.FileMeta{{Index: 7, Name: "Pokemon - HeartGold (USA).nds", Path: "Nintendo - DS/Pokemon - HeartGold (USA).nds", Size: 134217728}})
				_ = idx.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			env.router = NewRouter(env.cfg, env.mgr, nil, nil, nil, svc)
			id := minervaRequest(t, env)
			for _, route := range []struct{ name, method, path string }{
				{"search", "GET", "/api/search?q=heartgold&platform=nds"},
				{"request", "POST", "/api/requests/" + id + "/search"},
				{"torznab", "GET", "/torznab/api?t=search&q=heartgold&platform=nds"},
			} {
				t.Run(route.name, func(t *testing.T) {
					before := search.GetSourceHealth("minerva")
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						rr := httptest.NewRecorder()
						env.router.ServeHTTP(rr, httptest.NewRequest(route.method, route.path, nil))
						done <- rr
					}()
					var rr *httptest.ResponseRecorder
					select {
					case rr = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("fan-out did not finish")
					}
					wantStatus(t, rr, 200)
					count := 0
					if route.name == "torznab" {
						var feed struct {
							Items []struct {
								Title     string `xml:"title"`
								Size      int64  `xml:"size"`
								GUID      string `xml:"guid"`
								Link      string `xml:"link"`
								Enclosure *struct {
									URL string `xml:"url,attr"`
								} `xml:"enclosure"`
								Attrs []struct {
									Name  string `xml:"name,attr"`
									Value string `xml:"value,attr"`
								} `xml:"attr"`
							} `xml:"channel>item"`
						}
						if err := xml.Unmarshal(rr.Body.Bytes(), &feed); err != nil {
							t.Fatal(err)
						}
						count = len(feed.Items)
						if enabled && count == 1 {
							item := feed.Items[0]
							if item.Size != 134217728 || item.Link != "" || item.Enclosure != nil || item.GUID != "minerva:0123456789012345678901234567890123456789:7" {
								t.Fatalf("item = %+v", item)
							}
							for _, forbidden := range []string{"https://minerva.invalid/nds.torrent", "magnet:", "<link", "<enclosure"} {
								if strings.Contains(rr.Body.String(), forbidden) {
									t.Errorf("Torznab selection exposes %q: %s", forbidden, rr.Body.String())
								}
							}
							attrs := map[string]string{}
							for _, attr := range item.Attrs {
								attrs[attr.Name] = attr.Value
							}
							for key, want := range map[string]string{"torrent_file_index": "7", "torrent_file_path": "Nintendo - DS/Pokemon - HeartGold (USA).nds", "torrent_file_size": "134217728", "infohash": "0123456789012345678901234567890123456789"} {
								if attrs[key] != want {
									t.Errorf("attr %s = %q, want %q", key, attrs[key], want)
								}
							}
						}
					} else {
						var body struct {
							Results []*models.SearchResult `json:"results"`
						}
						if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
							t.Fatal(err)
						}
						count = len(body.Results)
						if enabled && count == 1 {
							hit := body.Results[0]
							if hit.Indexer != "Minerva" || hit.SourceType != "torrent" || hit.Seeders != 0 || hit.SafetyScore != 95 || hit.Size != 134217728 || hit.TorrentFileIndex == nil || *hit.TorrentFileIndex != 7 || hit.TorrentFilePath != "Nintendo - DS/Pokemon - HeartGold (USA).nds" || hit.TorrentFileSize != 134217728 {
								t.Fatalf("Minerva hit = %+v", hit)
							}
						}
					}
					wantCount := 0
					if enabled {
						wantCount = 1
					}
					if count != wantCount {
						t.Fatalf("result count = %d, want %d; body=%s", count, wantCount, rr.Body.String())
					}
					after := search.GetSourceHealth("minerva")
					beforeOK, beforeFail := 0, 0
					if before != nil {
						beforeOK, beforeFail = before.SearchOK, before.SearchFail
					}
					afterOK, afterFail := 0, 0
					if after != nil {
						afterOK, afterFail = after.SearchOK, after.SearchFail
					}
					if afterOK != beforeOK+wantCount || afterFail != beforeFail {
						t.Fatalf("unexpected Minerva query health: before=%+v after=%+v", before, after)
					}
				})
			}
			if networkCalls.Load() != 0 {
				t.Fatalf("search fetched Minerva metadata %d times", networkCalls.Load())
			}
			if !enabled {
				if _, err := os.Stat(filepath.Join(env.cfg.DataDir, "minerva")); !os.IsNotExist(err) {
					t.Fatalf("disabled source created an index: %v", err)
				}
			}
		})
	}
}

func TestDownloadWithoutSelectionKeepsGenericRouting(t *testing.T) {
	env := newTestEnv(t, nil)
	body := selectiveBody()
	delete(body, "torrent_file_index")
	encoded, _ := json.Marshal(body)
	rr := env.do("POST", "/api/download", string(encoded))
	wantStatus(t, rr, 200)
	job, ok := env.jobs.Get(decodeMap(t, rr)["job_id"].(string))
	if !ok || job["source"] == "minerva" || job["torrent_file_index"] != nil {
		t.Fatalf("legacy generic job = %+v", job)
	}
}
