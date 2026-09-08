package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"gamarr/internal/config"
	"gamarr/internal/minerva"
)

// startMinerva owns the sync ticker and index. Call its idempotent cleanup only
// after scheduler searches and HTTP handlers have stopped using the service.
func startMinerva(ctx context.Context, cfg *config.Config, newTicker func(time.Duration) (<-chan time.Time, func())) (*minerva.Service, func(), error) {
	if cfg.Sources == nil || !cfg.Sources.Minerva.Enabled {
		return nil, func() {}, nil
	}
	svc, err := minerva.Open(cfg.DataDir, cfg.Sources.Minerva)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	startSync := func() {
		if err := svc.StartSync(ctx, false); err != nil && !errors.Is(err, minerva.ErrSyncInProgress) {
			slog.Warn("could not start Minerva sync", "error", err)
		}
	}
	if !svc.Ready(ctx) {
		startSync()
	}
	interval := time.Duration(cfg.Sources.Minerva.SyncIntervalHours) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ticks, stopTicker := newTicker(interval)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopTicker()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				if ctx.Err() != nil {
					return
				}
				startSync()
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			if err := svc.Close(); err != nil {
				slog.Warn("could not close Minerva index", "error", err)
			}
		})
	}
	return svc, stop, nil
}

func newSyncTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}
