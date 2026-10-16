package httpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/migrate"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This fixture deliberately requires a separately named disposable database.
// Every migration runs unchanged in a fresh schema with the real pgvector
// extension. It never starts an application server, scheduler or email worker.
func newAdminWorkspaceMigratedDB(t *testing.T) (context.Context, *db.Pool) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("GMCL_HANDLER_TEST_DSN"))
	if dsn == "" {
		t.Skip("GMCL_HANDLER_TEST_DSN is not configured")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if (config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") || !strings.HasPrefix(config.ConnConfig.Database, "gmcl_handler_qa_") {
		t.Fatal("handler migration tests require a loopback gmcl_handler_qa_ disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var vectorVersion string
	if err := admin.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname='vector'`).Scan(&vectorVersion); err != nil || vectorVersion == "" {
		_ = admin.Close(context.Background())
		t.Fatalf("real pgvector must already be installed in the disposable database: %v", err)
	}
	schema := "gmcl_qa_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close(context.Background())
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quotedSchema + ",public"
	// One session also makes deliberately broken TEMP shadow tables reliable
	// for handler read-failure tests, without altering the migrated schema.
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		_ = admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop isolated handler schema: %v", err)
		}
		_ = admin.Close(cleanupCtx)
	})
	wrapped := &db.Pool{Pool: pool}
	if err := migrate.Run(ctx, wrapped, os.DirFS(filepath.Join("..", "..")), "migrations"); err != nil {
		t.Fatalf("complete unchanged migration chain: %v", err)
	}
	return ctx, wrapped
}

func TestAdminWorkspaceCompleteMigrationsPostgres(t *testing.T) {
	ctx, pool := newAdminWorkspaceMigratedDB(t)
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil || applied != len(files) {
		t.Fatalf("applied migrations=%d, want %d, error=%v", applied, len(files), err)
	}
	var dimensions int
	var distance float64
	if err := pool.QueryRow(ctx, `SELECT vector_dims('[1,0,0]'::vector),'[1,0,0]'::vector <=> '[1,0,0]'::vector`).Scan(&dimensions, &distance); err != nil || dimensions != 3 || distance != 0 {
		t.Fatalf("real vector operations: dimensions=%d distance=%v error=%v", dimensions, distance, err)
	}
	var indexMethod string
	if err := pool.QueryRow(ctx, `SELECT method.amname FROM pg_class index
		JOIN pg_am method ON method.oid=index.relam
		WHERE index.relname='idx_rule_chunks_embedding' AND index.relnamespace=current_schema()::regnamespace`).Scan(&indexMethod); err != nil || indexMethod != "hnsw" {
		t.Fatalf("actual embedding index method=%q, error=%v", indexMethod, err)
	}
	t.Logf("all %d unchanged migrations applied with the real pgvector extension and HNSW index", applied)
}
