package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

//////////////////////
// TOKEN BUCKET
//////////////////////

type TokenBucket struct {
	capacity   int
	tokens     float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func NewTokenBucket(capacity int, refillRate float64) *TokenBucket {
	return &TokenBucket{
		capacity:   capacity,
		tokens:     float64(capacity),
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()

	// Refill tokens
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > float64(tb.capacity) {
		tb.tokens = float64(tb.capacity)
	}

	tb.lastRefill = now

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}

	return false
}

//////////////////////
// RATE LIMITER (Per User)
//////////////////////

type RateLimiter struct {
	users      sync.Map // map[string]*TokenBucket
	capacity   int
	refillRate float64
}

func NewRateLimiter(capacity int, refillRate float64) *RateLimiter {
	return &RateLimiter{
		capacity:   capacity,
		refillRate: refillRate,
	}
}

func (rl *RateLimiter) getBucket(userID string) *TokenBucket {
	val, ok := rl.users.Load(userID)
	if ok {
		return val.(*TokenBucket)
	}

	bucket := NewTokenBucket(rl.capacity, rl.refillRate)
	actual, _ := rl.users.LoadOrStore(userID, bucket)
	return actual.(*TokenBucket)
}

func (rl *RateLimiter) Allow(userID string) bool {
	bucket := rl.getBucket(userID)
	return bucket.Allow()
}

//////////////////////
// HTTP SERVER
//////////////////////

func main() {
	// 5 requests per 10 seconds
	capacity := 5
	refillRate := 0.5 // 5 tokens per 10 sec = 0.5 per second

	limiter := NewRateLimiter(capacity, refillRate)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		// In real systems use:
		// - API key
		// - JWT user ID
		// - Client ID
		// For demo we use IP
		userID := r.RemoteAddr

		if !limiter.Allow(userID) {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintln(w, "429 - Too Many Requests")
			return
		}

		fmt.Fprintln(w, "Request Allowed")
	})

	fmt.Println("Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
