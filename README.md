# snapchat-go

A Snapchat-inspired real-time messaging backend written in Go.  
Provides ephemeral direct messages, 24-hour stories, friend management, and media uploads over a REST + WebSocket API.

---

## Table of Contents

- [Features](#features)
- [Architecture Overview](#architecture-overview)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Configuration](#configuration)
- [Running the Server](#running-the-server)
- [Database Setup](#database-setup)
- [API Reference](#api-reference)
  - [Authentication](#authentication)
  - [Users](#users)
  - [Friends](#friends)
  - [Messages](#messages)
  - [Stories](#stories)
  - [Media Upload](#media-upload)
  - [WebSocket](#websocket)
- [WebSocket Event Reference](#websocket-event-reference)
- [Example Walkthrough](#example-walkthrough)
- [Project Structure](#project-structure)
- [Dependencies](#dependencies)

---

## Features

| Feature | Description |
|---|---|
| **Ephemeral messages** | Messages self-destruct `ttl_seconds` after the recipient reads them (default 24 h) |
| **Real-time delivery** | New messages and read receipts are pushed instantly over WebSocket |
| **Stories** | Posts that auto-expire 24 hours after creation, visible only to friends |
| **Story view tracking** | Counts how many friends have viewed each story |
| **Friend system** | Send / accept / reject friend requests; bidirectional friend list |
| **Media uploads** | Upload images and videos (JPEG, PNG, GIF, WebP, MP4, WebM — max 50 MB) |
| **JWT authentication** | Stateless tokens valid for 7 days; no session storage required |

---

## Architecture Overview

```
┌─────────────┐   REST (JSON)    ┌──────────────────────────────┐
│  API Client ├──────────────────►  chi router  (cmd/server)     │
│  (curl /    │                  │                                │
│   Postman / │   WebSocket      │  internal/                     │
│   mobile)   ◄──────────────────┤    auth/    ← JWT middleware   │
└─────────────┘                  │    user/    ← profile & search │
                                 │    friend/  ← social graph     │
                                 │    message/ ← ephemeral DMs    │
                                 │    story/   ← 24-h stories     │
                                 │    media/   ← file uploads     │
                                 │    ws/      ← hub + clients    │
                                 │    db/      ← pgx pool         │
                                 └──────────┬───────────────────┘
                                            │ pgx/v5
                                            ▼
                                    ┌───────────────┐
                                    │  PostgreSQL   │
                                    └───────────────┘
```

---

## Prerequisites

| Software | Minimum Version | Purpose |
|---|---|---|
| **Go** | 1.22 | Compiling and running the server |
| **PostgreSQL** | 13 | Primary database (uses `pgcrypto` extension) |
| **Git** | any | Cloning the repository |

> **Windows users:** Install Go from https://go.dev/dl and PostgreSQL from https://www.postgresql.org/download/windows/.  
> **macOS users:** `brew install go postgresql@16`  
> **Linux (Debian/Ubuntu):** `apt install golang-go postgresql`

No other tools are required. All Go dependencies are fetched automatically by `go mod tidy`.

---

## Installation

### 1. Clone the repository

```bash
git clone https://github.com/your-username/snapchat-go.git
cd snapchat-go
```

### 2. Download Go dependencies

```bash
go mod tidy
```

This downloads and caches all third-party modules listed in `go.mod`.

### 3. Verify the build

```bash
go build ./...
```

No output means the build succeeded.

---

## Configuration

All configuration is done through **environment variables**.  
Copy the example file and edit it:

```bash
cp .env.example .env
```

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | TCP port the HTTP server listens on |
| `DATABASE_URL` | *(see below)* | Full PostgreSQL connection string (overrides individual vars) |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `postgres` | PostgreSQL username |
| `DB_PASSWORD` | `postgres` | PostgreSQL password |
| `DB_NAME` | `snapchat` | PostgreSQL database name |
| `JWT_SECRET` | `change-me-in-production` | HMAC-SHA256 signing key for JWTs — **change this in production** |

**Option A — single connection string:**
```
DATABASE_URL=postgres://myuser:mypassword@localhost:5432/snapchat?sslmode=disable
```

**Option B — individual variables:**
```
DB_HOST=localhost
DB_PORT=5432
DB_USER=myuser
DB_PASSWORD=mypassword
DB_NAME=snapchat
```

### Loading .env on startup

The server reads environment variables directly from the process environment.  
Use one of these methods to load your `.env` file:

**Linux / macOS / Git Bash:**
```bash
export $(grep -v '^#' .env | xargs) && go run ./cmd/server
```

**PowerShell (Windows):**
```powershell
Get-Content .env | Where-Object { $_ -notmatch '^#' -and $_ -ne '' } |
  ForEach-Object { $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v) }
go run ./cmd/server
```

---

## Database Setup

### 1. Create the database

```bash
# Linux / macOS
createdb snapchat

# PostgreSQL prompt (any platform)
psql -U postgres -c "CREATE DATABASE snapchat;"
```

### 2. Run the migration

```bash
psql -U postgres -d snapchat -f internal/db/migrations/001_init.sql
```

This creates five tables (`users`, `friendships`, `messages`, `stories`, `story_views`) and the necessary indexes.

### 3. Verify

```bash
psql -U postgres -d snapchat -c "\dt"
```

You should see all five tables listed.

---

## Running the Server

### Development

```bash
go run ./cmd/server
```

### Build a binary and run

```bash
go build -o snapchat-server ./cmd/server
./snapchat-server
```

### Windows (build and run)

```powershell
go build -o snapchat-server.exe .\cmd\server
.\snapchat-server.exe
```

The server prints a line like:

```
2026/05/09 12:00:00 listening on :8080
```

---

## API Reference

All authenticated endpoints require the header:

```
Authorization: Bearer <token>
```

The token is returned by `/auth/register` and `/auth/login`.

---

### Authentication

#### Register a new account

```http
POST /auth/register
Content-Type: application/json

{
  "username": "alice",
  "email":    "alice@example.com",
  "password": "supersecret"
}
```

**Response `201 Created`:**
```json
{ "token": "<jwt>" }
```

---

#### Log in

```http
POST /auth/login
Content-Type: application/json

{
  "email":    "alice@example.com",
  "password": "supersecret"
}
```

**Response `200 OK`:**
```json
{ "token": "<jwt>" }
```

---

### Users

#### Get your own profile

```http
GET /users/me
Authorization: Bearer <token>
```

**Response `200 OK`:**
```json
{
  "id":         "550e8400-...",
  "username":   "alice",
  "email":      "alice@example.com",
  "avatar_url": null,
  "created_at": "2026-05-09T10:00:00Z"
}
```

---

#### Search users by username

```http
GET /users/search?q=ali
Authorization: Bearer <token>
```

Returns up to 20 users whose username contains the query string (case-insensitive).

---

#### Get user by username

```http
GET /users/bob
Authorization: Bearer <token>
```

Returns the public profile of the user (email omitted).

---

### Friends

#### Send a friend request

```http
POST /friends/request
Authorization: Bearer <token>
Content-Type: application/json

{ "addressee_id": "<uuid-of-target-user>" }
```

**Response `201 Created`:**
```json
{ "id": "<friendship-uuid>" }
```

---

#### List incoming friend requests

```http
GET /friends/requests
Authorization: Bearer <token>
```

Returns pending requests addressed to you.

---

#### Accept or reject a request

```http
PUT /friends/requests/<friendship-id>
Authorization: Bearer <token>
Content-Type: application/json

{ "action": "accept" }
```

`action` must be `"accept"` or `"reject"`.  
**Response `204 No Content`.**

---

#### List accepted friends

```http
GET /friends
Authorization: Bearer <token>
```

Returns all users you are friends with (accepted in either direction).

---

### Messages

#### Send a message

```http
POST /messages
Authorization: Bearer <token>
Content-Type: application/json

{
  "recipient_id": "<uuid>",
  "content":      "Hey! This message will self-destruct.",
  "msg_type":     "text",
  "ttl_seconds":  3600
}
```

| Field | Required | Default | Description |
|---|---|---|---|
| `recipient_id` | yes | — | UUID of the recipient |
| `content` | one of content/media_url | — | Text body |
| `media_url` | one of content/media_url | — | URL from `/media/upload` |
| `msg_type` | no | `text` | `text` \| `image` \| `video` \| `file` |
| `ttl_seconds` | no | `86400` | Seconds until deletion after the message is read |

**Response `201 Created`:** full Message JSON object.

> The recipient receives a `new_message` WebSocket event immediately if connected.

---

#### Get conversation history

```http
GET /messages/<friend-uuid>
Authorization: Bearer <token>
```

Returns all non-expired messages between you and the specified friend, oldest first.

---

#### Mark a message as read (starts expiry countdown)

```http
PUT /messages/<message-id>/read
Authorization: Bearer <token>
```

- Sets `read_at = NOW()`
- Sets `expires_at = NOW() + ttl_seconds`
- Pushes a `message_read` WebSocket event to the sender

**Response `204 No Content`.**

---

### Stories

#### Post a story

```http
POST /stories
Authorization: Bearer <token>
Content-Type: application/json

{
  "content":   "Good morning everyone!",
  "media_url": "/uploads/photo.jpg"
}
```

At least one of `content` or `media_url` is required.  
**Response `201 Created`:** Story JSON object with `expires_at` 24 hours from now.

---

#### Get friends' story feed

```http
GET /stories/feed
Authorization: Bearer <token>
```

Returns non-expired stories from all accepted friends, newest first.  
Each story includes `views` (total unique viewers) and `viewed_by_me` (boolean).

---

#### Get your own stories

```http
GET /stories/mine
Authorization: Bearer <token>
```

Returns your own active stories with view counts — useful for showing who watched.

---

#### Record a story view

```http
POST /stories/<story-id>/view
Authorization: Bearer <token>
```

Idempotent — calling multiple times has no effect.  
**Response `204 No Content`.**

---

### Media Upload

```http
POST /media/upload
Authorization: Bearer <token>
Content-Type: multipart/form-data

file=<binary>
```

| Property | Value |
|---|---|
| Form field name | `file` |
| Maximum size | 50 MB |
| Accepted types | JPEG, PNG, GIF, WebP, MP4, WebM |

**Response `201 Created`:**
```json
{ "url": "/uploads/3f2a1b4c-....jpg" }
```

Use the returned `url` as `media_url` in messages or stories.

**Accessing uploaded files:**
```
GET /uploads/3f2a1b4c-....jpg
```
No authentication required — files are served as static assets.

---

### WebSocket

Connect to receive real-time events.  
Authentication is via the `token` query parameter (browsers cannot set the `Authorization` header for WebSocket connections).

```
ws://localhost:8080/ws?token=<jwt>
```

The connection is **receive-only** — all writes go through the REST API.

---

## WebSocket Event Reference

Events are JSON objects with a `type` string and a `payload` object.

### `new_message`

Sent to the **recipient** when a new message arrives.

```json
{
  "type": "new_message",
  "payload": {
    "id":           "msg-uuid",
    "sender_id":    "uuid",
    "recipient_id": "uuid",
    "content":      "Hello!",
    "msg_type":     "text",
    "ttl_seconds":  86400,
    "created_at":   "2026-05-09T10:00:00Z"
  }
}
```

### `message_read`

Sent to the **sender** when the recipient marks a message as read.

```json
{
  "type": "message_read",
  "payload": {
    "message_id": "msg-uuid",
    "expires_at": "2026-05-10T10:00:00Z"
  }
}
```

---

## Example Walkthrough

The following curl commands walk through the full flow from registration to an ephemeral message.

```bash
# 1. Register two users
TOKEN_A=$(curl -s -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","email":"alice@test.com","password":"pass123"}' | jq -r .token)

TOKEN_B=$(curl -s -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"bob","email":"bob@test.com","password":"pass456"}' | jq -r .token)

# 2. Alice looks up Bob
BOB_ID=$(curl -s http://localhost:8080/users/bob \
  -H "Authorization: Bearer $TOKEN_A" | jq -r .id)

# 3. Alice sends Bob a friend request
REQ_ID=$(curl -s -X POST http://localhost:8080/friends/request \
  -H "Authorization: Bearer $TOKEN_A" \
  -H "Content-Type: application/json" \
  -d "{\"addressee_id\":\"$BOB_ID\"}" | jq -r .id)

# 4. Bob accepts the request
curl -s -X PUT http://localhost:8080/friends/requests/$REQ_ID \
  -H "Authorization: Bearer $TOKEN_B" \
  -H "Content-Type: application/json" \
  -d '{"action":"accept"}'

# 5. Alice sends Bob a message (expires 1 hour after Bob reads it)
ALICE_ID=$(curl -s http://localhost:8080/users/me \
  -H "Authorization: Bearer $TOKEN_A" | jq -r .id)

MSG_ID=$(curl -s -X POST http://localhost:8080/messages \
  -H "Authorization: Bearer $TOKEN_A" \
  -H "Content-Type: application/json" \
  -d "{\"recipient_id\":\"$BOB_ID\",\"content\":\"This will disappear!\",\"ttl_seconds\":3600}" \
  | jq -r .id)

# 6. Bob reads the message (starts the 1-hour expiry countdown)
curl -s -X PUT http://localhost:8080/messages/$MSG_ID/read \
  -H "Authorization: Bearer $TOKEN_B"

# 7. Alice posts a 24-hour story
curl -s -X POST http://localhost:8080/stories \
  -H "Authorization: Bearer $TOKEN_A" \
  -H "Content-Type: application/json" \
  -d '{"content":"Good morning!"}'

# 8. Bob views Alice's story feed
curl -s http://localhost:8080/stories/feed \
  -H "Authorization: Bearer $TOKEN_B"
```

---

## Project Structure

```
snapchat-go/
├── cmd/
│   └── server/
│       └── main.go              # Entry point: wires all packages, starts HTTP server
├── internal/
│   ├── auth/
│   │   └── auth.go              # Register, Login handlers + JWT middleware
│   ├── user/
│   │   └── user.go              # Me, Search, GetByUsername handlers
│   ├── friend/
│   │   └── friend.go            # SendRequest, ListRequests, RespondRequest, List
│   ├── message/
│   │   └── message.go           # Send, Conversation, MarkRead (ephemeral expiry)
│   ├── story/
│   │   └── story.go             # Create, Feed, View, MyStories (24-h expiry)
│   ├── media/
│   │   └── media.go             # Upload handler (MIME sniffing, UUID filenames)
│   ├── ws/
│   │   ├── hub.go               # Thread-safe client registry, Send/Online methods
│   │   └── client.go            # WebSocket upgrade, read/write pumps, ping-pong
│   └── db/
│       ├── db.go                # pgx connection pool factory
│       └── migrations/
│           └── 001_init.sql     # Full schema with comments
├── uploads/                     # Uploaded files stored here at runtime
├── .env.example                 # Template for environment variables
├── go.mod                       # Module definition and direct dependencies
├── go.sum                       # Dependency checksums (committed to version control)
└── README.md                    # This file
```

---

## Dependencies

| Module | Version | Purpose |
|---|---|---|
| `github.com/go-chi/chi/v5` | v5.1.0 | Lightweight HTTP router |
| `github.com/golang-jwt/jwt/v5` | v5.2.1 | JWT creation and validation |
| `github.com/google/uuid` | v1.6.0 | UUID generation for primary keys and filenames |
| `github.com/gorilla/websocket` | v1.5.3 | WebSocket server implementation |
| `github.com/jackc/pgx/v5` | v5.6.0 | PostgreSQL driver and connection pool |
| `golang.org/x/crypto` | v0.24.0 | bcrypt password hashing |

All dependencies are pure Go and have no C or system-library requirements.  
They are downloaded automatically by `go mod tidy`.
