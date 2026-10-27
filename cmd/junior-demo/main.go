// junior-demo is an isolated demonstration executable. It never starts the
// production application's workers and cannot connect to its database.
package main

import (
	"context"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/httpserver"
	"cricket-ground-feedback/internal/juniordemo"
	"cricket-ground-feedback/internal/migrate"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"net/http"
	"os"
	"time"
)

func run() error {
	config, err := pgxpool.ParseConfig(os.Getenv("DEMO_DB_DSN"))
	if err != nil {
		return fmt.Errorf("invalid demo connection configuration")
	}
	if config.ConnConfig.Database != "gmcl_junior_demo" || config.ConnConfig.User != "gmcl_junior_demo" {
		return fmt.Errorf("dedicated demo database and user required")
	}
	password, secret, base := os.Getenv("DEMO_ACCESS_PASSWORD"), os.Getenv("DEMO_SESSION_SECRET"), os.Getenv("DEMO_BASE_URL")
	if len(password) < 20 || len(secret) < 32 {
		return fmt.Errorf("strong demo password and independent session secret required")
	}
	expires, err := time.Parse(time.RFC3339, os.Getenv("DEMO_EXPIRES_AT"))
	if err != nil || !expires.After(time.Now()) {
		return fmt.Errorf("future demo expiry required")
	}
	for key, value := range map[string]string{"ADMIN_SESSION_SECRET": secret, "DISABLE_2FA": "1", "SUPER_ADMIN_EMAILS": "", "JUNIOR_PORTAL_ENABLED": "true", "JUNIOR_OUTBOUND_EMAIL_ENABLED": "false", "SMTP_HOST": "", "EMAIL_OVERRIDE": "", "PUBLIC_BASE_URL": base, "APP_BASE_URL": base, "SANCTIONS_EMAIL_DISABLED": "true", "INELIGIBLE_OUTBOUND_EMAIL_ENABLED": "false", "SANCTION_OUTBOX_SCHEDULER_ENABLED": "false", "STARRED_WEEKLY_SYNC_ENABLED": "false", "RULES_ASSISTANT_ENABLED": "false", "JUNIOR_UPLOAD_DIR": "/demo-data/badges"} {
		if err = os.Setenv(key, value); err != nil {
			return err
		}
	}
	if dir := os.Getenv("DEMO_UPLOAD_DIR"); dir != "" {
		if err = os.Setenv("JUNIOR_UPLOAD_DIR", dir); err != nil {
			return err
		}
	}
	config.MaxConns = 5
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	native, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return err
	}
	pool := &db.Pool{Pool: native}
	defer pool.Close()
	migrationDir := os.Getenv("DEMO_MIGRATE_DIR")
	if migrationDir == "" {
		migrationDir = "migrations"
	}
	if err = migrate.Run(ctx, pool, os.DirFS(migrationDir), "."); err != nil {
		return err
	}
	if _, err = juniordemo.Seed(ctx, pool, password); err != nil {
		return err
	}
	assets := os.Getenv("DEMO_ASSET_DIR")
	if assets == "" {
		assets = "."
	}
	handler, err := httpserver.NewJuniorDemoHandler(ctx, pool, httpserver.JuniorDemoOptions{Password: password, BaseURL: base, AssetDir: assets, ExpiresAt: expires})
	if err != nil {
		return err
	}
	addr := os.Getenv("DEMO_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8092"
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("Fictional junior demonstration ready; real email disabled; access expires %s", expires.Format(time.RFC3339))
	return server.ListenAndServe()
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
