package main

import (
	"net/http"
	"sync"
)

// drainingHandler joins handlers even if Server.Shutdown reaches its deadline.
// Closing their connections alone does not stop a handler's local index queries.
type drainingHandler struct {
	next    http.Handler
	mu      sync.RWMutex
	closing bool
}

func (h *drainingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closing {
		http.Error(w, "Server shutting down", http.StatusServiceUnavailable)
		return
	}
	h.next.ServeHTTP(w, r)
}
func (h *drainingHandler) Drain() {
	h.mu.Lock()
	h.closing = true
	h.mu.Unlock()
}
