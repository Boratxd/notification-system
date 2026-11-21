package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
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

var priorityNames = map[int]string{0: "high", 1: "normal", 2: "low"}
var priorities = map[string]int{"high": 0, "normal": 1, "low": 2}
var maxContentLenght = map[string]int{"sms": 160, "email": 10000, "push": 256}

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
		if req.Recipient == "" || req.Content == "" {
			writeError(w, http.StatusBadRequest, "recipient and content are required")
			return
		}
		limit, ok := maxContentLenght[req.Channel]
		if !ok {
			writeError(w, http.StatusBadRequest, "The channel must be sms, email or push")
			return
		}
		if len([]rune(req.Content)) > limit {
			writeError(w, http.StatusBadRequest, "Notification content exceeds the limit for this channel")
			return
		}
		if req.Priority == "" {
			req.Priority = "normal"
		}
		priority, ok := priorities[req.Priority]
		if !ok {
			writeError(w, http.StatusBadRequest, "Priority must be high, normal or low")
			return
		}

		id := uuid.New()
		_, err := pool.Exec(r.Context(), `INSERT INTO notifications (id,recipient,channel,content,priority) VALUES ($1, $2, $3, $4, $5)`, id.String(), req.Recipient, req.Channel, req.Content, priority)
		if err != nil {
			log.Printf("insert failed: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not save the notification")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"id": id.String(), "status": "pending"})
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
		err := pool.QueryRow(r.Context(), `UPDATE notifications SET status = 'cancalled', updated_at = now() WHERE id = $1 AND status = 'pending' RETURNING id::text`, id).Scan(&cancelledID)

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
	fmt.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
