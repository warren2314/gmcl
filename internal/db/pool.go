package db

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps pgxpool for app-wide database access.
type Pool struct {
	*pgxpool.Pool
}

// NewFromEnv connects using DB_DSN.
func NewFromEnv(ctx context.Context) (*Pool, error) {
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("DB_DSN is not set")
	}
	return New(ctx, dsn)
}

// New creates a pool from a DSN string.
func New(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Pool{Pool: p}, nil
}
