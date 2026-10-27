package download

import (
	"context"
	"sync/atomic"
	"time"
)

// downloadStallTimeout is how long a direct download may go without progress:
// first the response headers, then each read of the body. Tests shorten it.
var downloadStallTimeout = 2 * time.Minute

// stallGuard cancels a request's context once it stops making progress, so a
// slow transfer may take as long as it needs while data keeps arriving. An
// http.Client Timeout would instead cut off any file that takes longer than the
// timeout to stream, however steadily it arrives.
type stallGuard struct {
	idle    time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

// newStallGuard returns a context for one request, its guard, and a stop
// function that releases both; call stop once the response is finished with.
func newStallGuard(idle time.Duration) (context.Context, *stallGuard, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	g := &stallGuard{idle: idle}
	g.timer = time.AfterFunc(idle, func() {
		g.stalled.Store(true)
		cancel()
	})
	return ctx, g, func() {
		g.timer.Stop()
		cancel()
	}
}

// Progress restarts the idle window after headers or body bytes arrive.
func (g *stallGuard) Progress() {
	g.timer.Reset(g.idle)
}

// Stalled reports whether the guard cancelled the request.
func (g *stallGuard) Stalled() bool {
	return g.stalled.Load()
}
