// Package user exposes HTTP handlers for reading and searching user profiles.
//
// All handlers require a valid JWT produced by the auth package — they rely
// on auth.Middleware being applied upstream in the router.
package user

import (
	"encoding/json"
	"net/http"
	"time"

	"snapchat-go/internal/auth"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User is the public representation of an account returned by the API.
// The Email field is only included on the /users/me endpoint (own profile).
// AvatarURL is omitted from JSON when null.
type User struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	AvatarURL *string   `json:"avatar_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Me returns the full profile of the currently authenticated user,
// including their email address.
//
// GET /users/me
// Response (200 OK): User JSON object.
func Me(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		var u User
		err := db.QueryRow(r.Context(),
			`SELECT id, username, email, avatar_url, created_at FROM users WHERE id=$1`, uid,
		).Scan(&u.ID, &u.Username, &u.Email, &u.AvatarURL, &u.CreatedAt)
		if err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}

// Search performs a case-insensitive partial match on usernames and returns
// up to 20 results. Use this to find people to send friend requests to.
//
// GET /users/search?q=<query>
// Response (200 OK): JSON array of User objects (email omitted).
func Search(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if q == "" {
			http.Error(w, "q param required", http.StatusBadRequest)
			return
		}
		rows, err := db.Query(r.Context(),
			`SELECT id, username, avatar_url, created_at FROM users
             WHERE username ILIKE '%' || $1 || '%' LIMIT 20`, q)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		users := []User{}
		for rows.Next() {
			var u User
			if err := rows.Scan(&u.ID, &u.Username, &u.AvatarURL, &u.CreatedAt); err != nil {
				continue
			}
			users = append(users, u)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(users)
	}
}

// GetByUsername looks up a single user by their exact username.
// Useful for viewing a profile before sending a friend request.
//
// GET /users/{username}
// Response (200 OK): User JSON object (email omitted).
// Returns 404 if no user with that username exists.
func GetByUsername(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := chi.URLParam(r, "username")
		var u User
		err := db.QueryRow(r.Context(),
			`SELECT id, username, avatar_url, created_at FROM users WHERE username=$1`, username,
		).Scan(&u.ID, &u.Username, &u.AvatarURL, &u.CreatedAt)
		if err != nil {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(u)
	}
}
