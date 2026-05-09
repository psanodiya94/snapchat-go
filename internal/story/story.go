// Package story implements 24-hour ephemeral stories visible to friends.
//
// # Story lifecycle
//
//  1. A user posts a story via POST /stories (text, image, or video).
//     The database default sets expires_at = NOW() + 24 hours automatically.
//  2. Friends can view the story feed via GET /stories/feed.
//     Each story includes a view count and a viewed_by_me flag.
//  3. Calling POST /stories/{id}/view records the viewer (idempotent).
//  4. A background goroutine in main deletes expired stories every minute.
//
// Users can check their own stories and their view counts via GET /stories/mine.
package story

import (
	"encoding/json"
	"net/http"
	"time"

	"snapchat-go/internal/auth"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Story represents a single story post returned by the API.
// ViewedBy is only included in the feed response (not in MyStories).
type Story struct {
	ID       uuid.UUID `json:"id"`
	UserID   uuid.UUID `json:"user_id"`
	Username string    `json:"username"`
	// Content holds a text caption; null for media-only stories.
	Content *string `json:"content,omitempty"`
	// MediaURL points to a previously uploaded file (see POST /media/upload).
	MediaURL  *string   `json:"media_url,omitempty"`
	// ExpiresAt is when the story will be automatically deleted (24 h after posting).
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	// Views is the total number of unique viewers.
	Views int `json:"views"`
	// ViewedBy is true if the currently authenticated user has already viewed
	// this story.  Only present in the feed response.
	ViewedBy *bool `json:"viewed_by_me,omitempty"`
}

// Create posts a new story for the authenticated user.
// The story automatically expires 24 hours after creation (set by the DB default).
//
// POST /stories
// Request body:
//
//	{
//	  "content":   "Good morning!",    // optional if media_url is set
//	  "media_url": "/uploads/photo.jpg" // optional if content is set
//	}
//
// Response (201 Created): Story JSON object.
func Create(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)

		var body struct {
			Content  string `json:"content"`
			MediaURL string `json:"media_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Content == "" && body.MediaURL == "" {
			http.Error(w, "content or media_url required", http.StatusBadRequest)
			return
		}

		var content, mediaURL *string
		if body.Content != "" {
			content = &body.Content
		}
		if body.MediaURL != "" {
			mediaURL = &body.MediaURL
		}

		var s Story
		err := db.QueryRow(r.Context(),
			`INSERT INTO stories (user_id, content, media_url)
             VALUES ($1,$2,$3)
             RETURNING id, user_id, content, media_url, expires_at, created_at`,
			uid, content, mediaURL,
		).Scan(&s.ID, &s.UserID, &s.Content, &s.MediaURL, &s.ExpiresAt, &s.CreatedAt)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(s)
	}
}

// Feed returns all non-expired stories from the authenticated user's friends,
// ordered newest-first.  Each entry includes the total view count and whether
// the current user has already viewed it.
//
// GET /stories/feed
// Response (200 OK): JSON array of Story objects.
func Feed(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		rows, err := db.Query(r.Context(),
			`SELECT s.id, s.user_id, u.username, s.content, s.media_url, s.expires_at, s.created_at,
                    (SELECT COUNT(*) FROM story_views sv WHERE sv.story_id=s.id) AS views,
                    EXISTS(SELECT 1 FROM story_views sv WHERE sv.story_id=s.id AND sv.viewer_id=$1) AS viewed_by_me
             FROM stories s
             JOIN users u ON u.id = s.user_id
             WHERE s.expires_at > NOW()
               AND s.user_id IN (
                   SELECT CASE WHEN requester_id=$1 THEN addressee_id ELSE requester_id END
                   FROM friendships
                   WHERE (requester_id=$1 OR addressee_id=$1) AND status='accepted'
               )
             ORDER BY s.created_at DESC`, uid)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		stories := []Story{}
		for rows.Next() {
			var s Story
			if err := rows.Scan(&s.ID, &s.UserID, &s.Username, &s.Content, &s.MediaURL,
				&s.ExpiresAt, &s.CreatedAt, &s.Views, &s.ViewedBy); err != nil {
				continue
			}
			stories = append(stories, s)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stories)
	}
}

// View records that the authenticated user has viewed a story.
// The operation is idempotent — calling it multiple times has no effect
// thanks to the ON CONFLICT DO NOTHING clause.
//
// POST /stories/{id}/view
// Response (204 No Content).
func View(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		sid, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			http.Error(w, "invalid story id", http.StatusBadRequest)
			return
		}

		_, err = db.Exec(r.Context(),
			`INSERT INTO story_views (story_id, viewer_id) VALUES ($1,$2)
             ON CONFLICT (story_id, viewer_id) DO NOTHING`,
			sid, uid)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// MyStories returns the authenticated user's own non-expired stories together
// with their view counts.  Useful for showing a "your story" section with
// audience analytics.
//
// GET /stories/mine
// Response (200 OK): JSON array of Story objects (viewed_by_me omitted).
func MyStories(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)
		rows, err := db.Query(r.Context(),
			`SELECT s.id, s.user_id, u.username, s.content, s.media_url, s.expires_at, s.created_at,
                    (SELECT COUNT(*) FROM story_views sv WHERE sv.story_id=s.id) AS views
             FROM stories s
             JOIN users u ON u.id=s.user_id
             WHERE s.user_id=$1 AND s.expires_at > NOW()
             ORDER BY s.created_at DESC`, uid)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		stories := []Story{}
		for rows.Next() {
			var s Story
			if err := rows.Scan(&s.ID, &s.UserID, &s.Username, &s.Content, &s.MediaURL,
				&s.ExpiresAt, &s.CreatedAt, &s.Views); err != nil {
				continue
			}
			stories = append(stories, s)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stories)
	}
}
