// Package db provides the PostgreSQL connection pool used across all packages.
//
// Connection parameters are read from environment variables at startup.
// Either set DATABASE_URL for a single connection string, or use the
// individual DB_HOST / DB_PORT / DB_USER / DB_PASSWORD / DB_NAME variables.
// All variables fall back to sensible local-development defaults so the
// server can start without any configuration for local testing.
package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// New creates and validates a pgx connection pool from environment variables.
//
// It first checks for DATABASE_URL; if not set it assembles a DSN from the
// individual DB_* variables. The pool is pinged before being returned so
// callers know immediately if the database is unreachable.
func New(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			getenv("DB_HOST", "localhost"),
			getenv("DB_PORT", "5432"),
			getenv("DB_USER", "postgres"),
			getenv("DB_PASSWORD", "postgres"),
			getenv("DB_NAME", "snapchat"),
		)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

// getenv returns the value of the environment variable named by key, or
// fallback if the variable is unset or empty.
func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
