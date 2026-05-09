// Package friend manages the social graph: sending, accepting, and rejecting
// friend requests, and listing accepted friends.
//
// # Friendship lifecycle
//
//  1. User A calls SendRequest with User B's ID → row inserted with status "pending".
//  2. User B calls RespondRequest with action "accept" or "reject" → status updated.
//  3. Both users can then exchange messages and see each other's stories.
//
// The friendships table enforces a UNIQUE constraint on (requester_id, addressee_id)
// so duplicate requests are silently ignored (ON CONFLICT DO NOTHING).
package friend

import (
	"encoding/json"
	"net/http"
	"time"

	"snapchat-go/internal/auth"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Friendship represents a row in the friendships table and is returned by
// ListRequests so the addressee can see who sent them a request.
type Friendship struct {
	ID          uuid.UUID `json:"id"`
	RequesterID uuid.UUID `json:"requester_id"`
	AddresseeID uuid.UUID `json:"addressee_id"`
	// Status is one of "pending", "accepted", or "rejected".
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Friend is the condensed profile returned by the List endpoint — just
// enough information to display a friend list in a UI.
type Friend struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	AvatarURL *string   `json:"avatar_url,omitempty"`
}

// SendRequest sends a friend request from the authenticated user to another user.
//
// POST /friends/request
// Request body: { "addressee_id": "<uuid>" }
// Response (201 Created): { "id": "<friendship-uuid>" }
//
// Returns 400 if the user tries to friend themselves.
// Returns 409 if a request in either direction already exists.
func SendRequest(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		var body struct {
			AddresseeID uuid.UUID `json:"addressee_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AddresseeID == uuid.Nil {
			http.Error(w, "addressee_id required", http.StatusBadRequest)
			return
		}
		if uid == body.AddresseeID {
			http.Error(w, "cannot friend yourself", http.StatusBadRequest)
			return
		}

		var id uuid.UUID
		err := db.QueryRow(r.Context(),
			`INSERT INTO friendships (requester_id, addressee_id, status)
             VALUES ($1,$2,'pending')
             ON CONFLICT (requester_id, addressee_id) DO NOTHING
             RETURNING id`,
			uid, body.AddresseeID,
		).Scan(&id)
		if err != nil {
			http.Error(w, "request already sent or conflict", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": id.String()})
	}
}

// ListRequests returns all pending friend requests addressed to the
// authenticated user.  The requester_id in each row identifies who sent
// the request; the caller can look them up via GET /users/{username}.
//
// GET /friends/requests
// Response (200 OK): JSON array of Friendship objects with status "pending".
func ListRequests(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		rows, err := db.Query(r.Context(),
			`SELECT f.id, f.requester_id, f.addressee_id, f.status, f.created_at
             FROM friendships f
             WHERE f.addressee_id=$1 AND f.status='pending'`, uid)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		list := []Friendship{}
		for rows.Next() {
			var f Friendship
			if err := rows.Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt); err != nil {
				continue
			}
			list = append(list, f)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	}
}

// RespondRequest accepts or rejects a pending friend request.
// Only the addressee (the person who received the request) can respond to it.
//
// PUT /friends/requests/{id}
// Request body: { "action": "accept" } or { "action": "reject" }
// Response (204 No Content) on success.
// Returns 404 if the request does not exist or the caller is not the addressee.
func RespondRequest(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		fid, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var body struct {
			// Action must be "accept" or "reject".
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		var status string
		switch body.Action {
		case "accept":
			status = "accepted"
		case "reject":
			status = "rejected"
		default:
			http.Error(w, "action must be accept or reject", http.StatusBadRequest)
			return
		}

		tag, err := db.Exec(r.Context(),
			`UPDATE friendships SET status=$1
             WHERE id=$2 AND addressee_id=$3 AND status='pending'`,
			status, fid, uid)
		if err != nil || tag.RowsAffected() == 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// List returns all accepted friends of the authenticated user.
// The query matches friendships where the caller is either the requester
// or the addressee, so direction does not matter once a request is accepted.
//
// GET /friends
// Response (200 OK): JSON array of Friend objects.
func List(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		rows, err := db.Query(r.Context(),
			`SELECT u.id, u.username, u.avatar_url
             FROM users u
             JOIN friendships f ON (
                 (f.requester_id=$1 AND f.addressee_id=u.id) OR
                 (f.addressee_id=$1 AND f.requester_id=u.id)
             )
             WHERE f.status='accepted'`, uid)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		friends := []Friend{}
		for rows.Next() {
			var f Friend
			if err := rows.Scan(&f.ID, &f.Username, &f.AvatarURL); err != nil {
				continue
			}
			friends = append(friends, f)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(friends)
	}
}
