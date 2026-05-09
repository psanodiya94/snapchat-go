// Package auth handles user registration, login, and request authentication.
//
// # Registration and Login
//
// Both endpoints accept JSON bodies and return a signed JWT on success.
// Passwords are hashed with bcrypt (cost 10) before being stored; the
// plain-text password never touches the database.
//
// # JWT
//
// Tokens are signed with HMAC-SHA256.  The signing key is read from the
// JWT_SECRET environment variable; a hard-coded fallback is used when the
// variable is absent so the server works out-of-the-box for local
// development.  Tokens expire after 7 days.
//
// # Middleware
//
// Middleware validates the Bearer token on every protected route and injects
// the authenticated user's UUID into the request context under UserIDKey.
// WebSocket upgrade requests may also pass the token as the "token" query
// parameter because browsers cannot set the Authorization header on a
// WebSocket connection.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// contextKey is an unexported type used for context keys in this package,
// preventing collisions with keys defined in other packages.
type contextKey string

// UserIDKey is the context key under which the authenticated user's UUID is stored.
// Other packages retrieve it via the UserID helper rather than accessing it directly.
const UserIDKey contextKey = "userID"

// registerReq is the JSON body accepted by the Register endpoint.
type registerReq struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginReq is the JSON body accepted by the Login endpoint.
type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// tokenResp is the JSON response returned by Register and Login on success.
type tokenResp struct {
	Token string `json:"token"`
}

// jwtSecret returns the HMAC signing key for JWTs.
// In production this must be set via the JWT_SECRET environment variable.
func jwtSecret() []byte {
	s := os.Getenv("JWT_SECRET")
	if s == "" {
		s = "change-me-in-production"
	}
	return []byte(s)
}

// Register creates a new user account.
//
// Request body (JSON):
//
//	{ "username": "alice", "email": "alice@example.com", "password": "secret" }
//
// Successful response (201 Created):
//
//	{ "token": "<jwt>" }
//
// The password is hashed with bcrypt before storage.
// Returns 409 Conflict if the username or email is already taken.
func Register(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Email == "" || req.Password == "" {
			http.Error(w, "username, email and password required", http.StatusBadRequest)
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		var id uuid.UUID
		err = db.QueryRow(r.Context(),
			`INSERT INTO users (username, email, password) VALUES ($1,$2,$3) RETURNING id`,
			req.Username, req.Email, string(hash),
		).Scan(&id)
		if err != nil {
			if strings.Contains(err.Error(), "unique") {
				http.Error(w, "username or email already taken", http.StatusConflict)
				return
			}
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}

		tok, err := makeToken(id)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(tokenResp{Token: tok})
	}
}

// Login authenticates an existing user by email and password.
//
// Request body (JSON):
//
//	{ "email": "alice@example.com", "password": "secret" }
//
// Successful response (200 OK):
//
//	{ "token": "<jwt>" }
//
// Returns 401 Unauthorized for unknown email or wrong password.
// Both cases return the same error message to prevent user enumeration.
func Login(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		var id uuid.UUID
		var hash string
		err := db.QueryRow(r.Context(),
			`SELECT id, password FROM users WHERE email=$1`, req.Email,
		).Scan(&id, &hash)
		if err != nil {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}

		tok, err := makeToken(id)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tokenResp{Token: tok})
	}
}

// Middleware is an HTTP middleware that enforces JWT authentication on every
// request that passes through it.
//
// It reads the token from the Authorization header ("Bearer <token>") or,
// for WebSocket upgrade requests that cannot set headers, from the "token"
// query parameter.  On success the authenticated user's UUID is stored in
// the request context under UserIDKey.  Any invalid or missing token results
// in an immediate 401 Unauthorized response.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			// WebSocket clients cannot set the Authorization header, so they
			// pass the token as a query parameter instead.
			header = "Bearer " + r.URL.Query().Get("token")
		}
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		claims := &jwt.RegisteredClaims{}
		tok, err := jwt.ParseWithClaims(parts[1], claims, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return jwtSecret(), nil
		})
		if err != nil || !tok.Valid {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		id, err := uuid.Parse(claims.Subject)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserID extracts the authenticated user's UUID from the request context.
// It returns the zero UUID if no user is present (i.e. the request did not
// pass through Middleware).
func UserID(r *http.Request) uuid.UUID {
	id, _ := r.Context().Value(UserIDKey).(uuid.UUID)
	return id
}

// makeToken creates a signed JWT for the given user ID with a 7-day expiry.
func makeToken(id uuid.UUID) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   id.String(),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret())
}
