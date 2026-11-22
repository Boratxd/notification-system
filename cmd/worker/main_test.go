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

func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		code           int
		retryAfter     string
		wantRetryable  bool
		wantRetryAfter time.Duration
	}{
		{400, "", false, 0},
		{404, "", false, 0},
		{408, "", true, 0},
		{429, "", true, 0},
		{429, "7", true, 7 * time.Second},
		{500, "", true, 0},
		{502, "", true, 0},
		{503, "", true, 0},
	}
	for _, tc := range tests {
		got := classifyStatus(tc.code, tc.retryAfter)
		if got.retryable != tc.wantRetryable {
			t.Errorf("status %d: retryable = %v, want %v", tc.code, got.retryable, tc.wantRetryable)
		}
		if got.retryAfter != tc.wantRetryAfter {
			t.Errorf("status %d: retryAfter = %v, want %v", tc.code, got.retryAfter, tc.wantRetryAfter)
		}
	}
}
