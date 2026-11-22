package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL is not set, skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv := httptest.NewServer(newMux(pool))
	t.Cleanup(srv.Close)
	return srv, pool
}

func cleanupRows(t *testing.T, pool *pgxpool.Pool, marker string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications WHERE content LIKE $1`, marker+"%")
	})
}

func doJSON(t *testing.T, srv *httptest.Server, method, path string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestIdempotencyKeyPreventsDuplicates(t *testing.T) {
	srv, pool := testServer(t)
	marker := "itest-" + uuid.NewString()
	cleanupRows(t, pool, marker)

	body := map[string]string{"recipient": "+905551234567", "channel": "sms", "content": marker}
	headers := map[string]string{"Idempotency-Key": marker}

	code1, first := doJSON(t, srv, "POST", "/notifications", headers, body)
	if code1 != http.StatusAccepted {
		t.Fatalf("first request: status = %d, want 202", code1)
	}
	code2, second := doJSON(t, srv, "POST", "/notifications", headers, body)
	if code2 != http.StatusOK {
		t.Fatalf("second request: status = %d, want 200", code2)
	}
	if first["id"] != second["id"] {
		t.Fatalf("ids differ: %v vs %v", first["id"], second["id"])
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notifications WHERE idempotency_key = $1`, marker).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows with this key = %d, want 1", count)
	}
}

func TestCancelPendingNotification(t *testing.T) {
	srv, pool := testServer(t)
	marker := "itest-" + uuid.NewString()
	cleanupRows(t, pool, marker)

	code, created := doJSON(t, srv, "POST", "/notifications", nil,
		map[string]string{"recipient": "+905551234567", "channel": "sms", "content": marker})
	if code != http.StatusAccepted {
		t.Fatalf("create: status = %d, want 202", code)
	}
	id := created["id"].(string)

	if code, _ := doJSON(t, srv, "POST", "/notifications/"+id+"/cancel", nil, nil); code != http.StatusOK {
		t.Fatalf("first cancel: status = %d, want 200", code)
	}
	if code, _ := doJSON(t, srv, "POST", "/notifications/"+id+"/cancel", nil, nil); code != http.StatusConflict {
		t.Fatalf("second cancel: status = %d, want 409", code)
	}
	if code, _ := doJSON(t, srv, "POST", "/notifications/"+uuid.NewString()+"/cancel", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404", code)
	}
	if code, _ := doJSON(t, srv, "POST", "/notifications/not-a-uuid/cancel", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("bad id: status = %d, want 400", code)
	}
}

func TestBatchWithInvalidItemWritesNothing(t *testing.T) {
	srv, pool := testServer(t)
	marker := "itest-" + uuid.NewString()
	cleanupRows(t, pool, marker)

	body := map[string]any{"notifications": []map[string]string{
		{"recipient": "+905551234567", "channel": "sms", "content": marker + "-ok"},
		{"recipient": "+905551234567", "channel": "fax", "content": marker + "-bad"},
	}}
	code, resp := doJSON(t, srv, "POST", "/notifications/batch", nil, body)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if resp["details"] == nil {
		t.Fatalf("expected validation details in response, got %v", resp)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notifications WHERE content LIKE $1`, marker+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows written for an invalid batch = %d, want 0", count)
	}
}

func TestBatchCreatesAllAndIsQueryableByBatchID(t *testing.T) {
	srv, pool := testServer(t)
	marker := "itest-" + uuid.NewString()
	cleanupRows(t, pool, marker)

	body := map[string]any{"notifications": []map[string]string{
		{"recipient": "+905551234567", "channel": "sms", "content": marker + "-1"},
		{"recipient": "a@b.com", "channel": "email", "content": marker + "-2", "priority": "high"},
		{"recipient": "token", "channel": "push", "content": marker + "-3", "priority": "low"},
	}}
	code, resp := doJSON(t, srv, "POST", "/notifications/batch", nil, body)
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if resp["count"] != float64(3) {
		t.Fatalf("count = %v, want 3", resp["count"])
	}

	batchID := resp["batch_id"].(string)
	code, list := doJSON(t, srv, "GET", "/notifications?batch_id="+batchID, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", code)
	}
	if items, _ := list["data"].([]any); len(items) != 3 {
		t.Fatalf("items in batch = %d, want 3", len(items))
	}
}
