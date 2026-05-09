-- Migration 001 — initial schema
--
-- Creates all tables required by snapchat-go:
--   users        — registered accounts
--   friendships  — directed friend requests with a status lifecycle
--   messages     — ephemeral direct messages with TTL-based expiry
--   stories      — 24-hour public posts visible to friends
--   story_views  — tracks who has viewed each story
--
-- Requires the pgcrypto extension for gen_random_uuid().
-- Run once against a fresh database:
--   psql -d snapchat -f internal/db/migrations/001_init.sql

-- Enable pgcrypto so we can use gen_random_uuid() for primary keys.
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ── users ────────────────────────────────────────────────────────────────────
-- Stores registered user accounts.
-- password holds the bcrypt hash — never the plain-text value.
-- avatar_url is nullable; clients set it by uploading via POST /media/upload
-- and then (in a future update) calling PATCH /users/me.
CREATE TABLE users (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    username    TEXT        UNIQUE NOT NULL,
    email       TEXT        UNIQUE NOT NULL,
    password    TEXT        NOT NULL,             -- bcrypt hash
    avatar_url  TEXT,                             -- optional profile picture URL
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── friendships ──────────────────────────────────────────────────────────────
-- Represents a directed friend request.  The UNIQUE constraint on
-- (requester_id, addressee_id) prevents duplicate requests in the same direction.
--
-- Status values:
--   pending   — request sent, not yet acted on
--   accepted  — both users are friends
--   rejected  — addressee declined the request
CREATE TABLE friendships (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    requester_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    addressee_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       TEXT        NOT NULL CHECK (status IN ('pending','accepted','rejected')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (requester_id, addressee_id)
);

-- ── messages ─────────────────────────────────────────────────────────────────
-- Stores direct messages between two users.
--
-- Ephemeral lifecycle:
--   1. Row inserted with expires_at = NULL and read_at = NULL.
--   2. Recipient calls PUT /messages/{id}/read:
--        read_at  ← NOW()
--        expires_at ← NOW() + ttl_seconds
--   3. Background job deletes rows where expires_at < NOW() every minute.
--
-- msg_type controls how the client renders the message:
--   text  — plain text content
--   image — media_url points to an image
--   video — media_url points to a video
--   file  — media_url points to a generic file download
CREATE TABLE messages (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    sender_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content      TEXT,                            -- text body; NULL for media-only messages
    media_url    TEXT,                            -- URL of uploaded file; NULL for text messages
    msg_type     TEXT        NOT NULL DEFAULT 'text'
                             CHECK (msg_type IN ('text','image','video','file')),
    ttl_seconds  INT         NOT NULL DEFAULT 86400, -- seconds after read until deletion (default 24 h)
    expires_at   TIMESTAMPTZ,                    -- set when the message is read; NULL = not yet read
    read_at      TIMESTAMPTZ,                    -- NULL = unread
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index by sender and recipient for fast conversation lookups.
CREATE INDEX idx_messages_sender    ON messages(sender_id);
CREATE INDEX idx_messages_recipient ON messages(recipient_id);
-- Partial index on expires_at to speed up the background cleanup job.
CREATE INDEX idx_messages_expires   ON messages(expires_at) WHERE expires_at IS NOT NULL;

-- ── stories ──────────────────────────────────────────────────────────────────
-- Stores user stories visible to friends for 24 hours.
-- expires_at defaults to NOW() + 24 h at insert time.
-- The background cleanup job deletes rows where expires_at < NOW().
CREATE TABLE stories (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content    TEXT,                              -- optional text caption
    media_url  TEXT,                              -- optional image or video URL
    expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index by user for fast "my stories" lookups.
CREATE INDEX idx_stories_user    ON stories(user_id);
-- Index on expires_at for fast cleanup queries and feed filtering.
CREATE INDEX idx_stories_expires ON stories(expires_at);

-- ── story_views ───────────────────────────────────────────────────────────────
-- Tracks which users have viewed each story.
-- The UNIQUE constraint on (story_id, viewer_id) makes recording a view
-- idempotent — repeated calls to POST /stories/{id}/view have no effect.
CREATE TABLE story_views (
    id        UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    story_id  UUID        NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
    viewer_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    viewed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (story_id, viewer_id)
);
