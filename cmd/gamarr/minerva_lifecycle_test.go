package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/minerva"
	"gamarr/internal/scheduler"
	"gamarr/internal/sources"
)

func seedMinervaLifecycle(t *testing.T, cfg *config.Config) {
	t.Helper()
	svc, err := minerva.Open(cfg.DataDir, cfg.Sources.Minerva)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	idx, err := minerva.OpenIndex(filepath.Join(cfg.DataDir, "minerva", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.ReplaceCollection(context.Background(), minerva.CollectionRecord{PlatformSlug: "nds", BrowsePath: "NDS/", BundleVersion: "v1.0", TorrentURL: "https://minerva.invalid/nds.torrent", InfoHash: strings.Repeat("a", 40), ContentSHA256: "fixture"}, []minerva.FileMeta{{Index: 0, Name: "HeartGold.nds", Path: "HeartGold.nds", Size: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetState(context.Background(), "assets_etag", `"previous"`); err != nil {
		t.Fatal(err)
	}
}

func TestMinervaLifecycleDisabled(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer upstream.Close()
	cfg := &config.Config{DataDir: t.TempDir(), Sources: &sources.Registry{Minerva: sources.MinervaSpec{AssetsURL: upstream.URL, BaseURL: upstream.URL}}}
	svc, stop, err := startMinerva(context.Background(), cfg, func(time.Duration) (<-chan time.Time, func()) {
		t.Fatal("disabled Minerva created ticker")
		return nil, func() {}
	})
	if err != nil || svc != nil {
		t.Fatalf("disabled service=%v error=%v", svc, err)
	}
	sched := scheduler.New(cfg, nil, newSchedulerSearch(cfg, svc), nil, nil)
	sched.Start()
	sched.Stop()
	stop()
	if calls.Load() != 0 {
		t.Fatalf("disabled HTTP calls=%d", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "minerva")); !os.IsNotExist(err) {
		t.Fatalf("disabled index exists: %v", err)
	}
}

func TestMinervaLifecycleInitialSyncIsNonblockingAndCancelled(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(cancelled) }))
	defer upstream.Close()
	cfg := &config.Config{DataDir: t.TempDir(), Sources: &sources.Registry{Minerva: sources.MinervaSpec{Enabled: true, AssetsURL: upstream.URL}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tickerStopped := make(chan struct{})
	svc, stop, err := startMinerva(ctx, cfg, func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() { close(tickerStopped) }
	})
	if err != nil {
		t.Fatal(err)
	}
	if svc == nil {
		t.Fatal("enabled startup did not open Minerva")
	}
	defer stop()
	if !svc.Status(ctx).Syncing {
		t.Fatal("initial sync was not claimed before startup returned")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("initial sync did not fetch metadata")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("process cancellation did not cancel sync")
	}
	select {
	case <-tickerStopped:
	case <-time.After(3 * time.Second):
		t.Fatal("process cancellation did not stop ticker")
	}
	stop()
	if _, err := svc.Search(context.Background(), "HeartGold", "nds", 20); err == nil {
		t.Fatal("cleanup did not close index")
	}
}

func TestMinervaLifecyclePeriodicIncrementalSync(t *testing.T) {
	for _, tc := range []struct {
		hours int
		want  time.Duration
	}{{7, 7 * time.Hour}, {24, 24 * time.Hour}, {0, 24 * time.Hour}, {-1, 24 * time.Hour}} {
		t.Run(strconv.Itoa(tc.hours)+"hours", func(t *testing.T) {
			var calls atomic.Int32
			requests := make(chan string, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				requests <- r.Header.Get("If-None-Match")
				<-r.Context().Done()
			}))
			defer upstream.Close()
			cfg := &config.Config{DataDir: t.TempDir(), Sources: &sources.Registry{Minerva: sources.MinervaSpec{Enabled: true, AssetsURL: upstream.URL, SyncIntervalHours: tc.hours}}}
			seedMinervaLifecycle(t, cfg)
			ticks := make(chan time.Time)
			var tickers, stopped atomic.Int32
			svc, stop, err := startMinerva(context.Background(), cfg, func(d time.Duration) (<-chan time.Time, func()) {
				if d != tc.want {
					t.Errorf("interval=%s, want %s", d, tc.want)
				}
				tickers.Add(1)
				return ticks, func() { stopped.Add(1) }
			})
			if err != nil {
				t.Fatal(err)
			}
			if svc == nil {
				t.Fatal("enabled startup did not open Minerva")
			}
			defer stop()
			if !svc.Ready(context.Background()) || svc.Status(context.Background()).Syncing {
				t.Fatal("ready index started initial sync")
			}
			if calls.Load() != 0 {
				t.Fatal("ready startup fetched remote metadata")
			}
			ticks <- time.Now()
			select {
			case validator := <-requests:
				if validator != `"previous"` {
					t.Fatalf("periodic sync forced rebuild: validator=%q", validator)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("tick did not start sync")
			}
			ticks <- time.Now() // active sync must reject another tick without another HTTP call.
			stop()
			if calls.Load() != 1 || tickers.Load() != 1 || stopped.Load() != 1 {
				t.Fatalf("calls=%d tickers=%d stopped=%d", calls.Load(), tickers.Load(), stopped.Load())
			}
		})
	}
}

type minervaLogHandler struct{ records chan slog.Record }

func (h minervaLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h minervaLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.records <- r.Clone()
	return nil
}
func (h minervaLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h minervaLogHandler) WithGroup(string) slog.Handler      { return h }

func TestMinervaLifecycleReportsPeriodicStartError(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Sources: &sources.Registry{Minerva: sources.MinervaSpec{Enabled: true}}}
	seedMinervaLifecycle(t, cfg)
	records := make(chan slog.Record, 4)
	old := slog.Default()
	slog.SetDefault(slog.New(minervaLogHandler{records}))
	defer slog.SetDefault(old)
	ticks := make(chan time.Time)
	svc, stop, err := startMinerva(context.Background(), cfg, func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} })
	if err != nil {
		t.Fatal(err)
	}
	if svc == nil {
		t.Fatal("enabled startup did not open Minerva")
	}
	defer stop()
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		ticks <- time.Now()
		select {
		case record := <-records:
			if !strings.Contains(record.Message, "Minerva") || record.Level < slog.LevelWarn {
				t.Fatalf("unexpected start error log: %+v", record)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("non-busy sync start error was silently ignored")
		}
	}
}

func TestMinervaLifecycleOpenError(t *testing.T) {
	// A regular file cannot contain the Minerva index directory.
	file, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	cfg := &config.Config{DataDir: file.Name(), Sources: &sources.Registry{Minerva: sources.MinervaSpec{Enabled: true}}}
	svc, _, err := startMinerva(context.Background(), cfg, func(time.Duration) (<-chan time.Time, func()) {
		t.Fatal("failed open created ticker")
		return nil, func() {}
	})
	if err == nil || svc != nil {
		t.Fatalf("service=%v error=%v", svc, err)
	}
}
