package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHTTPDrainWaitsForActiveHandler(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	handler := &drainingHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		once.Do(func() { close(entered); <-release; close(finished) })
	})}
	served := make(chan struct{})
	go func() { handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); close(served) }()
	<-entered
	drained := make(chan struct{})
	go func() { handler.Drain(); close(drained) }()
	select {
	case <-drained:
		t.Error("HTTP drain returned while handler still uses Minerva")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-served
	<-drained
	select {
	case <-finished:
	default:
		t.Fatal("handler was not finished")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("handler admitted after drain: status=%d", rr.Code)
	}
	handler.Drain()
}
