package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gamarr/internal/config"
)

// indexerJSON builds a Prowlarr indexer definition with arbitrary capabilities,
// so a test can describe a books tracker, a disabled one, or a games tracker
// that only declares its categories as subcategories.
func indexerJSON(id int, name string, enabled bool, cats ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id": id, "name": name, "enable": enabled,
		"capabilities": map[string]interface{}{"categories": cats},
	}
}

func cat(id int, subs ...map[string]interface{}) map[string]interface{} {
	c := map[string]interface{}{"id": id}
	if len(subs) > 0 {
		c["subCategories"] = subs
	}
	return c
}

// indexerServer serves GET /api/v1/indexer and counts how often it is asked.
func indexerServer(t *testing.T, indexers []map[string]interface{}) (*config.Config, *int32) {
	t.Helper()
	ClearIndexerCache()
	t.Cleanup(ClearIndexerCache)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(indexers)
	}))
	t.Cleanup(srv.Close)

	return &config.Config{ProwlarrURL: srv.URL, ProwlarrAPIKey: "key"}, &calls
}

func ids(indexers []Indexer) []int {
	out := make([]int, 0, len(indexers))
	for _, i := range indexers {
		out = append(out, i.ID)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The reported instance: a games tracker at a high ID, surrounded by trackers
// that carry no games. Only the games one should be queried, whatever its ID.
func TestGameIndexersDiscoversByCapability(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(3, "BooksOnly", true, cat(7000), cat(7020)),
		indexerJSON(9, "NoCategories", true),
		indexerJSON(11, "ConsoleTracker", true, cat(1000)),
		indexerJSON(16, "GamesTracker", true, cat(4000), cat(4050)),
	})

	got := ids(GameIndexers(cfg))
	if !equalInts(got, []int{11, 16}) {
		t.Errorf("discovered %v, want the two game trackers [11 16]", got)
	}
}

// Trackers commonly declare the useful categories one level down.
func TestGameIndexersFindsGamesInSubcategories(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(1, "Nested", true, cat(8000, cat(8010)), cat(4000, cat(4050))),
	})

	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{1}) {
		t.Errorf("discovered %v, want [1]", got)
	}
}

func TestGameIndexersSkipsDisabledIndexers(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(1, "DisabledGames", false, cat(4000)),
		indexerJSON(2, "EnabledGames", true, cat(4000)),
	})

	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{2}) {
		t.Errorf("discovered %v, want only the enabled tracker [2]", got)
	}
}

// Searching nothing and reporting "no releases" is the failure this replaces,
// so an instance where nothing advertises games searches everything enabled.
func TestGameIndexersFallsBackWhenNothingAdvertisesGames(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(1, "GeneralTracker", true, cat(8000)),
		indexerJSON(2, "AnotherGeneral", true),
		indexerJSON(3, "Disabled", false),
	})

	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{1, 2}) {
		t.Errorf("discovered %v, want every enabled indexer [1 2]", got)
	}
}

func TestGameIndexersReturnsNamesForReporting(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(4, "GamesTracker", true, cat(4000)),
	})

	got := GameIndexers(cfg)
	if len(got) != 1 || got[0].Name != "GamesTracker" {
		t.Errorf("got %+v, want the indexer name carried through", got)
	}
}

// An explicit list still wins — it is how a user narrows the set.
func TestGameIndexersHonorsConfiguredOverride(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(1, "Fast", true, cat(4000)),
		indexerJSON(2, "Slow", true, cat(4000)),
	})
	cfg.ProwlarrGameIndexers = []int{2}

	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{2}) {
		t.Errorf("got %v, want the configured [2]", got)
	}
}

// The silent failure from the report: an ID the instance does not have is
// dropped rather than queried into an empty HTTP 200.
func TestGameIndexersDropsConfiguredIDsTheInstanceLacks(t *testing.T) {
	cfg, _ := indexerServer(t, []map[string]interface{}{
		indexerJSON(5, "RealTracker", true, cat(4000)),
		indexerJSON(6, "DisabledTracker", false, cat(4000)),
	})
	cfg.ProwlarrGameIndexers = []int{5, 6, 15, 99}

	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{5}) {
		t.Errorf("got %v, want only the existing enabled indexer [5]", got)
	}
}

// If Prowlarr cannot be reached, an explicit list is still usable; discovery
// has nothing to go on and says so rather than inventing IDs.
func TestGameIndexersWhenProwlarrIsUnreachable(t *testing.T) {
	ClearIndexerCache()
	t.Cleanup(ClearIndexerCache)
	cfg := &config.Config{ProwlarrURL: "http://127.0.0.1:1", ProwlarrAPIKey: "key"}

	if got := GameIndexers(cfg); len(got) != 0 {
		t.Errorf("got %v, want none without a configured list", got)
	}

	ClearIndexerCache()
	cfg.ProwlarrGameIndexers = []int{7, 8}
	if got := ids(GameIndexers(cfg)); !equalInts(got, []int{7, 8}) {
		t.Errorf("got %v, want the configured list [7 8]", got)
	}
}

func TestGameIndexersNotConfigured(t *testing.T) {
	if got := GameIndexers(&config.Config{}); got != nil {
		t.Errorf("got %v, want nil when Prowlarr is not configured", got)
	}
}

// Every search would otherwise re-read the indexer list.
func TestGameIndexersCachesTheIndexerList(t *testing.T) {
	cfg, calls := indexerServer(t, []map[string]interface{}{
		indexerJSON(1, "GamesTracker", true, cat(4000)),
	})

	for i := 0; i < 3; i++ {
		GameIndexers(cfg)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("asked Prowlarr %d times, want 1", got)
	}

	ClearIndexerCache()
	GameIndexers(cfg)
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("asked Prowlarr %d times after a clear, want 2", got)
	}
}
