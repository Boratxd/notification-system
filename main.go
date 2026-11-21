package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"
)

type createNotificationRequest struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Content   string `json:"content"`
	Priority  string `json:"priority"`
}

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
	fmt.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
