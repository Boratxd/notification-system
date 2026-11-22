package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type createNotificationRequest struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Content   string `json:"content"`
	Priority  string `json:"priority"`
}
type notificationResponse struct {
	ID        string    `json:"id"`
	Recipient string    `json:"recipient"`
	Channel   string    `json:"channel"`
	Content   string    `json:"content"`
	Priority  string    `json:"priority"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type batchRequest struct {
	Notifications []createNotificationRequest `json:"notifications"`
}

type batchError struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

var priorityNames = map[int]string{0: "high", 1: "normal", 2: "low"}
var priorities = map[string]int{"high": 0, "normal": 1, "low": 2}
var maxContentLenght = map[string]int{"sms": 160, "email": 10000, "push": 256}

const maxBatchSize = 1000

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func createNotificationHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createNotificationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		priority, msg := validateNotification(req)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		var kArg any
		key := r.Header.Get("Idempotency-Key")
		if key != "" {
			kArg = key
		}

		id := uuid.New()
		var insertedID string
		err := pool.QueryRow(r.Context(), `
			INSERT INTO notifications (id, recipient, channel, content, priority, idempotency_key)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
			RETURNING id::text`,
			id.String(), req.Recipient, req.Channel, req.Content, priority, kArg).Scan(&insertedID)

		if err == nil {
			writeJSON(w, http.StatusAccepted, map[string]string{"id": insertedID, "status": "pending"})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("insert Failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not save the notification")
			return
		}
		var existingID, existingStatus string
		err = pool.QueryRow(r.Context(),
			`SELECT id::text, status FROM notifications WHERE idempotency_key = $1`, key).Scan(&existingID, &existingStatus)
		if err != nil {
			log.Printf("idempotency lookup failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not save the notification")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": existingID, "status": existingStatus, "idempotent_replay": "true"})
	}
}

func getNotificationHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid id")
			return
		}

		var n notificationResponse
		var priority int
		err := pool.QueryRow(r.Context(), `SELECT id::text, recipient, channel, content, priority, status, created_at, updated_at FROM notifications WHERE id = $1`, id).Scan(&n.ID, &n.Recipient, &n.Channel, &n.Content, &priority, &n.Status, &n.CreatedAt, &n.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Notification can not be found")
			return
		}
		if err != nil {
			log.Printf("Query Failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not read the notification")
			return
		}

		n.Priority = priorityNames[priority]
		writeJSON(w, http.StatusOK, n)
	}
}
func cancelNotificationHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			writeError(w, http.StatusBadRequest, " Invalid Notification ID!")
			return
		}
		var cancelledID string
		err := pool.QueryRow(r.Context(), `UPDATE notifications SET status = 'cancelled', updated_at = now() WHERE id = $1 AND status = 'pending' RETURNING id::text`, id).Scan(&cancelledID)

		if err == nil {
			writeJSON(w, http.StatusOK, map[string]string{"id": cancelledID, "status": "cancelled"})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("cancel failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not cancel the notification")
			return
		}
		var currentStatus string
		err = pool.QueryRow(r.Context(), `SELECT status FROM notifications WHERE id = $1`, id).Scan(&currentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Notification can not be found")
			return
		}
		if err != nil {
			log.Printf("Notification status lookup failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not read the notification")
			return
		}
		writeError(w, http.StatusConflict, "Only pening notifications can be cancelled")
	}
}

func listNotificationsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, offset := 20, 0
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 100 {
				writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
			limit = n
		}
		if v := q.Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, "offset must be 0 or greater")
				return
			}
			offset = n
		}
		var conditions []string
		var args []any
		addFilter := func(clause string, value any) {
			args = append(args, value)
			conditions = append(conditions, fmt.Sprintf(clause, len(args)))
		}
		if v := q.Get("status"); v != "" {
			addFilter("status = $%d", v)
		}
		if v := q.Get("channel"); v != "" {
			addFilter("channel = $%d", v)
		}

		if v := q.Get("batch_id"); v != "" {
			if _, err := uuid.Parse(v); err != nil {
				writeError(w, http.StatusBadRequest, "batch_id must be a valid UUID")
				return
			}
			addFilter("batch_id = $%d", v)
		}

		if v := q.Get("from"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "from must be an RFC3339 timestamp")
				return
			}
			addFilter("created_at>=$%d", t)
		}
		if v := q.Get("to"); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "to must be an RFC3330 timestamp")
				return
			}
			addFilter("created_at<=$%d", t)
		}
		where := ""
		if len(conditions) > 0 {
			where = "WHERE " + strings.Join(conditions, " AND ")
		}
		args = append(args, limit, offset)
		query := fmt.Sprintf(`SELECT id::text, recipient, channel, content, priority, status, created_at, updated_at FROM notifications %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))
		rows, err := pool.Query(r.Context(), query, args...)
		if err != nil {
			log.Printf("list query failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not list notifications")
			return
		}
		defer rows.Close()

		items := []notificationResponse{}
		for rows.Next() {
			var n notificationResponse
			var priority int
			if err := rows.Scan(&n.ID, &n.Recipient, &n.Channel, &n.Content, &priority, &n.Status, &n.CreatedAt, &n.UpdatedAt); err != nil {
				log.Printf("list scan failed: %v", err)
				writeError(w, http.StatusInternalServerError, "Could not read notifications")
				return
			}
			n.Priority = priorityNames[priority]
			items = append(items, n)
		}
		if err := rows.Err(); err != nil {
			log.Printf("list rows failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not read notifications")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": items, "limit": limit, "offset": offset})
	}
}

func validateNotification(req createNotificationRequest) (int, string) {
	if req.Recipient == "" || req.Content == "" {
		return 0, "Recipient and content needed"
	}
	limit, ok := maxContentLenght[req.Channel]
	if !ok {
		return 0, "Channel must be either sms,email or push"
	}
	if len([]rune(req.Content)) > limit {
		return 0, "content exceeds the limit of the channel"
	}
	if req.Priority == "" {
		req.Priority = "normal"
	}
	priority, ok := priorities[req.Priority]
	if !ok {
		return 0, "priority must be high,normal or low"
	}
	return priority, ""
}

func createBatchHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON or body too large")
			return
		}
		if len(req.Notifications) == 0 {
			writeError(w, http.StatusBadRequest, "notifications must not be empty")
			return
		}
		if len(req.Notifications) > maxBatchSize {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("a batch can contain at most %d notifications", maxBatchSize))
			return
		}
		prios := make([]int, len(req.Notifications))
		var problems []batchError
		for i, n := range req.Notifications {
			p, msg := validateNotification(n)
			if msg != "" {
				problems = append(problems, batchError{Index: i, Error: msg})
				continue
			}
			prios[i] = p
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "validation failed", "details": problems})
			return
		}
		ctx := r.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			log.Printf("begin failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not save the batch")
			return
		}
		defer tx.Rollback(ctx)

		batchID := uuid.New().String()
		ids := make([]string, 0, len(req.Notifications))
		for i, n := range req.Notifications {
			id := uuid.New().String()
			_, err := tx.Exec(ctx, `
				INSERT INTO notifications (id, batch_id, recipient, channel, content, priority)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				id, batchID, n.Recipient, n.Channel, n.Content, prios[i])
			if err != nil {
				log.Printf("batch insert failed at index %d: %v", i, err)
				writeError(w, http.StatusInternalServerError, "Could not save the batch")
				return
			}
			ids = append(ids, id)

		}
		if err := tx.Commit(ctx); err != nil {
			log.Printf("commit failed: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save the batch")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"batch_id": batchID, "count": len(ids), "ids": ids})
	}
}

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://notif:notif@localhost:5432/notifications?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			http.Error(w, "database down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	http.HandleFunc("POST /notifications", createNotificationHandler(pool))
	http.HandleFunc("GET /notifications/{id}", getNotificationHandler(pool))
	http.HandleFunc("POST /notifications/{id}/cancel", cancelNotificationHandler(pool))
	http.HandleFunc("GET /notifications", listNotificationsHandler(pool))
	http.HandleFunc("POST /notifications/batch", createBatchHandler(pool))
	fmt.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
