// Package message implements Snapchat-style ephemeral direct messaging.
//
// # Ephemeral message lifecycle
//
//  1. Sender calls POST /messages with optional ttl_seconds (default 86400 = 24 h).
//     The row is inserted with expires_at = NULL and read_at = NULL.
//  2. Recipient receives a real-time "new_message" WebSocket event immediately.
//  3. When the recipient calls PUT /messages/{id}/read, read_at is set to NOW()
//     and expires_at is set to NOW() + ttl_seconds.
//  4. A "message_read" event is pushed to the sender with the expiry timestamp.
//  5. A background goroutine in main deletes rows where expires_at < NOW()
//     every minute, permanently erasing the message.
//
// Unread messages are never deleted, so they remain until the recipient
// views them (or the account is removed).
package message

import (
	"encoding/json"
	"net/http"
	"time"

	"snapchat-go/internal/auth"
	"snapchat-go/internal/ws"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Message represents a single direct message between two users.
//
// The TTLSeconds field controls how long (in seconds) after the message is
// read before it is permanently deleted.  ExpiresAt is null until the
// recipient marks the message as read.
type Message struct {
	ID          uuid.UUID  `json:"id"`
	SenderID    uuid.UUID  `json:"sender_id"`
	RecipientID uuid.UUID  `json:"recipient_id"`
	// Content holds the text body; null for media-only messages.
	Content  *string `json:"content,omitempty"`
	// MediaURL points to a previously uploaded file (see POST /media/upload).
	MediaURL *string    `json:"media_url,omitempty"`
	// MsgType is one of "text", "image", "video", or "file".
	MsgType    string     `json:"msg_type"`
	// TTLSeconds is how many seconds after reading before the message expires.
	TTLSeconds int        `json:"ttl_seconds"`
	// ExpiresAt is null until the message is read, then set to read_at + TTLSeconds.
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	// ReadAt is null until the recipient calls the read endpoint.
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Send delivers a direct message from the authenticated user to a recipient
// and pushes a real-time "new_message" event to any active WebSocket
// connections the recipient has open.
//
// POST /messages
// Request body:
//
//	{
//	  "recipient_id": "<uuid>",
//	  "content":      "Hey!",         // optional if media_url is set
//	  "media_url":    "/uploads/x.jpg", // optional if content is set
//	  "msg_type":     "text",          // "text"|"image"|"video"|"file" (default: "text")
//	  "ttl_seconds":  3600             // seconds until deletion after read (default: 86400)
//	}
//
// Response (201 Created): full Message JSON object.
func Send(db *pgxpool.Pool, hub *ws.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)

		var body struct {
			RecipientID uuid.UUID `json:"recipient_id"`
			Content     string    `json:"content"`
			MediaURL    string    `json:"media_url"`
			MsgType     string    `json:"msg_type"`
			TTLSeconds  int       `json:"ttl_seconds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.RecipientID == uuid.Nil {
			http.Error(w, "recipient_id required", http.StatusBadRequest)
			return
		}
		if body.Content == "" && body.MediaURL == "" {
			http.Error(w, "content or media_url required", http.StatusBadRequest)
			return
		}

		if body.MsgType == "" {
			body.MsgType = "text"
		}
		if body.TTLSeconds <= 0 {
			body.TTLSeconds = 86400 // default: 24 hours after reading
		}

		var content, mediaURL *string
		if body.Content != "" {
			content = &body.Content
		}
		if body.MediaURL != "" {
			mediaURL = &body.MediaURL
		}

		var msg Message
		err := db.QueryRow(r.Context(),
			`INSERT INTO messages (sender_id, recipient_id, content, media_url, msg_type, ttl_seconds)
             VALUES ($1,$2,$3,$4,$5,$6)
             RETURNING id, sender_id, recipient_id, content, media_url, msg_type, ttl_seconds, expires_at, read_at, created_at`,
			uid, body.RecipientID, content, mediaURL, body.MsgType, body.TTLSeconds,
		).Scan(&msg.ID, &msg.SenderID, &msg.RecipientID, &msg.Content, &msg.MediaURL,
			&msg.MsgType, &msg.TTLSeconds, &msg.ExpiresAt, &msg.ReadAt, &msg.CreatedAt)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		// Deliver real-time notification to all of the recipient's open connections.
		payload, _ := json.Marshal(msg)
		hub.Send(body.RecipientID, ws.Event{Type: "new_message", Payload: payload})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(msg)
	}
}

// Conversation returns the message history between the authenticated user and
// one other user, ordered oldest-first.  Expired messages are excluded from
// the results — they will be hard-deleted by the background cleanup job
// shortly after expiry.
//
// GET /messages/{friendID}
// Response (200 OK): JSON array of Message objects, oldest first.
func Conversation(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		otherID, err := uuid.Parse(chi.URLParam(r, "friendID"))
		if err != nil {
			http.Error(w, "invalid friendID", http.StatusBadRequest)
			return
		}

		rows, err := db.Query(r.Context(),
			`SELECT id, sender_id, recipient_id, content, media_url, msg_type, ttl_seconds, expires_at, read_at, created_at
             FROM messages
             WHERE (
                 (sender_id=$1 AND recipient_id=$2) OR
                 (sender_id=$2 AND recipient_id=$1)
             )
             AND (expires_at IS NULL OR expires_at > NOW())
             ORDER BY created_at ASC`, uid, otherID)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		msgs := []Message{}
		for rows.Next() {
			var m Message
			if err := rows.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Content, &m.MediaURL,
				&m.MsgType, &m.TTLSeconds, &m.ExpiresAt, &m.ReadAt, &m.CreatedAt); err != nil {
				continue
			}
			msgs = append(msgs, m)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	}
}

// MarkRead marks a message as read and starts its expiry countdown.
//
// This is the core of the ephemeral mechanic:
//   - read_at  is set to NOW()
//   - expires_at is set to NOW() + ttl_seconds
//
// Once expires_at is set, the message will disappear from Conversation results
// and will be physically deleted by the background cleanup goroutine within
// one minute of expiry.
//
// A "message_read" WebSocket event is pushed to the sender so they can show
// a "seen" indicator and the expiry countdown in the UI.
//
// PUT /messages/{id}/read
// Response (204 No Content) on success.
// Returns 404 if the message does not exist, the caller is not the recipient,
// or the message was already marked as read.
func MarkRead(db *pgxpool.Pool, hub *ws.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		msgID, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var msg Message
		err = db.QueryRow(r.Context(),
			`UPDATE messages
             SET read_at   = NOW(),
                 expires_at = NOW() + (ttl_seconds * INTERVAL '1 second')
             WHERE id=$1 AND recipient_id=$2 AND read_at IS NULL
             RETURNING id, sender_id, recipient_id, ttl_seconds, expires_at, read_at, created_at`,
			msgID, uid,
		).Scan(&msg.ID, &msg.SenderID, &msg.RecipientID, &msg.TTLSeconds,
			&msg.ExpiresAt, &msg.ReadAt, &msg.CreatedAt)
		if err != nil {
			http.Error(w, "not found or already read", http.StatusNotFound)
			return
		}

		// Tell the sender their message was opened and when it will self-destruct.
		payload, _ := json.Marshal(map[string]any{
			"message_id": msg.ID,
			"expires_at": msg.ExpiresAt,
		})
		hub.Send(msg.SenderID, ws.Event{Type: "message_read", Payload: payload})

		w.WriteHeader(http.StatusNoContent)
	}
}
