package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"cricket-ground-feedback/internal/db"
)

// Run reads SQL files from the given FS (under dir), sorts by name, and executes each in order.
// It tracks applied migrations in a schema_migrations table to avoid re-running them.
func Run(ctx context.Context, pool *db.Pool, fsys fs.FS, dir string) error {
	// Ensure the tracking table exists.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		// Check if already applied.
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}

		fpath := path.Join(dir, name)
		body, err := fs.ReadFile(fsys, fpath)
		if err != nil {
			return fmt.Errorf("read %s: %w", fpath, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("execute %s: %w", fpath, err)
		}

		// Record the migration.
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}
