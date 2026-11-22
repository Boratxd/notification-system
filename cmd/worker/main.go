package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type job struct {
	ID        string
	Recipient string
	Channel   string
	Content   string
}

type providerResponse struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
}

type failure struct {
	reason     string
	retryable  bool
	retryAfter time.Duration
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://notif:notif@localhost:5432/notifications?sslmode=disable"
	}
	webhookURL := os.Getenv("WEBHOOK_URL")
	if webhookURL == "" {
		logger.Error("WEBHOOK_URL is required")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		logger.Error("cannot create db pool", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	client := &http.Client{Timeout: 10 * time.Second}

	logger.Info("worker started")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("worker stopping")
			return
		case <-ticker.C:
			jobs, err := claim(ctx, pool, 10)
			if err != nil {
				logger.Error("claim failed", "error", err)
				continue
			}
			for _, j := range jobs {
				process(ctx, pool, client, webhookURL, logger, j)
			}
		}
	}

}

func claim(ctx context.Context, pool *pgxpool.Pool, limit int) ([]job, error) {
	rows, err := pool.Query(ctx, `
		UPDATE notifications
		SET status = 'processing',
		    locked_until = now() + interval '30 seconds',
		    attempts = attempts + 1,
		    updated_at = now()
		WHERE id IN (
			SELECT id FROM notifications
			WHERE (status = 'pending' AND next_attempt_at <= now())
			   OR (status = 'processing' AND locked_until < now())
			ORDER BY priority, next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id::text, recipient, channel, content`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.ID, &j.Recipient, &j.Channel, &j.Content); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func process(ctx context.Context, pool *pgxpool.Pool, client *http.Client, url string, logger *slog.Logger, j job) {
	body, _ := json.Marshal(map[string]string{"to": j.Recipient, "channel": j.Channel, "content": j.Content})

	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		handleFailure(ctx, pool, logger, j.ID, failure{reason: fmt.Sprintf("request error: %v", err), retryable: true})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		f := failure{reason: fmt.Sprintf("Provider returned %d", resp.StatusCode)}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			f.retryable = true
			if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
				f.retryAfter = time.Duration(secs) * time.Second
			}
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout:
			f.retryable = true
		}
		handleFailure(ctx, pool, logger, j.ID, f)
		return
	}
	var pr providerResponse
	_ = json.Unmarshal(respBody, &pr)
	_, err = pool.Exec(ctx, `
		UPDATE notifications
		SET status = 'sent', sent_at = now(), provider_message_id = $2,
		    locked_until = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing'`, j.ID, pr.MessageID)
	if err != nil {
		logger.Error("mark sent failed", "id", j.ID, "error", err)
		return
	}
	logger.Info("notification sent", "id", j.ID, "channel", j.Channel, "provider_message_id", pr.MessageID)
}
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d + time.Duration(rand.Int63n(int64(d)/5))
}

func handleFailure(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, id string, f failure) {
	var attempts int
	_ = pool.QueryRow(ctx, `SELECT attempts FROM notifications WHERE id = $1`, id).Scan(&attempts)

	wait := backoff(attempts)
	if f.retryAfter > wait {
		wait = f.retryAfter
	}

	var status string
	err := pool.QueryRow(ctx, `
		UPDATE notifications
		SET status = CASE WHEN $3::boolean AND attempts < max_attempts THEN 'pending' ELSE 'failed' END,
		    next_attempt_at = CASE WHEN $3::boolean AND attempts < max_attempts
		                           THEN now() + make_interval(secs => $4::float8)
		                           ELSE next_attempt_at END,
		    last_error = $2, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing'
		RETURNING status`, id, f.reason, f.retryable, wait.Seconds()).Scan(&status)

	if err != nil {
		logger.Error("Handle failure failed", "id", id, "error", err)
		return
	}
	if status == "pending" {
		logger.Warn("delivery failed, will retry", "id", id, "reason", f.reason, "attempts", attempts, "retry_in", wait.String())
	} else {
		logger.Error("delivery failed permanently", "id", id, "reason", f.reason, "attempts", attempts)
	}
}
