package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
		markFailed(ctx, pool, logger, j.ID, fmt.Sprintf("Request error: %v", err))
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		markFailed(ctx, pool, logger, j.ID, fmt.Sprintf("provider returned %d", resp.StatusCode))
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

func markFailed(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, id, reason string) {
	_, err := pool.Exec(ctx, `
		UPDATE notifications
		SET status = 'failed', last_error = $2, locked_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'processing'`, id, reason)
	if err != nil {
		logger.Error("mark failed", "id", id, "error", err)
		return
	}
	logger.Warn("notification failed", "id", id, "reason", reason)
}
