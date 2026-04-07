package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cricket-ground-feedback/internal/auth"
	"cricket-ground-feedback/internal/db"
	"cricket-ground-feedback/internal/httpserver"
	"cricket-ground-feedback/internal/migrate"
	"cricket-ground-feedback/internal/seed"
)

func main() {
	resetEmail := flag.String("reset-admin", "", "Reset the password for an admin user (provide email/username)")
	resetPass := flag.String("password", "", "New password to set (use with -reset-admin)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := db.NewFromEnv(ctx)
	cancel()
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	// Admin password reset mode — does not start the HTTP server.
	if *resetEmail != "" {
		if *resetPass == "" {
			fmt.Fprintln(os.Stderr, "error: -password is required with -reset-admin")
			os.Exit(1)
		}
		hash, err := auth.HashPassword(*resetPass)
		if err != nil {
			log.Fatalf("failed to hash password: %v", err)
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rcancel()
		tag, err := pool.Exec(rctx, `
			UPDATE admin_users SET password_hash=$1, force_password_change=FALSE
			WHERE username=$2 OR email=$2
		`, hash, *resetEmail)
		if err != nil {
			log.Fatalf("failed to update password: %v", err)
		}
		if tag.RowsAffected() == 0 {
			fmt.Fprintf(os.Stderr, "no admin user found with username/email %q\n", *resetEmail)
			os.Exit(1)
		}
		fmt.Printf("Password updated for %s\n", *resetEmail)
		os.Exit(0)
	}

	if os.Getenv("MIGRATE") == "1" {
		migrateDir := getEnv("MIGRATE_DIR", "migrations")
		fsys := os.DirFS(migrateDir)
		mctx, mcancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := migrate.Run(mctx, pool, fsys, "."); err != nil {
			log.Fatalf("migrations failed: %v", err)
		}
		mcancel()
		log.Printf("migrations completed")
	}

	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := seed.RunSeedData(sctx, pool); err != nil {
		log.Printf("seed data warning: %v", err)
	}
	scancel()
	sctx2, scancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	if err := seed.RunIfEnabled(sctx2, pool); err != nil {
		log.Printf("seed admin warning: %v", err)
	}
	scancel2()

	router, cleanup, err := httpserver.NewServerWithPool(pool)
	if err != nil {
		log.Fatalf("failed to initialise server: %v", err)
	}
	defer cleanup()

	addr := getEnv("APP_HTTP_ADDR", ":8080")

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// graceful shutdown
	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	log.Printf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe: %v", err)
	}

	<-idleConnsClosed
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

