package store

import (
	"net/http"
	"testing"
	"time"
)

type fixedRand float64

func (r fixedRand) Float64() float64 { return float64(r) }

func TestBackoffExponentialGrowthCapAndJitter(t *testing.T) {
	t.Parallel()
	policy := Backoff{Base: 5 * time.Second, Cap: 20 * time.Second, Attempts: 5}

	for _, test := range []struct {
		name    string
		rand    fixedRand
		attempt int
		want    time.Duration
	}{
		{name: "lower jitter bound", rand: 0, attempt: 1, want: 2500 * time.Millisecond},
		{name: "upper jitter bound", rand: 1, attempt: 1, want: 5 * time.Second},
		{name: "exponential", rand: 1, attempt: 2, want: 10 * time.Second},
		{name: "cap", rand: 1, attempt: 4, want: 20 * time.Second},
		{name: "cap lower jitter", rand: 0, attempt: 5, want: 10 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Delay(test.attempt, test.rand); got != test.want {
				t.Fatalf("Delay(%d) = %s, want %s", test.attempt, got, test.want)
			}
		})
	}
}

func TestBackoffRetryAfterOverridesAndCaps(t *testing.T) {
	t.Parallel()
	policy := Backoff{Base: time.Second, Cap: time.Minute, Attempts: 2}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		header string
		want   time.Duration
	}{
		{header: "17", want: 17 * time.Second},
		{header: "999", want: 5 * time.Minute},
		{header: now.Add(45 * time.Second).Format(http.TimeFormat), want: 45 * time.Second},
	} {
		if got, ok := policy.RetryAfter(test.header, now); !ok || got != test.want {
			t.Fatalf("RetryAfter(%q) = %s, %t; want %s, true", test.header, got, ok, test.want)
		}
	}
	if _, ok := policy.RetryAfter("not-a-delay", now); ok {
		t.Fatal("invalid Retry-After accepted")
	}
}
