package main

import (
	"testing"
	"time"
)

func TestBackoffGrowsExponentiallyWithJitter(t *testing.T) {
	for attempt := 1; attempt <= 6; attempt++ {
		base := time.Duration(1<<uint(attempt)) * time.Second
		for i := 0; i < 100; i++ {
			got := backoff(attempt)
			if got < base || got >= base+base/5 {
				t.Fatalf("attempt %d: bacokff %v outside [%v,%v)", attempt, got, base, base+base/5)
			}
		}
	}
}

func TestBackoffIsCapped(t *testing.T) {
	limit := 5*time.Minute + time.Minute
	for i := 0; i < 100; i++ {
		if got := backoff(30); got > limit {
			t.Fatalf("backoff %v exceeds cap %v", got, limit)
		}
	}
}
