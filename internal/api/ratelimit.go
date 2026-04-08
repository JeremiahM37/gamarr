package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RateLimiter is an in-memory per-IP rate limiter with per-route rules.
type RateLimiter struct {
	mu        sync.Mutex
	windowSec int
	rules     map[string]int
	buckets   map[rateLimitKey]*rateLimitBucket
}

type rateLimitKey struct {
	rule     string
	identity string
}

type rateLimitBucket struct {
	timestamps []time.Time
}

// NewRateLimiter creates a rate limiter with the given window (seconds) and rules.
func NewRateLimiter(windowSec int, rules map[string]int) *RateLimiter {
	rl := &RateLimiter{
		windowSec: windowSec,
		rules:     rules,
		buckets:   make(map[rateLimitKey]*rateLimitBucket),
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

// cleanup removes expired buckets.
func (rl *RateLimiter) cleanup() {
	now := time.Now()
	cutoff := now.Add(-time.Duration(rl.windowSec) * time.Second)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for key, bucket := range rl.buckets {
		if len(bucket.timestamps) == 0 || bucket.timestamps[len(bucket.timestamps)-1].Before(cutoff) {
			delete(rl.buckets, key)
		}
	}
}

func (rl *RateLimiter) ruleForPath(path string) string {
	if path == "/api/login" {
		return "login"
	}
	if strings.HasPrefix(path, "/api/search") {
		return "search"
	}
	if strings.HasPrefix(path, "/api/download") {
		return "download"
	}
	if strings.HasPrefix(path, "/api/") {
		return "api"
	}
	return "default"
}

// Check returns whether a request is allowed and rate limit info.
func (rl *RateLimiter) Check(identity, path string) (allowed bool, retryAfter int, rule string, limit int) {
	now := time.Now()
	rule = rl.ruleForPath(path)
	limit = rl.rules[rule]
	if limit == 0 {
		limit = rl.rules["default"]
	}
	if limit == 0 {
		limit = 600
	}

	key := rateLimitKey{rule: rule, identity: identity}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	bucket, ok := rl.buckets[key]
	if !ok {
		bucket = &rateLimitBucket{}
		rl.buckets[key] = bucket
	}

	// Prune old entries.
	cutoff := now.Add(-time.Duration(rl.windowSec) * time.Second)
	i := 0
	for i < len(bucket.timestamps) && bucket.timestamps[i].Before(cutoff) {
		i++
	}
	bucket.timestamps = bucket.timestamps[i:]

	if len(bucket.timestamps) >= limit {
		retryAfter = rl.windowSec
		if len(bucket.timestamps) > 0 {
			retryAfter = int(time.Duration(rl.windowSec)*time.Second-now.Sub(bucket.timestamps[0])) / int(time.Second)
			if retryAfter < 1 {
				retryAfter = 1
			}
		}
		return false, retryAfter, rule, limit
	}

	bucket.timestamps = append(bucket.timestamps, now)
	return true, 0, rule, limit
}

// rateLimitMiddleware wraps an HTTP handler with rate limiting.
func rateLimitMiddleware(rl *RateLimiter, next http.Handler) http.Handler {
	if rl == nil {
		return next
	}

	exempt := map[string]bool{
		"/api/health": true,
		"/health":     true,
		"/metrics":    true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		identity := r.RemoteAddr
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			identity = strings.TrimSpace(parts[len(parts)-1])
		}

		allowed, retryAfter, rule, limit := rl.Check(identity, r.URL.Path)
		if !allowed {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
				"error":       "Rate limit exceeded",
				"rule":        rule,
				"limit":       limit,
				"retry_after": retryAfter,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
