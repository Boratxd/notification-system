package main

import (
	"strings"
	"testing"
)

func TestValidateNotification(t *testing.T) {
	valid := createNotificationRequest{Recipient: "+905551234567", Channel: "sms", Content: "hello"}
	tests := []struct {
		name         string
		mutate       func(r *createNotificationRequest)
		wantPriority int
		wantErr      bool
	}{
		{"valid with default priority", func(r *createNotificationRequest) {}, 1, false},
		{"valid high priority", func(r *createNotificationRequest) { r.Priority = "high" }, 0, false},
		{"valid low priority", func(r *createNotificationRequest) { r.Priority = "low" }, 2, false},
		{"missing recipient", func(r *createNotificationRequest) { r.Recipient = "" }, 0, true},
		{"missing content", func(r *createNotificationRequest) { r.Content = "" }, 0, true},
		{"unknown channel", func(r *createNotificationRequest) { r.Channel = "fax" }, 0, true},
		{"unknown priority", func(r *createNotificationRequest) { r.Priority = "extreme" }, 0, true},
		{"sms at the limit", func(r *createNotificationRequest) { r.Content = strings.Repeat("a", 160) }, 1, false},
		{"sms over the limit", func(r *createNotificationRequest) { r.Content = strings.Repeat("a", 161) }, 0, true},
		{"turkish characters are counted as characters, not bytes", func(r *createNotificationRequest) { r.Content = strings.Repeat("ç", 160) }, 1, false},
		{"email allows long content", func(r *createNotificationRequest) {
			r.Channel = "email"
			r.Content = strings.Repeat("a", 5000)
		}, 1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			priorty, msg := validateNotification(req)
			if tc.wantErr {
				if msg == "" {
					t.Fatalf("expected a validation error, got none")
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected validation error: %s", msg)
			}
			if priorty != tc.wantPriority {
				t.Fatalf("priority = %d, want %d", priorty, tc.wantPriority)
			}
		})
	}
}
