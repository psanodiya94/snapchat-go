// Command server is the entry point for the snapchat-go API server.
//
// It wires together all internal packages, establishes the database connection
// pool, registers all HTTP routes, and starts a background goroutine that
// periodically deletes expired messages and stories from the database.
//
// # Configuration
//
// All configuration is provided through environment variables.  See
// .env.example for the full list.  The server can start with no environment
// variables set for local development using default values.
//
// # Starting the server
//
//	go run ./cmd/server
//
// The server listens on :8080 by default (override with PORT env var).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"snapchat-go/internal/auth"
	"snapchat-go/internal/db"
	"snapchat-go/internal/friend"
	"snapchat-go/internal/media"
	"snapchat-go/internal/message"
	"snapchat-go/internal/story"
	"snapchat-go/internal/user"
	"snapchat-go/internal/ws"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	ctx := context.Background()

	// Connect to PostgreSQL. Fatals on failure so the process exits cleanly
	// instead of running in a degraded state.
	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	// Ensure the uploads directory exists before registering the static handler.
	uploadDir := "uploads"
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Fatalf("uploads dir: %v", err)
	}

	// Hub manages all active WebSocket connections and delivers real-time events.
	hub := ws.NewHub()

	// Background cleanup goroutine: permanently delete rows whose expiry has
	// passed.  Runs every minute.  Ephemeral messages are deleted after
	// expires_at; stories are deleted 24 h after creation.
	go func() {
		tick := time.NewTicker(time.Minute)
		for range tick.C {
			pool.Exec(ctx, `DELETE FROM messages WHERE expires_at IS NOT NULL AND expires_at < NOW()`)
			pool.Exec(ctx, `DELETE FROM stories WHERE expires_at < NOW()`)
		}
	}()

	r := chi.NewRouter()

	// Global middleware applied to every request.
	r.Use(middleware.Logger)    // structured request/response logging
	r.Use(middleware.Recoverer) // catches panics and returns 500
	r.Use(middleware.RealIP)    // reads X-Real-IP / X-Forwarded-For
	r.Use(middleware.RequestID) // injects a unique X-Request-Id header

	// ── Public routes (no authentication required) ────────────────────────
	r.Post("/auth/register", auth.Register(pool))
	r.Post("/auth/login", auth.Login(pool))

	// Serve uploaded files directly from the filesystem.
	// URLs look like /uploads/<uuid>.<ext>.
	r.Handle("/uploads/*", http.StripPrefix("/uploads/",
		http.FileServer(http.Dir(uploadDir))))

	// ── Authenticated routes (JWT required) ──────────────────────────────
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware)

		// User profile and discovery
		r.Get("/users/me", user.Me(pool))
		r.Get("/users/search", user.Search(pool))
		r.Get("/users/{username}", user.GetByUsername(pool))

		// Friend management
		r.Post("/friends/request", friend.SendRequest(pool))
		r.Get("/friends/requests", friend.ListRequests(pool))
		r.Put("/friends/requests/{id}", friend.RespondRequest(pool))
		r.Get("/friends", friend.List(pool))

		// Ephemeral direct messages
		r.Post("/messages", message.Send(pool, hub))
		r.Get("/messages/{friendID}", message.Conversation(pool))
		r.Put("/messages/{id}/read", message.MarkRead(pool, hub))

		// 24-hour stories
		r.Post("/stories", story.Create(pool))
		r.Get("/stories/feed", story.Feed(pool))
		r.Get("/stories/mine", story.MyStories(pool))
		r.Post("/stories/{id}/view", story.View(pool))

		// File uploads (images and videos for messages/stories)
		r.Post("/media/upload", media.Upload(uploadDir))

		// Real-time WebSocket — token passed as ?token=<jwt> query param
		r.Get("/ws", ws.ServeWS(hub))
	})

	addr := ":" + getenv("PORT", "8080")
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal(err)
	}
}

// getenv returns the value of the environment variable named by key, or
// fallback if the variable is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
