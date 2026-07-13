// Package store implements Partner Center authentication, transport, retries, and endpoint access.
package store

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRetryAfter = 5 * time.Minute

// Rand is the injectable randomness used by retry backoff.
type Rand interface {
	Float64() float64
}

// Backoff defines a capped full-jitter exponential retry schedule.
type Backoff struct {
	Base     time.Duration
	Cap      time.Duration
	Attempts int
}

// Delay returns rand(0.5, 1.0) * min(cap, base * 2^(attempt-1)).
func (b Backoff) Delay(attempt int, random Rand) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	maximum := b.Base
	for current := 1; current < attempt && maximum < b.Cap; current++ {
		if maximum > b.Cap/2 {
			maximum = b.Cap
			break
		}
		maximum *= 2
	}
	if maximum > b.Cap {
		maximum = b.Cap
	}
	factor := 0.5 + 0.5*random.Float64()
	return time.Duration(float64(maximum) * factor)
}

// RetryAfter parses seconds or an HTTP date, capped at five minutes.
func (b Backoff) RetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return minDuration(time.Duration(seconds)*time.Second, maxRetryAfter), true
	}
	when, err := time.Parse(http.TimeFormat, value)
	if err != nil || when.Before(now) {
		return 0, false
	}
	return minDuration(when.Sub(now), maxRetryAfter), true
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
